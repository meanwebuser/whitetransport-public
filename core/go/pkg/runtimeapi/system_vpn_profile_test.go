package runtimeapi

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSystemVPNProfileJSONContract(t *testing.T) {
	issued := time.Date(2026, 7, 20, 12, 30, 0, 0, time.UTC)
	profile := SystemVPNProfile{
		SchemaRevision:   "system-vpn-profile.v1",
		DaemonInstanceID: "daemon-1",
		ProfileRevision:  7,
		SessionID:        "session-1",
		SelectedNodeID:   "node-1",
		Ready:            true,
		IssuedAt:         issued,
		ExpiresAt:        issued.Add(2 * time.Minute),
		SocksListen:      "127.0.0.1:1080",
		RouteMode:        "bypass",
		DestinationCIDRs: []string{
			"203.0.113.0/24",
			"2001:db8:1234::/48",
		},
		UserBypassCIDRs:       []string{"192.168.0.0/16", "fe80::/10"},
		LANAccess:             true,
		CarrierControlOrigins: []string{"https://api.ok.ru", "https://api.vk.com"},
		CarrierControlRoutes: map[string][]string{
			"api.ok.ru":  {"203.0.113.10/32"},
			"api.vk.com": {"2001:db8::10/128"},
		},
		DNSSnapshot: map[string][]string{
			"api.ok.ru":  {"203.0.113.10"},
			"api.vk.com": {"2001:db8::10"},
		},
		DNSServers: []string{"1.1.1.1", "2606:4700:4700::1111"},
		Dependencies: []SystemVPNDependency{
			{Purpose: "discovery", Carrier: "vk", Scheme: "https", Host: "api.vk.com", Port: 443, Addresses: []string{"2001:db8::10"}, DNSExpiresAt: issued.Add(time.Minute)},
			{Purpose: "control", Carrier: "ok", Scheme: "https", Host: "api.ok.ru", Port: 443, Addresses: []string{"203.0.113.10"}, DNSExpiresAt: issued.Add(time.Minute)},
			{Purpose: "egress", Carrier: "wbstream", Scheme: "https", Host: "egress.example", Port: 443, Addresses: []string{"203.0.113.20"}, DNSExpiresAt: issued.Add(time.Minute)},
		},
		MTU: 1500,
		Readiness: SystemVPNReadiness{
			Ready:      true,
			Provenance: "config+active-session+resolver",
		},
	}
	if err := profile.SetHash(); err != nil {
		t.Fatalf("set profile hash: %v", err)
	}
	status := Status{
		State:            "connected",
		SessionActive:    true,
		SystemVPNProfile: &profile,
		SystemVPNProfileReadiness: &SystemVPNReadiness{
			Ready:      true,
			Provenance: profile.Readiness.Provenance,
		},
	}

	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	for _, field := range []string{"system_vpn_profile", "system_vpn_profile_readiness"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("status missing %q: %s", field, payload)
		}
	}

	var decoded Status
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode typed status: %v", err)
	}
	if decoded.SystemVPNProfile == nil || decoded.SystemVPNProfile.ProfileRevision != 7 {
		t.Fatalf("decoded profile = %+v", decoded.SystemVPNProfile)
	}
	if decoded.SystemVPNProfile.SocksListen != "127.0.0.1:1080" || decoded.SystemVPNProfile.RouteMode != "bypass" {
		t.Fatalf("decoded profile routing = %+v", decoded.SystemVPNProfile)
	}
}

