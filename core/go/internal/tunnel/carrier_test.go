package tunnel

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

func TestCarrierTunnelSupportsEndpointWithBinding(t *testing.T) {
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKDocs1024: {
			Carrier:  newTestMemoryCarrier(t, "sup-test"),
			Endpoint: carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://sup"},
		},
	}
	tun := NewCarrierTunnel("client", bindings)
	if !tun.SupportsEndpoint(carriers.Endpoint{Carrier: carriers.CarrierVKDocs1024}) {
		t.Fatal("should support vk.docs.1024")
	}
	if tun.SupportsEndpoint(carriers.Endpoint{Carrier: carriers.CarrierVKMessages}) {
		t.Fatal("should not support vk.messages without binding")
	}
}

func TestCarrierTunnelEgressThroughMemoryCarrier(t *testing.T) {
	carrier := newTestMemoryCarrier(t, "egress-e2e")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://e2e"}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKDocs1024: {Carrier: carrier, Endpoint: ep},
	}

	// Echo server
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoListener.Close()
	echoAddr := echoListener.Addr().String()
	go func() {
		for {
			conn, err := echoListener.Accept()
			if err != nil {
				return
			}
			go io.Copy(conn, conn)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start node-side egress handler
	nodeTun := NewCarrierTunnel("test-node", bindings)
	go func() {
		_ = nodeTun.ServeEgress(ctx, bindings)
	}()

	time.Sleep(50 * time.Millisecond)

	// Client dials through carrier tunnel
	clientTun := NewCarrierTunnel("test-client", bindings)
	conn, err := clientTun.DialContext(ctx, ep, echoAddr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	// Send data
	_, err = conn.Write([]byte("hello carrier"))
	if err != nil {
		t.Fatalf("client write: %v", err)
	}

	// Read echo
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}

	got := strings.TrimSpace(string(buf[:n]))
	if got != "hello carrier" {
		t.Fatalf("expected echo 'hello carrier', got %q", got)
	}
}

func TestCarrierTunnelMultipleEgressStreams(t *testing.T) {
	carrier := newTestMemoryCarrier(t, "egress-multi")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://multi"}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKDocs1024: {Carrier: carrier, Endpoint: ep},
	}

	// Two echo servers
	startEcho := func(t *testing.T) (net.Listener, string) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go io.Copy(conn, conn)
			}
		}()
		return listener, listener.Addr().String()
	}

	l1, addr1 := startEcho(t)
	defer l1.Close()
	l2, addr2 := startEcho(t)
	defer l2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nodeTun := NewCarrierTunnel("test-node", bindings)
	go func() {
		_ = nodeTun.ServeEgress(ctx, bindings)
	}()

	time.Sleep(50 * time.Millisecond)

	clientTun := NewCarrierTunnel("test-client", bindings)

	conn1, err := clientTun.DialContext(ctx, ep, addr1)
	if err != nil {
		t.Fatalf("DialContext 1: %v", err)
	}
	defer conn1.Close()

	conn2, err := clientTun.DialContext(ctx, ep, addr2)
	if err != nil {
		t.Fatalf("DialContext 2: %v", err)
	}
	defer conn2.Close()

	// Write to both concurrently
	_, err = conn1.Write([]byte("stream1"))
	if err != nil {
		t.Fatalf("conn1 write: %v", err)
	}
	_, err = conn2.Write([]byte("stream2"))
	if err != nil {
		t.Fatalf("conn2 write: %v", err)
	}

	buf1 := make([]byte, 16)
	conn1.SetReadDeadline(time.Now().Add(5 * time.Second))
	n1, _ := conn1.Read(buf1)

	buf2 := make([]byte, 16)
	conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	n2, _ := conn2.Read(buf2)

	if strings.TrimSpace(string(buf1[:n1])) != "stream1" {
		t.Fatalf("conn1 expected 'stream1', got %q", string(buf1[:n1]))
	}
	if strings.TrimSpace(string(buf2[:n2])) != "stream2" {
		t.Fatalf("conn2 expected 'stream2', got %q", string(buf2[:n2]))
	}
}

// newTestMemoryCarrier creates a test memory carrier that reports the given
// carrier id in its descriptor.
func newTestMemoryCarrier(t *testing.T, id string) *testMemoryCarrier {
	t.Helper()
	descriptor, err := carriers.FindStandardDescriptor(carriers.CarrierVKDocs1024)
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	return &testMemoryCarrier{
		descriptor: descriptor,
		inner:      carriers.NewMemoryCarrier(id),
	}
}

type testMemoryCarrier struct {
	descriptor carriers.Descriptor
	inner      *carriers.MemoryCarrier
}

func (c *testMemoryCarrier) Descriptor() carriers.Descriptor {
	return c.descriptor
}

func (c *testMemoryCarrier) Write(ctx context.Context, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	return c.inner.Write(ctx, endpoint, envelope)
}

func (c *testMemoryCarrier) Read(ctx context.Context, endpoint carriers.Endpoint, cursor carriers.Cursor) (carriers.ReadResult, error) {
	return c.inner.Read(ctx, endpoint, cursor)
}

func (c *testMemoryCarrier) DeleteMessage(ctx context.Context, endpoint carriers.Endpoint, id string) error {
	return c.inner.DeleteMessage(ctx, endpoint, id)
}

func (c *testMemoryCarrier) Probe(ctx context.Context, endpoint carriers.Endpoint) (carriers.Metrics, error) {
	return c.inner.Probe(ctx, endpoint)
}
