package router

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// blockingCarrier is a fake carrier whose Read blocks until the context is
// cancelled, simulating a slow or stuck read path.
type blockingCarrier struct {
	blockOnRead chan struct{}
	mu          sync.Mutex
	blocked     bool
}

func (b *blockingCarrier) Descriptor() carriers.Descriptor {
	return carriers.Descriptor{
		ID:             "test.blocking",
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap},
	}
}

func (b *blockingCarrier) Read(ctx context.Context, endpoint carriers.Endpoint, cursor carriers.Cursor) (carriers.ReadResult, error) {
	b.mu.Lock()
	b.blocked = true
	ch := b.blockOnRead
	b.mu.Unlock()
	select {
	case <-ch:
		return carriers.ReadResult{}, nil
	case <-ctx.Done():
		return carriers.ReadResult{}, ctx.Err()
	}
}

func (b *blockingCarrier) Write(ctx context.Context, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	return nil
}

func (b *blockingCarrier) Probe(ctx context.Context, endpoint carriers.Endpoint) (carriers.Metrics, error) {
	return carriers.Metrics{}, nil
}

func (b *blockingCarrier) DeleteMessage(ctx context.Context, endpoint carriers.Endpoint, id string) error {
	return nil
}

func (b *blockingCarrier) Unblock() {
	b.mu.Lock()
	if b.blocked {
		close(b.blockOnRead)
		b.blocked = false
	}
	b.mu.Unlock()
}

func TestRouterShutdownFast(t *testing.T) {
	bc := &blockingCarrier{
		blockOnRead: make(chan struct{}),
	}
	r := New()
	ep := carriers.Endpoint{ID: "test-ep", Address: "test-addr"}

	err := r.Register("test-key", bc, ep, func(ctx context.Context, carrierID, endpointID string, envelope fabric.Envelope) {
		// noop handler
	}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Give the reader goroutine time to start and begin the blocking Read.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	cancel()
	r.Stop() // waits for all goroutines to finish
	elapsed := time.Since(start)

	t.Logf("shutdown took %v", elapsed)

	// The read is blocking, but context cancellation should cause Read to
	// return quickly (ctx.Err). The Stop should complete in well under a
	// second. Allow 2s for CI/vm noise.
	if elapsed > 2*time.Second {
		t.Errorf("shutdown too slow: %v (want < 2s)", elapsed)
	}
}
