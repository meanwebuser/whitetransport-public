package main

import (
	"encoding/json"
	"net/netip"
	"testing"
)

func TestChooseDirectTunPeerAvoidsForeignVPNPrefixes(t *testing.T) {
	for _, test := range []struct {
		name     string
		occupied []string
		want     string
	}{
		{"unused", nil, "198.18.0.1"},
		{"bezvpn-16", []string{"198.18.0.1/16"}, "198.19.0.1"},
		{"two-vpns", []string{"198.18.0.1/16", "198.19.0.1/24"}, "198.19.1.1"},
		{"whole-pool", []string{"198.18.0.1/15"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var occupied []netip.Prefix
			for _, cidr := range test.occupied {
				occupied = append(occupied, netip.MustParsePrefix(cidr))
			}
			peer, err := chooseDirectTunPeer(occupied)
			if (err != nil) != (test.want == "") || peer != test.want {
				t.Fatalf("peer=%q error=%v, want %q", peer, err, test.want)
			}
			if peer != "" {
				for _, prefix := range occupied {
					if prefix.Contains(netip.MustParseAddr(peer)) {
						t.Fatal("chosen peer overlaps another interface prefix")
					}
				}
			}
		})
	}
}

func TestRefreshAndCleanupPreserveAllocatedTunPeer(t *testing.T) {
	cfg := Config{Mode: "full", SOCKSHost: "127.0.0.1", SOCKSPort: 12345, MTU: 1500, DaemonInstanceID: "daemon-a", ProfileRevision: 7}
	peer := "198.19.0.1"
	current := State{PID: 123, Interface: "utun8", TunPeer: peer, Gateway: "192.0.2.1", Config: cfg, Routes: bindOwnedTunPeer(cfg.RoutePlan(), peer).Routes}
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var restored State
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	for _, route := range restored.Routes {
		if got := darwinRouteNextHop(route, restored.Gateway); got != peer {
			t.Fatalf("cleanup next hop=%q, want persisted allocated peer", got)
		}
	}
	next := cfg
	next.ProfileRevision++
	mutations := 0
	changed := func(Route) error { mutations++; return nil }
	got, err := refreshState(restored, next, next.RoutePlan(), changed, changed, func(State) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if mutations != 0 || got.TunPeer != peer || got.Interface != current.Interface {
		t.Fatal("metadata refresh changed the allocated route or interface")
	}
	for _, route := range got.Routes {
		if darwinRouteNextHop(route, got.Gateway) != peer {
			t.Fatal("refresh reverted to another VPN's default peer")
		}
	}
}
