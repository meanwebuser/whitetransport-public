//go:build darwin

package main

import "testing"

func TestParseRouteGetDefault(t *testing.T) {
	out := `   route to: default
destination: default
       mask: default
    gateway: 192.168.2.1
  interface: en0
`

	gateway, iface := parseRouteGetDefault(out)
	if gateway != "192.168.2.1" || iface != "en0" {
		t.Fatalf("parseRouteGetDefault() = %q, %q; want 192.168.2.1, en0", gateway, iface)
	}
}

func TestParseRouteGetDefaultRejectsInterfaceOnlyTunnel(t *testing.T) {
	out := `   route to: default
destination: default
       mask: default
  interface: utun7
`

	gateway, iface := parseRouteGetDefault(out)
	if gateway != "" || iface != "" {
		t.Fatalf("parseRouteGetDefault() = %q, %q; want empty result", gateway, iface)
	}
}

func TestParsePhysicalDefaultRouteSkipsInterfaceOnlyTunnel(t *testing.T) {
	out := `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            link#27            UCSg                utun7
default            192.168.2.1        UGScIg                en0
default            link#26            UCSIg               utun6
`

	gateway, iface := parsePhysicalDefaultRoute(out)
	if gateway != "192.168.2.1" || iface != "en0" {
		t.Fatalf("parsePhysicalDefaultRoute() = %q, %q; want 192.168.2.1, en0", gateway, iface)
	}
}

func TestParsePhysicalDefaultRouteRejectsInterfaceOnlyRoutes(t *testing.T) {
	out := `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            link#27            UCSg                utun7
`

	gateway, iface := parsePhysicalDefaultRoute(out)
	if gateway != "" || iface != "" {
		t.Fatalf("parsePhysicalDefaultRoute() = %q, %q; want empty result", gateway, iface)
	}
}

func TestParsePhysicalDNSResolversSelectsPhysicalInterface(t *testing.T) {
	out := `DNS configuration

resolver #1
  nameserver[0] : 100.100.100.100
  nameserver[1] : fd7a:115c:a1e0::53
  if_index : 26 (utun6)

DNS configuration (for scoped queries)

resolver #1
  nameserver[0] : 1.1.1.1
  nameserver[1] : 1.1.1.1
  if_index : 16 (en0)
`

	resolvers := parsePhysicalDNSResolvers(out, "en0")
	if len(resolvers) != 1 || resolvers[0] != "1.1.1.1" {
		t.Fatalf("parsePhysicalDNSResolvers() = %#v; want [1.1.1.1]", resolvers)
	}
}

func TestAddPhysicalDNSBypassesDeduplicatesExistingRoute(t *testing.T) {
	plan := Plan{Mode: "full", Routes: []Route{
		{CIDR: "0.0.0.0/1", Via: "utun", Kind: "full"},
		{CIDR: "1.1.1.1/32", Via: "gateway", Kind: "bypass"},
	}}

	got := addPhysicalDNSBypasses(plan, []string{"1.1.1.1", "8.8.8.8"})
	if len(got.Routes) != 3 {
		t.Fatalf("route count = %d; want 3", len(got.Routes))
	}
	last := got.Routes[2]
	if last.CIDR != "8.8.8.8/32" || last.Via != "gateway" || last.Kind != "dns-bypass" {
		t.Fatalf("last route = %+v; want physical DNS bypass", last)
	}
}
