package router

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestRouterNoSharedReadAcrossEndpoints(t *testing.T) {
	r := New()
	mc := carriers.NewMemoryCarrier("mem")
	ep1 := carriers.Endpoint{ID: "ep1", Carrier: "mem", Address: "mem://ep1"}
	ep2 := carriers.Endpoint{ID: "ep2", Carrier: "mem", Address: "mem://ep2"}
	var mu sync.Mutex
	readCounts := make(map[string]int)
	handler := func(_ context.Context, id, eid string, _ fabric.Envelope) {
		mu.Lock()
		readCounts[id+":"+eid]++
		mu.Unlock()
	}
	if err := r.Register("ep1", mc, ep1, handler, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("ep2", mc, ep2, handler, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	env := fabric.NewEnvelope("t1", fabric.TrafficBootstrap, "test", []byte("hello"))
	_ = mc.Write(ctx, ep1, env)
	_ = mc.Write(ctx, ep2, env)
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if readCounts["ep1:ep1"] == 0 || readCounts["ep2:ep2"] == 0 {
		t.Fatalf("handlers missing counts: %+v", readCounts)
	}
}
