package router

import (
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

// CarrierSnapshot is the health summary of a single carrier.
type CarrierSnapshot struct {
	CarrierID      string    `json:"carrier_id"`
	Healthy        bool      `json:"healthy"`
	ReadSuccesses  int64     `json:"read_successes"`
	ReadFailures   int64     `json:"read_failures"`
	WriteSuccesses int64     `json:"write_successes"`
	WriteFailures  int64     `json:"write_failures"`
	Reliability    float64   `json:"reliability"`
	LastSuccessAt  time.Time `json:"last_success_at,omitempty"`
	LastFailureAt  time.Time `json:"last_failure_at,omitempty"`
}

// CarrierHealth tracks per-carrier read/write success and failure rates.
type CarrierHealth struct {
	mu           sync.RWMutex
	entries      map[string]*carrierHealthEntry
	TokenChecker tokens.TokenHealthChecker // optional token health gate
}

type carrierHealthEntry struct {
	readSuccesses  int64
	readFailures   int64
	writeSuccesses int64
	writeFailures  int64
	lastSuccessAt  time.Time
	lastFailureAt  time.Time
}

// NewCarrierHealth creates a new health tracker.
func NewCarrierHealth() *CarrierHealth {
	return &CarrierHealth{entries: make(map[string]*carrierHealthEntry)}
}

// RecordReadSuccess records a successful read from a carrier.
func (h *CarrierHealth) RecordReadSuccess(carrierID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.getEntry(carrierID)
	e.readSuccesses++
	e.lastSuccessAt = time.Now().UTC()
}

// RecordReadFailure records a failed read from a carrier.
func (h *CarrierHealth) RecordReadFailure(carrierID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.getEntry(carrierID)
	e.readFailures++
	e.lastFailureAt = time.Now().UTC()
}

// RecordWriteSuccess records a successful write to a carrier.
func (h *CarrierHealth) RecordWriteSuccess(carrierID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.getEntry(carrierID)
	e.writeSuccesses++
	e.lastSuccessAt = time.Now().UTC()
}

// RecordWriteFailure records a failed write to a carrier.
func (h *CarrierHealth) RecordWriteFailure(carrierID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.getEntry(carrierID)
	e.writeFailures++
	e.lastFailureAt = time.Now().UTC()
}

// Snapshot returns a point-in-time health summary of all tracked carriers.
func (h *CarrierHealth) Snapshot() map[string]CarrierSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]CarrierSnapshot, len(h.entries))
	for id, e := range h.entries {
		total := e.readSuccesses + e.readFailures + e.writeSuccesses + e.writeFailures
		reliability := 0.0
		if total > 0 {
			reliability = float64(e.readSuccesses+e.writeSuccesses) / float64(total)
		}
		out[id] = CarrierSnapshot{
			CarrierID:      id,
			Healthy:        e.readFailures == 0 || e.readSuccesses > e.readFailures,
			ReadSuccesses:  e.readSuccesses,
			ReadFailures:   e.readFailures,
			WriteSuccesses: e.writeSuccesses,
			WriteFailures:  e.writeFailures,
			Reliability:    reliability,
			LastSuccessAt:  e.lastSuccessAt,
			LastFailureAt:  e.lastFailureAt,
		}
	}
	return out
}

// Metrics returns health metrics for a specific carrier, compatible with
// carriers.Metrics for policy engine consumption. When a TokenChecker is
// attached, healthy = transportHealthy && tokenHealthy.
// Reliability is computed from read/write success ratios.
func (h *CarrierHealth) Metrics(carrierID string) carriers.Metrics {
	h.mu.RLock()
	defer h.mu.RUnlock()
	e, ok := h.entries[carrierID]
	transportHealthy := true
	if ok {
		transportHealthy = e.readFailures == 0 || e.readSuccesses > e.readFailures
	}
	healthy := transportHealthy
	if h.TokenChecker != nil && !h.TokenChecker.IsCarrierHealthy(carrierID) {
		healthy = false
	}
	var lastOK time.Time
	if ok {
		lastOK = e.lastSuccessAt
	}
	m := carriers.Metrics{
		Healthy: healthy,
		LastOK:  lastOK,
	}
	if ok {
		total := e.readSuccesses + e.readFailures + e.writeSuccesses + e.writeFailures
		if total > 0 {
			m.Reliability = float64(e.readSuccesses+e.writeSuccesses) / float64(total)
		}
	}
	return m
}

func (h *CarrierHealth) getEntry(carrierID string) *carrierHealthEntry {
	e, ok := h.entries[carrierID]
	if !ok {
		e = &carrierHealthEntry{}
		h.entries[carrierID] = e
	}
	return e
}
