package carriers

import (
	"sort"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// TestDeriveTrafficClassesMatchesStandardDescriptors verifies that
// DeriveTrafficClasses produces a superset of the manually maintained
// TrafficClasses for every standard carrier descriptor.
func TestDeriveTrafficClassesMatchesStandardDescriptors(t *testing.T) {
	for _, desc := range StandardDescriptors() {
		derived := DeriveTrafficClasses(desc.Capabilities)
		derivedSet := toSet(derived)

		for _, tc := range desc.TrafficClasses {
			if !derivedSet[tc] {
				t.Errorf("carrier %s: TrafficClasses contains %q but DeriveTrafficClasses did not produce it (derived=%v caps=%v)",
					desc.ID, tc, derived, desc.Capabilities)
			}
		}
	}
}

func TestDescriptorEffectiveMetricsKeepsPriorAndUsesPosterior(t *testing.T) {
	evidence := NewEvidenceState()
	descriptor := Descriptor{
		ID:       "prior-and-posterior",
		Metrics:  Metrics{Healthy: true, Latency: 2 * time.Second, BandwidthBPS: 1_000, Reliability: 0.8, QuotaRemaining: -1},
		Evidence: evidence,
	}
	evidence.Observe(MetricSourceProbe, Metrics{Healthy: false, Latency: 900 * time.Millisecond, Reliability: 0.25, FailureReason: "tcp_timeout"}, time.Unix(10, 0).UTC())
	got := descriptor.EffectiveMetrics()
	if got.Healthy || got.Latency != 900*time.Millisecond || got.BandwidthBPS != 1_000 || got.Reliability != 0.25 {
		t.Fatalf("effective metrics = %+v, want posterior health/latency/reliability with prior bandwidth", got)
	}
	latest, ok := evidence.Latest()
	if !ok || latest.Source != MetricSourceProbe || latest.ObservedAt != time.Unix(10, 0).UTC() {
		t.Fatalf("latest evidence = %+v/%v", latest, ok)
	}
}

func TestEvidenceStatePromotesPosteriorToNextStartupPrior(t *testing.T) {
	evidence := NewEvidenceState()
	observedAt := time.Unix(20, 0).UTC()
	evidence.Observe(MetricSourceProbe, Metrics{Healthy: true, Latency: 120 * time.Millisecond, Reliability: 0.99}, observedAt)
	observation, ok := evidence.Latest()
	if !ok {
		t.Fatal("expected current posterior observation")
	}

	restarted := NewEvidenceState()
	restarted.AdoptPrior(observation.Metrics, observation.ObservedAt)
	prior, ok := restarted.Prior()
	if !ok || prior.Source != MetricSourceCatalog || prior.ObservedAt != observedAt {
		t.Fatalf("startup prior = %+v/%v", prior, ok)
	}
	if _, ok := restarted.Latest(); ok {
		t.Fatal("startup prior must be separate from current-process observations")
	}
}

// TestDeriveTrafficClassesSpecificMappings validates exact derivation for
// well-known carrier archetypes.
func TestDeriveTrafficClassesSpecificMappings(t *testing.T) {
	tests := []struct {
		name string
		caps []Capability
		want []fabric.TrafficClass
	}{
		{
			name: "mailbox+rendezvous carrier (vk.messages)",
			caps: []Capability{CapRendezvous, CapMailbox, CapRetained, CapRetrospective, CapMutable},
			want: []fabric.TrafficClass{
				fabric.TrafficBootstrap, fabric.TrafficControl,
				fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog,
				fabric.TrafficRepair,
			},
		},
		{
			name: "stream+duplex carrier (wbstream)",
			caps: []Capability{CapRendezvous, CapStream, CapDuplex},
			want: []fabric.TrafficClass{
				fabric.TrafficStream, fabric.TrafficEgress,
			},
		},
		{
			name: "bulk carrier (vk.docs)",
			caps: []Capability{CapBulk, CapRetained},
			want: []fabric.TrafficClass{
				fabric.TrafficEgress, fabric.TrafficBulk, fabric.TrafficRepair,
			},
		},
		{
			name: "egress-only stream (ssh.tcp)",
			caps: []Capability{CapStream, CapDuplex},
			want: []fabric.TrafficClass{
				fabric.TrafficStream, fabric.TrafficEgress,
			},
		},
		{
			name: "retained-only carrier (vk.photos)",
			caps: []Capability{CapBulk, CapRetained},
			want: []fabric.TrafficClass{
				fabric.TrafficEgress, fabric.TrafficBulk, fabric.TrafficRepair,
			},
		},
		{
			name: "empty capabilities",
			caps: nil,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveTrafficClasses(tt.caps)
			if !trafficClassesEqual(got, tt.want) {
				t.Errorf("DeriveTrafficClasses(%v) = %v, want %v", tt.caps, got, tt.want)
			}
		})
	}
}

// TestDeriveTrafficClassesSSHNotEligibleForBootstrap verifies that a
// stream-only carrier like SSH (no CapMailbox) is never eligible for
// bootstrap or control traffic.
func TestDeriveTrafficClassesSSHNotEligibleForBootstrap(t *testing.T) {
	sshCaps := []Capability{CapStream, CapDuplex}
	derived := DeriveTrafficClasses(sshCaps)
	for _, tc := range derived {
		if tc == fabric.TrafficBootstrap || tc == fabric.TrafficControl {
			t.Fatalf("SSH-like carrier must not be eligible for %s, but DeriveTrafficClasses returned it", tc)
		}
	}
}

func TestHasCapability(t *testing.T) {
	desc := Descriptor{Capabilities: []Capability{CapMailbox, CapRetained}}
	if !HasCapability(desc, CapMailbox) {
		t.Error("expected HasCapability to find CapMailbox")
	}
	if HasCapability(desc, CapStream) {
		t.Error("expected HasCapability to not find CapStream")
	}
}

func toSet(classes []fabric.TrafficClass) map[fabric.TrafficClass]bool {
	m := make(map[fabric.TrafficClass]bool, len(classes))
	for _, c := range classes {
		m[c] = true
	}
	return m
}

func trafficClassesEqual(a, b []fabric.TrafficClass) bool {
	if len(a) != len(b) {
		return false
	}
	sa := make([]string, len(a))
	sb := make([]string, len(b))
	for i, c := range a {
		sa[i] = string(c)
	}
	for i, c := range b {
		sb[i] = string(c)
	}
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}
