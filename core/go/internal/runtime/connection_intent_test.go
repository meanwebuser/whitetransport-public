package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestConnectionIntentRecoversAfterAllNodesDisappear(t *testing.T) {
	for _, name := range []string{"recover", "disconnect_cancels", "same_node_returns_with_stale_peer"} {
		disconnect := name == "disconnect_cancels"
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			controlEP := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://intent-control"}
			egressEP := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://intent-egress"}
			bindings := map[string]policy.CarrierBinding{
				controlEP.Carrier: {Carrier: newMemoryCarrierWithDescriptor(t, "intent-control", controlEP.Carrier), Endpoint: controlEP},
				egressEP.Carrier:  {Carrier: newMemoryCarrierWithDescriptor(t, "intent-egress", egressEP.Carrier), Endpoint: egressEP},
			}
			makeNode := func(id string) *ControlPlane {
				n, e := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: id, SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
				if e != nil {
					t.Fatal(e)
				}
				return n
			}
			nodeA := makeNode("initial")
			returnedID := "returned"
			if name == "same_node_returns_with_stale_peer" {
				returnedID = "initial"
			}
			nodeB := makeNode(returnedID)
			client, e := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "intent-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Stop()
			defer nodeB.Stop()
			defer nodeA.Stop()
			aCtx, stopA := context.WithCancel(ctx)
			defer stopA()
			nodeA.Start(aCtx)
			client.Start(ctx)
			waitForNodeVisible(t, client, "initial", true)
			requestCtx, endRequest := context.WithCancel(ctx)
			if _, e := client.Connect(requestCtx, "initial"); e != nil {
				t.Fatal(e)
			}
			endRequest() // HTTP request completion must not erase user intent.
			stopA()
			active := client.activeSessionSnapshot()
			client.mu.Lock()
			client.nodes = map[string]discoveredNode{}
			client.mu.Unlock()
			if _, _, e := client.reselectNodeAfterCarrierExhaustion(ctx, active, ""); e == nil {
				t.Fatal("outage must fail its immediate replacement attempt")
			}
			if disconnect {
				client.Disconnect()
			}
			if name == "same_node_returns_with_stale_peer" {
				client.storeAdvertisement(session.NodeAdvertisement{NodeID: "stale-peer", Carriers: []carriers.Endpoint{controlEP}})
			}
			nodeB.Start(ctx)
			waitForNodeVisible(t, client, returnedID, true)
			deadline := time.Now().Add(3500 * time.Millisecond)
			for time.Now().Before(deadline) {
				status := client.Status()
				if disconnect && status.SessionActive {
					t.Fatalf("Disconnect resurrected session: %+v", status)
				}
				if !disconnect && status.ActiveNodeID == returnedID && status.SessionActive {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !disconnect {
				t.Fatalf("path returned but session did not recover without another Connect/Dial: %+v", client.Status())
			}
		})
	}
}

type gatedSessionAnswerCarrier struct {
	*memoryCarrier
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *gatedSessionAnswerCarrier) Write(ctx context.Context, ep carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.PayloadType == session.PayloadSessionAnswer {
		c.once.Do(func() { close(c.started) })
		select {
		case <-c.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.memoryCarrier.Write(ctx, ep, envelope)
}

func TestConnectionIntentCancellationRejectsLateAnswer(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "disconnect"
		if stop {
			name = "stop"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://late-answer"}
			carrier := &gatedSessionAnswerCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "late-answer", ep.Carrier), started: make(chan struct{}), release: make(chan struct{})}
			egressEP := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://late-egress"}
			bindings := map[string]policy.CarrierBinding{ep.Carrier: {Carrier: carrier, Endpoint: ep}, egressEP.Carrier: {Carrier: newMemoryCarrierWithDescriptor(t, "late-egress", egressEP.Carrier), Endpoint: egressEP}}
			node, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "late-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "late-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Stop()
			defer node.Stop()
			client.Start(ctx)
			if _, err := client.Connect(ctx, ""); err == nil {
				t.Fatal("expected initial no-node failure")
			}
			node.Start(ctx)
			select {
			case <-carrier.started:
			case <-ctx.Done():
				t.Fatal("background retry did not reach answer")
			}
			done := make(chan struct{})
			go func() {
				if stop {
					client.Stop()
				} else {
					client.Disconnect()
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("explicit cancellation waited for delayed answer")
			}
			close(carrier.release)
			time.Sleep(1200 * time.Millisecond)
			if status := client.Status(); status.SessionActive || status.State != statusStateDisconnected {
				t.Fatalf("late answer revived cancelled intent: %+v", status)
			}
		})
	}
}

func TestNodeRecoveryMutexWaitRespectsRequestDeadline(t *testing.T) {
	c := &ControlPlane{}
	c.nodeAutoHealMu.Lock()
	defer c.nodeAutoHealMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := c.DialEgress(ctx, "unused:80"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected request deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("recovery mutex ignored request cancellation")
	}
}

func TestDisconnectCancelsQueuedConnect(t *testing.T) {
	c := &ControlPlane{state: statusStateDisconnected, nodes: map[string]discoveredNode{}}
	c.nodeAutoHealMu.Lock()
	result := make(chan error, 1)
	go func() { _, err := c.Connect(context.Background(), ""); result <- err }()
	time.Sleep(40 * time.Millisecond) // Allow Connect to reach the held recovery lock.
	c.cancelConnectionIntent()        // The first, non-blocking phase of Disconnect/Stop.
	c.nodeAutoHealMu.Unlock()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("queued Connect did not finish")
	}
	c.mu.RLock()
	intent := c.clientIntentCtx
	c.mu.RUnlock()
	if intent != nil {
		t.Fatal("queued Connect recreated intent after Disconnect cancellation")
	}
}
