package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

// SystemVPNClock supplies the time used for profile issuance and expiry.
// Production uses wallClock; tests inject a deterministic implementation.
type SystemVPNClock interface {
	Now() time.Time
}

// SystemVPNResolver returns one immutable host-address snapshot and its
// authoritative freshness deadline. A zero expiry is rejected fail-closed.
type SystemVPNResolver interface {
	Resolve(context.Context, string) (SystemVPNResolution, error)
}

// SystemVPNNetworkOriginProvider exposes credential-free network origins for
// an already configured provider session. It is intentionally optional:
// providers without an authoritative active origin keep the profile unready.
type SystemVPNNetworkOriginProvider interface {
	SystemVPNNetworkOrigins() []string
}

// SystemVPNResolution is the resolver result consumed by the profile builder.
type SystemVPNResolution struct {
	Addresses []netip.Addr
	ExpiresAt time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }

type netSystemVPNResolver struct {
	resolver *net.Resolver
}

func (r netSystemVPNResolver) Resolve(ctx context.Context, host string) (SystemVPNResolution, error) {
	if r.resolver == nil {
		r.resolver = net.DefaultResolver
	}
	addresses, err := r.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return SystemVPNResolution{}, err
	}
	unique := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		unique[address.Unmap()] = struct{}{}
	}
	resolved := make([]netip.Addr, 0, len(unique))
	for address := range unique {
		resolved = append(resolved, address)
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].String() < resolved[j].String() })
	if len(resolved) == 0 {
		return SystemVPNResolution{}, errors.New("resolver returned no addresses")
	}
	// net.Resolver intentionally does not expose TTLs. Bound the fallback by
	// the profile contract's maximum lifetime; deployments with authoritative
	// TTLs can inject a resolver and shorten this deadline.
	return SystemVPNResolution{
		Addresses: resolved,
		ExpiresAt: time.Now().UTC().Add(runtimeapi.SystemVPNProfileMaxLifetime),
	}, nil
}

// NewSystemVPNProfileResolver returns the production resolver implementation.
func NewSystemVPNProfileResolver() SystemVPNResolver {
	return netSystemVPNResolver{resolver: net.DefaultResolver}
}

// NewDaemonInstanceID returns a random process identity. It is generated once
// per ControlPlane and never reused as a profile revision.
func NewDaemonInstanceID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate daemon instance id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

// SystemVPNBindingInput identifies one authoritative configured carrier role.
// ID is the configured binding key (including an optional channel-role suffix)
// and Carrier is the runtime carrier descriptor ID.
type SystemVPNBindingInput struct {
	ID             string
	Carrier        string
	Purpose        string
	OriginOverride string
}

// SystemVPNProfileInput is an immutable snapshot copied while ControlPlane's
// lock is held. A profile is emitted only when it describes a connected client
// and every binding in this snapshot resolves to a safe origin and address set.
type SystemVPNProfileInput struct {
	Config            config.Config
	ActualSocksListen string
	DaemonInstanceID  string
	ProfileRevision   uint64
	SessionID         string
	SelectedNodeID    string
	Bindings          []SystemVPNBindingInput
}

// SystemVPNProfileBuilder derives and validates the atomic runtime profile.
type SystemVPNProfileBuilder struct {
	clock    SystemVPNClock
	resolver SystemVPNResolver
}

// NewSystemVPNProfileBuilder constructs a builder with injected clock and
// resolver dependencies. Nil dependencies use the production implementations.
func NewSystemVPNProfileBuilder(clock SystemVPNClock, resolver SystemVPNResolver) *SystemVPNProfileBuilder {
	if clock == nil {
		clock = wallClock{}
	}
	if resolver == nil {
		resolver = NewSystemVPNProfileResolver()
	}
	return &SystemVPNProfileBuilder{clock: clock, resolver: resolver}
}

