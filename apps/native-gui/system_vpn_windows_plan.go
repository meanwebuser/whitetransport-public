package main

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

const (
	windowsSystemVPNInterfaceName = "WhiteTransport"
	windowsSystemVPNInterfaceIPv4 = "100.64.0.1/30"
	windowsSystemVPNInterfaceIPv6 = "fd00:5754::1/126"
)

type windowsPhysicalDefaultRoute struct {
	IPv4Gateway netip.Addr
	IPv4IfIndex int
	IPv6Gateway netip.Addr
	IPv6IfIndex int
}

type windowsVPNRoute struct {
	Destination    netip.Prefix
	Gateway        netip.Addr
	InterfaceIndex int
}

type windowsVPNRoutePlan struct {
	InterfaceName  string
	InterfaceIPv4  string
	InterfaceIPv6  string
	InterfaceIndex int
	TunnelRoutes   []windowsVPNRoute
	BypassRoutes   []windowsVPNRoute
	CleanupRoutes  []windowsVPNRoute
}

// buildWindowsVPNRoutePlan converts the authoritative profile into only the
// routes owned by this session. Physical gateways are carried explicitly so
// cleanup can remove exactly what the session added without rewriting the
// user's pre-existing route table.
func buildWindowsVPNRoutePlan(profile runtimeapi.SystemVPNProfile, physical windowsPhysicalDefaultRoute, tunInterfaceIndex int) (windowsVPNRoutePlan, error) {
	if tunInterfaceIndex <= 0 {
		return windowsVPNRoutePlan{}, fmt.Errorf("Windows TUN interface index must be positive")
	}
	plan := windowsVPNRoutePlan{
		InterfaceName:  windowsSystemVPNInterfaceName,
		InterfaceIPv4:  windowsSystemVPNInterfaceIPv4,
		InterfaceIPv6:  windowsSystemVPNInterfaceIPv6,
		InterfaceIndex: tunInterfaceIndex,
	}
	addTunnel := func(raw string) error {
		prefix, err := parseWindowsRoutePrefix(raw)
		if err != nil {
			return err
		}
		plan.TunnelRoutes = append(plan.TunnelRoutes, windowsVPNRoute{Destination: prefix, InterfaceIndex: tunInterfaceIndex})
		return nil
	}
	addBypass := func(raw string) error {
		prefix, err := parseWindowsRoutePrefix(raw)
		if err != nil {
			return err
		}
		gateway, interfaceIndex, err := physicalRouteForPrefix(prefix, physical)
		if err != nil {
			// A host without an IPv6 uplink cannot send an IPv6 bypass route
			// anywhere. Keep the route policy fail-closed for the available
			// families without blocking the usable IPv4 system VPN.
			if prefix.Addr().Is6() && !physical.IPv6Gateway.IsValid() {
				return nil
			}
			return err
		}
		plan.BypassRoutes = append(plan.BypassRoutes, windowsVPNRoute{Destination: prefix, Gateway: gateway, InterfaceIndex: interfaceIndex})
		return nil
	}

	switch profile.RouteMode {
	case runtimeapi.SystemVPNRouteNone:
		for _, raw := range windowsDefaultTunnelCIDRs(physical) {
			if err := addTunnel(raw); err != nil {
				return windowsVPNRoutePlan{}, err
			}
		}
		for _, raw := range appendWindowsBypassCIDRs(profile) {
			if err := addBypass(raw); err != nil {
				return windowsVPNRoutePlan{}, err
			}
		}
	case runtimeapi.SystemVPNRouteOnly:
		for _, raw := range profile.DestinationCIDRs {
			if err := addTunnel(raw); err != nil {
				return windowsVPNRoutePlan{}, err
			}
		}
	case runtimeapi.SystemVPNRouteBypass:
		for _, raw := range windowsDefaultTunnelCIDRs(physical) {
			if err := addTunnel(raw); err != nil {
				return windowsVPNRoutePlan{}, err
			}
		}
		for _, raw := range appendWindowsBypassCIDRs(profile) {
			if err := addBypass(raw); err != nil {
				return windowsVPNRoutePlan{}, err
			}
		}
	default:
		return windowsVPNRoutePlan{}, fmt.Errorf("unsupported Windows system VPN route mode %q", profile.RouteMode)
	}

	plan.TunnelRoutes = uniqueWindowsVPNRoutes(plan.TunnelRoutes)
	plan.BypassRoutes = uniqueWindowsVPNRoutes(plan.BypassRoutes)
	plan.CleanupRoutes = append(plan.CleanupRoutes, plan.TunnelRoutes...)
	plan.CleanupRoutes = append(plan.CleanupRoutes, plan.BypassRoutes...)
	plan.CleanupRoutes = uniqueWindowsVPNRoutes(plan.CleanupRoutes)
	return plan, nil
}

