package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestExpireStaleNodesMarksWithdrawn(t *testing.T) {
	cp := &ControlPlane{
		cfg:   config.Config{Role: config.RoleClient, ClientID: "test"},
		nodes: make(map[string]discoveredNode),
	}

	// Add a stale node (seen 10 minutes ago).
	cp.nodes["stale-node"] = discoveredNode{
		LastSeenAt: time.Now().UTC().Add(-10 * time.Minute),
		Withdrawn:  false,
	}
	// Add a fresh node.
	cp.nodes["fresh-node"] = discoveredNode{
		LastSeenAt: time.Now().UTC(),
		Withdrawn:  false,
	}

	cp.expireStaleNodes()

	nodes := cp.ListNodes()
	for _, n := range nodes {
		switch n.NodeID {
		case "stale-node":
			if n.Available {
				t.Errorf("stale-node should be withdrawn, got available=true")
			}
		case "fresh-node":
			if !n.Available {
				t.Errorf("fresh-node should be available, got available=false")
			}
		}
	}
}

func TestStoreHeartbeatRefreshesNode(t *testing.T) {
	cp := &ControlPlane{
		cfg:   config.Config{Role: config.RoleClient, ClientID: "test"},
		nodes: make(map[string]discoveredNode),
	}

	// Node was stale but heartbeat arrives.
	cp.nodes["example-exit-node"] = discoveredNode{
		LastSeenAt: time.Now().UTC().Add(-10 * time.Minute),
		Withdrawn:  true,
	}

	now := time.Now().UTC()
	cp.storeHeartbeat(session.NodeHeartbeat{
		NodeID:    "example-exit-node",
		Timestamp: now,
	})

	cp.mu.RLock()
	node := cp.nodes["example-exit-node"]
	cp.mu.RUnlock()

	if node.Withdrawn {
		t.Error("heartbeat should clear withdrawn flag")
	}
	if node.LastSeenAt.Before(now.Add(-time.Second)) {
		t.Errorf("heartbeat should update LastSeenAt, got %v", node.LastSeenAt)
	}
}

func TestReconnectStateTransition(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://reconnect-state-control"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "reconnect-state-control", carriers.CarrierVKMessages)

	cp, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "client-reconnect", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer cp.Stop()
	cp.Start(ctx)

	// Simulate connected state then force reconnecting.
	cp.mu.Lock()
	cp.state = statusStateConnected
	cp.active = &activeSession{NodeID: "example-exit-node", SessionID: "sess-1"}
	cp.mu.Unlock()

	// Verify status shows connected.
	if cp.Status().State != statusStateConnected {
		t.Fatalf("expected connected, got %s", cp.Status().State)
	}

	// Simulate setting reconnecting.
	cp.mu.Lock()
	cp.state = statusStateReconnecting
	cp.reconnectAttempts = 1
	cp.mu.Unlock()

	status := cp.Status()
	if status.State != statusStateReconnecting {
		t.Fatalf("expected reconnecting, got %s", status.State)
	}
	if status.ReconnectAttempts != 1 {
		t.Fatalf("expected reconnect attempts=1, got %d", status.ReconnectAttempts)
	}
}
