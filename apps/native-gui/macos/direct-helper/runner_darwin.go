//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func platformStart(cfg Config) Result {
	if state, err := readState(cfg.StatePath); err == nil && processAlive(state.PID) {
		_ = writeStatusSnapshot(cfg.StatusPath, state)
		return Result{Command: "start", Error: fmt.Sprintf("helper already running (pid %d)", state.PID), State: &state}
	}
	// direct-helper runs as root via sudo while the Wails GUI remains the
	// console user. Logs contain only redacted diagnostics, so keep the log
	// directory and file owner-readable by that GUI process across restarts.
	if err := os.MkdirAll(parentDir(cfg.LogPath), 0o755); err != nil {
		return Result{Command: "start", Error: err.Error()}
	}
	if err := os.Chmod(parentDir(cfg.LogPath), 0o755); err != nil {
		return Result{Command: "start", Error: fmt.Sprintf("set log directory permissions: %v", err)}
	}
	logf, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Result{Command: "start", Error: fmt.Sprintf("open log: %v", err)}
	}
	defer logf.Close()
	if err := logf.Chmod(0o644); err != nil {
		return Result{Command: "start", Error: fmt.Sprintf("set log permissions: %v", err)}
	}
	logf.WriteString("start requested\n")

	gateway, gatewayIface, err := defaultGateway()
	if err != nil {
		return Result{Command: "start", Error: err.Error()}
	}
	before := listUtun()
	proxy := "socks5://" + net.JoinHostPort(cfg.SOCKSHost, strconv.Itoa(cfg.SOCKSPort))
	// Bind all tun2socks dials to the physical interface. Without this, the
	// local SOCKS endpoint can follow a newly-installed route back into utun,
	// producing a self-loop and TCP resets. Explicit loglevel keeps startup
	// errors visible in the helper log without leaking proxy credentials.
	cmd := exec.Command(cfg.Tun2SocksPath, tun2socksArgs(proxy, cfg.MTU, gatewayIface)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return Result{Command: "start", Error: fmt.Sprintf("start tun2socks: %v", err)}
	}
	iface, err := waitForNewUtun(before, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return Result{Command: "start", Error: err.Error()}
	}
	local, peer := directTunAddresses()
	if _, err := runLogged(logf, "ifconfig", iface, "inet", local, peer, "up"); err != nil {
		_ = cmd.Process.Kill()
		return Result{Command: "start", Error: fmt.Sprintf("configure %s: %v", iface, err)}
	}
	if _, err := runLogged(logf, "ifconfig", iface, "mtu", strconv.Itoa(cfg.MTU)); err != nil {
		_ = cmd.Process.Kill()
		return Result{Command: "start", Error: fmt.Sprintf("set %s mtu: %v", iface, err)}
	}
	if !processAlive(cmd.Process.Pid) {
		return Result{Command: "start", Error: "tun2socks exited before route installation"}
	}
	plan := cfg.RoutePlan()
	if plan.Mode == "full" || plan.Mode == "bypass" {
		if resolverOutput, resolverErr := runLogged(nil, "/usr/sbin/scutil", "--dns"); resolverErr == nil {
			plan = addPhysicalDNSBypasses(plan, parsePhysicalDNSResolvers(string(resolverOutput), gatewayIface))
		}
	}
	installed := make([]Route, 0, len(plan.Routes))
	for _, route := range plan.Routes {
		if err := addRoute(logf, route, iface, gateway); err != nil {
			for i := len(installed) - 1; i >= 0; i-- {
				_ = deleteRoute(logf, installed[i], iface, gateway)
			}
			_ = cmd.Process.Kill()
			return Result{Command: "start", Error: fmt.Sprintf("add route %s: %v", route.CIDR, err)}
		}
		installed = append(installed, route)
	}
	state := State{PID: cmd.Process.Pid, Interface: iface, Gateway: gateway, GatewayIface: gatewayIface, Routes: installed, Config: cfg}
	if err := writeStatusSnapshot(cfg.StatusPath, state); err != nil {
		for i := len(installed) - 1; i >= 0; i-- {
			_ = deleteRoute(logf, installed[i], iface, gateway)
		}
		_ = cmd.Process.Kill()
		return Result{Command: "start", Error: fmt.Sprintf("write status snapshot: %v", err)}
	}
	if err := writeState(cfg.StatePath, state); err != nil {
		_ = removeStatusSnapshot(cfg.StatusPath)
		for i := len(installed) - 1; i >= 0; i-- {
			_ = deleteRoute(logf, installed[i], iface, gateway)
		}
		_ = cmd.Process.Kill()
		return Result{Command: "start", Error: fmt.Sprintf("write state: %v", err)}
	}
	return Result{OK: true, Command: "start", Message: "direct-utun helper started", State: &state, Plan: &plan}
}

