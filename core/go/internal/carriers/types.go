package carriers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// DeliveryMode is the carrier transport shape visible to policy.
type DeliveryMode string

const (
	DeliveryMailbox DeliveryMode = "mailbox"
	DeliveryStream  DeliveryMode = "stream"
	DeliveryBulk    DeliveryMode = "bulk"
)

// Capability describes a carrier property used by policy and session
// negotiation. It is deliberately about delivery behavior, not provider brand.
type Capability string

const (
	CapRendezvous    Capability = "rendezvous"
	CapDatagram      Capability = "datagram"
	CapStream        Capability = "stream"
	CapMailbox       Capability = "mailbox"
	CapBulk          Capability = "bulk"
	CapRetained      Capability = "retained"
	CapDuplex        Capability = "duplex"
	CapRetrospective Capability = "retrospective" // can read past messages
	CapMutable       Capability = "mutable"       // can delete/modify past messages

	// Extended capabilities (vision §8-9). Carriers selectively declare these
	// so the policy scorer can reward fine-grained property matches.
	CapList            Capability = "list"
	CapPoll            Capability = "poll"
	CapSubscribe       Capability = "subscribe"
	CapEdit            Capability = "edit"
	CapDelete          Capability = "delete"
	CapOverwrite       Capability = "overwrite"
	CapAppendOnly      Capability = "append_only"
	CapEphemeral       Capability = "ephemeral"
	CapDurable         Capability = "durable"
	CapOrdered         Capability = "ordered"
	CapIdempotentWrite Capability = "idempotent_write"
	// CapTCP and CapUDP describe the actual probe shapes an adapter can
	// measure. They are orthogonal to CapStream: a carrier may carry a stream
	// while its control path cannot expose datagrams to the consumer.
	CapTCP           Capability = "tcp"
	CapUDP           Capability = "udp"
	CapAuthenticated Capability = "authenticated"
	CapHealthProbe   Capability = "health_probe"
)

// MetricSource identifies where a metric came from. Catalog values are prior
// knowledge; probe and payload values are posterior observations.
type MetricSource string

const (
	MetricSourceCatalog MetricSource = "catalog"
	MetricSourceProbe   MetricSource = "probe"
	MetricSourcePayload MetricSource = "payload"
)

// Limits describe carrier cost and sizing constraints.
type Limits struct {
	MaxPayloadBytes   int
	ChunkPayloadBytes int
	SendsPerMinute    int
	PollsPerMinute    int
	DailyBytes        int64
}

// Metrics are live health inputs used by the policy engine.
type Metrics struct {
	Healthy       bool
	Latency       time.Duration
	BandwidthBPS  int64
	LastOK        time.Time
	FailureReason string
	// Reliability is a 0.0–1.0 success rate derived from read/write outcomes.
	// A value of 0 means "no data yet"; the scorer treats it as 1.0 (unknown-good).
	Reliability float64
	// CostPerMB is an abstract cost unit per megabyte transferred (0 = free).
	CostPerMB float64
	// QuotaRemaining is bytes remaining in the daily quota (-1 = unlimited).
	QuotaRemaining int64
}

// MetricObservation is one posterior update for a descriptor. Metrics keeps
// the existing compact shape while the source and timestamp make it possible
// to distinguish modeled catalog values from measured runtime evidence.
type MetricObservation struct {
	Source     MetricSource
	ObservedAt time.Time
	HealthySet bool
	Metrics    Metrics
}

// EvidenceState stores posterior observations without putting mutable state in
// policy. A carrier owns the state and Descriptor.EffectiveMetrics presents the
// latest evidence to generic policy code.
type EvidenceState struct {
	mu           sync.RWMutex
	prior        MetricObservation
	hasPrior     bool
	observations []MetricObservation
}

func NewEvidenceState() *EvidenceState { return &EvidenceState{} }

