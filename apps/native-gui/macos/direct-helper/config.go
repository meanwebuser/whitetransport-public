package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	SOCKSHost         string    `json:"socks_host"`
	SOCKSPort         int       `json:"socks_port"`
	Mode              string    `json:"mode"`
	BypassCIDRs       []string  `json:"bypass_cidrs"`
	OnlyCIDRs         []string  `json:"only_cidrs"`
	Tun2SocksPath     string    `json:"tun2socks_path"`
	MTU               int       `json:"mtu"`
	StatePath         string    `json:"state_path"`
	LogPath           string    `json:"log_path"`
	TestResultPath    string    `json:"test_result_path"`
	DaemonInstanceID  string    `json:"daemon_instance_id"`
	ProfileRevision   uint64    `json:"profile_revision"`
	ProfileHash       string    `json:"profile_hash"`
	SessionID         string    `json:"session_id"`
	ProfileValidUntil time.Time `json:"profile_valid_until"`
}

type Route struct {
	CIDR string `json:"cidr"`
	Via  string `json:"via"`
	Kind string `json:"kind"`
}

type Plan struct {
	Mode   string  `json:"mode"`
	Routes []Route `json:"routes"`
}

// RouteForIP applies the same longest-prefix rule used by the kernel route
// table to a planned address. A false match means the plan installs no route
// for the address, so the pre-existing physical route remains responsible.
// It is intentionally side-effect free and lets local acceptance prove both
// legs of a split tunnel without requiring a provisioned macOS host.
func (p Plan) RouteForIP(address string) (Route, bool, error) {
	ip := net.ParseIP(strings.TrimSpace(address))
	if ip == nil {
		return Route{}, false, fmt.Errorf("invalid route lookup address %q", address)
	}
	var selected Route
	selectedPrefix := -1
	for _, route := range p.Routes {
		_, network, err := net.ParseCIDR(route.CIDR)
		if err != nil {
			return Route{}, false, fmt.Errorf("invalid planned route %q: %w", route.CIDR, err)
		}
		if !network.Contains(ip) {
			continue
		}
		prefix, _ := network.Mask.Size()
		if prefix > selectedPrefix {
			selected = route
			selectedPrefix = prefix
		}
	}
	if selectedPrefix < 0 {
		return Route{}, false, nil
	}
	return selected, true, nil
}

// tun2socks' documented Darwin setup uses a host route to the utun itself and
// a loopback point-to-point address. Keeping both ends at 198.18.0.1 avoids
// making the synthetic peer a second routable endpoint on macOS.
const (
	directTunLocalIPv4 = "198.18.0.1"
	directTunPeerIPv4  = "198.18.0.1"
)

func directTunAddresses() (local, peer string) {
	return directTunLocalIPv4, directTunPeerIPv4
}

// darwinRouteNextHop returns the route(8) next hop for a planned route. A
// point-to-point utun route must name its synthetic peer, matching tun2socks'
// documented Darwin setup; passing only -interface leaves the route without
// the peer gateway expected by the utun packet path.
func darwinRouteNextHop(route Route, physicalGateway string) string {
	if route.Via == "utun" {
		return directTunPeerIPv4
	}
	return physicalGateway
}

func tun2socksArgs(proxy string, mtu int, physicalInterface string) []string {
	args := []string{
		"-device", "tun://utun",
		"-proxy", proxy,
		"-mtu", strconv.Itoa(mtu),
	}
	if strings.TrimSpace(physicalInterface) != "" {
		args = append(args, "-interface", physicalInterface)
	}
	return append(args, "-loglevel", "warn")
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	cfg.setDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) setDefaults() {
	if c.Mode == "" {
		c.Mode = "full"
	}
	if c.Tun2SocksPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		c.Tun2SocksPath = filepath.Join(home, "Library", "Application Support", "WhiteTransport", "bin", "tun2socks")
	}
	if c.MTU == 0 {
		c.MTU = 1500
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	base := filepath.Join(home, "Library", "Application Support", "WhiteTransport", "direct-helper")
	if c.StatePath == "" {
		c.StatePath = filepath.Join(base, "state.json")
	}
	if c.LogPath == "" {
		c.LogPath = filepath.Join(home, "Library", "Logs", "WhiteTransport", "direct-helper.log")
	}
	if c.TestResultPath == "" {
		c.TestResultPath = filepath.Join(base, "test-result.json")
	}
}

func (c Config) Validate() error {
	host := strings.TrimSpace(c.SOCKSHost)
	if host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("socks_host must be loopback (127.0.0.1 or ::1), got %q", c.SOCKSHost)
	}
	if c.SOCKSPort < 1 || c.SOCKSPort > 65535 {
		return fmt.Errorf("socks_port must be 1..65535, got %d", c.SOCKSPort)
	}
	if c.Mode != "full" && c.Mode != "bypass" && c.Mode != "only" {
		return fmt.Errorf("mode must be full, bypass, or only, got %q", c.Mode)
	}
	if c.Mode == "only" && len(c.OnlyCIDRs) == 0 {
		return errors.New("only mode requires only_cidrs")
	}
	for _, cidr := range append(append([]string{}, c.BypassCIDRs...), c.OnlyCIDRs...) {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
	}
	if c.MTU < 576 || c.MTU > 65535 {
		return fmt.Errorf("mtu must be 576..65535, got %d", c.MTU)
	}
	return nil
}

func (c Config) RoutePlan() Plan {
	plan := Plan{Mode: c.Mode}
	switch c.Mode {
	case "full", "bypass":
		plan.Routes = append(plan.Routes, Route{CIDR: "0.0.0.0/1", Via: "utun", Kind: "full"}, Route{CIDR: "128.0.0.0/1", Via: "utun", Kind: "full"})
		// Full mode may carry authoritative carrier/control exclusions even
		// though it has no user-selected bypass policy. Those exact routes must
		// remain on the physical gateway to prevent provider recursion.
		for _, cidr := range c.BypassCIDRs {
			plan.Routes = append(plan.Routes, Route{CIDR: cidr, Via: "gateway", Kind: "bypass"})
		}
	case "only":
		for _, cidr := range c.OnlyCIDRs {
			plan.Routes = append(plan.Routes, Route{CIDR: cidr, Via: "utun", Kind: "only"})
		}
	}
	return plan
}
