package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

type unauthorizedOfferCarrier struct {
	*memoryCarrier
	offers atomic.Int32
}

func (c *unauthorizedOfferCarrier) Write(ctx context.Context, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.PayloadType == session.PayloadSessionOffer {
		c.offers.Add(1)
		return errors.New("HTTP 401 Unauthorized")
	}
	return c.memoryCarrier.Write(ctx, endpoint, envelope)
}

// Exercise the real offer/ACK/answer loop; only the provider's authentication
// failure is injected. Discovery runs normally before contact order is pinned.
func TestControlPlaneAuthFailureAllowsIndependentFallback(t *testing.T) {
	for _, name := range []string{"same_node", "second_node", "pinned_failed_node"} {
		secondNode := name != "same_node"
		t.Run(name, func(t *testing.T) {
			badEP := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://bad-auth"}
			goodEP := carriers.Endpoint{ID: carriers.CarrierOKMessages, Carrier: carriers.CarrierOKMessages, Address: "memory://good-auth"}
			egressEP := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://egress"}
			bad := &unauthorizedOfferCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "bad-auth", carriers.CarrierVKMessages)}
			good := newMemoryCarrierWithDescriptor(t, "good-auth", carriers.CarrierOKMessages)
			egress := newMemoryCarrierWithDescriptor(t, "egress", carriers.CarrierVKDocs1024)
			bindings := map[string]policy.CarrierBinding{
				badEP.Carrier:    {Carrier: bad, Endpoint: badEP},
				goodEP.Carrier:   {Carrier: good, Endpoint: goodEP},
				egressEP.Carrier: {Carrier: egress, Endpoint: egressEP},
			}
			nodeBindings := map[string]policy.CarrierBinding{goodEP.Carrier: bindings[goodEP.Carrier], egressEP.Carrier: bindings[egressEP.Carrier]}
			node, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "z-healthy", SocksListen: "127.0.0.1:0"}, nodeBindings, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "auth-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			defer node.Stop()
			defer client.Stop()
			node.Start(ctx)
			client.Start(ctx)
			waitForNodeVisible(t, client, "z-healthy", true)
			client.mu.Lock()
			healthy := client.nodes["z-healthy"]
			healthy.Advertisement.Carriers = []carriers.Endpoint{badEP, goodEP}
			healthy.Advertisement.Label = "z-healthy"
			client.nodes["z-healthy"] = healthy
			if secondNode {
				failed := healthy
				failed.Advertisement.NodeID = "a-failed"
				failed.Advertisement.Label = "a-failed"
				failed.Advertisement.Carriers = []carriers.Endpoint{badEP}
				client.nodes["a-failed"] = failed
			}
			client.mu.Unlock()
			if secondNode && client.ListNodes()[0].NodeID != "a-failed" {
				t.Fatal("failed node must be attempted before the healthy node")
			}
			selected := "z-healthy"
			if secondNode {
				selected = ""
			}
			if name == "pinned_failed_node" {
				selected = "a-failed"
			}
			status, err := client.Connect(ctx, selected)
			if name == "pinned_failed_node" {
				if err == nil || status.SessionActive || status.State != statusStateReconnecting {
					t.Fatalf("explicit node pin escaped to another node: status=%+v err=%v", status, err)
				}
				if got := countPayloads(t, good, goodEP, session.PayloadSessionAnswer); got != 0 {
					t.Fatalf("healthy alternate contacted despite node pin: answers=%d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("independent authenticated carrier was blocked: %v", err)
			}
			if status.State != statusStateConnected || status.ActiveNodeID != "z-healthy" {
				t.Fatalf("unexpected session: %+v", status)
			}
			if got := bad.offers.Load(); got != 1 {
				t.Fatalf("bad credential attempts = %d, want exactly 1", got)
			}
			if got := countPayloads(t, good, goodEP, session.PayloadSessionAnswer); got != 1 {
				t.Fatalf("real healthy session answers = %d, want 1", got)
			}
		})
	}
}