// BuildSystemVPNProfile builds one complete validated profile. Errors are
// short typed readiness reasons; caller-facing status must redact the detail.
func (b *SystemVPNProfileBuilder) BuildSystemVPNProfile(ctx context.Context, input SystemVPNProfileInput) (*runtimeapi.SystemVPNProfile, error) {
	now := b.clock.Now().UTC()
	if ctx == nil {
		ctx = context.Background()
	}
	if input.Config.Role != config.RoleClient {
		return nil, errors.New("client_role_required")
	}
	if strings.TrimSpace(input.DaemonInstanceID) == "" {
		return nil, errors.New("daemon_instance_id_missing")
	}
	if input.ProfileRevision == 0 {
		return nil, errors.New("profile_revision_missing")
	}
	if strings.TrimSpace(input.SessionID) == "" || strings.TrimSpace(input.SelectedNodeID) == "" {
		return nil, errors.New("active_session_missing")
	}
	if strings.TrimSpace(input.ActualSocksListen) == "" {
		return nil, errors.New("actual_socks_listener_missing")
	}
	mode, err := input.Config.Routing.NormalizedRouteMode()
	if err != nil {
		return nil, errors.New("routing_config_invalid")
	}
	destinationCIDRs, err := normalizedDestinationCIDRs(input.Config.Routing.DestinationCIDRs)
	if err != nil {
		return nil, errors.New("destination_routes_invalid")
	}
	if len(input.Config.Routing.DNSServers) == 0 {
		return nil, errors.New("dns_servers_missing")
	}
	if input.Config.Routing.MTU < 576 || input.Config.Routing.MTU > 65535 {
		return nil, errors.New("mtu_missing_or_invalid")
	}
	for _, server := range input.Config.Routing.DNSServers {
		if net.ParseIP(strings.TrimSpace(server)) == nil {
			return nil, errors.New("dns_server_invalid")
		}
	}
	if len(input.Bindings) == 0 {
		return nil, errors.New("carrier_dependencies_missing")
	}

	dependencies := make([]runtimeapi.SystemVPNDependency, 0, len(input.Bindings))
	originSet := make(map[string]struct{})
	dnsSnapshot := make(map[string][]string)
	carrierRoutes := make(map[string][]string)
	for _, binding := range input.Bindings {
		dependency, origin, err := b.buildDependency(ctx, input.Config, binding, now)
		if err != nil {
			return nil, errors.New("carrier_dependency_not_authoritative")
		}
		dependencies = append(dependencies, dependency)
		if binding.Purpose == runtimeapi.SystemVPNDependencyDiscovery || binding.Purpose == runtimeapi.SystemVPNDependencyControl || binding.Purpose == runtimeapi.SystemVPNDependencyFailover {
			originSet[origin] = struct{}{}
		}
		host := strings.ToLower(dependency.Host)
		addresses := append([]string(nil), dependency.Addresses...)
		dnsSnapshot[host] = mergeUniqueStrings(dnsSnapshot[host], addresses)
		for _, address := range addresses {
			prefix, parseErr := netip.ParseAddr(address)
			if parseErr != nil {
				return nil, errors.New("carrier_dependency_address_invalid")
			}
			bits := 128
			if prefix.Is4() {
				bits = 32
			}
			carrierRoutes[host] = mergeUniqueStrings(carrierRoutes[host], []string{prefix.String() + "/" + strconv.Itoa(bits)})
		}
	}
	if !hasDependencyPurpose(dependencies, runtimeapi.SystemVPNDependencyDiscovery) || !hasDependencyPurpose(dependencies, runtimeapi.SystemVPNDependencyControl) || !hasDependencyPurpose(dependencies, runtimeapi.SystemVPNDependencyEgress) {
		return nil, errors.New("carrier_dependency_set_incomplete")
	}

	origins := make([]string, 0, len(originSet))
	for origin := range originSet {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	userBypassCIDRs := []string(nil)
	if input.Config.Routing.LANAccess {
		userBypassCIDRs = runtimeapi.LANBypassCIDRs()
	}
	expiresAt := now.Add(runtimeapi.SystemVPNProfileMaxLifetime)
	for _, dependency := range dependencies {
		if dependency.DNSExpiresAt.Before(expiresAt) {
			expiresAt = dependency.DNSExpiresAt
		}
	}
	if !expiresAt.After(now) {
		return nil, errors.New("dns_snapshot_stale")
	}
	for index := range dependencies {
		if dependencies[index].DNSExpiresAt.After(expiresAt) {
			dependencies[index].DNSExpiresAt = expiresAt
		}
	}
	profile := &runtimeapi.SystemVPNProfile{
		SchemaRevision:        runtimeapi.SystemVPNProfileSchemaRevision,
		DaemonInstanceID:      input.DaemonInstanceID,
		ProfileRevision:       input.ProfileRevision,
		SessionID:             input.SessionID,
		SelectedNodeID:        input.SelectedNodeID,
		Ready:                 true,
		IssuedAt:              now,
		ExpiresAt:             expiresAt,
		SocksListen:           input.ActualSocksListen,
		RouteMode:             runtimeapi.SystemVPNRouteMode(mode),
		DestinationCIDRs:      destinationCIDRs,
		UserBypassCIDRs:       userBypassCIDRs,
		LANAccess:             input.Config.Routing.LANAccess,
		CarrierControlOrigins: origins,
		CarrierControlRoutes:  carrierRoutes,
		DNSSnapshot:           dnsSnapshot,
		DNSServers:            append([]string(nil), input.Config.Routing.DNSServers...),
		Dependencies:          dependencies,
		MTU:                   input.Config.Routing.MTU,
		Readiness: runtimeapi.SystemVPNReadiness{
			Ready:      true,
			Provenance: "config+active-session+bound-socks+resolver",
		},
	}
	profile.SortProfileSlices()
	if err := profile.SetHash(); err != nil {
		return nil, errors.New("profile_hash_unavailable")
	}
	if err := profile.Validate(now); err != nil {
		return nil, errors.New("profile_validation_failed")
	}
	return profile, nil
}

func (b *SystemVPNProfileBuilder) buildDependency(ctx context.Context, cfg config.Config, binding SystemVPNBindingInput, now time.Time) (runtimeapi.SystemVPNDependency, string, error) {
	if strings.TrimSpace(binding.Purpose) == "" || strings.TrimSpace(binding.ID) == "" {
		return runtimeapi.SystemVPNDependency{}, "", errors.New("binding incomplete")
	}
	scheme, host, port, origin, err := "", "", 0, "", error(nil)
	if strings.TrimSpace(binding.OriginOverride) != "" {
		scheme, host, port, origin, err = parseCarrierOrigin(binding.OriginOverride)
	} else {
		carrierConfig, ok := findCarrierConfig(cfg, binding.ID, binding.Carrier)
		if !ok {
			return runtimeapi.SystemVPNDependency{}, "", errors.New("binding config missing")
		}
		scheme, host, port, origin, err = configuredCarrierOrigin(carrierConfig)
	}
	if err != nil {
		return runtimeapi.SystemVPNDependency{}, "", err
	}
	if address := net.ParseIP(host); address != nil {
		resolution := SystemVPNResolution{Addresses: []netip.Addr{netip.MustParseAddr(address.String())}, ExpiresAt: now.Add(runtimeapi.SystemVPNProfileMaxLifetime)}
		return makeDependency(binding, scheme, host, port, resolution, origin)
	}
	resolution, err := b.resolver.Resolve(ctx, host)
	if err != nil {
		return runtimeapi.SystemVPNDependency{}, "", err
	}
	if resolution.ExpiresAt.IsZero() || len(resolution.Addresses) == 0 {
		return runtimeapi.SystemVPNDependency{}, "", errors.New("resolution incomplete")
	}
	return makeDependency(binding, scheme, host, port, resolution, origin)
}

func makeDependency(binding SystemVPNBindingInput, scheme, host string, port int, resolution SystemVPNResolution, origin string) (runtimeapi.SystemVPNDependency, string, error) {
	addresses := make([]string, 0, len(resolution.Addresses))
	seen := make(map[string]struct{}, len(resolution.Addresses))
	for _, address := range resolution.Addresses {
		canonical := address.Unmap().String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		addresses = append(addresses, canonical)
	}
	if len(addresses) == 0 || resolution.ExpiresAt.IsZero() {
		return runtimeapi.SystemVPNDependency{}, "", errors.New("resolution incomplete")
	}
	sort.Strings(addresses)
	return runtimeapi.SystemVPNDependency{
		Purpose:      strings.ToLower(strings.TrimSpace(binding.Purpose)),
		Carrier:      strings.TrimSpace(binding.Carrier),
		Scheme:       scheme,
		Host:         strings.ToLower(host),
		Port:         port,
		Addresses:    addresses,
		DNSExpiresAt: resolution.ExpiresAt,
	}, origin, nil
}

func findCarrierConfig(cfg config.Config, bindingID, carrierID string) (config.CarrierConfig, bool) {
	baseID := strings.TrimSpace(bindingID)
	if index := strings.IndexByte(baseID, ':'); index > 0 {
		baseID = baseID[:index]
	}
	for _, candidate := range cfg.CarrierConfigs {
		if strings.TrimSpace(candidate.ID) == baseID || strings.TrimSpace(candidate.ID) == strings.TrimSpace(bindingID) {
			return candidate, true
		}
	}
	for _, candidate := range cfg.CarrierConfigs {
		if strings.TrimSpace(candidate.CarrierType) == strings.TrimSpace(carrierID) || strings.TrimSpace(candidate.ID) == strings.TrimSpace(carrierID) {
			return candidate, true
		}
	}
	return config.CarrierConfig{}, false
}

func configuredCarrierOrigin(cfg config.CarrierConfig) (string, string, int, string, error) {
	runtimeID := strings.TrimSpace(cfg.CarrierType)
	if runtimeID == "" {
		runtimeID = strings.TrimSpace(cfg.ID)
	}
	baseURL := ""
	switch runtimeID {
	case carriers.CarrierFileMailbox:
		// file.mailbox is a local control fixture, not a network provider.
		// Give the host route profile an explicit loopback dependency so a
		// system VPN never captures the local mailbox endpoint itself.
		return parseCarrierOrigin("http://127.0.0.1")
	case "vk.messages":
		if cfg.VKMessages != nil {
			baseURL = cfg.VKMessages.BaseURL
		}
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://api.vk.com/method"
		}
	case "ok.messages":
		if cfg.OKMessages != nil {
			baseURL = cfg.OKMessages.BaseURL
		}
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://api.ok.ru/graph"
		}
	case "vk.docs.256", "vk.docs.1024":
		if cfg.VKDocs != nil {
			baseURL = cfg.VKDocs.BaseURL
		}
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://api.vk.com/method"
		}
	case "ok.docs.256":
		if cfg.OKDocs != nil {
			baseURL = cfg.OKDocs.BaseURL
		}
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://api.ok.ru/fb.do"
		}
	case "yandex.disk":
		if cfg.YandexDisk != nil {
			baseURL = cfg.YandexDisk.BaseURL
		}
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://cloud-api.yandex.net/v1/disk"
		}
	case "wbstream", "telemost", "dion":
		if cfg.WhitelistBypass != nil {
			baseURL = cfg.WhitelistBypass.ServerURL
		}
		if strings.TrimSpace(baseURL) == "" {
			return "", "", 0, "", errors.New("dynamic provider origin is not authoritative")
		}
	default:
		baseURL = cfg.Endpoint.Address
	}
	return parseCarrierOrigin(baseURL)
}

