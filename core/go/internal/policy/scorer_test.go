package policy

import (
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestCapabilityScorerGatesOnCapabilities(t *testing.T) {
	scorer := DefaultScorer()

	// SSH carrier (stream+duplex, no mailbox) must not score for bootstrap.
	sshDesc := carriers.Descriptor{
		ID:           "ssh.tcp",
		Capabilities: []carriers.Capability{carriers.CapStream, carriers.CapDuplex},
		Metrics:      carriers.Metrics{Healthy: true, Latency: 150 * time.Millisecond, BandwidthBPS: 2 * 1024 * 1024, QuotaRemaining: -1},
	}
	if score := scorer.Score(sshDesc, fabric.TrafficBootstrap); score >= 0 {
		t.Fatalf("SSH carrier should be ineligible for bootstrap, got score %.2f", score)
	}
	if score := scorer.Score(sshDesc, fabric.TrafficControl); score >= 0 {
		t.Fatalf("SSH carrier should be ineligible for control, got score %.2f", score)
	}
	// But SSH should be eligible for egress.
	if score := scorer.Score(sshDesc, fabric.TrafficEgress); score < 0 {
		t.Fatalf("SSH carrier should be eligible for egress, got score %.2f", score)
	}
}

func TestCapabilityScorerGatesOnHealth(t *testing.T) {
	scorer := DefaultScorer()

	vkDesc := carriers.Descriptor{
		ID:           carriers.CarrierVKMessages,
		Capabilities: []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox},
		Metrics:      carriers.Metrics{Healthy: false},
	}
	if score := scorer.Score(vkDesc, fabric.TrafficBootstrap); score >= 0 {
		t.Fatalf("unhealthy carrier should be ineligible, got score %.2f", score)
	}
}

func TestCapabilityScorerGatesOnQuota(t *testing.T) {
	scorer := DefaultScorer()

	desc := carriers.Descriptor{
		ID:           "test.bulk",
		Capabilities: []carriers.Capability{carriers.CapBulk},
		Metrics:      carriers.Metrics{Healthy: true, BandwidthBPS: 1000, QuotaRemaining: 0},
	}
	if score := scorer.Score(desc, fabric.TrafficBulk); score >= 0 {
		t.Fatalf("exhausted-quota carrier should be ineligible, got score %.2f", score)
	}
}

func TestCapabilityScorerGatesOnLatency(t *testing.T) {
	scorer := DefaultScorer()

	// Very high latency carrier should be rejected for bootstrap (max 5s).
	desc := carriers.Descriptor{
		ID:           "slow.mailbox",
		Capabilities: []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox},
		Metrics:      carriers.Metrics{Healthy: true, Latency: 10 * time.Second, BandwidthBPS: 1000, QuotaRemaining: -1},
	}
	if score := scorer.Score(desc, fabric.TrafficBootstrap); score >= 0 {
		t.Fatalf("high-latency carrier should be ineligible for bootstrap, got score %.2f", score)
	}
	// But should be eligible for log (max 30s).
	if score := scorer.Score(desc, fabric.TrafficLog); score < 0 {
		t.Fatalf("high-latency carrier should be eligible for log, got score %.2f", score)
	}
}

func TestCapabilityScorerPrefersFasterCarrierForEgress(t *testing.T) {
	scorer := DefaultScorer()

	fast := carriers.Descriptor{
		ID:           "fast.stream",
		Capabilities: []carriers.Capability{carriers.CapStream, carriers.CapDuplex},
		Metrics:      carriers.Metrics{Healthy: true, Latency: 50 * time.Millisecond, BandwidthBPS: 10 * 1024 * 1024, Reliability: 0.95, QuotaRemaining: -1},
	}
	slow := carriers.Descriptor{
		ID:           "slow.stream",
		Capabilities: []carriers.Capability{carriers.CapStream, carriers.CapDuplex},
		Metrics:      carriers.Metrics{Healthy: true, Latency: 500 * time.Millisecond, BandwidthBPS: 500 * 1024, Reliability: 0.90, QuotaRemaining: -1},
	}
	fastScore := scorer.Score(fast, fabric.TrafficEgress)
	slowScore := scorer.Score(slow, fabric.TrafficEgress)
	if fastScore <= slowScore {
		t.Fatalf("fast carrier (%.2f) should score higher than slow (%.2f) for egress", fastScore, slowScore)
	}
}

func TestCapabilityScorerPrefersCheaperCarrierForBulk(t *testing.T) {
	scorer := DefaultScorer()

	free := carriers.Descriptor{
		ID:           "free.bulk",
		Capabilities: []carriers.Capability{carriers.CapBulk},
		Metrics:      carriers.Metrics{Healthy: true, BandwidthBPS: 1000 * 1024, Reliability: 0.9, QuotaRemaining: -1},
	}
	expensive := carriers.Descriptor{
		ID:           "expensive.bulk",
		Capabilities: []carriers.Capability{carriers.CapBulk},
		Metrics:      carriers.Metrics{Healthy: true, BandwidthBPS: 1000 * 1024, Reliability: 0.9, CostPerMB: 5.0, QuotaRemaining: -1},
	}
	freeScore := scorer.Score(free, fabric.TrafficBulk)
	expScore := scorer.Score(expensive, fabric.TrafficBulk)
	if freeScore <= expScore {
		t.Fatalf("free carrier (%.2f) should score higher than expensive (%.2f) for bulk", freeScore, expScore)
	}
}

