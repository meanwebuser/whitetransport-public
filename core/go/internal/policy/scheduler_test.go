package policy

import (
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestSchedulePayloadMirrorsControlChunks(t *testing.T) {
	plan, err := DefaultAdaptivePolicy().Plan(fabric.TrafficControl, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}

	placements, err := SchedulePayload(plan, 9000)
	if err != nil {
		t.Fatal(err)
	}
	if len(placements) != 3 {
		t.Fatalf("expected three VK message chunks, got %d", len(placements))
	}
	for _, placement := range placements {
		if placement.CarrierID != carriers.CarrierVKMessages {
			t.Fatalf("expected primary VK messages, got %+v", placement)
		}
		if len(placement.MirrorCarrierIDs) != 1 || placement.MirrorCarrierIDs[0] != carriers.CarrierOKMessages {
			t.Fatalf("expected OK message mirror, got %+v", placement)
		}
		if len(placement.HedgeCarrierIDs) != 0 || placement.HedgeAfter != 0 {
			t.Fatalf("mirrored control must not be a hedged placement: %+v", placement)
		}
	}
}

func TestSchedulePayloadStripesBulkAcrossProviders(t *testing.T) {
	plan, err := DefaultAdaptivePolicy().Plan(fabric.TrafficBulk, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}

	placements, err := SchedulePayload(plan, 4*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(placements) < 3 {
		t.Fatalf("expected striped bulk placements across providers, got %d", len(placements))
	}
	seen := map[string]bool{}
	for _, placement := range placements {
		seen[placement.CarrierID] = true
		if len(placement.MirrorCarrierIDs) != 0 || len(placement.HedgeCarrierIDs) != 0 {
			t.Fatalf("striped bulk should not clone chunks by default: %+v", placement)
		}
	}
	for _, id := range []string{carriers.CarrierVKDocs1024, carriers.CarrierVKDocs256, carriers.CarrierOKDocs256} {
		if !seen[id] {
			t.Fatalf("expected carrier %s in striped placements, got %v", id, seen)
		}
	}
}

func TestSchedulePayloadStripesBulkBelowLargestCarrierChunk(t *testing.T) {
	plan, err := DefaultAdaptivePolicy().Plan(fabric.TrafficBulk, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}

	placements, err := SchedulePayload(plan, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, placement := range placements {
		seen[placement.CarrierID] = true
		if placement.Size > 196608 {
			t.Fatalf("striped bulk chunk should be capped by smallest lane budget, got %+v", placement)
		}
	}
	if !seen[carriers.CarrierVKDocs1024] || !seen[carriers.CarrierVKDocs256] || !seen[carriers.CarrierOKDocs256] {
		t.Fatalf("expected sub-3MB bulk payload to still use parallel lanes, got %v", seen)
	}
}

func TestSchedulePayloadHedgesAdminWithoutImmediateMirror(t *testing.T) {
	plan, err := DefaultAdaptivePolicy().Plan(fabric.TrafficAdmin, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}

	// Keep this a chunking assertion as the adaptive policy evolves: one byte
	// beyond the selected primary's budget must produce exactly two chunks.
	payloadBytes := plan.Primary.Limits.ChunkPayloadBytes + 1
	placements, err := SchedulePayload(plan, payloadBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(placements) != 2 {
		t.Fatalf("expected two primary chunks, got %d", len(placements))
	}
	for index, placement := range placements {
		if placement.CarrierID != carriers.CarrierSSHFabric {
			t.Fatalf("expected SSH fabric primary, got %+v", placement)
		}
		expectedSize := plan.Primary.Limits.ChunkPayloadBytes
		if index == 1 {
			expectedSize = 1
		}
		if placement.Index != index || placement.Size != expectedSize {
			t.Fatalf("expected deterministic chunk %d size %d, got %+v", index, expectedSize, placement)
		}
		if len(placement.MirrorCarrierIDs) != 0 {
			t.Fatalf("hedged admin must not mirror immediately: %+v", placement)
		}
		if len(placement.HedgeCarrierIDs) != 1 || placement.HedgeCarrierIDs[0] != carriers.CarrierVKMessages {
			t.Fatalf("expected VK message hedge carrier, got %+v", placement)
		}
		if placement.HedgeAfter != time.Second {
			t.Fatalf("expected one second hedge delay, got %s", placement.HedgeAfter)
		}
	}
}

func TestSchedulePayloadKeepsEgressPrimaryAndHedgesSlowCarriers(t *testing.T) {
	plan, err := DefaultAdaptivePolicy().Plan(fabric.TrafficEgress, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}

	placements, err := SchedulePayload(plan, 5000)
	if err != nil {
		t.Fatal(err)
	}
	// With singbox.vless as primary (16KB chunk budget) the 5KB payload
	// fits in a single chunk; the chunk itself carries the full hedge set
	// computed from the scorer's order.
	if len(placements) != 1 {
		t.Fatalf("expected one singbox.vless egress chunk, got %d", len(placements))
	}
	placement := placements[0]
	if placement.CarrierID != carriers.CarrierSingBoxVLESS {
		t.Fatalf("egress must use singbox.vless primary, got %+v", placement)
	}
	if len(placement.MirrorCarrierIDs) != 0 {
		t.Fatalf("egress must not eagerly mirror by default: %+v", placement)
	}
	expectedHedges := []string{
		carriers.CarrierSSHTCP,
		carriers.CarrierSSHFabric,
		carriers.CarrierWBStreamVP8,
		carriers.CarrierVKDocs1024,
		carriers.CarrierVKDocs256,
		carriers.CarrierOKDocs256,
		carriers.CarrierOKPhotos,
		carriers.CarrierVKPhotos,
		carriers.CarrierYandexDisk,
		carriers.CarrierMailIMAPSMTP,
		carriers.CarrierGitRepository,
	}
	if !sameIDs(placement.HedgeCarrierIDs, expectedHedges) {
		t.Fatalf("egress should keep slow carriers as delayed hedges, want %v got %v", expectedHedges, placement.HedgeCarrierIDs)
	}
}

func TestSchedulePayloadRejectsRedundantUntilFECExists(t *testing.T) {
	plan := RoutePlan{
		TrafficClass: fabric.TrafficBulk,
		Strategy:     DeliveryRedundant,
		Primary:      carriers.StandardDescriptors()[0],
	}

	if _, err := SchedulePayload(plan, 1024); err == nil {
		t.Fatal("expected redundant scheduling to fail until parity support exists")
	}
}
