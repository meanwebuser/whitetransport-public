package main

import (
	"path/filepath"
	"testing"
)

func TestConfigDefaultsUseApplicationSupportTun2Socks(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	cfg := Config{SOCKSHost: "127.0.0.1", SOCKSPort: 1080, MTU: 1500}
	cfg.setDefaults()
	want := filepath.Join("/Users/alice", "Library", "Application Support", "WhiteTransport", "bin", "tun2socks")
	if cfg.Tun2SocksPath != want {
		t.Fatalf("tun2socks path = %q, want %q", cfg.Tun2SocksPath, want)
	}
	if filepath.Clean(cfg.Tun2SocksPath) == "/usr/local/bin/tun2socks" {
		t.Fatal("config must not depend on an external /usr/local tun2socks")
	}
}

func TestConfigRejectsNonLoopbackSOCKS(t *testing.T) {
	cfg := Config{SOCKSHost: "10.0.0.2", SOCKSPort: 1080, Mode: "full", MTU: 1500}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-loopback SOCKS rejection")
	}
}

func TestRoutePlans(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "full", cfg: Config{SOCKSHost: "127.0.0.1", SOCKSPort: 1080, Mode: "full", MTU: 1500}, want: []string{"0.0.0.0/1", "128.0.0.0/1"}},
		{name: "full carrier bypass", cfg: Config{SOCKSHost: "127.0.0.1", SOCKSPort: 1080, Mode: "full", BypassCIDRs: []string{"198.51.100.10/32", "2001:db8::10/128"}, MTU: 1500}, want: []string{"0.0.0.0/1", "128.0.0.0/1", "198.51.100.10/32", "2001:db8::10/128"}},
		{name: "bypass", cfg: Config{SOCKSHost: "127.0.0.1", SOCKSPort: 1080, Mode: "bypass", BypassCIDRs: []string{"192.0.2.0/24"}, MTU: 1500}, want: []string{"0.0.0.0/1", "128.0.0.0/1", "192.0.2.0/24"}},
		{name: "only", cfg: Config{SOCKSHost: "::1", SOCKSPort: 1080, Mode: "only", OnlyCIDRs: []string{"198.51.100.0/24"}, MTU: 1500}, want: []string{"198.51.100.0/24"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			plan := tt.cfg.RoutePlan()
			if len(plan.Routes) != len(tt.want) {
				t.Fatalf("routes=%v, want %v", plan.Routes, tt.want)
			}
			for i, route := range plan.Routes {
				if route.CIDR != tt.want[i] {
					t.Fatalf("route[%d]=%s, want %s", i, route.CIDR, tt.want[i])
				}
			}
		})
	}
}

func TestRoutePlanDecisionKeepsSplitLegsOnTheirIntendedPath(t *testing.T) {
	cfg := Config{
		SOCKSHost: "127.0.0.1",
		SOCKSPort: 1080,
		Mode:      "only",
		OnlyCIDRs: []string{"203.0.113.0/24"},
		MTU:       1500,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	plan := cfg.RoutePlan()

	tunneled, matched, err := plan.RouteForIP("203.0.113.42")
	if err != nil {
		t.Fatalf("included route lookup: %v", err)
	}
	if !matched || tunneled.Via != "utun" || tunneled.CIDR != "203.0.113.0/24" {
		t.Fatalf("included destination decision = %#v matched=%t, want utun route", tunneled, matched)
	}

	_, matched, err = plan.RouteForIP("198.51.100.42")
	if err != nil {
		t.Fatalf("excluded route lookup: %v", err)
	}
	if matched {
		t.Fatal("non-destination unexpectedly matched the utun leg; it should remain on the physical route")
	}
}

func TestRoutePlanDecisionPrefersSpecificPhysicalBypassOverFullTunnel(t *testing.T) {
	cfg := Config{
		SOCKSHost:   "127.0.0.1",
		SOCKSPort:   1080,
		Mode:        "full",
		BypassCIDRs: []string{"198.51.100.10/32"},
		MTU:         1500,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	plan := cfg.RoutePlan()

	bypass, matched, err := plan.RouteForIP("198.51.100.10")
	if err != nil {
		t.Fatalf("bypass route lookup: %v", err)
	}
	if !matched || bypass.Via != "gateway" || bypass.CIDR != "198.51.100.10/32" {
		t.Fatalf("bypass decision = %#v matched=%t, want gateway host route", bypass, matched)
	}

	tunneled, matched, err := plan.RouteForIP("198.51.100.11")
	if err != nil {
		t.Fatalf("full-tunnel route lookup: %v", err)
	}
	if !matched || tunneled.Via != "utun" {
		t.Fatalf("ordinary decision = %#v matched=%t, want utun route", tunneled, matched)
	}
}
