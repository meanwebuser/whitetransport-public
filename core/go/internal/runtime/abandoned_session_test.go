package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

type clientOutageCarrier struct {
	*memoryCarrier
	offline      atomic.Bool
	failedWrites atomic.Int32
	releases     atomic.Int32
}

func (c *clientOutageCarrier) Write(ctx context.Context, ep carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.Source == "outage-client" && c.offline.Load() {
		c.failedWrites.Add(1)
		return errors.New("client network unavailable")
	}
	if envelope.PayloadType == session.PayloadSessionRelease {
		c.releases.Add(1)
	}
	return c.memoryCarrier.Write(ctx, ep, envelope)
}

func TestClientNetworkReturnReleasesAbandonedLiveNodeSession(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		name := "recover"
		if disconnect {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://client-outage"}
			control := &clientOutageCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "client-outage", ep.Carrier)}
			egressEP := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://outage-egress"}
			bindings := map[string]policy.CarrierBinding{ep.Carrier: {Carrier: control, Endpoint: ep}, egressEP.Carrier: {Carrier: newMemoryCarrierWithDescriptor(t, "outage-egress", egressEP.Carrier), Endpoint: egressEP}}
			node, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "live-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "outage-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Stop()
			defer node.Stop()
			node.Start(ctx)
			client.Start(ctx)
			waitForNodeVisible(t, client, "live-node", true)
			if _, err := client.Connect(ctx, "live-node"); err != nil {
				t.Fatal(err)
			}
			old := client.activeSessionSnapshot()
			control.offline.Store(true)
			if _, _, err := client.reselectNodeAfterCarrierExhaustion(ctx, old, ""); err == nil {
				t.Fatal("expected no independent replacement")
			}
			deadline := time.Now().Add(2 * time.Second)
			for control.failedWrites.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if control.failedWrites.Load() == 0 {
				t.Fatal("outage did not reach background attempt")
			}
			node.mu.RLock()
			stillBusy := node.nodeBusy && node.nodeSessionID == old.SessionID
			node.mu.RUnlock()
			if !stillBusy {
				t.Fatal("node must retain the abandoned session throughout client outage")
			}
			if disconnect {
				client.Disconnect()
			}
			control.offline.Store(false)
			deadline = time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				status := client.Status()
				if disconnect && status.SessionActive {
					t.Fatal("cancelled recovery resurrected session")
				}
				if !disconnect && status.SessionActive && status.SessionID != old.SessionID {
					if control.releases.Load() != 1 {
						t.Fatalf("old-session releases=%d, want1", control.releases.Load())
					}
					client.mu.RLock()
					pending := len(client.abandonedSessions)
					client.mu.RUnlock()
					if pending != 0 {
						t.Fatalf("successful release retained %d abandoned sessions", pending)
					}
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !disconnect {
				t.Fatalf("network returned but live node remains busy with old session: %+v", client.Status())
			}
		})
	}
}

func TestAbandonedSessionRetentionIsBoundedByCountAndLease(t *testing.T) {
	c := &ControlPlane{}
	for i := 0; i < 100; i++ {
		c.rememberAbandonedSessionLocked(&activeSession{NodeID: fmt.Sprint(i), SessionID: fmt.Sprint(i), ExpiresAt: time.Now().Add(time.Minute)})
	}
	if len(c.abandonedSessions) != maxAbandonedSessions {
		t.Fatalf("retained session count=%d", len(c.abandonedSessions))
	}
	for _, pending := range c.abandonedSessions {
		pending.ExpiresAt = time.Now().Add(-time.Second)
	}
	c.pruneAbandonedSessionsLocked()
	if len(c.abandonedSessions) != 0 {
		t.Fatal("expired leases retained release work")
	}
}
