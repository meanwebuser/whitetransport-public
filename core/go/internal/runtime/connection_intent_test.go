package runtime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

type recoveryBudgetCarrier struct {
	*memoryCarrier
	remaining chan time.Duration
}

func TestConnectionRecoveryContextWithoutRoomPreservesDeadlineAndCancellation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		c := &ControlPlane{cfg: config.Config{ClientRoomCreation: enabled}}
		parent, cancelParent := context.WithCancel(context.Background())
		ctx, cancel := c.connectionRecoveryContext(parent)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > nodeReselectionTimeout || time.Until(deadline) < nodeReselectionTimeout-time.Second {
			t.Fatal("no eligible room must retain the original bounded control budget")
		}
		cancelParent()
		if ctx.Err() != context.Canceled {
			t.Fatal("room budget detached the parent cancellation")
		}
		cancel()
	}
}

func (c *recoveryBudgetCarrier) Write(ctx context.Context, ep carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.PayloadType == session.PayloadSessionOffer {
		if deadline, ok := ctx.Deadline(); ok {
			select {
			case c.remaining <- time.Until(deadline):
			default:
			}
		}
	}
	return c.memoryCarrier.Write(ctx, ep, envelope)
}

func TestBackgroundRecoveryPreservesControlBudgetAfterRoomPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ep := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://recovery-budget"}
	control := &recoveryBudgetCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "recovery-budget", ep.Carrier), remaining: make(chan time.Duration, 1)}
	egressEP := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://budget-egress"}
	nodeBindings := map[string]policy.CarrierBinding{ep.Carrier: {Carrier: control, Endpoint: ep}, egressEP.Carrier: {Carrier: newMemoryCarrierWithDescriptor(t, "budget-egress", egressEP.Carrier), Endpoint: egressEP}}
	clientBindings := make(map[string]policy.CarrierBinding)
	for id, binding := range nodeBindings {
		clientBindings[id] = binding
	}
	room := &roomPreparationProvider{fakeVideoProvider: &fakeVideoProvider{}, id: "slow-room", create: func(ctx context.Context) (string, error) {
		// Exercise setup before offer dispatch without spending a real8s.
		select {
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "", fmt.Errorf("room unavailable")
	}}
	roomCarrier, err := carriers.NewProviderCarrier(room, carriers.Endpoint{ID: "slow-room", Carrier: "slow-room"})
	if err != nil {
		t.Fatal(err)
	}
	clientBindings["slow-room"] = policy.CarrierBinding{Carrier: roomCarrier}
	node, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "budget-node", SocksListen: "127.0.0.1:0"}, nodeBindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "budget-client", SocksListen: "127.0.0.1:0", ClientRoomCreation: true}, clientBindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Stop()
	defer client.Stop()
	node.Start(ctx)
	client.Start(ctx)
	waitForNodeVisible(t, client, "budget-node", true)
	client.mu.Lock()
	client.clientIntentCtx, client.clientIntentCancel = context.WithCancel(ctx)
	client.mu.Unlock()
	client.recoverConnectionIntent()
	if !client.Status().SessionActive {
		t.Fatalf("background recovery failed: %+v", client.Status())
	}
	remaining := <-control.remaining
	if remaining < nodeReselectionTimeout+8*time.Second-time.Second {
		t.Fatalf("offer control deadline remaining=%s: room setup consumed the original control budget", remaining)
	}
}

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
			node.mu.RLock()
			busy := node.nodeBusy
			node.mu.RUnlock()
			if busy {
				t.Fatal("cancelled offer left node busy until the full session lease expired")
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