func parseCarrierOrigin(baseURL string) (string, string, int, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", 0, "", errors.New("carrier origin is not sanitized")
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := 0
	if rawPort := parsed.Port(); rawPort != "" {
		port, err = strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return "", "", 0, "", errors.New("carrier origin port is invalid")
		}
	} else {
		switch scheme {
		case "http", "ws":
			port = 80
		case "https", "wss", "tls":
			port = 443
		case "ssh":
			port = 22
		default:
			return "", "", 0, "", errors.New("carrier origin port is ambiguous")
		}
	}
	origin := scheme + "://" + host
	defaultPort := (scheme == "http" || scheme == "ws") && port == 80 || (scheme == "https" || scheme == "wss" || scheme == "tls") && port == 443
	if !defaultPort {
		origin += ":" + strconv.Itoa(port)
	}
	return scheme, host, port, origin, nil
}

func normalizedDestinationCIDRs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix.Bits() == 0 {
			return nil, errors.New("invalid destination CIDR")
		}
		canonical := prefix.Masked().String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out, nil
}

func mergeUniqueStrings(current, values []string) []string {
	seen := make(map[string]struct{}, len(current)+len(values))
	for _, value := range current {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		current = append(current, value)
	}
	sort.Strings(current)
	return current
}

func hasDependencyPurpose(dependencies []runtimeapi.SystemVPNDependency, purpose string) bool {
	for _, dependency := range dependencies {
		if dependency.Purpose == purpose {
			return true
		}
	}
	return false
}

