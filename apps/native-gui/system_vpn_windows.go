//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
	tunengine "github.com/xjasonlyu/tun2socks/v2/engine"
)

const windowsSystemVPNMTU = 1500

var errWindowsSystemVPNOperation = errors.New("Windows system VPN operation failed")

type windowsCommandRunner func(context.Context, ...string) ([]byte, error)

type windowsTunEngine interface {
	Start(proxy string, device string, mtu int) error
	Stop() error
}

type productionWindowsTunEngine struct{}

func (productionWindowsTunEngine) Start(proxy string, device string, mtu int) error {
	tunengine.Insert(&tunengine.Key{Proxy: proxy, Device: device, MTU: mtu, LogLevel: "warn"})
	return tunengine.Start()
}

func (productionWindowsTunEngine) Stop() error { return tunengine.Stop() }

type windowsSystemVPNRuntime struct {
	identity       systemVPNProfileIdentity
	plan           windowsVPNRoutePlan
	tunInterfaceID int
}

type windowsSystemVPNHost struct {
	runner windowsCommandRunner
	engine windowsTunEngine

	mu     sync.Mutex
	active *windowsSystemVPNRuntime
	logs   []guiruntime.LogLine
}

func newSystemVPNHost() systemVPNHost {
	return newWindowsSystemVPNHost()
}

func newWindowsSystemVPNHost() *windowsSystemVPNHost {
	return &windowsSystemVPNHost{runner: runWindowsPowerShell, engine: productionWindowsTunEngine{}}
}

func (h *windowsSystemVPNHost) Supported() bool { return true }

func (h *windowsSystemVPNHost) Permission(ctx context.Context) (systemVPNObservation, error) {
	output, err := h.run(ctx, windowsAdministratorProbeScript())
	if err != nil {
		return systemVPNObservation{State: guiruntime.SystemVPNPermissionRequired, ProviderState: guiruntime.SystemVPNPermissionRequired}, fmt.Errorf("permission_required: Windows administrator privileges are required: %w", err)
	}
	if strings.TrimSpace(string(output)) != "true" {
		return systemVPNObservation{State: guiruntime.SystemVPNPermissionRequired, ProviderState: guiruntime.SystemVPNPermissionRequired}, fmt.Errorf("permission_required: Windows administrator privileges are required")
	}
	return h.Status(ctx)
}

func (h *windowsSystemVPNHost) Start(ctx context.Context, raw json.RawMessage) (systemVPNObservation, error) {
	identity, err := decodeSystemVPNProfileIdentity(raw)
	if err != nil {
		return systemVPNObservation{}, err
	}
	var profile runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return systemVPNObservation{}, fmt.Errorf("decode Windows system VPN profile: %w", err)
	}
	if err := profile.Validate(time.Now().UTC()); err != nil {
		return systemVPNObservation{}, fmt.Errorf("validate Windows system VPN profile: %w", err)
	}
	if _, err := h.Permission(ctx); err != nil {
		return systemVPNObservation{}, err
	}

	h.mu.Lock()
	if h.active != nil {
		h.mu.Unlock()
		return systemVPNObservation{}, fmt.Errorf("%w: Windows system VPN is already running", errWindowsSystemVPNOperation)
	}
	h.mu.Unlock()

	proxy, err := windowsSOCKSProxy(profile.SocksListen)
	if err != nil {
		return systemVPNObservation{}, err
	}
	if err := h.engine.Start(proxy, "tun://"+windowsSystemVPNInterfaceName, profile.MTU); err != nil {
		return systemVPNObservation{}, fmt.Errorf("%w: start Wintun/tun2socks: %v", errWindowsSystemVPNOperation, err)
	}
	started := true
	cleanupEngine := func() {
		if started {
			_ = h.engine.Stop()
			started = false
		}
	}

	snapshot, err := h.readNetworkSnapshot(ctx)
	if err != nil {
		cleanupEngine()
		return systemVPNObservation{}, fmt.Errorf("%w: read Windows network snapshot: %v", errWindowsSystemVPNOperation, err)
	}
	plan, err := buildWindowsVPNRoutePlan(profile, snapshot.physical, snapshot.tunInterfaceID)
	if err != nil {
		cleanupEngine()
		return systemVPNObservation{}, fmt.Errorf("%w: build Windows route plan: %v", errWindowsSystemVPNOperation, err)
	}
	plan = appendWindowsOnLinkDNSRoutes(plan, snapshot.dnsServers, snapshot.physical)
	plan = subtractExistingWindowsVPNRoutes(plan, snapshot.existingRoutes)
	configureOutput, err := h.run(ctx, windowsConfigureScript(plan))
	if err != nil {
		_, _ = h.run(ctx, windowsRemoveRoutesScript(plan))
		cleanupEngine()
		_, _ = h.run(ctx, windowsRemoveInterfaceScript(plan.InterfaceName))
		return systemVPNObservation{}, fmt.Errorf("%w: configure Wintun routes: %v (%s)", errWindowsSystemVPNOperation, err, strings.TrimSpace(string(configureOutput)))
	}
	started = false
	// The engine is still running; started only governs the error cleanup path.
	runtime := &windowsSystemVPNRuntime{identity: identity, plan: plan, tunInterfaceID: snapshot.tunInterfaceID}
	h.mu.Lock()
	h.active = runtime
	h.appendLogLocked("Windows Wintun system VPN connected")
	h.mu.Unlock()
	return windowsObservation(guiruntime.SystemVPNConnected, runtime), nil
}

