package tunnel

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

// recordingCarrier captures all envelopes written to it and serves them back on Read.
type recordingCarrier struct {
	mu        sync.Mutex
	written   []fabric.Envelope
	envelopes []fabric.Envelope
	desc      carriers.Descriptor
}

func newRecordingCarrier(id string) *recordingCarrier {
	return &recordingCarrier{
		desc: carriers.Descriptor{
			ID:             id,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficEgress, fabric.TrafficStream},
			Capabilities:   []carriers.Capability{carriers.CapStream},
		},
	}
}

func (c *recordingCarrier) Descriptor() carriers.Descriptor { return c.desc }

func (c *recordingCarrier) Write(_ context.Context, _ carriers.Endpoint, env fabric.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, env)
	return nil
}

func (c *recordingCarrier) Read(_ context.Context, _ carriers.Endpoint, _ carriers.Cursor) (carriers.ReadResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.envelopes
	c.envelopes = nil
	return carriers.ReadResult{Envelopes: out, Cursor: "next"}, nil
}

func (c *recordingCarrier) DeleteMessage(_ context.Context, _ carriers.Endpoint, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Filter out the deleted message
	var filtered []fabric.Envelope
	for _, env := range c.envelopes {
		if env.ID != id {
			filtered = append(filtered, env)
		}
	}
	c.envelopes = filtered
	return nil
}

func (c *recordingCarrier) Written() []fabric.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]fabric.Envelope(nil), c.written...)
}

func (c *recordingCarrier) Probe(_ context.Context, _ carriers.Endpoint) (carriers.Metrics, error) {
	return carriers.Metrics{Healthy: true}, nil
}

// TestWriteLoopKeepalive verifies that the client writeLoop exits cleanly
// on context cancel and doesn't panic during idle periods.
func TestWriteLoopKeepalive(t *testing.T) {
	carrier := newRecordingCarrier("test-keepalive")
	ep := carriers.Endpoint{ID: "test-keepalive:ep", Carrier: "test-keepalive", Address: "test://ep"}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}

	tun := NewCarrierTunnel("client-test", map[string]policy.CarrierBinding{"test-keepalive": binding})

	client, server := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		tun.writeLoop(ctx, "tun-keepalive-test", server, binding)
		close(done)
	}()

	// Let the writeLoop run briefly, then close the pipe reader so
	// server.Read() returns an error and writeLoop exits.
	time.Sleep(200 * time.Millisecond)
	client.Close()
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writeLoop did not exit after pipe close")
	}
}

// TestKeepaliveSkipped verifies that readLoop skips keepalive envelopes
// (Sequence=0, nil payload) and only forwards real data.
func TestKeepaliveSkipped(t *testing.T) {
	carrier := newRecordingCarrier("test-ka-skip")
	ep := carriers.Endpoint{ID: "test-ka-skip:ep", Carrier: "test-ka-skip", Address: "test://ep"}

	// Mailbox carriers return the sender's own writes too. The client must not
	// reflect its request back into the local SOCKS stream before node data.
	carrier.mu.Lock()
	carrier.envelopes = []fabric.Envelope{
		{
			Version:     1,
			ID:          "tun-ka-test",
			Source:      "client-test",
			PayloadType: tunnelData,
			Sequence:    1,
			Payload:     []byte("client request"),
		},
		{
			Version:     1,
			ID:          "tun-ka-test",
			Source:      "node-remote",
			PayloadType: tunnelData,
			Sequence:    0,
			Payload:     nil, // keepalive
		},
		{
			Version:     1,
			ID:          "tun-ka-test",
			Source:      "node-remote",
			PayloadType: tunnelData,
			Sequence:    1,
			Payload:     []byte("real data"),
		},
	}
	carrier.mu.Unlock()

	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	tun := NewCarrierTunnel("client-test", map[string]policy.CarrierBinding{"test-ka-skip": binding})

	client, server := net.Pipe()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// readLoop writes carrier data to client conn; we read from server
	// (the other end of the pipe).
	go tun.readLoop(ctx, "tun-ka-test", client, binding)

	// Read from server — should get only "real data", not the keepalive.
	// readLoop polls on a 2-second ticker, so the deadline must exceed that.
	buf := make([]byte, 1024)
	server.SetReadDeadline(time.Now().Add(4 * time.Second))
	n, err := server.Read(buf)
	server.Close()
	if err != nil {
		t.Fatalf("unexpected read error: %v", err)
	}
	if string(buf[:n]) != "real data" {
		t.Fatalf("expected 'real data', got %q", string(buf[:n]))
	}

	cancel()
}

// TestEgressKeepaliveSkipped verifies processEgressEnvelope skips keepalives.
func TestEgressKeepaliveSkipped(t *testing.T) {
	tun := NewCarrierTunnel("node-test", nil)

	keepaliveEnv := fabric.Envelope{
		Version:     1,
		ID:          "tun-egress-ka",
		Source:      "client-remote",
		PayloadType: tunnelData,
		Sequence:    0,
		Payload:     nil,
	}

	// Should not panic or create a tunnel connection for a keepalive.
	tun.processEgressEnvelope(context.Background(), policy.CarrierBinding{}, keepaliveEnv)

	tun.mu.Lock()
	count := len(tun.active)
	tun.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 active tunnels after keepalive, got %d", count)
	}
}