// SetActualSocksAddr records the listener address returned by the proxy after
// binding. A configured :0 or stale configured port is never published.
func (c *ControlPlane) SetActualSocksAddr(address string) {
	address = strings.TrimSpace(address)
	c.mu.Lock()
	if c.actualSocksListen == address {
		c.mu.Unlock()
		return
	}
	c.actualSocksListen = address
	c.profileRevision++
	active := c.active != nil && c.cfg.Role == config.RoleClient
	c.systemVPNProfile = nil
	c.systemVPNReadiness = &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/listener", Reason: "profile_refresh_required"}
	c.mu.Unlock()
	if active {
		c.refreshSystemVPNProfile()
	}
}

// refreshSystemVPNProfile derives a profile outside the runtime lock, then
// commits it only if the connected session and revision are unchanged. This
// prevents a slow DNS result from replacing a newer session snapshot.
func (c *ControlPlane) refreshSystemVPNProfile() {
	c.mu.RLock()
	if c.cfg.Role != config.RoleClient || c.active == nil || c.state != statusStateConnected || c.profileBuilder == nil {
		c.mu.RUnlock()
		return
	}
	active := *c.active
	active.EgressEndpoints = append([]carriers.Endpoint(nil), c.active.EgressEndpoints...)
	input := SystemVPNProfileInput{
		Config:            c.cfg,
		ActualSocksListen: c.actualSocksListen,
		DaemonInstanceID:  c.daemonInstanceID,
		ProfileRevision:   c.profileRevision,
		SessionID:         active.SessionID,
		SelectedNodeID:    active.NodeID,
		Bindings:          c.systemVPNBindingInputsLocked(&active),
	}
	builder := c.profileBuilder
	c.mu.RUnlock()

	profile, err := builder.BuildSystemVPNProfile(context.Background(), input)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.SessionID != input.SessionID || c.profileRevision != input.ProfileRevision || c.state != statusStateConnected {
		return
	}
	if err != nil {
		c.systemVPNProfile = nil
		c.systemVPNReadiness = &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/profile", Reason: redactSystemVPNProfileReason(err)}
		return
	}
	if c.systemVPNProfile != nil && !systemVPNDependenciesEqual(c.systemVPNProfile.Dependencies, profile.Dependencies) {
		c.profileRevision++
		profile.ProfileRevision = c.profileRevision
		if hashErr := profile.SetHash(); hashErr != nil {
			c.systemVPNProfile = nil
			c.systemVPNReadiness = &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/profile", Reason: "profile_hash_unavailable"}
			return
		}
	}
	c.systemVPNProfile = profile.Clone()
	c.systemVPNReadiness = &runtimeapi.SystemVPNReadiness{Ready: true, Provenance: profile.Readiness.Provenance}
}

