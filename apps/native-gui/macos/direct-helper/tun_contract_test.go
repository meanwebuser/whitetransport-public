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
