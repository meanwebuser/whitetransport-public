package main

import (
	"encoding/json"
	"testing"
)

func TestPhysicalRoutePlanOnIPv4HotspotLeavesIPv6Untouched(t *testing.T) {
	plan := Config{Mode: "full", BypassCIDRs: []string{"87.240.129.140/32", "64:ff9b::57f0:818c/128"}}.RoutePlan()
	gateway := parsePhysicalIPv6DefaultRoute("default fe80::9%utun0 UGcIg utun0\n", "en0")
	if gateway != "" {
		t.Fatal("another tunnel's IPv6 default must not become a physical bypass gateway")
	}
	got, err := physicalRoutePlan(plan, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 3 {
		t.Fatalf("routes=%+v, want two IPv4 tunnel prefixes and IPv4 bypass", got.Routes)
	}
	if _, matched, err := got.RouteForIP("64:ff9b::57f0:818c"); err != nil || matched {
		t.Fatal("IPv4-only helper must not intercept native IPv6 routing")
	}
	if route, matched, _ := got.RouteForIP("8.8.8.8"); !matched || route.Via != "utun" {
		t.Fatal("ordinary IPv4 payload must still use the tunnel")
	}
	if route, matched, _ := got.RouteForIP("87.240.129.140"); !matched || route.Via != "gateway" {
		t.Fatal("IPv4 provider bypass must remain physical")
	}
}

func TestPhysicalRoutePlanPersistsScopedIPv6BypassForCleanup(t *testing.T) {
	gateway := parsePhysicalIPv6DefaultRoute("default fe80::9%utun0 UGcIg utun0\ndefault fe80::1 UGcIg en0\n", "en0")
	if gateway != "fe80::1%en0" {
		t.Fatalf("gateway=%q, want physical link-local gateway scoped to en0", gateway)
	}
	plan, err := physicalRoutePlan(Config{Mode: "bypass", BypassCIDRs: []string{"64:ff9b::57f0:818c/128"}}.RoutePlan(), gateway)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(State{Gateway: "172.20.10.1", Routes: plan.Routes})
	if err != nil {
		t.Fatal(err)
	}
	var restored State
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if got := darwinRouteNextHop(restored.Routes[2], restored.Gateway); got != gateway {
		t.Fatalf("cleanup next hop=%q, want installed IPv6 gateway %q", got, gateway)
	}
}

func TestPhysicalRoutePlanRejectsIPv6TunnelDestination(t *testing.T) {
	for _, gateway := range []string{"", "fe80::1%en0"} {
		if _, err := physicalRoutePlan(Config{Mode: "only", OnlyCIDRs: []string{"2001:db8::/32"}}.RoutePlan(), gateway); err == nil {
			t.Fatal("IPv6 ONLY route must fail explicitly rather than bypass the requested tunnel")
		}
	}
}
