package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

type delayedLivenessFailureCarrier struct {
	*memoryCarrier
	started chan struct{}
	finish  chan struct{}
}

func (c *delayedLivenessFailureCarrier) Write(ctx context.Context, ep carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.PayloadType == session.PayloadSessionOffer {
		close(c.started)
		select {
		case <-c.finish:
			return errors.New("control route unavailable")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.memoryCarrier.Write(ctx, ep, envelope)
}

func TestManualEndpointPinRejectsInflightLivenessFailure(t *testing.T) {
	ep := carriers.Endpoint{ID: "retained", Carrier: carriers.CarrierFileMailbox, Address: "memory://retained"}
	carrier := &delayedLivenessFailureCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "retained", ep.Carrier), started: make(chan struct{}), finish: make(chan struct{})}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	c, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "manual-liveness", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{ep.Carrier: binding}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.active = &activeSession{NodeID: "node", SessionID: "session", ControlEndpoint: ep, ControlBinding: binding, EgressEndpoints: []carriers.Endpoint{ep}}
	c.state = statusStateConnected
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.monitorSessionLiveness(ctx, session.Answer{SessionID: "session"}); close(done) }()
	select {
	case <-carrier.started:
	case <-time.After(12 * time.Second):
		t.Fatal("automatic liveness probe did not start")
	}
	if _, err := c.SelectEgressEndpoint(ep.ID); err != nil {
		t.Fatal(err)
	}
	close(carrier.finish)
	time.Sleep(50 * time.Millisecond)
	if status := c.Status(); status.State != statusStateConnected {
		t.Fatalf("stale probe overrode manual pin: %+v", status)
	}
	c.mu.RLock()
	failed := c.connectionLivenessFailed
	c.mu.RUnlock()
	if failed {
		t.Fatal("manual session scheduled automatic replacement")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
}
