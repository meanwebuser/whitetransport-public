package fabric

import (
	"fmt"
	"time"
)

// TrafficClass describes which logical class of encrypted envelope is being
// moved. Carriers do not interpret the payload, but policy and routing do.
type TrafficClass string

const (
	TrafficBootstrap TrafficClass = "bootstrap"
	TrafficControl   TrafficClass = "control"
	TrafficAdmin     TrafficClass = "admin"
	TrafficHealth    TrafficClass = "health"
	TrafficLog       TrafficClass = "log"
	TrafficStream    TrafficClass = "stream"
	TrafficBulk      TrafficClass = "bulk"
	TrafficRepair    TrafficClass = "repair"
	TrafficEgress    TrafficClass = "egress"
)

// CurrentVersion is the only supported on-wire envelope version.
const CurrentVersion = 1

// Envelope is the carrier-independent message shape. Carrier adapters move
// envelope bytes; they do not decide whether the payload is discovery, control,
// stream data, or fallback repair data.
type Envelope struct {
	Version      int           `json:"v"`
	ID           string        `json:"id"`
	SessionID    string        `json:"session_id,omitempty"`
	Source       string        `json:"src"`
	Destination  string        `json:"dst,omitempty"`
	TrafficClass TrafficClass  `json:"traffic_class"`
	PayloadType  string        `json:"payload_type"`
	Sequence     uint64        `json:"seq,omitempty"`
	Ack          uint64        `json:"ack,omitempty"`
	TTL          time.Duration `json:"ttl,omitempty"`
	Priority     int           `json:"priority,omitempty"`
	// ChunkIndex is the zero-based position of this envelope within a chunked
	// payload. Zero means either the first chunk or a non-chunked envelope.
	ChunkIndex int `json:"chunk_index,omitempty"`
	// ChunkTotal is the number of chunks the original payload was split into.
	// Zero means the envelope is a standalone (non-chunked) message.
	ChunkTotal int `json:"chunk_total,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Payload    []byte    `json:"payload"`
}

// Validate rejects envelopes that cannot be safely routed or decoded.
func (e Envelope) Validate() error {
	if e.Version != CurrentVersion {
		return fmt.Errorf("unsupported envelope version %d", e.Version)
	}
	if e.ID == "" {
		return fmt.Errorf("envelope id is required")
	}
	switch e.TrafficClass {
	case TrafficBootstrap, TrafficControl, TrafficAdmin, TrafficHealth, TrafficLog, TrafficStream, TrafficBulk, TrafficRepair, TrafficEgress:
		return nil
	default:
		return fmt.Errorf("unsupported traffic class %q", e.TrafficClass)
	}
}

// IsChunk returns true when this envelope is one piece of a larger payload.
func (e Envelope) IsChunk() bool {
	return e.ChunkTotal > 0
}

// BaseID returns the shared group ID for chunked envelopes. For non-chunked
// envelopes it returns the envelope ID as-is.
func (e Envelope) BaseID() string {
	if e.IsChunk() {
		return e.ID[:len(e.ID)-len(fmt.Sprintf(".%d", e.ChunkIndex))]
	}
	return e.ID
}

// NewEnvelope constructs a versioned envelope with the canonical default
// timestamp.
func NewEnvelope(id string, traffic TrafficClass, payloadType string, payload []byte) Envelope {
	return Envelope{
		Version:      CurrentVersion,
		ID:           id,
		TrafficClass: traffic,
		PayloadType:  payloadType,
		CreatedAt:    time.Now().UTC(),
		Payload:      payload,
	}
}
