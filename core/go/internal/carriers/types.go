package carriers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