func platformStop(cfg Config) Result {
	state, err := readState(cfg.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return Result{OK: true, Command: "stop", Message: "already stopped"}
	}
	if err != nil {
		return Result{Command: "stop", Error: err.Error()}
	}
	logf, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Result{Command: "stop", Error: err.Error()}
	}
	defer logf.Close()
	if err := logf.Chmod(0o644); err != nil {
		return Result{Command: "stop", Error: fmt.Sprintf("set log permissions: %v", err)}
	}
	var failures []string
	for i := len(state.Routes) - 1; i >= 0; i-- {
		if err := deleteRoute(logf, state.Routes[i], state.Interface, state.Gateway); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if processAlive(state.PID) {
		if err := syscall.Kill(state.PID, syscall.SIGTERM); err != nil {
			failures = append(failures, fmt.Sprintf("stop tun2socks pid %d: %v", state.PID, err))
		}
	}
	if len(failures) > 0 {
		return Result{Command: "stop", Error: strings.Join(failures, "; "), State: &state}
	}
	if err := os.Remove(cfg.StatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{Command: "stop", Error: fmt.Sprintf("remove state: %v", err), State: &state}
	}
	if err := removeStatusSnapshot(cfg.StatusPath); err != nil {
		return Result{Command: "stop", Error: fmt.Sprintf("remove status snapshot: %v", err), State: &state}
	}
	return Result{OK: true, Command: "stop", Message: "direct-utun helper stopped", State: &state}
}

func platformStatus(cfg Config) Result {
	snapshot, err := readStatusSnapshot(cfg.StatusPath)
	if errors.Is(err, os.ErrNotExist) {
		return Result{OK: true, Command: "status", Message: "stopped"}
	}
	if err != nil {
		return Result{Command: "status", Error: err.Error()}
	}
	state := snapshot.state()
	if !processAlive(snapshot.PID) {
		_ = removeStatusSnapshot(cfg.StatusPath)
		return Result{OK: true, Command: "status", Message: "stale status; process is not running", State: &state}
	}
	return Result{OK: true, Command: "status", Message: "running", State: &state}
}

func runLogged(logf *os.File, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if logf != nil {
		_, _ = logf.WriteString(name + " " + strings.Join(args, " ") + "\n" + string(out))
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

func defaultGateway() (string, string, error) {
	out, routeErr := runLogged(nil, "/sbin/route", "-n", "get", "default")
	if routeErr == nil {
		if gateway, iface := parseRouteGetDefault(string(out)); gateway != "" && iface != "" {
			return gateway, iface, nil
		}
	}

	// A pre-existing packet tunnel can own the primary default route as an
	// interface-only utun entry. Keep using the physical IPv4 default so
	// tun2socks does not bind its outbound traffic back into that tunnel.
	routes, netstatErr := runLogged(nil, "/usr/sbin/netstat", "-rn", "-f", "inet")
	if netstatErr == nil {
		if gateway, iface := parsePhysicalDefaultRoute(string(routes)); gateway != "" && iface != "" {
			return gateway, iface, nil
		}
	}
	if routeErr != nil {
		return "", "", fmt.Errorf("read default gateway: %v", routeErr)
	}
	if netstatErr != nil {
		return "", "", fmt.Errorf("read physical default gateway: %v", netstatErr)
	}
	return "", "", fmt.Errorf("default route output missing gateway/interface")
}

func parseRouteGetDefault(out string) (string, string) {
	var gateway, iface string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "gateway:" {
			gateway = fields[1]
		}
		if len(fields) >= 2 && fields[0] == "interface:" {
			iface = fields[1]
		}
	}
	if net.ParseIP(gateway) == nil || iface == "" {
		return "", ""
	}
	return gateway, iface
}

func parsePhysicalDefaultRoute(out string) (string, string) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "default" {
			continue
		}
		gateway, iface := fields[1], fields[3]
		if net.ParseIP(gateway) == nil || iface == "" || strings.HasPrefix(iface, "utun") {
			continue
		}
		return gateway, iface
	}
	return "", ""
}

