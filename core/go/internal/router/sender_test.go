package router

import (
	"context"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestSendQueueDeliversEnvelope(t *testing.T) {
	health := NewCarrierHealth()
	q := NewSendQueue(health)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	defer q.Stop()

	mc := carriers.NewMemoryCarrier("test-send")
	ep := carriers.Endpoint{ID: "test-ep", Carrier: "test", Address: "memory://test"}
	env := fabric.NewEnvelope("test-1", fabric.TrafficControl, "test.payload", []byte("hello"))

	err := q.SendSync(ctx, "test", mc, ep, env)
	if err != nil {
		t.Fatalf("send sync: %v", err)
	}

	// Verify the envelope was written.
	read, err := mc.Read(ctx, ep, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(read.Envelopes))
	}
	if read.Envelopes[0].ID != "test-1" {
		t.Errorf("expected test-1, got %s", read.Envelopes[0].ID)
	}

	// Verify health was updated.
	snap := health.Snapshot()
	if snap["test"].WriteSuccesses != 1 {
		t.Errorf("expected 1 write success, got %d", snap["test"].WriteSuccesses)
	}
}

func TestSendQueueAsync(t *testing.T) {
	health := NewCarrierHealth()
	q := NewSendQueue(health)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	defer q.Stop()

	mc := carriers.NewMemoryCarrier("test-async")
	ep := carriers.Endpoint{ID: "async-ep", Carrier: "test", Address: "memory://async"}

	env1 := fabric.NewEnvelope("async-1", fabric.TrafficControl, "test", []byte("a"))
	env2 := fabric.NewEnvelope("async-2", fabric.TrafficControl, "test", []byte("b"))

	ch1 := q.Send("test", mc, ep, env1)
	ch2 := q.Send("test", mc, ep, env2)

	// Wait for both to complete.
	select {
	case err := <-ch1:
		if err != nil {
			t.Fatalf("send 1: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for send 1")
	}
	select {
	case err := <-ch2:
		if err != nil {
			t.Fatalf("send 2: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for send 2")
	}
}

func TestReadonlySnapshot(t *testing.T) {
	r := New()
	desc, _ := carriers.FindStandardDescriptor(carriers.CarrierVKMessages)
	mc := carriers.NewMemoryCarrierWithDescriptor(desc)
	ep := carriers.Endpoint{ID: "ep-1", Carrier: carriers.CarrierVKMessages}

	err := r.RegisterScope("reader-1", mc, ep, func(ctx context.Context, cid, eid string, env fabric.Envelope) {}, time.Second, ScopeDiscovery)
	if err != nil {
		t.Fatal(err)
	}
	err = r.RegisterScope("reader-2", mc, ep, func(ctx context.Context, cid, eid string, env fabric.Envelope) {}, time.Second, ScopeEgress)
	if err != nil {
		t.Fatal(err)
	}

	snap := r.ReadonlySnapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 readers, got %d", len(snap))
	}
	if snap["reader-1"].Scope != ScopeDiscovery {
		t.Errorf("expected discovery scope, got %s", snap["reader-1"].Scope)
	}
	if snap["reader-2"].Scope != ScopeEgress {
		t.Errorf("expected egress scope, got %s", snap["reader-2"].Scope)
	}
}

func TestDynamicRegistrationAfterStart(t *testing.T) {
	r := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	desc, _ := carriers.FindStandardDescriptor(carriers.CarrierVKMessages)
	mc := carriers.NewMemoryCarrierWithDescriptor(desc)
	ep := carriers.Endpoint{ID: "dyn-ep", Carrier: carriers.CarrierVKMessages}

	err := r.Register("before-start", mc, ep, func(ctx context.Context, cid, eid string, env fabric.Envelope) {}, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	// Register after start should work.
	mc2 := carriers.NewMemoryCarrierWithDescriptor(desc)
	ep2 := carriers.Endpoint{ID: "dyn-ep-2", Carrier: carriers.CarrierVKMessages}
	err = r.RegisterScope("after-start", mc2, ep2, func(ctx context.Context, cid, eid string, env fabric.Envelope) {}, 500*time.Millisecond, ScopeSession)
	if err != nil {
		t.Fatalf("register after start: %v", err)
	}
	if r.Registered() != 2 {
		t.Fatalf("expected 2 readers, got %d", r.Registered())
	}
}
