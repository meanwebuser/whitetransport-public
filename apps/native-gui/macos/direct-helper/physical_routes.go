package main

import (
	"fmt"
	"net/netip"
	"strings"
)

// physicalRoutePlan binds bypasses to a next hop of the same address family.
// This helper configures an IPv4 utun only: without a physical IPv6 default,
// omit redundant IPv6 bypasses and leave the pre-existing IPv6 table untouched.
// Explicit IPv6 tunnel destinations must fail, never silently use native egress.
func physicalRoutePlan(plan Plan, ipv6Gateway string) (Plan, error) {
	routes := make([]Route, 0, len(plan.Routes))
	for _, route := range plan.Routes {
		prefix, err := netip.ParsePrefix(route.CIDR)
		if err != nil {
			return Plan{}, fmt.Errorf("parse planned route: %w", err)
		}
		if prefix.Addr().Is6() {
			if route.Via == "utun" {
				return Plan{}, fmt.Errorf("IPv6 tunnel destinations are unsupported by the IPv4 direct helper")
			}
			if ipv6Gateway == "" {
				continue
			}
			gateway, err := netip.ParseAddr(ipv6Gateway)
			if err != nil || !gateway.Is6() || gateway.Is4In6() {
				return Plan{}, fmt.Errorf("physical IPv6 bypass requires an IPv6 gateway")
			}
			route.Gateway = ipv6Gateway
		}
		routes = append(routes, route)
	}
	plan.Routes = routes
	return plan, nil
}

func parsePhysicalIPv6DefaultRoute(output, physicalInterface string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "default" || fields[3] != physicalInterface {
			continue
		}
		gateway, err := netip.ParseAddr(fields[1])
		if err != nil || !gateway.Is6() || gateway.Is4In6() {
			continue
		}
		if gateway.IsLinkLocalUnicast() && gateway.Zone() == "" {
			gateway = gateway.WithZone(physicalInterface)
		}
		return gateway.String()
	}
	return ""
}
