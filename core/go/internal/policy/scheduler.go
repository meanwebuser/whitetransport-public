package policy

import (
	"errors"
	"fmt"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

// ChunkPlacement is one scheduler decision for a contiguous payload range.
// Carrier adapters execute these placements; policy remains provider-agnostic.
type ChunkPlacement struct {
	Index            int
	Offset           int
	Size             int
	CarrierID        string
	BindingKey       string   // optional: compound binding key (defaults to CarrierID when empty)
	MirrorCarrierIDs []string
	HedgeCarrierIDs  []string
	HedgeAfter       time.Duration
}

// SchedulePayload converts a route plan and payload size into deterministic
// chunk placements. It intentionally does not do FEC yet; DeliveryRedundant is
// reserved until parity frames exist.
func SchedulePayload(plan RoutePlan, payloadBytes int) ([]ChunkPlacement, error) {
	if payloadBytes < 0 {
		return nil, errors.New("payloadBytes must be non-negative")
	}
	if plan.Primary.ID == "" {
		return nil, errors.New("route plan requires primary carrier")
	}
	if payloadBytes == 0 {
		return nil, nil
	}

	switch plan.Strategy {
	case DeliverySingle:
		return scheduleSingle(plan, payloadBytes), nil
	case DeliveryMirrored:
		return scheduleMirrored(plan, payloadBytes), nil
	case DeliveryHedged:
		return scheduleHedged(plan, payloadBytes), nil
	case DeliveryStriped:
		return scheduleStriped(plan, payloadBytes), nil
	case DeliveryRedundant:
		return nil, errors.New("redundant delivery requires FEC/parity support")
	default:
		return nil, fmt.Errorf("unsupported delivery strategy %q", plan.Strategy)
	}
}

func scheduleSingle(plan RoutePlan, payloadBytes int) []ChunkPlacement {
	return scheduleSequential(plan.Primary, nil, nil, 0, payloadBytes)
}

func scheduleMirrored(plan RoutePlan, payloadBytes int) []ChunkPlacement {
	mirrors := carrierIDs(plan.Parallel)
	if plan.MirrorCount > 1 && len(mirrors) > plan.MirrorCount-1 {
		mirrors = mirrors[:plan.MirrorCount-1]
	}
	return scheduleSequential(plan.Primary, mirrors, nil, 0, payloadBytes)
}

func scheduleHedged(plan RoutePlan, payloadBytes int) []ChunkPlacement {
	return scheduleSequential(plan.Primary, nil, carrierIDs(plan.Parallel), plan.HedgeTimeout, payloadBytes)
}

func scheduleStriped(plan RoutePlan, payloadBytes int) []ChunkPlacement {
	lanes := append([]carriers.Descriptor{plan.Primary}, plan.Parallel...)
	stripeChunkSize := smallestChunkBudget(lanes)
	placements := make([]ChunkPlacement, 0)
	offset := 0
	index := 0
	for offset < payloadBytes {
		carrier := lanes[index%len(lanes)]
		chunkSize := minInt(chunkBudget(carrier), stripeChunkSize)
		if remaining := payloadBytes - offset; chunkSize > remaining {
			chunkSize = remaining
		}
		placements = append(placements, ChunkPlacement{
			Index:     index,
			Offset:    offset,
			Size:      chunkSize,
			CarrierID: carrier.ID,
		})
		offset += chunkSize
		index++
	}
	return placements
}

func smallestChunkBudget(descriptors []carriers.Descriptor) int {
	smallest := 0
	for _, desc := range descriptors {
		budget := chunkBudget(desc)
		if smallest == 0 || budget < smallest {
			smallest = budget
		}
	}
	return smallest
}

func scheduleSequential(primary carriers.Descriptor, mirrors []string, hedges []string, hedgeAfter time.Duration, payloadBytes int) []ChunkPlacement {
	placements := make([]ChunkPlacement, 0)
	offset := 0
	index := 0
	for offset < payloadBytes {
		chunkSize := chunkBudget(primary)
		if remaining := payloadBytes - offset; chunkSize > remaining {
			chunkSize = remaining
		}
		placements = append(placements, ChunkPlacement{
			Index:            index,
			Offset:           offset,
			Size:             chunkSize,
			CarrierID:        primary.ID,
			MirrorCarrierIDs: cloneStrings(mirrors),
			HedgeCarrierIDs:  cloneStrings(hedges),
			HedgeAfter:       hedgeAfter,
		})
		offset += chunkSize
		index++
	}
	return placements
}

func chunkBudget(desc carriers.Descriptor) int {
	if desc.Limits.ChunkPayloadBytes > 0 {
		return desc.Limits.ChunkPayloadBytes
	}
	if desc.Limits.MaxPayloadBytes > 0 {
		return desc.Limits.MaxPayloadBytes
	}
	return 1024
}

func carrierIDs(descriptors []carriers.Descriptor) []string {
	ids := make([]string, 0, len(descriptors))
	for _, desc := range descriptors {
		ids = append(ids, desc.ID)
	}
	return ids
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}
