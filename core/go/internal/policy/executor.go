package policy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// CarrierBinding connects a scheduled carrier id to its adapter and endpoint.
type CarrierBinding struct {
	Carrier  carriers.Carrier
	Endpoint carriers.Endpoint
	Role     string // "discovery", "node-client", "logs", "admin", "flex", "" (legacy)

	vkAccountTag []byte
}

var bindingLabelProcessKey = sync.OnceValue(func() [sha256.Size]byte {
	var key [sha256.Size]byte
	rand.Read(key[:])
	return key
})

// WithVKCredentialID attaches the credential provenance captured at construction.
// The returned binding retains only a keyed digest of the ID.
func (b CarrierBinding) WithVKCredentialID(id string) CarrierBinding {
	b.vkAccountTag = nil
	if id != "" {
		key := bindingLabelProcessKey()
		mac := hmac.New(sha256.New, key[:])
		mac.Write([]byte("whitetransport/vk-binding-account/v1\x00"))
		mac.Write([]byte(id))
		b.vkAccountTag = mac.Sum(nil)
	}
	return b
}

// BindingLabel returns a local process-stable label in v1:<32 lowercase hex>
// format, or an empty string when the VK account or mailbox is unknown.
func (b CarrierBinding) BindingLabel(actualEndpoint carriers.Endpoint) string {
	if len(b.vkAccountTag) == 0 || b.Carrier == nil {
		return ""
	}
	carrierID := b.Carrier.Descriptor().ID
	switch carrierID {
	case carriers.CarrierVKMessages, carriers.CarrierVKDocs256, carriers.CarrierVKDocs1024:
	default:
		return ""
	}
	// Match the native VK adapter's address precedence.
	mailbox := strings.TrimSpace(actualEndpoint.Address)
	if mailbox == "" {
		mailbox = strings.TrimSpace(actualEndpoint.Metadata["peer_id"])
	}
	if mailbox == "" {
		return ""
	}

	key := bindingLabelProcessKey()
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte("whitetransport/vk-binding-label/v1\x00"))
	mac.Write(b.vkAccountTag)
	mac.Write([]byte(carrierID + "\x00" + mailbox))
	return "v1:" + hex.EncodeToString(mac.Sum(nil)[:16])
}

// PendingHedge records a delayed write that should happen only if the primary
// chunk is not acknowledged before HedgeAfter.
type PendingHedge struct {
	Placement ChunkPlacement
	CarrierID string
	Envelope  fabric.Envelope
	Endpoint  carriers.Endpoint
}

// DispatchResult summarizes immediate writes and delayed hedge candidates.
type DispatchResult struct {
	ImmediateWrites int
	PendingHedges   []PendingHedge
}

// DispatchScheduledPayload writes scheduled chunks to carrier adapters. It
// sends primary and mirror placements immediately; hedges are returned for a
// later ACK-aware retry loop instead of being sent eagerly.
func DispatchScheduledPayload(
	ctx context.Context,
	base fabric.Envelope,
	placements []ChunkPlacement,
	bindings map[string]CarrierBinding,
) (DispatchResult, error) {
	result := DispatchResult{}
	totalChunks := len(placements)
	for _, placement := range placements {
		if placement.Offset < 0 || placement.Size < 0 || placement.Offset+placement.Size > len(base.Payload) {
			return DispatchResult{}, fmt.Errorf("invalid placement range index=%d offset=%d size=%d payload=%d", placement.Index, placement.Offset, placement.Size, len(base.Payload))
		}
		chunk := chunkEnvelope(base, placement, totalChunks)
		// Prefer explicit BindingKey when set, fall back to CarrierID.
		writeKey := placement.CarrierID
		if placement.BindingKey != "" {
			writeKey = placement.BindingKey
		}
		if err := writePlacement(ctx, writeKey, chunk, bindings); err != nil {
			return DispatchResult{}, err
		}
		result.ImmediateWrites++
		for _, mirrorID := range placement.MirrorCarrierIDs {
			if err := writePlacement(ctx, mirrorID, chunk, bindings); err != nil {
				return DispatchResult{}, err
			}
			result.ImmediateWrites++
		}
		for _, hedgeID := range placement.HedgeCarrierIDs {
			binding, ok := bindings[hedgeID]
			if !ok {
				binding, ok = findBindingForCarrier(hedgeID, bindings)
				if !ok {
					return DispatchResult{}, fmt.Errorf("missing carrier binding %q", hedgeID)
				}
			}
			result.PendingHedges = append(result.PendingHedges, PendingHedge{
				Placement: placement,
				CarrierID: hedgeID,
				Envelope:  chunk,
				Endpoint:  binding.Endpoint,
			})
		}
	}
	return result, nil
}

func writePlacement(ctx context.Context, carrierID string, envelope fabric.Envelope, bindings map[string]CarrierBinding) error {
	// Try exact key first.
	binding, ok := bindings[carrierID]
	if !ok {
		// Compound-key fallback: carrierID is a plain descriptor ID like
		// "vk.messages" but the binding key is "vk.messages:discovery".
		binding, ok = findBindingForCarrier(carrierID, bindings)
		if !ok {
			return fmt.Errorf("missing carrier binding %q", carrierID)
		}
	}
	return binding.Carrier.Write(ctx, binding.Endpoint, envelope)
}

// findBindingForCarrier resolves a plain carrier/descriptor ID to a binding
// by checking compound keys (prefix match) and endpoint carrier fields.
func findBindingForCarrier(carrierID string, bindings map[string]CarrierBinding) (CarrierBinding, bool) {
	for key, b := range bindings {
		if HasBindingKeyPrefix(key, carrierID) {
			return b, true
		}
		if b.Endpoint.Carrier == carrierID || b.Carrier.Descriptor().ID == carrierID {
			return b, true
		}
	}
	return CarrierBinding{}, false
}

func chunkEnvelope(base fabric.Envelope, placement ChunkPlacement, totalChunks int) fabric.Envelope {
	chunk := base
	chunk.ID = fmt.Sprintf("%s.%d", base.ID, placement.Index)
	chunk.Sequence = uint64(placement.Index)
	chunk.PayloadType = base.PayloadType + ".chunk"
	chunk.ChunkIndex = placement.Index
	chunk.ChunkTotal = totalChunks
	chunk.Payload = append([]byte(nil), base.Payload[placement.Offset:placement.Offset+placement.Size]...)
	return chunk
}
