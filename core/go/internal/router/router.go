package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// ReadScope describes why a carrier reader exists.
type ReadScope string

const (
	ScopeDiscovery ReadScope = "discovery"
	ScopeSession   ReadScope = "session"
	ScopeEgress    ReadScope = "egress"
)

// ReaderStats captures per-reader metrics for health reporting.
type ReaderStats struct {
	CarrierID   string    `json:"carrier_id"`
	EndpointID  string    `json:"endpoint_id"`
	Scope       ReadScope `json:"scope"`
	LastReadAt  time.Time `json:"last_read_at,omitempty"`
	ReadErrors  int64     `json:"read_errors"`
	EnvelopesIn int64     `json:"envelopes_in"`
}

// EnvelopeHandler processes a single envelope received from a carrier.
type EnvelopeHandler func(ctx context.Context, carrierID string, endpointID string, envelope fabric.Envelope)

type carrierReader struct {
	carrier      carriers.Carrier
	endpoint     carriers.Endpoint
	handler      EnvelopeHandler
	interval     time.Duration
	scope        ReadScope
	cursor       carriers.Cursor
	cursorChange func(carrierID, endpointID string, cursor carriers.Cursor)
	stats        ReaderStats
}

// CarrierRouter owns all carrier read paths. Each carrier+endpoint+scope tuple
// is polled by exactly one goroutine, enforcing a single-owner read path.
type CarrierRouter struct {
	mu      sync.RWMutex
	readers map[string]*carrierReader
	health  *CarrierHealth
	running bool
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	ctx     context.Context
}

// New creates an empty CarrierRouter.
func New() *CarrierRouter {
	return NewWithHealth(nil)
}

// NewWithHealth creates a router that publishes read outcomes to the shared
// carrier-health surface under the configured endpoint ID.
func NewWithHealth(health *CarrierHealth) *CarrierRouter {
	return &CarrierRouter{readers: make(map[string]*carrierReader), health: health}
}

// Register adds a carrier+endpoint pair to be polled at the given interval.
func (r *CarrierRouter) Register(key string, carrier carriers.Carrier, endpoint carriers.Endpoint, handler EnvelopeHandler, interval time.Duration) error {
	return r.RegisterScope(key, carrier, endpoint, handler, interval, ScopeDiscovery)
}

// RegisterScope adds a carrier+endpoint+scope tuple to be polled.
// The same carrier+endpoint can be registered with different scopes.
func (r *CarrierRouter) RegisterScope(key string, carrier carriers.Carrier, endpoint carriers.Endpoint, handler EnvelopeHandler, interval time.Duration, scope ReadScope) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.readers[key]; ok {
		return fmt.Errorf("router: %s is already registered (single-owner read path)", key)
	}
	// Allow same carrier+endpoint with different scopes.
	for k, reader := range r.readers {
		if reader.carrier == carrier && reader.endpoint.ID == endpoint.ID && reader.scope == scope {
			return fmt.Errorf("router: carrier+endpoint+scope (%s, %s, %s) already registered as %q", carrier.Descriptor().ID, endpoint.ID, scope, k)
		}
	}
	cr := &carrierReader{
		carrier:  carrier,
		endpoint: endpoint,
		handler:  handler,
		interval: interval,
		scope:    scope,
		stats:    ReaderStats{CarrierID: carrier.Descriptor().ID, EndpointID: endpoint.ID, Scope: scope},
	}
	r.readers[key] = cr

	// If already running, spawn the reader goroutine immediately.
	if r.running && r.ctx != nil {
		r.wg.Add(1)
		go r.runReader(r.ctx, key, cr)
	}
	return nil
}

// Unregister removes a carrier+endpoint pair from polling.
func (r *CarrierRouter) Unregister(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.readers, key)
}

// Start begins polling all registered carriers in separate goroutines.
func (r *CarrierRouter) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return fmt.Errorf("router: already started")
	}
	r.running = true
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.ctx = ctx
	for key, reader := range r.readers {
		key := key
		reader := reader
		r.wg.Add(1)
		go r.runReader(ctx, key, reader)
	}
	return nil
}

// SetCursorHandler registers a callback that fires after each successful Read
// with the updated cursor for the given reader key.
func (r *CarrierRouter) SetCursorHandler(key string, handler func(carrierID, endpointID string, cursor carriers.Cursor)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	reader, ok := r.readers[key]
	if !ok {
		return fmt.Errorf("router: reader %s not found", key)
	}
	reader.cursorChange = handler
	return nil
}

// Stop cancels all polling goroutines and waits for them to finish.
func (r *CarrierRouter) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.running = false
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

// Registered returns the number of registered carrier readers.
func (r *CarrierRouter) Registered() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.readers)
}

// ReadonlySnapshot returns a point-in-time snapshot of all reader stats.
func (r *CarrierRouter) ReadonlySnapshot() map[string]ReaderStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]ReaderStats, len(r.readers))
	for key, reader := range r.readers {
		out[key] = reader.stats
	}
	return out
}

func (r *CarrierRouter) runReader(ctx context.Context, key string, reader *carrierReader) {
	defer r.wg.Done()
	ticker := time.NewTicker(reader.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := reader.carrier.Read(ctx, reader.endpoint, reader.cursor)
			if err != nil {
				if ctx.Err() == nil && r.health != nil {
					r.health.RecordReadFailure(reader.endpoint.ID)
				}
				r.mu.Lock()
				reader.stats.ReadErrors++
				r.mu.Unlock()
				continue
			}
			if r.health != nil {
				r.health.RecordReadSuccess(reader.endpoint.ID)
			}
			reader.cursor = result.Cursor
			if reader.cursorChange != nil {
				reader.cursorChange(reader.carrier.Descriptor().ID, reader.endpoint.ID, reader.cursor)
			}
			r.mu.Lock()
			reader.stats.LastReadAt = time.Now().UTC()
			reader.stats.EnvelopesIn += int64(len(result.Envelopes))
			r.mu.Unlock()
			for _, envelope := range result.Envelopes {
				reader.handler(ctx, key, reader.endpoint.ID, envelope)
			}
		}
	}
}