func TestAutoRoutesGeneratesRoutesForAllTrafficClasses(t *testing.T) {
	routes := AutoRoutes(carriers.StandardDescriptors(), DefaultScorerWithOverrides())

	// Every traffic class should have at least one route.
	trafficClasses := []fabric.TrafficClass{
		fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin,
		fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficStream,
		fabric.TrafficEgress, fabric.TrafficBulk, fabric.TrafficRepair,
	}
	for _, tc := range trafficClasses {
		found := false
		for _, r := range routes {
			if r.TrafficClass == tc {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AutoRoutes produced no route for traffic class %s", tc)
		}
	}
}

func TestAutoRoutesWithOverridesMatchesExpectedPrimaries(t *testing.T) {
	// The override table maps each carrier to a fixed score per traffic class,
	// so the resulting primary is deterministic. We assert on those primaries
	// directly instead of cross-checking against the removed DefaultRoutes().
	// TODO: scorer migration — the override table is no longer the source of
	// truth for DefaultAdaptivePolicy; this test guards the override table
	// for any external caller that still wants the legacy hardcoded ordering.
	expected := map[fabric.TrafficClass]string{
		fabric.TrafficBootstrap: carriers.CarrierVKMessages,
		fabric.TrafficControl:   carriers.CarrierVKMessages,
		fabric.TrafficAdmin:     carriers.CarrierVKMessages,
		fabric.TrafficHealth:    carriers.CarrierVKMessages,
		fabric.TrafficLog:       carriers.CarrierVKMessages,
		fabric.TrafficStream:    carriers.CarrierWBStreamVP8,
		fabric.TrafficEgress:    carriers.CarrierSingBoxVLESS,
		fabric.TrafficBulk:      carriers.CarrierVKDocs1024,
		fabric.TrafficRepair:    carriers.CarrierVKDocs256,
	}
	autoRoutes := AutoRoutes(carriers.StandardDescriptors(), DefaultScorerWithOverrides())
	for tc, want := range expected {
		got := firstRouteFor(autoRoutes, tc)
		if got == "" {
			t.Errorf("traffic %s: AutoRoutes produced no primary", tc)
			continue
		}
		if got != want {
			t.Errorf("traffic %s: AutoRoutes primary=%s, want %s", tc, got, want)
		}
	}
}

// TestAutoRoutesNewCarrierAutoRouted verifies that adding a new carrier
// with stream+duplex capabilities automatically makes it an egress candidate
// with zero policy code changes.
func TestAutoRoutesNewCarrierAutoRouted(t *testing.T) {
	descs := append(carriers.StandardDescriptors(), carriers.Descriptor{
		ID:           "s3.transfer",
		Provider:     "aws",
		Mode:         carriers.DeliveryStream,
		Capabilities: []carriers.Capability{carriers.CapStream, carriers.CapDuplex},
		Metrics:      carriers.Metrics{Healthy: true, Latency: 100 * time.Millisecond, BandwidthBPS: 50 * 1024 * 1024, Reliability: 0.99, QuotaRemaining: -1},
	})

	scorer := DefaultScorer() // no overrides — pure scoring
	routes := AutoRoutes(descs, scorer)

	// The new S3 carrier should appear for egress.
	found := false
	for _, r := range routes {
		if r.TrafficClass == fabric.TrafficEgress && r.CarrierID == "s3.transfer" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("new S3 carrier was not auto-routed for egress despite having stream+duplex capabilities")
	}
}

func TestAutoRoutesIncapableCarrierExcluded(t *testing.T) {
	routes := AutoRoutes(carriers.StandardDescriptors(), DefaultScorer())

	for _, r := range routes {
		if r.CarrierID == carriers.CarrierSSHTCP &&
			(r.TrafficClass == fabric.TrafficBootstrap || r.TrafficClass == fabric.TrafficControl) {
			t.Fatalf("SSH carrier must not be routed for %s (no mailbox capability)", r.TrafficClass)
		}
	}
}

func TestAutoRoutesMetricsDrivenFailover(t *testing.T) {
	scorer := DefaultScorer()

	// Two mailbox carriers: primary is unhealthy, secondary should promote.
	primary := carriers.Descriptor{
		ID:           "primary.mailbox",
		Capabilities: []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox},
		Metrics:      carriers.Metrics{Healthy: false},
	}
	secondary := carriers.Descriptor{
		ID:           "secondary.mailbox",
		Capabilities: []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox},
		Metrics:      carriers.Metrics{Healthy: true, Latency: 200 * time.Millisecond, BandwidthBPS: 10 * 1024, Reliability: 0.9, QuotaRemaining: -1},
	}
	routes := AutoRoutes([]carriers.Descriptor{primary, secondary}, scorer)

	// Only secondary should appear for bootstrap.
	for _, r := range routes {
		if r.TrafficClass == fabric.TrafficBootstrap {
			if r.CarrierID == "primary.mailbox" {
				t.Fatal("unhealthy primary should not be routed for bootstrap")
			}
			if r.CarrierID == "secondary.mailbox" && r.Priority == 0 {
				return // secondary is the primary route
			}
		}
	}
	t.Fatal("healthy secondary should be routed for bootstrap when primary is unhealthy")
}

func firstRouteFor(routes []Route, tc fabric.TrafficClass) string {
	for _, r := range routes {
		if r.TrafficClass == tc {
			return r.CarrierID
		}
	}
	return ""
}
