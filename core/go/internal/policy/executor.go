package policy

import (
	"context"
	"fmt"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// CarrierBinding connects a scheduled carrier id to its adapter and endpoint.
type CarrierBinding struct {
	Carrier  carriers.Carrier
	Endpoint carriers.Endpoint
	Role     string // "discovery", "node-client", "logs", "admin", "flex", "" (legacy)
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
