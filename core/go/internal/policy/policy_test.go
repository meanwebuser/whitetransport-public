package policy

import (
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestPolicySelectsHealthyCarrierByTrafficClass(t *testing.T) {
	p := CarrierPolicy{Routes: []Route{
		{TrafficClass: fabric.TrafficControl, CarrierID: "vk-chat", Priority: 20},
		{TrafficClass: fabric.TrafficControl, CarrierID: "yandex-disk", Priority: 30},
		{TrafficClass: fabric.TrafficStream, CarrierID: "wbstream-vp8", Priority: 10},
	}}
	available := []carriers.Descriptor{
		{ID: "vk-chat", Metrics: carriers.Metrics{Healthy: false}},
		{ID: "yandex-disk", Metrics: carriers.Metrics{Healthy: true}},
		{ID: "wbstream-vp8", Metrics: carriers.Metrics{Healthy: true}},
	}
	selected, err := p.Select(fabric.TrafficControl, available)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "yandex-disk" {
		t.Fatalf("expected fallback yandex-disk, got %s", selected.ID)
	}
}

func TestDefaultAdaptivePolicyRoutesCurrentCarrierPlan(t *testing.T) {
	p := DefaultAdaptivePolicy()
	available := carriers.StandardDescriptors()

	assertSelected := func(traffic fabric.TrafficClass, expected string) {
		t.Helper()
		selected, err := p.Select(traffic, available)
		if err != nil {
			t.Fatalf("select %s: %v", traffic, err)
		}
		if selected.ID != expected {
			t.Fatalf("select %s: expected %s, got %s", traffic, expected, selected.ID)
		}
	}

	// Primaries are now derived from the capability scorer's ranking of the
	// standard descriptors. With pure scoring (no overrides) singbox.vless
	// wins the realtime/egress slots on raw latency+bandwidth, and
	// vk.docs.1024 wins both bulk and repair on bandwidth. The legacy
	// hardcoded priority table is gone.
	assertSelected(fabric.TrafficBootstrap, carriers.CarrierVKMessages)
	assertSelected(fabric.TrafficControl, carriers.CarrierVKMessages)
	assertSelected(fabric.TrafficStream, carriers.CarrierSingBoxVLESS)
	assertSelected(fabric.TrafficEgress, carriers.CarrierSingBoxVLESS)
	assertSelected(fabric.TrafficBulk, carriers.CarrierVKDocs1024)
	assertSelected(fabric.TrafficRepair, carriers.CarrierVKDocs1024)
}

func TestDefaultAdaptivePolicyFallsBackToNextBestBulkCarrier(t *testing.T) {
	// With the scorer-driven routing the fall-back for bulk is whichever
	// healthy bulk-capable carrier scores highest once VK docs are marked
	// unhealthy. ok.docs.256 wins on measured bandwidth over Yandex Disk.
	p := DefaultAdaptivePolicy()
	available := carriers.StandardDescriptors()
	for i := range available {
		switch available[i].ID {
		case carriers.CarrierVKDocs1024, carriers.CarrierVKDocs256:
			available[i].Metrics.Healthy = false
		}
	}

	selected, err := p.Select(fabric.TrafficBulk, available)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != carriers.CarrierOKDocs256 {
		t.Fatalf("expected OK docs 256 fallback, got %s", selected.ID)
	}
}

func TestDefaultAdaptivePolicyPlansMirroredControl(t *testing.T) {
	p := DefaultAdaptivePolicy()

	plan, err := p.Plan(fabric.TrafficControl, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != DeliveryMirrored {
		t.Fatalf("expected mirrored control plan, got %s", plan.Strategy)
	}
	if plan.Primary.ID != carriers.CarrierVKMessages {
		t.Fatalf("expected VK messages primary, got %s", plan.Primary.ID)
	}
	if plan.MirrorCount != 2 || len(plan.Parallel) != 1 || plan.Parallel[0].ID != carriers.CarrierOKMessages {
		t.Fatalf("expected OK messages mirror candidate, got mirror=%d parallel=%+v", plan.MirrorCount, plan.Parallel)
	}
	if plan.HedgeTimeout != 750*time.Millisecond {
		t.Fatalf("expected 750ms hedge timeout, got %s", plan.HedgeTimeout)
	}
}

func TestDefaultAdaptivePolicyPlansStripedBulk(t *testing.T) {
	p := DefaultAdaptivePolicy()

	plan, err := p.Plan(fabric.TrafficBulk, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != DeliveryStriped {
		t.Fatalf("expected striped bulk plan, got %s", plan.Strategy)
	}
	if plan.Primary.ID != carriers.CarrierVKDocs1024 {
		t.Fatalf("expected fastest VK docs primary, got %s", plan.Primary.ID)
	}
	// Parallel order is the scorer's descending ranking of remaining bulk
	// carriers: vk.docs.256 > ok.docs.256 > ok.photos > yandex.disk.files > vk.photos.
	expectedParallel := []string{
		carriers.CarrierVKDocs256,
		carriers.CarrierOKDocs256,
		carriers.CarrierOKPhotos,
		carriers.CarrierYandexDisk,
		carriers.CarrierVKPhotos,
	}
	if got := descriptorIDs(plan.Parallel); !sameIDs(got, expectedParallel) {
		t.Fatalf("expected parallel %v, got %v", expectedParallel, got)
	}
	if got := descriptorIDs(plan.Repair); !sameIDs(got, expectedParallel) {
		t.Fatalf("expected repair carriers %v, got %v", expectedParallel, got)
	}
	if plan.MaxInFlightChunks <= 1 {
		t.Fatalf("striped bulk should allow multiple chunks in flight, got %d", plan.MaxInFlightChunks)
	}
}

func TestDefaultAdaptivePolicyHedgesEgressAcrossEligibleCarriers(t *testing.T) {
	p := DefaultAdaptivePolicy()

	plan, err := p.Plan(fabric.TrafficEgress, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != DeliveryHedged {
		t.Fatalf("expected hedged egress plan, got %s", plan.Strategy)
	}
	// singbox.vless wins on raw latency+bandwidth under pure scoring.
	if plan.Primary.ID != carriers.CarrierSingBoxVLESS {
		t.Fatalf("expected sing-box primary, got %s", plan.Primary.ID)
	}
	// Egress hedges follow the scorer's order after singbox.vless. Both SSH
	// streams outrank the bulk carriers; ssh.tcp keeps a small advantage over
	// ssh.fabric because CapEphemeral earns an additional egress bonus.
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
	}
	if got := descriptorIDs(plan.Parallel); !sameIDs(got, expectedHedges) {
		t.Fatalf("expected egress hedges %v, got %v", expectedHedges, got)
	}
}

func descriptorIDs(descriptors []carriers.Descriptor) []string {
	ids := make([]string, 0, len(descriptors))
	for _, desc := range descriptors {
		ids = append(ids, desc.ID)
	}
	return ids
}

func sameIDs(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