func windowsDefaultTunnelCIDRs(physical windowsPhysicalDefaultRoute) []string {
	cidrs := []string{"0.0.0.0/1", "128.0.0.0/1"}
	if physical.IPv6Gateway.IsValid() && physical.IPv6IfIndex > 0 {
		cidrs = append(cidrs, "::/1", "8000::/1")
	}
	return cidrs
}

func appendWindowsBypassCIDRs(profile runtimeapi.SystemVPNProfile) []string {
	values := make([]string, 0, len(profile.UserBypassCIDRs)+len(profile.DestinationCIDRs)+len(profile.DNSServers)+len(profile.CarrierControlRoutes))
	values = append(values, profile.UserBypassCIDRs...)
	values = append(values, profile.DestinationCIDRs...)
	for _, server := range profile.DNSServers {
		if address, err := netip.ParseAddr(strings.TrimSpace(server)); err == nil {
			values = append(values, address.String()+"/"+fmt.Sprint(address.BitLen()))
		}
	}
	for _, routes := range profile.CarrierControlRoutes {
		values = append(values, routes...)
	}
	return values
}

func parseWindowsRoutePrefix(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse Windows route %q: %w", raw, err)
	}
	return prefix.Masked(), nil
}

func physicalRouteForPrefix(prefix netip.Prefix, physical windowsPhysicalDefaultRoute) (netip.Addr, int, error) {
	if prefix.Addr().Is4() {
		if !physical.IPv4Gateway.IsValid() || physical.IPv4IfIndex <= 0 {
			return netip.Addr{}, 0, fmt.Errorf("no IPv4 physical default route for bypass %s", prefix)
		}
		return physical.IPv4Gateway, physical.IPv4IfIndex, nil
	}
	if !physical.IPv6Gateway.IsValid() || physical.IPv6IfIndex <= 0 {
		return netip.Addr{}, 0, fmt.Errorf("no IPv6 physical default route for bypass %s", prefix)
	}
	return physical.IPv6Gateway, physical.IPv6IfIndex, nil
}

func uniqueWindowsVPNRoutes(routes []windowsVPNRoute) []windowsVPNRoute {
	seen := make(map[string]struct{}, len(routes))
	unique := make([]windowsVPNRoute, 0, len(routes))
	for _, route := range routes {
		key := fmt.Sprintf("%s|%s|%d", route.Destination, route.Gateway, route.InterfaceIndex)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, route)
	}
	sort.Slice(unique, func(i, j int) bool {
		left := fmt.Sprintf("%s|%s|%d", unique[i].Destination, unique[i].Gateway, unique[i].InterfaceIndex)
		right := fmt.Sprintf("%s|%s|%d", unique[j].Destination, unique[j].Gateway, unique[j].InterfaceIndex)
		return left < right
	})
	return unique
}

func subtractExistingWindowsVPNRoutes(plan windowsVPNRoutePlan, existing []windowsVPNRoute) windowsVPNRoutePlan {
	seen := make(map[string]struct{}, len(existing))
	for _, route := range existing {
		seen[windowsVPNRouteKey(route)] = struct{}{}
	}
	filter := func(routes []windowsVPNRoute) []windowsVPNRoute {
		filtered := make([]windowsVPNRoute, 0, len(routes))
		for _, route := range routes {
			if _, exists := seen[windowsVPNRouteKey(route)]; exists {
				continue
			}
			filtered = append(filtered, route)
		}
		return filtered
	}
	plan.TunnelRoutes = filter(plan.TunnelRoutes)
	plan.BypassRoutes = filter(plan.BypassRoutes)
	plan.CleanupRoutes = append(append([]windowsVPNRoute(nil), plan.TunnelRoutes...), plan.BypassRoutes...)
	return plan
}

func windowsVPNRouteKey(route windowsVPNRoute) string {
	return fmt.Sprintf("%s|%s|%d", route.Destination, route.Gateway, route.InterfaceIndex)
}

func windowsRoutePlanContains(routes []windowsVPNRoute, destination string, interfaceIndex int) bool {
	prefix, err := parseWindowsRoutePrefix(destination)
	if err != nil {
		return false
	}
	for _, route := range routes {
		if route.Destination == prefix && route.InterfaceIndex == interfaceIndex {
			return true
		}
	}
	return false
}