func parsePhysicalDNSResolvers(out, physicalInterface string) []string {
	physicalInterface = strings.TrimSpace(physicalInterface)
	if physicalInterface == "" {
		return nil
	}

	var resolvers []string
	var blockResolvers []string
	blockInterface := ""
	flush := func() {
		if blockInterface == physicalInterface {
			resolvers = append(resolvers, blockResolvers...)
		}
		blockResolvers = nil
		blockInterface = ""
	}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "resolver #") {
			flush()
			continue
		}
		if strings.HasPrefix(trimmed, "nameserver[") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 3 && net.ParseIP(fields[2]) != nil && strings.Contains(fields[2], ".") {
				blockResolvers = append(blockResolvers, fields[2])
			}
			continue
		}
		if strings.HasPrefix(trimmed, "if_index") {
			open := strings.LastIndex(trimmed, "(")
			close := strings.LastIndex(trimmed, ")")
			if open >= 0 && close > open {
				blockInterface = strings.TrimSpace(trimmed[open+1 : close])
			}
		}
	}
	flush()
	return uniqueStrings(resolvers)
}

func addPhysicalDNSBypasses(plan Plan, resolvers []string) Plan {
	seen := make(map[string]struct{}, len(plan.Routes)+len(resolvers))
	for _, route := range plan.Routes {
		seen[route.CIDR] = struct{}{}
	}
	for _, resolver := range resolvers {
		cidr := resolver + "/32"
		if _, exists := seen[cidr]; exists {
			continue
		}
		plan.Routes = append(plan.Routes, Route{CIDR: cidr, Via: "gateway", Kind: "dns-bypass"})
		seen[cidr] = struct{}{}
	}
	return plan
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func listUtun() map[string]struct{} {
	out, err := runLogged(nil, "ifconfig", "-l")
	if err != nil {
		return map[string]struct{}{}
	}
	set := map[string]struct{}{}
	for _, name := range strings.Fields(string(out)) {
		if strings.HasPrefix(name, "utun") {
			set[name] = struct{}{}
		}
	}
	return set
}

func waitForNewUtun(before map[string]struct{}, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for name := range listUtun() {
			if _, exists := before[name]; !exists {
				return name, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("tun2socks did not create a new utun interface")
}

func addRoute(logf *os.File, route Route, iface, gateway string) error {
	family := routeFamily(route.CIDR)
	if route.Via == "utun" {
		_, err := runLogged(logf, "route", "-n", "add", family, "-net", route.CIDR, darwinRouteNextHop(route, gateway))
		return err
	}
	_, err := runLogged(logf, "route", "-n", "add", family, "-net", route.CIDR, gateway)
	return err
}

func deleteRoute(logf *os.File, route Route, iface, gateway string) error {
	family := routeFamily(route.CIDR)
	if route.Via == "utun" {
		_, err := runLogged(logf, "route", "-n", "delete", family, "-net", route.CIDR, darwinRouteNextHop(route, gateway))
		return err
	}
	_, err := runLogged(logf, "route", "-n", "delete", family, "-net", route.CIDR, gateway)
	return err
}

func routeFamily(cidr string) string {
	_, network, err := net.ParseCIDR(cidr)
	if err == nil && network.IP.To4() == nil {
		return "-inet6"
	}
	return "-inet"
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func parentDir(path string) string {
	idx := strings.LastIndexByte(path, '/')
	if idx <= 0 {
		return "."
	}
	return path[:idx]
}