// Observe records one bounded observation. The state intentionally retains a
// short history so receipts can explain a failover without retaining payloads.
func (e *EvidenceState) Observe(source MetricSource, metrics Metrics, observedAt time.Time) {
	if e == nil {
		return
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	e.mu.Lock()
	e.observations = append(e.observations, MetricObservation{Source: source, ObservedAt: observedAt, HealthySet: true, Metrics: metrics})
	if len(e.observations) > 32 {
		e.observations = append([]MetricObservation(nil), e.observations[len(e.observations)-32:]...)
	}
	e.mu.Unlock()
}

// AdoptPrior promotes a previously measured value to the next process
// baseline. The embedded catalog remains the fallback for fields that were
// not measured, while a fresh Observe call can replace this baseline during
// the current process.
func (e *EvidenceState) AdoptPrior(metrics Metrics, observedAt time.Time) {
	if e == nil {
		return
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	e.mu.Lock()
	e.prior = MetricObservation{
		Source:     MetricSourceCatalog,
		ObservedAt: observedAt,
		HealthySet: true,
		Metrics:    metrics,
	}
	e.hasPrior = true
	e.observations = nil
	e.mu.Unlock()
}

func (e *EvidenceState) Prior() (MetricObservation, bool) {
	if e == nil {
		return MetricObservation{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.hasPrior {
		return MetricObservation{}, false
	}
	return e.prior, true
}

func (e *EvidenceState) Latest() (MetricObservation, bool) {
	if e == nil {
		return MetricObservation{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.observations) == 0 {
		return MetricObservation{}, false
	}
	return e.observations[len(e.observations)-1], true
}

func (e *EvidenceState) History() []MetricObservation {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]MetricObservation(nil), e.observations...)
}

// applyObservation overlays known measured fields while preserving catalog
// fields for dimensions the probe did not measure.
func applyObservation(base Metrics, observation MetricObservation) Metrics {
	observed := observation.Metrics
	if observation.HealthySet || observed.FailureReason != "" || !observed.LastOK.IsZero() {
		base.Healthy = observed.Healthy
	}
	if observed.Latency > 0 {
		base.Latency = observed.Latency
	}
	if observed.BandwidthBPS > 0 {
		base.BandwidthBPS = observed.BandwidthBPS
	}
	if observed.LastOK.After(base.LastOK) {
		base.LastOK = observed.LastOK
	}
	if observed.FailureReason != "" {
		base.FailureReason = observed.FailureReason
	}
	if observed.Reliability > 0 {
		base.Reliability = observed.Reliability
	}
	if observed.CostPerMB > 0 {
		base.CostPerMB = observed.CostPerMB
	}
	if observed.QuotaRemaining != 0 {
		base.QuotaRemaining = observed.QuotaRemaining
	}
	return base
}

// Descriptor is a stable carrier description advertised by clients and nodes.
type Descriptor struct {
	ID             string
	Provider       string
	Mode           DeliveryMode
	TrafficClasses []fabric.TrafficClass
	Capabilities   []Capability
	Limits         Limits
	Metrics        Metrics
	Notes          string
	// Evidence is process-local mutable state and is never serialized as part
	// of a descriptor advertised to a peer.
	Evidence *EvidenceState `json:"-"`
}

// EffectiveMetrics returns prior catalog metrics overlaid with the latest
// posterior observation. Callers should use this instead of reading Metrics
// directly when ranking or exposing a route plan.
func (d Descriptor) EffectiveMetrics() Metrics {
	metrics := d.Metrics
	if prior, ok := d.Evidence.Prior(); ok {
		metrics = applyObservation(metrics, prior)
	}
	if latest, ok := d.Evidence.Latest(); ok {
		metrics = applyObservation(metrics, latest)
	}
	return metrics
}

// Observe records posterior evidence for this descriptor when it owns an
// evidence state. A descriptor without state remains a static catalog value.
func (d Descriptor) Observe(source MetricSource, metrics Metrics, observedAt time.Time) {
	if d.Evidence != nil {
		d.Evidence.Observe(source, metrics, observedAt)
	}
}

// AdoptPrior promotes persisted measured metrics to the next process baseline.
func (d Descriptor) AdoptPrior(metrics Metrics, observedAt time.Time) {
	if d.Evidence != nil {
		d.Evidence.AdoptPrior(metrics, observedAt)
	}
}

// Endpoint is a session-specific address for a carrier, such as a VK peer id,
// Yandex Disk folder, or WBStream room.
type Endpoint struct {
	ID       string
	Carrier  string
	Address  string
	Metadata map[string]string
}

// Cursor records carrier-specific read progress.
type Cursor string

// ReadResult returns envelopes and an updated cursor.
type ReadResult struct {
	Envelopes []fabric.Envelope
	Cursor    Cursor
}

// Carrier is the common interface for every delivery mechanism. Concrete
// adapters may be slow mailboxes, high-bandwidth realtime streams, or bulk
// object stores.
type Carrier interface {
	Descriptor() Descriptor
	Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error
	Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error)
	Probe(ctx context.Context, endpoint Endpoint) (Metrics, error)
	// DeleteMessage deletes a specific message by its ID. Not all carriers support deletion.
	// If deletion is not supported, this method returns an error.
	DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error
}

// SafeEgressRecoveryProber marks a carrier whose recovery check is known not
// to create a room, call, or foreground egress payload. It is intentionally
// separate from Carrier.Probe because ProviderCarrier.Probe may send data into
// an active provider session and is therefore not safe for background use.
type SafeEgressRecoveryProber interface {
	SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error)
}

// BaseCarrier provides default implementations for optional methods.
type BaseCarrier struct{}

// DeleteMessage provides a default implementation that returns "not implemented".
func (b *BaseCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	return fmt.Errorf("delete message not implemented for carrier type: %s", "")
}

// HealthStatus is the result of a LifecycleCarrier health check.
type HealthStatus struct {
	Healthy     bool
	Ready       bool
	Message     string
	LastChecked time.Time
}

// LifecycleCarrier extends Carrier with explicit start/stop/health for
// carriers that manage a session (video tunnels, WebRTC calls). Bridged
// carriers (ProviderCarrier) forward these to the underlying provider.
type LifecycleCarrier interface {
	Carrier
	Start(ctx context.Context, endpoint Endpoint) error
	Stop(ctx context.Context, endpoint Endpoint) error
	Health(ctx context.Context) HealthStatus
}

// ListenerCarrier owns a network listener that must be ready before its
// endpoint is advertised and stopped independently of session state.
type ListenerCarrier interface {
	Carrier
	StartListener(ctx context.Context, endpoint Endpoint) error
	StopListener(ctx context.Context) error
	ListenerHealth() HealthStatus
}

// NativeCarrier is a marker interface for carriers created directly in the
// binding layer without going through the ProviderCarrier bridge.
type NativeCarrier interface {
	Carrier
	IsNative()
}

// CarrierWithWriteResult extends Carrier for carriers that can return additional
// information about written messages, such as message IDs for later deletion.
type CarrierWithWriteResult interface {
	Carrier
	WriteWithResult(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) (*WriteResult, error)
}

// StreamDialer is implemented by carriers that can dial TCP streams for
// egress (e.g. SSH, sing-box, WBStream DataChannel). The unified tunnel
// detects this interface instead of type-asserting to specific carrier types.
type StreamDialer interface {
	DialStream(ctx context.Context, endpoint Endpoint, targetAddr string) (net.Conn, error)
}

// DatagramProber is an optional, explicit UDP measurement hook. It does not
// imply that the carrier can carry consumer UDP traffic; an adapter may expose
// only a configured tailnet/transport echo endpoint for exploration.
type DatagramProber interface {
	ProbeUDP(ctx context.Context) (time.Duration, error)
}

// ManagedStreamCarrier is a stream adapter whose local runtime is managed by
// the common sidecar lifecycle. The tunnel only knows this contract; it does
// not branch on a provider or protocol name.
type ManagedStreamCarrier interface {
	Carrier
	StreamDialer
	SetStreamDialFunc(func(context.Context, Endpoint, string) (net.Conn, error))
	SidecarSpec() SidecarSpec
}

// SidecarSpec is the non-secret process contract used by managed stream
// adapters. ConfigJSON builds the child configuration for a loopback listen
// address and is kept on the adapter so each protocol remains isolated.
type SidecarSpec struct {
	BinaryPath       string
	ConfigDir        string
	Server           string
	LocalListen      string
	StartTimeoutSecs int
	ConfigJSON       func(localListen, resolvedServer string) ([]byte, error)
}

// ParseEnvelope decodes a JSON-encoded envelope.
func ParseEnvelope(data []byte) (fabric.Envelope, error) {
	var env fabric.Envelope
	err := json.Unmarshal(data, &env)
	return env, err
}

// MarshalEnvelope encodes an envelope to JSON.
func MarshalEnvelope(env fabric.Envelope) ([]byte, error) {
	return json.Marshal(env)
}

// HasCapability reports whether the descriptor advertises the given capability.
func HasCapability(desc Descriptor, cap Capability) bool {
	for _, c := range desc.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// DeriveTrafficClasses computes the eligible traffic classes from a
// descriptor's capabilities. This replaces the need for manually maintained
// TrafficClasses slices: any carrier that declares the right capabilities is
// automatically eligible for the matching traffic classes.
//
// The mapping is:
//
//	CapRendezvous + CapMailbox → Bootstrap, Control
//	CapMailbox                 → Admin, Health, Log
//	CapStream + CapDuplex      → Stream, Egress
//	CapDatagram                → Egress
//	CapBulk                    → Bulk, Repair
//	CapRetained                → Repair (additional)
func DeriveTrafficClasses(caps []Capability) []fabric.TrafficClass {
	has := func(c Capability) bool {
		for _, x := range caps {
			if x == c {
				return true
			}
		}
		return false
	}

	var out []fabric.TrafficClass

	// Bootstrap / Control require rendezvous + mailbox.
	if has(CapRendezvous) && has(CapMailbox) {
		out = append(out, fabric.TrafficBootstrap, fabric.TrafficControl)
	}

	// Admin, Health, Log only need mailbox (store-and-forward).
	if has(CapMailbox) {
		out = append(out, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog)
	}

	// Stream requires streaming + duplex.
	if has(CapStream) && has(CapDuplex) {
		out = append(out, fabric.TrafficStream)
	}

	// Egress can be served by streaming or datagram carriers, bulk carriers
	// (chunked file transfer), or mailbox carriers (envelope tunneling).
	if has(CapStream) || has(CapBulk) || has(CapDatagram) {
		out = append(out, fabric.TrafficEgress)
	} else if has(CapMailbox) && has(CapOrdered) && has(CapDurable) {
		out = append(out, fabric.TrafficEgress)
	}

	// Bulk requires bulk capability.
	if has(CapBulk) {
		out = append(out, fabric.TrafficBulk)
	}

	// Repair can be served by bulk or retained carriers.
	if has(CapBulk) || has(CapRetained) {
		out = append(out, fabric.TrafficRepair)
	}

	return out
}
