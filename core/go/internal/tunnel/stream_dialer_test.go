package tunnel

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"sync"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

func TestGenericStreamDialerSelectionKeepsSessionEndpointAlias(t *testing.T) {
	constructors := map[string]func(map[string]policy.CarrierBinding) interface {
		SetSessionBinding(carriers.Endpoint, policy.CarrierBinding)
		DialContext(context.Context, carriers.Endpoint, string) (net.Conn, error)
	}{
		"composite": func(bindings map[string]policy.CarrierBinding) interface {
			SetSessionBinding(carriers.Endpoint, policy.CarrierBinding)
			DialContext(context.Context, carriers.Endpoint, string) (net.Conn, error)
		} {
			return NewCompositeTunnel(NewCarrierTunnel("stream-test", bindings))
		},
		"unified": func(bindings map[string]policy.CarrierBinding) interface {
			SetSessionBinding(carriers.Endpoint, policy.CarrierBinding)
			DialContext(context.Context, carriers.Endpoint, string) (net.Conn, error)
		} {
			return NewUnifiedCarrierTunnel("stream-test", bindings)
		},
	}

	for tunnelName, construct := range constructors {
		for _, carrierID := range []string{carriers.CarrierSSHTCP, carriers.CarrierSSHFabric} {
			t.Run(tunnelName+"/"+carrierID, func(t *testing.T) {
				streamCarrier := &recordingStreamDialerCarrier{descriptor: carriers.Descriptor{ID: carrierID}}
				baseEndpoint := carriers.Endpoint{ID: carrierID, Carrier: carrierID, Address: "base.invalid:22"}
				binding := policy.CarrierBinding{Carrier: streamCarrier, Endpoint: baseEndpoint}
				bindings := map[string]policy.CarrierBinding{carrierID: binding}
				tunnel := construct(bindings)
				alias := carriers.Endpoint{
					ID:       "node-88-" + carrierID,
					Carrier:  "route-88-" + carrierID,
					Address:  "session.invalid:2222",
					Metadata: map[string]string{"node": "example-exit-node"},
				}
				tunnel.SetSessionBinding(alias, binding)

				conn, err := tunnel.DialContext(context.Background(), alias, "target.invalid:443")
				if err != nil {
					t.Fatalf("dial aliased StreamDialer binding: %v", err)
				}
				_ = conn.Close()
				calledEndpoint, calledTarget, streamCalls, envelopeWrites := streamCarrier.calls()
				if streamCalls != 1 {
					t.Fatalf("DialStream calls = %d, want 1", streamCalls)
				}
				if envelopeWrites != 0 {
					t.Fatalf("CarrierTunnel envelope writes = %d, want 0", envelopeWrites)
				}
				if !reflect.DeepEqual(calledEndpoint, alias) {
					t.Fatalf("DialStream endpoint = %+v, want session alias %+v", calledEndpoint, alias)
				}
				if calledTarget != "target.invalid:443" {
					t.Fatalf("DialStream target = %q", calledTarget)
				}
			})
		}
	}
}

type recordingStreamDialerCarrier struct {
	mu             sync.Mutex
	descriptor     carriers.Descriptor
	endpoint       carriers.Endpoint
	target         string
	streamCalls    int
	envelopeWrites int
}

func (c *recordingStreamDialerCarrier) Descriptor() carriers.Descriptor { return c.descriptor }

func (c *recordingStreamDialerCarrier) Write(context.Context, carriers.Endpoint, fabric.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.envelopeWrites++
	return fmt.Errorf("unexpected envelope egress")
}

func (c *recordingStreamDialerCarrier) Read(context.Context, carriers.Endpoint, carriers.Cursor) (carriers.ReadResult, error) {
	return carriers.ReadResult{}, nil
}

func (c *recordingStreamDialerCarrier) Probe(context.Context, carriers.Endpoint) (carriers.Metrics, error) {
	return carriers.Metrics{Healthy: true}, nil
}

func (c *recordingStreamDialerCarrier) DeleteMessage(context.Context, carriers.Endpoint, string) error {
	return nil
}

func (c *recordingStreamDialerCarrier) DialStream(_ context.Context, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error) {
	c.mu.Lock()
	c.endpoint = endpoint
	c.target = targetAddr
	c.streamCalls++
	c.mu.Unlock()
	client, peer := net.Pipe()
	_ = peer.Close()
	return client, nil
}

func (c *recordingStreamDialerCarrier) calls() (carriers.Endpoint, string, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.endpoint, c.target, c.streamCalls, c.envelopeWrites
}
