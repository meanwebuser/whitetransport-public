package carriers

import (
	"sort"
	"testing"

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
