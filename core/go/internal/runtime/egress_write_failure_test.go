package runtime

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/router"
	"github.com/meanwebuser/whitetransport/core/internal/session"
	"github.com/meanwebuser/whitetransport/core/internal/tunnel"
)

func TestNodeEgressWriteFailureUnblocksClientAndRoutesNextRequest(t *testing.T) {
	t.Run("composite with legacy polling", func(t *testing.T) {
		t.Setenv("WT_CAPABILITY_SCORING", "0")
		testNodeEgressWriteFailure(t, false)
	})
	t.Run("unified with carrier router", func(t *testing.T) {
		t.Setenv("WT_CAPABILITY_SCORING", "1")
		testNodeEgressWriteFailure(t, true)
	})
}

func testNodeEgressWriteFailure(t *testing.T, useRouter bool) {
	t.Helper()
	const nodeID = "return-failure-node"
	const clientID = "return-failure-client"
	control := newMemoryCarrierWithDescriptor(t, "return-failure-control", carriers.CarrierVKMessages)
	primary := &failingReturnCarrier{
		memoryCarrier: newMemoryCarrierWithDescriptor(t, "return-failure-primary", carriers.CarrierVKDocs1024),
		nodeID:        nodeID,
	}
	backup := newMemoryCarrierWithDescriptor(t, "return-failure-backup", carriers.CarrierOKDocs256)
	bindings := make(map[string]policy.CarrierBinding)
	for _, adapter := range []carriers.Carrier{control, primary, backup} {
		id := adapter.Descriptor().ID
		bindings[id] = policy.CarrierBinding{
			Carrier: adapter, Endpoint: carriers.Endpoint{ID: id, Carrier: id, Address: "memory://" + id},
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 14*time.Second)
	defer cancel()
	makeRuntime := func(role config.Role, identity string) *ControlPlane {
		cfg := config.Config{Role: role, SocksListen: "127.0.0.1:0", BootstrapSecret: "stream-failure-fixture"}
		if role == config.RoleNode {
			cfg.NodeID = identity
		} else {
			cfg.ClientID = identity
		}
		plane, err := newTestControlPlane(cfg, bindings, policy.DefaultAdaptivePolicy(), tunnel.Dialer(cfg, bindings))
		if err != nil {
			t.Fatal(err)
		}
		if useRouter {
			controlRouter := router.New()
			plane.SetRouter(controlRouter, nil, nil)
			plane.Start(ctx)
			if err := controlRouter.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(controlRouter.Stop)
			return plane
		}
		plane.Start(ctx)
		return plane
	}
	node := makeRuntime(config.RoleNode, nodeID)
	defer node.Stop()
	client := makeRuntime(config.RoleClient, clientID)
	defer client.Stop()
	waitForNodeVisible(t, client, nodeID, true)
	first, err := client.Connect(ctx, nodeID)
	if err != nil || first.State != statusStateConnected {
		t.Fatalf("connect: state=%s err=%v", first.State, err)
	}
	client.mu.Lock()
	client.active.AutomaticEgressEndpointID = carriers.CarrierVKDocs1024
	client.mu.Unlock()

	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer origin.Close()
	go func() {
		for {
			conn, err := origin.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	open := func(wantRoute string) net.Conn {
		conn, route, err := client.DialEgress(ctx, origin.Addr().String())
		if err != nil {
			t.Fatalf("dial %s: %v", wantRoute, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if route != wantRoute {
			t.Fatalf("route=%s, want %s", route, wantRoute)
		}
		return conn
	}
	assertPayload := func(conn net.Conn, payload string) {
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
		if _, err := io.WriteString(conn, payload); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != payload {
			t.Fatalf("readback=%q err=%v", got, err)
		}
	}
	conn := open(carriers.CarrierVKDocs1024)
	assertPayload(conn, "first flight")
	primary.block.Store(true)
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := io.WriteString(conn, "remaining response"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) || !primary.rejected.Load() {
		t.Fatalf("expected immediate EOF after node write failure, got %v (write failed=%t)", err, primary.rejected.Load())
	}
	if degraded := client.Status(); degraded.State != statusStateDegraded || degraded.LastError != session.ErrEgressReturnCarrier.Error() {
		t.Fatalf("return failure did not reach connection status: state=%s error=%s", degraded.State, degraded.LastError)
	}
	assertPayload(open(carriers.CarrierOKDocs256), "backup response")
	if active := client.Status(); !active.SessionActive || active.SessionID != first.SessionID || active.LastError != "" {
		t.Fatal("route recovery replaced or disconnected the active session")
	}
}

type failingReturnCarrier struct {
	*memoryCarrier
	nodeID   string
	block    atomic.Bool
	rejected atomic.Bool
}

func (c *failingReturnCarrier) Write(ctx context.Context, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	if c.block.Load() && envelope.Source == c.nodeID {
		c.rejected.Store(true)
		return errors.New("injected document upload failure")
	}
	return c.memoryCarrier.Write(ctx, endpoint, envelope)
}
