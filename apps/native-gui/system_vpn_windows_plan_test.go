package main

import (
	"net/netip"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

func TestBuildWindowsVPNRoutePlanProtectsControlRoutes(t *testing.T) {
	now := time.Date(2026, 7, 23, 19, 0, 0, 0, time.UTC)
	profile := windowsVPNTestProfile(t, now, runtimeapi.SystemVPNRouteBypass)
	plan, err := buildWindowsVPNRoutePlan(profile, windowsPhysicalDefaultRoute{
		IPv4Gateway: netip.MustParseAddr("192.168.2.1"),
		IPv4IfIndex: 7,
		IPv6Gateway: netip.MustParseAddr("fe80::1"),
		IPv6IfIndex: 7,
	}, 42)
	if err != nil {
		t.Fatalf("build Windows VPN route plan: %v", err)
	}

	if plan.InterfaceName != windowsSystemVPNInterfaceName {
		t.Fatalf("interface name = %q, want %q", plan.InterfaceName, windowsSystemVPNInterfaceName)
	}
	if plan.InterfaceIPv4 != "100.64.0.1/30" || plan.InterfaceIPv6 != "fd00:5754::1/126" {
		t.Fatalf("interface addresses = (%q, %q)", plan.InterfaceIPv4, plan.InterfaceIPv6)
	}
	if !windowsRoutePlanContains(plan.TunnelRoutes, "0.0.0.0/1", 42) || !windowsRoutePlanContains(plan.TunnelRoutes, "128.0.0.0/1", 42) {
		t.Fatalf("full IPv4 split routes missing: %+v", plan.TunnelRoutes)
	}
	if !windowsRoutePlanContains(plan.TunnelRoutes, "::/1", 42) || !windowsRoutePlanContains(plan.TunnelRoutes, "8000::/1", 42) {
		t.Fatalf("full IPv6 split routes missing: %+v", plan.TunnelRoutes)
	}
	if !windowsRoutePlanContains(plan.BypassRoutes, "203.0.113.10/32", 7) {
		t.Fatalf("carrier/control bypass route missing: %+v", plan.BypassRoutes)
	}
	if !windowsRoutePlanContains(plan.BypassRoutes, "1.1.1.1/32", 7) {
		t.Fatalf("DNS bypass route missing: %+v", plan.BypassRoutes)
	}
	if !windowsRoutePlanContains(plan.BypassRoutes, "192.168.0.0/16", 7) {
		t.Fatalf("user bypass route missing: %+v", plan.BypassRoutes)
	}
	if !windowsRoutePlanContains(plan.BypassRoutes, "198.51.100.0/24", 7) {
		t.Fatalf("destination bypass route missing: %+v", plan.BypassRoutes)
	}
	if len(plan.CleanupRoutes) != len(plan.TunnelRoutes)+len(plan.BypassRoutes) {
		t.Fatalf("cleanup route count = %d, added route count = %d", len(plan.CleanupRoutes), len(plan.TunnelRoutes)+len(plan.BypassRoutes))
	}
}

func TestBuildWindowsVPNRoutePlanNoneMeansFullTunnel(t *testing.T) {
	now := time.Date(2026, 7, 23, 19, 0, 0, 0, time.UTC)
	profile := windowsVPNTestProfile(t, now, runtimeapi.SystemVPNRouteNone)
	plan, err := buildWindowsVPNRoutePlan(profile, windowsPhysicalDefaultRoute{IPv4Gateway: netip.MustParseAddr("192.168.2.1"), IPv4IfIndex: 7}, 42)
	if err != nil {
		t.Fatalf("build Windows full-tunnel route plan: %v", err)
	}
	if !windowsRoutePlanContains(plan.TunnelRoutes, "0.0.0.0/1", 42) || !windowsRoutePlanContains(plan.TunnelRoutes, "128.0.0.0/1", 42) {
		t.Fatalf("none mode did not install full-tunnel IPv4 routes: %+v", plan.TunnelRoutes)
	}
	if !windowsRoutePlanContains(plan.BypassRoutes, "203.0.113.10/32", 7) {
		t.Fatalf("none mode did not preserve control bypass: %+v", plan.BypassRoutes)
	}
}

func TestBuildWindowsVPNRoutePlanOmitsIPv6DefaultsWithoutPhysicalIPv6(t *testing.T) {
	now := time.Date(2026, 7, 23, 19, 0, 0, 0, time.UTC)
	profile := windowsVPNTestProfile(t, now, runtimeapi.SystemVPNRouteNone)
	plan, err := buildWindowsVPNRoutePlan(profile, windowsPhysicalDefaultRoute{
		IPv4Gateway: netip.MustParseAddr("192.168.2.1"),
		IPv4IfIndex: 7,
	}, 42)
	if err != nil {
		t.Fatalf("build Windows IPv4-only route plan: %v", err)
	}
	if windowsRoutePlanContains(plan.TunnelRoutes, "::/1", 42) || windowsRoutePlanContains(plan.TunnelRoutes, "8000::/1", 42) {
		t.Fatalf("IPv6 default routes installed without a physical IPv6 route: %+v", plan.TunnelRoutes)
	}
}

func TestBuildWindowsVPNRoutePlanOnlyDoesNotInstallDefaultRoutes(t *testing.T) {
	now := time.Date(2026, 7, 23, 19, 0, 0, 0, time.UTC)
	profile := windowsVPNTestProfile(t, now, runtimeapi.SystemVPNRouteOnly)
	profile.DestinationCIDRs = []string{"198.51.100.10/32", "2001:db8:1234::/128"}
	if err := profile.SetHash(); err != nil {
		t.Fatalf("set profile hash: %v", err)
	}
	plan, err := buildWindowsVPNRoutePlan(profile, windowsPhysicalDefaultRoute{IPv4Gateway: netip.MustParseAddr("192.168.2.1"), IPv4IfIndex: 7}, 42)
	if err != nil {
		t.Fatalf("build Windows VPN route plan: %v", err)
	}
	if len(plan.TunnelRoutes) != 2 || !windowsRoutePlanContains(plan.TunnelRoutes, "198.51.100.10/32", 42) || !windowsRoutePlanContains(plan.TunnelRoutes, "2001:db8:1234::/128", 42) {
		t.Fatalf("only routes = %+v", plan.TunnelRoutes)
	}
	if windowsRoutePlanContains(plan.TunnelRoutes, "0.0.0.0/1", 42) || windowsRoutePlanContains(plan.TunnelRoutes, "::/1", 42) {
		t.Fatalf("only mode installed default split routes: %+v", plan.TunnelRoutes)
	}
}

func windowsVPNTestProfile(t *testing.T, now time.Time, mode runtimeapi.SystemVPNRouteMode) runtimeapi.SystemVPNProfile {
	t.Helper()
	issued := now.Add(-time.Minute)
	profile := runtimeapi.SystemVPNProfile{
		SchemaRevision:        runtimeapi.SystemVPNProfileSchemaRevision,
		DaemonInstanceID:      "daemon-windows-test",
		ProfileRevision:       3,
		SessionID:             "session-windows-test",
		SelectedNodeID:        "node-windows-test",
		Ready:                 true,
		IssuedAt:              issued,
		ExpiresAt:             now.Add(4 * time.Minute),
		SocksListen:           "127.0.0.1:18890",
		RouteMode:             mode,
		DestinationCIDRs:      []string{"198.51.100.0/24"},
		UserBypassCIDRs:       []string{"192.168.0.0/16"},
		LANAccess:             true,
		CarrierControlOrigins: []string{"https://api.example.test"},
		CarrierControlRoutes:  map[string][]string{"api.example.test": {"203.0.113.10/32"}},
		DNSSnapshot:           map[string][]string{"api.example.test": {"203.0.113.10"}},
		DNSServers:            []string{"1.1.1.1"},
		Dependencies: []runtimeapi.SystemVPNDependency{{
			Purpose:      runtimeapi.SystemVPNDependencyControl,
			Carrier:      "wbstream",
			Scheme:       "https",
			Host:         "api.example.test",
			Port:         443,
			Addresses:    []string{"203.0.113.10"},
			DNSExpiresAt: now.Add(2 * time.Minute),
		}},
		MTU: 1500,
		Readiness: runtimeapi.SystemVPNReadiness{
			Ready:      true,
			Provenance: "windows-test",
		},
	}
	if mode == runtimeapi.SystemVPNRouteOnly {
		profile.DestinationCIDRs = []string{"198.51.100.10/32"}
		profile.LANAccess = false
	} else if mode == runtimeapi.SystemVPNRouteNone {
		profile.DestinationCIDRs = nil
		profile.LANAccess = false
	}
	if err := profile.SetHash(); err != nil {
		t.Fatalf("set profile hash: %v", err)
	}
	return profile
}