func (h *windowsSystemVPNHost) Stop(ctx context.Context) (systemVPNObservation, error) {
	h.mu.Lock()
	runtime := h.active
	h.active = nil
	h.mu.Unlock()
	if runtime == nil {
		return systemVPNObservation{State: guiruntime.SystemVPNDisconnected, ProviderState: guiruntime.SystemVPNDisconnected}, nil
	}

	var stopErr error
	if _, err := h.run(ctx, windowsRemoveRoutesScript(runtime.plan)); err != nil {
		stopErr = errors.Join(stopErr, fmt.Errorf("remove Windows VPN routes: %w", err))
	}
	if err := h.engine.Stop(); err != nil {
		stopErr = errors.Join(stopErr, fmt.Errorf("stop Wintun/tun2socks: %w", err))
	}
	if _, err := h.run(ctx, windowsRemoveInterfaceScript(runtime.plan.InterfaceName)); err != nil {
		stopErr = errors.Join(stopErr, fmt.Errorf("remove Windows Wintun interface: %w", err))
	}
	h.mu.Lock()
	h.appendLogLocked("Windows Wintun system VPN disconnected")
	h.mu.Unlock()
	if stopErr != nil {
		return windowsObservation(guiruntime.SystemVPNError, runtime), stopErr
	}
	return windowsObservation(guiruntime.SystemVPNDisconnected, runtime), nil
}

func (h *windowsSystemVPNHost) Status(context.Context) (systemVPNObservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		return systemVPNObservation{State: guiruntime.SystemVPNDisconnected, ProviderState: guiruntime.SystemVPNDisconnected}, nil
	}
	return windowsObservation(guiruntime.SystemVPNConnected, h.active), nil
}

func (h *windowsSystemVPNHost) Logs(context.Context) ([]guiruntime.LogLine, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	logs := make([]guiruntime.LogLine, len(h.logs))
	copy(logs, h.logs)
	return logs, nil
}

func (h *windowsSystemVPNHost) appendLogLocked(message string) {
	h.logs = append(h.logs, guiruntime.LogLine{Level: "info", Message: message, Fields: map[string]string{"source": "windows-system-vpn"}})
}