func (c *ControlPlane) systemVPNBindingInputsLocked(active *activeSession) []SystemVPNBindingInput {
	inputs := make([]SystemVPNBindingInput, 0, len(c.bootstrap)+len(c.control)+len(c.egress)+3)
	seen := make(map[string]struct{})
	add := func(ref carrierRef, purpose string) {
		carrier := ref.Descriptor.ID
		if strings.TrimSpace(carrier) == "" {
			carrier = ref.Binding.Endpoint.Carrier
		}
		origins := systemVPNNetworkOrigins(ref.Binding)
		if len(origins) == 0 {
			origins = []string{""}
		}
		for _, origin := range origins {
			key := strings.Join([]string{ref.ID, carrier, purpose, origin}, "\x00")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			inputs = append(inputs, SystemVPNBindingInput{ID: ref.ID, Carrier: carrier, Purpose: purpose, OriginOverride: origin})
		}
	}
	for _, ref := range c.bootstrap {
		add(ref, runtimeapi.SystemVPNDependencyDiscovery)
	}
	for _, ref := range c.control {
		add(ref, runtimeapi.SystemVPNDependencyControl)
	}
	for index, ref := range c.egress {
		purpose := runtimeapi.SystemVPNDependencyFailover
		if index == 0 {
			purpose = runtimeapi.SystemVPNDependencyEgress
		}
		add(ref, purpose)
	}
	if active != nil && len(active.EgressEndpoints) > 0 {
		// The authenticated session answer is the authority for the exact
		// remote stream endpoints used by SSH and sing-box. They must stay on
		// the physical route or a full tunnel recursively captures its own
		// transport connection.
		for _, endpoint := range active.EgressEndpoints {
			origin, ok := systemVPNSessionEndpointOrigin(endpoint)
			if !ok {
				continue
			}
			key := strings.Join([]string{"session-egress", endpoint.ID, endpoint.Carrier, origin}, "\x00")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			inputs = append(inputs, SystemVPNBindingInput{
				ID:             "session-egress:" + endpoint.ID,
				Carrier:        endpoint.Carrier,
				Purpose:        runtimeapi.SystemVPNDependencyFailover,
				OriginOverride: origin,
			})
		}
	}
	if c.cfg.AdminDiscovery.Enabled {
		inputs = append(inputs, SystemVPNBindingInput{ID: "admin-discovery", Carrier: "admin", Purpose: runtimeapi.SystemVPNDependencyDiscovery, OriginOverride: c.cfg.AdminDiscovery.AdminURL})
	}
	if c.cfg.AdminRelay.Enabled {
		inputs = append(inputs, SystemVPNBindingInput{ID: "admin-relay", Carrier: "admin", Purpose: runtimeapi.SystemVPNDependencyControl, OriginOverride: c.cfg.AdminRelay.AdminURL})
	}
	return inputs
}

