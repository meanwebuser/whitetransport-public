package carriers

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// MemoryCarrier is a deterministic in-process mailbox carrier used for tests
// and for the first daemon smoke path.
type MemoryCarrier struct {
	desc Descriptor
	mu   sync.Mutex
	box  map[string][]fabric.Envelope
}

// NewMemoryCarrier creates a healthy retained mailbox carrier.
func NewMemoryCarrier(id string) *MemoryCarrier {
	return &MemoryCarrier{
		desc: Descriptor{
			ID:           id,
			Provider:     "memory",
			Mode:         DeliveryMailbox,
			Capabilities: []Capability{CapRendezvous, CapMailbox, CapRetained},
			Limits:       Limits{MaxPayloadBytes: 1 << 20, SendsPerMinute: 600, PollsPerMinute: 600},
			Metrics:      Metrics{Healthy: true},
		},
		box: map[string][]fabric.Envelope{},
	}
}

// NewMemoryCarrierWithDescriptor creates a test carrier that advertises a
// specific catalog descriptor while retaining deterministic in-memory storage.
func NewMemoryCarrierWithDescriptor(desc Descriptor) *MemoryCarrier {
	return &MemoryCarrier{
		desc: desc,
		box:  map[string][]fabric.Envelope{},
	}
}

func (c *MemoryCarrier) Descriptor() Descriptor { return c.desc }

func (c *MemoryCarrier) Write(_ context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.box[endpoint.ID] = append(c.box[endpoint.ID], envelope)
	return nil
}

func (c *MemoryCarrier) Read(_ context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	start := 0
	if cursor != "" {
		if parsed, err := strconv.Atoi(string(cursor)); err == nil && parsed > 0 {
			start = parsed
		}
	}
	items := c.box[endpoint.ID]
	if start > len(items) {
		start = len(items)
	}
	out := append([]fabric.Envelope(nil), items[start:]...)
	return ReadResult{Envelopes: out, Cursor: Cursor(strconv.Itoa(len(items)))}, nil
}

func (c *MemoryCarrier) Probe(context.Context, Endpoint) (Metrics, error) {
	return Metrics{Healthy: true}, nil
}

func (c *MemoryCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *MemoryCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	// Memory carrier doesn't support message deletion
	return fmt.Errorf("delete message not implemented for memory carrier")
}