func (h *windowsSystemVPNHost) run(ctx context.Context, script string) ([]byte, error) {
	if h == nil || h.runner == nil {
		return nil, fmt.Errorf("Windows PowerShell runner is unavailable")
	}
	return h.runner(ctx, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
}

type windowsNetworkSnapshot struct {
	tunInterfaceID int
	physical       windowsPhysicalDefaultRoute
	existingRoutes []windowsVPNRoute
	dnsServers     []string
}

type windowsNetworkSnapshotJSON struct {
	TunInterfaceID int                        `json:"tun_if_index"`
	IPv4Gateway    string                     `json:"ipv4_gateway"`
	IPv4IfIndex    int                        `json:"ipv4_if_index"`
	IPv6Gateway    string                     `json:"ipv6_gateway"`
	IPv6IfIndex    int                        `json:"ipv6_if_index"`
	Routes         []windowsSnapshotRouteJSON `json:"routes"`
	DNSServers     []string                   `json:"dns_servers"`
}

type windowsSnapshotRouteJSON struct {
	DestinationPrefix string `json:"destination_prefix"`
	NextHop           string `json:"next_hop"`
	InterfaceIndex    int    `json:"if_index"`
}

func (h *windowsSystemVPNHost) readNetworkSnapshot(ctx context.Context) (windowsNetworkSnapshot, error) {
	output, err := h.run(ctx, windowsNetworkSnapshotScript())
	if err != nil {
		return windowsNetworkSnapshot{}, fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(output)))
	}
	var value windowsNetworkSnapshotJSON
	if err := json.Unmarshal(output, &value); err != nil {
		return windowsNetworkSnapshot{}, fmt.Errorf("decode Windows network snapshot: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	if value.TunInterfaceID <= 0 {
		return windowsNetworkSnapshot{}, fmt.Errorf("Windows Wintun interface index is missing")
	}
	physical := windowsPhysicalDefaultRoute{IPv4IfIndex: value.IPv4IfIndex, IPv6IfIndex: value.IPv6IfIndex}
	if value.IPv4Gateway != "" {
		physical.IPv4Gateway, err = netip.ParseAddr(value.IPv4Gateway)
		if err != nil {
			return windowsNetworkSnapshot{}, fmt.Errorf("parse Windows IPv4 gateway: %w", err)
		}
	}
	if value.IPv6Gateway != "" {
		physical.IPv6Gateway, err = netip.ParseAddr(value.IPv6Gateway)
		if err != nil {
			return windowsNetworkSnapshot{}, fmt.Errorf("parse Windows IPv6 gateway: %w", err)
		}
	}
	existingRoutes := make([]windowsVPNRoute, 0, len(value.Routes))
	for _, route := range value.Routes {
		prefix, prefixErr := parseWindowsRoutePrefix(route.DestinationPrefix)
		if prefixErr != nil || route.InterfaceIndex <= 0 {
			continue
		}
		gateway := netip.Addr{}
		if route.NextHop != "" {
			gateway, err = netip.ParseAddr(route.NextHop)
			if err != nil {
				continue
			}
		}
		existingRoutes = append(existingRoutes, windowsVPNRoute{Destination: prefix, Gateway: gateway, InterfaceIndex: route.InterfaceIndex})
	}
	return windowsNetworkSnapshot{tunInterfaceID: value.TunInterfaceID, physical: physical, existingRoutes: existingRoutes, dnsServers: value.DNSServers}, nil
}

func appendWindowsOnLinkDNSRoutes(plan windowsVPNRoutePlan, servers []string, physical windowsPhysicalDefaultRoute) windowsVPNRoutePlan {
	for _, raw := range servers {
		address, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		interfaceIndex := physical.IPv4IfIndex
		if address.Is6() {
			interfaceIndex = physical.IPv6IfIndex
		}
		if interfaceIndex <= 0 {
			continue
		}
		plan.BypassRoutes = append(plan.BypassRoutes, windowsVPNRoute{Destination: netip.PrefixFrom(address, address.BitLen()), InterfaceIndex: interfaceIndex})
	}
	plan.BypassRoutes = uniqueWindowsVPNRoutes(plan.BypassRoutes)
	plan.CleanupRoutes = append(append([]windowsVPNRoute(nil), plan.TunnelRoutes...), plan.BypassRoutes...)
	return plan
}

func windowsObservation(state guiruntime.SystemVPNState, runtime *windowsSystemVPNRuntime) systemVPNObservation {
	if runtime == nil {
		return systemVPNObservation{State: state, ProviderState: state}
	}
	return systemVPNObservation{State: state, ProviderState: state, DaemonInstanceID: runtime.identity.DaemonInstanceID, Revision: runtime.identity.Revision, SessionID: runtime.identity.SessionID, ProfileHash: runtime.identity.ProfileHash, ProfileValidUntil: runtime.identity.ProfileValidUntil}
}

func windowsSOCKSProxy(listen string) (string, error) {
	address, err := netip.ParseAddrPort(listen)
	if err != nil || !address.Addr().IsLoopback() || address.Port() == 0 {
		return "", fmt.Errorf("Windows system VPN SOCKS listener %q is invalid", listen)
	}
	return "socks5://" + listen, nil
}

func runWindowsPowerShell(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "powershell.exe", args...).CombinedOutput()
}

func windowsAdministratorProbeScript() string {
	return "$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent()); if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { 'true' } else { 'false'; exit 5 }"
}