func systemVPNSessionEndpointOrigin(endpoint carriers.Endpoint) (string, bool) {
	scheme := ""
	switch strings.TrimSpace(endpoint.Carrier) {
	case carriers.CarrierSSHTCP:
		scheme = "ssh"
	case carriers.CarrierSingBoxVLESS:
		scheme = "tls"
	default:
		return "", false
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(endpoint.Address))
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return "", false
	}
	origin := scheme + "://" + net.JoinHostPort(host, port)
	_, _, _, _, err = parseCarrierOrigin(origin)
	if err != nil {
		return "", false
	}
	return origin, true
}

func systemVPNNetworkOrigins(binding policy.CarrierBinding) []string {
	providerCarrier, ok := binding.Carrier.(*carriers.ProviderCarrier)
	if !ok {
		return nil
	}
	originProvider, ok := providerCarrier.GetProvider().(SystemVPNNetworkOriginProvider)
	if !ok {
		return nil
	}
	origins := append([]string(nil), originProvider.SystemVPNNetworkOrigins()...)
	for index := range origins {
		origins[index] = strings.TrimSpace(origins[index])
	}
	sort.Strings(origins)
	return origins
}

func redactSystemVPNProfileReason(err error) string {
	if err == nil {
		return ""
	}
	reason := strings.TrimSpace(err.Error())
	if reason == "" || strings.ContainsAny(reason, "\r\n") {
		return "profile_not_ready"
	}
	// Builders use a fixed reason vocabulary. Keep this guard so a future
	// implementation cannot accidentally publish a hostname or secret.
	for _, forbidden := range []string{"://", "@", "?", "#", "/", "\\", "token", "cookie", "secret"} {
		if strings.Contains(strings.ToLower(reason), forbidden) {
			return "profile_not_ready"
		}
	}
	return reason
}

func (c *ControlPlane) invalidateSystemVPNProfileLocked(reason string) {
	c.profileRevision++
	c.systemVPNProfile = nil
	if strings.TrimSpace(reason) == "" {
		reason = "profile_not_ready"
	}
	c.systemVPNReadiness = &runtimeapi.SystemVPNReadiness{Ready: false, Provenance: "runtime/session", Reason: reason}
}

func systemVPNDependenciesEqual(left, right []runtimeapi.SystemVPNDependency) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		// DNS expiry is freshness metadata, not route identity. An unchanged
		// address snapshot can be renewed in place without restarting the host
		// VPN; an address or endpoint change advances the profile revision.
		if left[index].Purpose != right[index].Purpose || left[index].Carrier != right[index].Carrier || left[index].Scheme != right[index].Scheme || left[index].Host != right[index].Host || left[index].Port != right[index].Port || len(left[index].Addresses) != len(right[index].Addresses) {
			return false
		}
		for addressIndex := range left[index].Addresses {
			if left[index].Addresses[addressIndex] != right[index].Addresses[addressIndex] {
				return false
			}
		}
	}
	return true
}
