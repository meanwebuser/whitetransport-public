package main

import "testing"

func TestDirectTunAddressContractMatchesMacPacketTunnel(t *testing.T) {
	local, peer := directTunAddresses()
	if local != "198.18.0.1" || peer != "198.18.0.1" {
		t.Fatalf("direct utun addresses local=%q peer=%q, want local=198.18.0.1 peer=198.18.0.1", local, peer)
	}
}

func TestTun2SocksCommandBindsPhysicalInterface(t *testing.T) {
	args := tun2socksArgs("socks5://127.0.0.1:8809", 1500, "en0")
	want := []string{"-device", "tun://utun", "-proxy", "socks5://127.0.0.1:8809", "-mtu", "1500", "-interface", "en0", "-loglevel", "warn"}
	if len(args) != len(want) {
		t.Fatalf("tun2socks args=%v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("tun2socks arg[%d]=%q, want %q (args=%v)", i, args[i], want[i], args)
		}
	}
}

func TestDarwinUtunRouteUsesConfiguredPointToPointPeer(t *testing.T) {
	got := darwinRouteNextHop(Route{CIDR: "8.47.69.0/32", Via: "utun"}, "192.0.2.1")
	if got != "198.18.0.1" {
		t.Fatalf("utun route next hop=%q, want synthetic peer 198.18.0.1", got)
	}
}

func TestDarwinUtunRouteUsesOwnedPeerWhenAnotherVPNHasDefaultPeer(t *testing.T) {
	owned := Route{CIDR: "0.0.0.0/1", Via: "utun", Gateway: "198.19.0.1"}
	if got := darwinRouteNextHop(owned, "192.0.2.1"); got != owned.Gateway {
		t.Fatalf("route next hop=%q aliases foreign VPN198.18.0.1; want owned peer %q", got, owned.Gateway)
	}
}

func TestDarwinIPv6BypassUsesStoredPhysicalGateway(t *testing.T) {
	route := Route{CIDR: "64:ff9b::57f0:818c/128", Via: "gateway", Gateway: "fe80::1%en0"}
	if got := darwinRouteNextHop(route, "172.20.10.1"); got != route.Gateway {
		t.Fatalf("IPv6 bypass next hop=%q, want scoped IPv6 gateway %q", got, route.Gateway)
	}
}