func windowsNetworkSnapshotScript() string {
	return "$adapter = Get-NetAdapter -Name '" + windowsSystemVPNInterfaceName + "' -ErrorAction Stop; $v4 = @(Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Sort-Object RouteMetric,InterfaceMetric | Select-Object -First 1); $v6 = @(Get-NetRoute -AddressFamily IPv6 -DestinationPrefix '::/0' -ErrorAction SilentlyContinue | Sort-Object RouteMetric,InterfaceMetric | Select-Object -First 1); $routes = @(Get-NetRoute -AddressFamily IPv4 -ErrorAction SilentlyContinue | Select-Object @{n='destination_prefix';e={$_.DestinationPrefix}},@{n='next_hop';e={$_.NextHop}},@{n='if_index';e={[int]$_.ifIndex}}) + @(Get-NetRoute -AddressFamily IPv6 -ErrorAction SilentlyContinue | Select-Object @{n='destination_prefix';e={$_.DestinationPrefix}},@{n='next_hop';e={$_.NextHop}},@{n='if_index';e={[int]$_.ifIndex}}); $dns = @(); if ($v4.Count -gt 0) { $dns += @(Get-DnsClientServerAddress -AddressFamily IPv4 -InterfaceIndex $v4[0].ifIndex -ErrorAction SilentlyContinue | Select-Object -ExpandProperty ServerAddresses) }; if ($v6.Count -gt 0) { $dns += @(Get-DnsClientServerAddress -AddressFamily IPv6 -InterfaceIndex $v6[0].ifIndex -ErrorAction SilentlyContinue | Select-Object -ExpandProperty ServerAddresses) }; @{tun_if_index=[int]$adapter.ifIndex; ipv4_gateway=if ($v4.Count -gt 0) {$v4[0].NextHop} else {''}; ipv4_if_index=if ($v4.Count -gt 0) {[int]$v4[0].ifIndex} else {0}; ipv6_gateway=if ($v6.Count -gt 0) {$v6[0].NextHop} else {''}; ipv6_if_index=if ($v6.Count -gt 0) {[int]$v6[0].ifIndex} else {0}; routes=$routes; dns_servers=$dns} | ConvertTo-Json -Depth 5 -Compress"
}

func windowsConfigureScript(plan windowsVPNRoutePlan) string {
	commands := []string{
		"$ErrorActionPreference = 'Stop'",
		"New-NetIPAddress -InterfaceIndex " + strconv.Itoa(plan.InterfaceIndex) + " -IPAddress '100.64.0.1' -PrefixLength 30 -AddressFamily IPv4 -Type Unicast -PolicyStore ActiveStore -ErrorAction SilentlyContinue | Out-Null",
		"New-NetIPAddress -InterfaceIndex " + strconv.Itoa(plan.InterfaceIndex) + " -IPAddress 'fd00:5754::1' -PrefixLength 126 -AddressFamily IPv6 -Type Unicast -PolicyStore ActiveStore -ErrorAction SilentlyContinue | Out-Null",
	}
	for _, route := range append(append([]windowsVPNRoute(nil), plan.TunnelRoutes...), plan.BypassRoutes...) {
		commands = append(commands, windowsNewRouteCommand(route))
	}
	commands = append(commands, "'configured'")
	return strings.Join(commands, "; ")
}

func windowsNewRouteCommand(route windowsVPNRoute) string {
	family := "IPv6"
	if route.Destination.Addr().Is4() {
		family = "IPv4"
	}
	nextHop := "0.0.0.0"
	if family == "IPv6" {
		nextHop = "::"
	}
	if route.Gateway.IsValid() {
		nextHop = route.Gateway.String()
	}
	return "New-NetRoute -AddressFamily " + family + " -DestinationPrefix '" + route.Destination.String() + "' -InterfaceIndex " + strconv.Itoa(route.InterfaceIndex) + " -NextHop '" + nextHop + "' -RouteMetric 5 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null"
}

func windowsRemoveRoutesScript(plan windowsVPNRoutePlan) string {
	commands := []string{"$ErrorActionPreference = 'SilentlyContinue'"}
	for _, route := range plan.CleanupRoutes {
		family := "IPv6"
		if route.Destination.Addr().Is4() {
			family = "IPv4"
		}
		nextHop := "0.0.0.0"
		if family == "IPv6" {
			nextHop = "::"
		}
		if route.Gateway.IsValid() {
			nextHop = route.Gateway.String()
		}
		commands = append(commands, "Remove-NetRoute -AddressFamily "+family+" -DestinationPrefix '"+route.Destination.String()+"' -InterfaceIndex "+strconv.Itoa(route.InterfaceIndex)+" -NextHop '"+nextHop+"' -Confirm:$false -ErrorAction SilentlyContinue")
	}
	commands = append(commands, "'routes-removed'")
	return strings.Join(commands, "; ")
}

func windowsRemoveInterfaceScript(name string) string {
	return "$ErrorActionPreference = 'SilentlyContinue'; Get-NetAdapter -Name '" + name + "' -ErrorAction SilentlyContinue | Remove-NetAdapter -Confirm:$false -ErrorAction SilentlyContinue; 'interface-removed'"
}