func TestSystemVPNProfileHashTracksRoutingIdentityNotDNSFreshness(t *testing.T) {
	issued := time.Date(2026, 7, 20, 12, 30, 0, 0, time.UTC)
	profile := SystemVPNProfile{
		SchemaRevision:   SystemVPNProfileSchemaRevision,
		DaemonInstanceID: "daemon-1",
		ProfileRevision:  7,
		SessionID:        "session-1",
		SelectedNodeID:   "node-1",
		Ready:            true,
		IssuedAt:         issued,
		ExpiresAt:        issued.Add(time.Minute),
		SocksListen:      "127.0.0.1:1080",
		RouteMode:        SystemVPNRouteNone,
		CarrierControlOrigins: []string{
			"https://api.ok.ru", "https://api.vk.com", "wss://egress.example",
		},
		CarrierControlRoutes: map[string][]string{
			"api.ok.ru": {"203.0.113.10/32"}, "api.vk.com": {"203.0.113.11/32"}, "egress.example": {"203.0.113.12/32"},
		},
		DNSSnapshot: map[string][]string{
			"api.ok.ru": {"203.0.113.10"}, "api.vk.com": {"203.0.113.11"}, "egress.example": {"203.0.113.12"},
		},
		DNSServers: []string{"1.1.1.1"},
		Dependencies: []SystemVPNDependency{
			{Purpose: SystemVPNDependencyControl, Carrier: "ok", Scheme: "https", Host: "api.ok.ru", Port: 443, Addresses: []string{"203.0.113.10"}, DNSExpiresAt: issued.Add(time.Minute)},
			{Purpose: SystemVPNDependencyDiscovery, Carrier: "vk", Scheme: "https", Host: "api.vk.com", Port: 443, Addresses: []string{"203.0.113.11"}, DNSExpiresAt: issued.Add(time.Minute)},
			{Purpose: SystemVPNDependencyEgress, Carrier: "wbstream", Scheme: "wss", Host: "egress.example", Port: 443, Addresses: []string{"203.0.113.12"}, DNSExpiresAt: issued.Add(time.Minute)},
		},
		MTU:       1500,
		Readiness: SystemVPNReadiness{Ready: true, Provenance: "test"},
	}
	profile.SortProfileSlices()
	if err := profile.SetHash(); err != nil {
		t.Fatalf("SetHash: %v", err)
	}
	originalHash := profile.ProfileHash

	profile.IssuedAt = issued.Add(45 * time.Second)
	profile.ExpiresAt = issued.Add(105 * time.Second)
	for index := range profile.Dependencies {
		profile.Dependencies[index].DNSExpiresAt = profile.ExpiresAt
	}
	if err := profile.SetHash(); err != nil {
		t.Fatalf("SetHash refreshed: %v", err)
	}
	if profile.ProfileHash != originalHash {
		t.Fatalf("freshness-only refresh changed routing identity: before=%s after=%s", originalHash, profile.ProfileHash)
	}

	profile.CarrierControlRoutes["api.vk.com"] = []string{"203.0.113.99/32"}
	profile.DNSSnapshot["api.vk.com"] = []string{"203.0.113.99"}
	profile.Dependencies[1].Addresses = []string{"203.0.113.99"}
	if err := profile.SetHash(); err != nil {
		t.Fatalf("SetHash changed route: %v", err)
	}
	if profile.ProfileHash == originalHash {
		t.Fatal("routing address change retained stale profile identity")
	}
}

func TestSystemVPNProfileValidationRejectsUnboundedOrSecretFields(t *testing.T) {
	issued := time.Date(2026, 7, 20, 12, 30, 0, 0, time.UTC)
	profile := SystemVPNProfile{
		SchemaRevision:   "system-vpn-profile.v1",
		DaemonInstanceID: "daemon-1",
		ProfileRevision:  1,
		SessionID:        "session-1",
		SelectedNodeID:   "node-1",
		Ready:            true,
		IssuedAt:         issued,
		ExpiresAt:        issued.Add(2 * time.Minute),
		SocksListen:      "127.0.0.1:1080",
		RouteMode:        "none",
		CarrierControlOrigins: []string{
			"https://user:secret@api.vk.com/method?access_token=secret#fragment",
		},
		DNSSnapshot: map[string][]string{"api.vk.com": {"203.0.113.10"}},
		DNSServers:  []string{"1.1.1.1"},
		MTU:         1500,
		Readiness:   SystemVPNReadiness{Ready: true, Provenance: "config+active-session+resolver"},
	}
	if err := profile.Validate(issued); err == nil {
		t.Fatal("profile with userinfo/query/fragment origin and missing host routes validated")
	}
}
