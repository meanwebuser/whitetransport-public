package runtimeapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// SystemVPNProfileSchemaRevision identifies the JSON contract consumed by a
	// host Network Extension. Bump it when fields change incompatibly.
	SystemVPNProfileSchemaRevision = "system-vpn-profile.v1"
	SystemVPNProfileMaxLifetime    = 5 * time.Minute
	SystemVPNProfileClockSkew      = 30 * time.Second
)

// SystemVPNRouteMode is the user-facing route policy for a system VPN.
type SystemVPNRouteMode string

const (
	SystemVPNRouteNone   SystemVPNRouteMode = "none"
	SystemVPNRouteBypass SystemVPNRouteMode = "bypass"
	SystemVPNRouteOnly   SystemVPNRouteMode = "only"
)

// SystemVPNReadiness explains whether a profile was emitted without exposing
// endpoint credentials or resolver details.
type SystemVPNReadiness struct {
	Ready      bool   `json:"ready"`
	Provenance string `json:"provenance,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// SystemVPNDependency is one sanitized, DNS-resolved carrier/control/egress
// dependency. It intentionally carries no provider URLs, query strings,
// credentials, or arbitrary endpoint metadata.
type SystemVPNDependency struct {
	Purpose      string    `json:"purpose"`
	Carrier      string    `json:"carrier"`
	Scheme       string    `json:"scheme"`
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	Addresses    []string  `json:"addresses"`
	DNSExpiresAt time.Time `json:"dns_expires_at"`
}

const (
	SystemVPNDependencyDiscovery = "discovery"
	SystemVPNDependencyControl   = "control"
	SystemVPNDependencyEgress    = "egress"
	SystemVPNDependencyFailover  = "failover"
)

// SystemVPNProfile is one immutable, session-scoped Network Extension input.
// It is published atomically inside runtimeapi.Status only when Validate
// succeeds; an absent profile is therefore a deliberate fail-closed result.
type SystemVPNProfile struct {
	SchemaRevision        string                `json:"schema_revision"`
	DaemonInstanceID      string                `json:"daemon_instance_id"`
	ProfileRevision       uint64                `json:"profile_revision"`
	ProfileHash           string                `json:"profile_hash"`
	SessionID             string                `json:"session_id"`
	SelectedNodeID        string                `json:"selected_node_id"`
	Ready                 bool                  `json:"ready"`
	IssuedAt              time.Time             `json:"issued_at"`
	ExpiresAt             time.Time             `json:"expires_at"`
	SocksListen           string                `json:"socks_listen"`
	RouteMode             SystemVPNRouteMode    `json:"route_mode"`
	DestinationCIDRs      []string              `json:"destination_cidrs,omitempty"`
	UserBypassCIDRs       []string              `json:"user_bypass_cidrs,omitempty"`
	LANAccess             bool                  `json:"lan_access"`
	CarrierControlOrigins []string              `json:"carrier_control_origins"`
	CarrierControlRoutes  map[string][]string   `json:"carrier_control_routes"`
	DNSSnapshot           map[string][]string   `json:"dns_snapshot"`
	DNSServers            []string              `json:"dns_servers"`
	Dependencies          []SystemVPNDependency `json:"dependencies"`
	MTU                   int                   `json:"mtu"`
	Readiness             SystemVPNReadiness    `json:"readiness"`
}

// Validate checks the complete profile against an injected current time.
// It rejects unsafe defaults, stale timestamps, non-loopback SOCKS listeners,
// malformed routes, unresolved endpoint hosts, and origin data that could
// carry credentials or query/path secrets into the host configuration.
func (p SystemVPNProfile) Validate(now time.Time) error {
	if p.SchemaRevision != SystemVPNProfileSchemaRevision {
		return fmt.Errorf("system vpn profile schema revision %q is unsupported", p.SchemaRevision)
	}
	if strings.TrimSpace(p.DaemonInstanceID) == "" || p.ProfileRevision == 0 || strings.TrimSpace(p.SessionID) == "" || strings.TrimSpace(p.SelectedNodeID) == "" {
		return fmt.Errorf("system vpn profile requires daemon_instance_id, positive profile_revision, session_id, and selected_node_id")
	}
	if strings.TrimSpace(p.ProfileHash) == "" {
		return fmt.Errorf("system vpn profile requires profile_hash")
	}
	if p.IssuedAt.IsZero() || p.ExpiresAt.IsZero() || !p.ExpiresAt.After(p.IssuedAt) {
		return fmt.Errorf("system vpn profile timestamps are invalid")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if p.IssuedAt.After(now.Add(SystemVPNProfileClockSkew)) || p.ExpiresAt.Before(now.Add(-SystemVPNProfileClockSkew)) {
		return fmt.Errorf("system vpn profile timestamps are stale or from the future")
	}
	if p.ExpiresAt.Sub(p.IssuedAt) > SystemVPNProfileMaxLifetime {
		return fmt.Errorf("system vpn profile lifetime exceeds %s", SystemVPNProfileMaxLifetime)
	}
	if err := validateLoopbackSocks(p.SocksListen); err != nil {
		return err
	}
	if err := validateRouteMode(p.RouteMode, p.DestinationCIDRs); err != nil {
		return err
	}
	if err := validateCIDRs("user_bypass_cidrs", p.UserBypassCIDRs, false); err != nil {
		return err
	}
	if p.LANAccess && !containsCIDRs(p.UserBypassCIDRs, LANBypassCIDRs()) {
		return fmt.Errorf("lan_access requires private and link-local user bypass CIDRs")
	}
	if err := validateOrigins(p.CarrierControlOrigins); err != nil {
		return err
	}
	if err := validateDNS(p.DNSServers, p.DNSSnapshot); err != nil {
		return err
	}
	if err := validateHostRoutes(p.CarrierControlOrigins, p.CarrierControlRoutes, p.DNSSnapshot); err != nil {
		return err
	}
	if err := validateDependencies(p.Dependencies, now, p.ExpiresAt); err != nil {
		return err
	}
	if p.MTU < 576 || p.MTU > 65535 {
		return fmt.Errorf("system vpn profile mtu %d is outside 576..65535", p.MTU)
	}
	if !p.Ready || !p.Readiness.Ready || strings.TrimSpace(p.Readiness.Provenance) == "" || strings.TrimSpace(p.Readiness.Reason) != "" {
		return fmt.Errorf("system vpn profile readiness is not authoritative")
	}
	hash, err := p.ComputeHash()
	if err != nil {
		return fmt.Errorf("compute profile hash: %w", err)
	}
	if !strings.EqualFold(hash, p.ProfileHash) {
		return fmt.Errorf("system vpn profile hash is not authoritative")
	}
	return nil
}

// ComputeHash returns the SHA-256 identity of the effective route profile.
// Freshness timestamps are validated independently and deliberately excluded:
// renewing an unchanged DNS snapshot must not force a live system VPN to
// reconnect. Address or routing changes still produce a different identity.
func (p SystemVPNProfile) ComputeHash() (string, error) {
	p.ProfileHash = ""
	p.IssuedAt = time.Time{}
	p.ExpiresAt = time.Time{}
	p.Dependencies = append([]SystemVPNDependency(nil), p.Dependencies...)
	for index := range p.Dependencies {
		p.Dependencies[index].DNSExpiresAt = time.Time{}
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// SetHash computes and stores the canonical profile hash.
func (p *SystemVPNProfile) SetHash() error {
	if p == nil {
		return fmt.Errorf("system vpn profile is nil")
	}
	hash, err := p.ComputeHash()
	if err != nil {
		return err
	}
	p.ProfileHash = hash
	return nil
}

// Clone returns a deep copy suitable for an atomic status snapshot.
func (p SystemVPNProfile) Clone() *SystemVPNProfile {
	clone := p
	clone.Dependencies = append([]SystemVPNDependency(nil), p.Dependencies...)
	for i := range clone.Dependencies {
		clone.Dependencies[i].Addresses = append([]string(nil), p.Dependencies[i].Addresses...)
	}
	clone.DestinationCIDRs = append([]string(nil), p.DestinationCIDRs...)
	clone.UserBypassCIDRs = append([]string(nil), p.UserBypassCIDRs...)
	clone.CarrierControlOrigins = append([]string(nil), p.CarrierControlOrigins...)
	clone.DNSServers = append([]string(nil), p.DNSServers...)
	clone.CarrierControlRoutes = cloneStringMap(p.CarrierControlRoutes)
	clone.DNSSnapshot = cloneStringMap(p.DNSSnapshot)
	return &clone
}

// LANBypassCIDRs are the documented private/link-local routes excluded when
// LAN access is enabled. They are intentionally explicit, not inferred from
// the host's current interfaces.
func LANBypassCIDRs() []string {
	return []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"fc00::/7",
		"fe80::/10",
	}
}

func validateLoopbackSocks(value string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("system vpn profile socks listener %q is invalid", value)
	}
	parsedHost := net.ParseIP(host)
	if parsedHost == nil || !parsedHost.IsLoopback() {
		return fmt.Errorf("system vpn profile socks listener must be loopback")
	}
	parsedPort, parseErr := strconv.Atoi(port)
	if parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
		return fmt.Errorf("system vpn profile socks listener port must be nonzero")
	}
	return nil
}

func validateRouteMode(mode SystemVPNRouteMode, destinationCIDRs []string) error {
	switch mode {
	case SystemVPNRouteNone:
		if len(destinationCIDRs) != 0 {
			return fmt.Errorf("route mode none cannot include destination CIDRs")
		}
	case SystemVPNRouteBypass:
		if err := validateCIDRs("destination_cidrs", destinationCIDRs, false); err != nil {
			return err
		}
	case SystemVPNRouteOnly:
		if len(destinationCIDRs) == 0 {
			return fmt.Errorf("route mode only requires destination CIDRs")
		}
		if err := validateCIDRs("destination_cidrs", destinationCIDRs, false); err != nil {
			return err
		}
	default:
		return fmt.Errorf("route mode %q is unsupported", mode)
	}
	return nil
}

func validateCIDRs(name string, values []string, hostOnly bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("%s contains invalid CIDR %q", name, raw)
		}
		prefix = prefix.Masked()
		if hostOnly && prefix.Bits() != prefix.Addr().BitLen() {
			return fmt.Errorf("%s route %q is not a host route", name, raw)
		}
		canonical := prefix.String()
		if _, exists := seen[canonical]; exists {
			return fmt.Errorf("%s contains duplicate CIDR %q", name, canonical)
		}
		seen[canonical] = struct{}{}
	}
	return nil
}

func validateOrigins(origins []string) error {
	if len(origins) == 0 {
		return fmt.Errorf("system vpn profile requires carrier/control origins")
	}
	seen := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || (parsed.Path != "" && parsed.Path != "/") {
			return fmt.Errorf("carrier/control origin %q is not a sanitized origin", raw)
		}
		origin := strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
		if _, exists := seen[origin]; exists {
			return fmt.Errorf("carrier/control origin %q is duplicated", raw)
		}
		seen[origin] = struct{}{}
	}
	return nil
}

func validateDNS(servers []string, snapshot map[string][]string) error {
	if len(servers) == 0 {
		return fmt.Errorf("system vpn profile requires dns servers")
	}
	for _, server := range servers {
		if net.ParseIP(strings.TrimSpace(server)) == nil {
			return fmt.Errorf("dns server %q is invalid", server)
		}
	}
	if len(snapshot) == 0 {
		return fmt.Errorf("system vpn profile requires a dns snapshot")
	}
	for host, addresses := range snapshot {
		if strings.TrimSpace(host) == "" || len(addresses) == 0 {
			return fmt.Errorf("dns snapshot for %q is incomplete", host)
		}
		for _, address := range addresses {
			if net.ParseIP(strings.TrimSpace(address)) == nil {
				return fmt.Errorf("dns snapshot for %q contains invalid address", host)
			}
		}
	}
	return nil
}

func validateDependencies(dependencies []SystemVPNDependency, now, profileExpiry time.Time) error {
	if len(dependencies) == 0 {
		return fmt.Errorf("system vpn profile requires resolved dependencies")
	}
	seen := make(map[string]struct{}, len(dependencies))
	purposes := make(map[string]bool)
	for _, dependency := range dependencies {
		purpose := strings.TrimSpace(strings.ToLower(dependency.Purpose))
		carrier := strings.TrimSpace(dependency.Carrier)
		scheme := strings.TrimSpace(strings.ToLower(dependency.Scheme))
		host := strings.TrimSpace(strings.ToLower(dependency.Host))
		if purpose == "" || carrier == "" || scheme == "" || host == "" {
			return fmt.Errorf("system vpn dependency is incomplete")
		}
		if dependency.Port < 1 || dependency.Port > 65535 {
			return fmt.Errorf("system vpn dependency %q has invalid port", host)
		}
		switch scheme {
		case "http", "https", "ws", "wss", "tcp", "tls", "ssh":
		default:
			return fmt.Errorf("system vpn dependency %q has unsupported scheme", host)
		}
		if strings.ContainsAny(host, "/?#@") {
			return fmt.Errorf("system vpn dependency host %q is not a hostname", host)
		}
		if _, err := url.ParseRequestURI(scheme + "://" + host); err != nil {
			return fmt.Errorf("system vpn dependency %q has invalid scheme or host", host)
		}
		key := purpose + "\x00" + carrier + "\x00" + scheme + "\x00" + host + fmt.Sprintf("\x00%d", dependency.Port)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("system vpn dependency %q is duplicated", host)
		}
		seen[key] = struct{}{}
		purposes[purpose] = true
		if len(dependency.Addresses) == 0 || dependency.DNSExpiresAt.IsZero() || !dependency.DNSExpiresAt.After(now) || dependency.DNSExpiresAt.After(profileExpiry) {
			return fmt.Errorf("system vpn dependency %q has stale or incomplete DNS resolution", host)
		}
		addressSeen := make(map[string]struct{}, len(dependency.Addresses))
		for _, rawAddress := range dependency.Addresses {
			address, err := netip.ParseAddr(strings.TrimSpace(rawAddress))
			if err != nil {
				return fmt.Errorf("system vpn dependency %q has invalid address", host)
			}
			canonical := address.String()
			if _, exists := addressSeen[canonical]; exists {
				return fmt.Errorf("system vpn dependency %q has duplicate address", host)
			}
			addressSeen[canonical] = struct{}{}
		}
	}
	for _, purpose := range []string{SystemVPNDependencyDiscovery, SystemVPNDependencyControl, SystemVPNDependencyEgress} {
		if !purposes[purpose] {
			return fmt.Errorf("system vpn dependency set is missing %s", purpose)
		}
	}
	return nil
}

func validateHostRoutes(origins []string, routes map[string][]string, snapshot map[string][]string) error {
	if len(routes) == 0 {
		return fmt.Errorf("system vpn profile requires carrier/control host routes")
	}
	for _, origin := range origins {
		host, err := originHost(origin)
		if err != nil {
			return err
		}
		addresses := snapshot[host]
		mapped := routes[host]
		if len(addresses) == 0 || len(mapped) == 0 || len(addresses) != len(mapped) {
			return fmt.Errorf("carrier/control host %q has incomplete route mapping", host)
		}
		for i, address := range addresses {
			ip := net.ParseIP(address)
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			expected := ip.String() + "/" + fmt.Sprint(bits)
			if mapped[i] != expected {
				return fmt.Errorf("carrier/control host %q route mapping is not authoritative", host)
			}
		}
	}
	return nil
}

func originHost(origin string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("carrier/control origin %q has no host", origin)
	}
	return strings.ToLower(parsed.Hostname()), nil
}

func containsCIDRs(values []string, required []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			seen[prefix.Masked().String()] = struct{}{}
		}
	}
	for _, value := range required {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return false
		}
		if _, ok := seen[prefix.Masked().String()]; !ok {
			return false
		}
	}
	return true
}

func cloneStringMap(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	clone := make(map[string][]string, len(values))
	for key, entries := range values {
		clone[key] = append([]string(nil), entries...)
	}
	return clone
}

// SortProfileSlices canonicalizes the order of all set-like profile fields.
func (p *SystemVPNProfile) SortProfileSlices() {
	if p == nil {
		return
	}
	sort.Strings(p.DestinationCIDRs)
	sort.Strings(p.UserBypassCIDRs)
	sort.Strings(p.CarrierControlOrigins)
	sort.Strings(p.DNSServers)
	for _, values := range p.CarrierControlRoutes {
		sort.Strings(values)
	}
	for _, values := range p.DNSSnapshot {
		sort.Strings(values)
	}
	sort.Slice(p.Dependencies, func(i, j int) bool {
		left, right := p.Dependencies[i], p.Dependencies[j]
		leftKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", left.Purpose, left.Carrier, left.Scheme, left.Host, left.Port)
		rightKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", right.Purpose, right.Carrier, right.Scheme, right.Host, right.Port)
		return leftKey < rightKey
	})
	for i := range p.Dependencies {
		sort.Strings(p.Dependencies[i].Addresses)
	}
}
