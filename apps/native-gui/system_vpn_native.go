package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

var errNativeSystemVPNOperation = errors.New("native system VPN operation failed")

type nativeSystemVPNBridge interface {
	Permission() (string, error)
	Start(profile string) (string, error)
	Stop() (string, error)
	Status() (string, error)
	Logs() (string, error)
}

type nativeSystemVPNHost struct {
	bridge      nativeSystemVPNBridge
	timeout     time.Duration
	operationMu sync.Mutex
}

type nativeSystemVPNResponse struct {
	Success               bool                      `json:"success"`
	State                 guiruntime.SystemVPNState `json:"state"`
	Error                 string                    `json:"error"`
	DaemonInstanceID      string                    `json:"daemon_instance_id"`
	ProfileRevision       uint64                    `json:"profile_revision"`
	ProfileHash           string                    `json:"profile_hash"`
	SessionID             string                    `json:"session_id"`
	ProfileValidUntil     time.Time                 `json:"profile_valid_until"`
	ProviderState         guiruntime.SystemVPNState `json:"provider_state"`
	ProviderStatusMatched bool                      `json:"provider_status_matched"`
	Logs                  []nativeSystemVPNLog      `json:"logs"`
}

type nativeSystemVPNLog struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Event     string `json:"event"`
	Message   string `json:"message"`
}

type macOSPacketTunnelConfiguration struct {
	RemoteAddress          string                `json:"remote_address"`
	DaemonInstanceID       string                `json:"daemon_instance_id"`
	ProfileRevision        uint64                `json:"profile_revision"`
	ProfileHash            string                `json:"profile_hash"`
	SessionID              string                `json:"session_id"`
	ProfileValidUntil      time.Time             `json:"profile_valid_until"`
	SocksEndpoint          macOSSocksEndpoint    `json:"socks_endpoint"`
	RouteMode              string                `json:"route_mode"`
	DestinationCIDRs       []string              `json:"destination_cidrs"`
	UserBypassCIDRs        []string              `json:"user_bypass_cidrs"`
	Bypass                 macOSBypassSet        `json:"bypass"`
	DNS                    macOSDNSConfiguration `json:"dns"`
	MTU                    int                   `json:"mtu"`
	TunnelIPv4Address      string                `json:"tunnel_ipv4_address"`
	TunnelIPv4SubnetMask   string                `json:"tunnel_ipv4_subnet_mask"`
	TunnelIPv6Address      string                `json:"tunnel_ipv6_address"`
	TunnelIPv6PrefixLength int                   `json:"tunnel_ipv6_prefix_length"`
}

type macOSSocksEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type macOSBypassSet struct {
	Authority           string              `json:"authority"`
	SourceEndpoints     []string            `json:"source_endpoints"`
	RequiredHosts       []string            `json:"required_hosts"`
	ResolvedCIDRs       []string            `json:"resolved_cidrs"`
	ResolvedCIDRsByHost map[string][]string `json:"resolved_cidrs_by_host"`
	ResolutionComplete  bool                `json:"resolution_complete"`
}

type macOSDNSConfiguration struct {
	Servers       []string `json:"servers"`
	MatchDomains  []string `json:"matchDomains"`
	SearchDomains []string `json:"searchDomains"`
}

func newNativeSystemVPNHost(bridge nativeSystemVPNBridge, timeout time.Duration) *nativeSystemVPNHost {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &nativeSystemVPNHost{bridge: bridge, timeout: timeout}
}

func (*nativeSystemVPNHost) Supported() bool { return true }

func (h *nativeSystemVPNHost) Permission(ctx context.Context) (systemVPNObservation, error) {
	response, err := h.call(ctx, h.bridge.Permission)
	return response.observation(), err
}

func (h *nativeSystemVPNHost) Start(ctx context.Context, profile json.RawMessage) (systemVPNObservation, error) {
	if _, err := decodeSystemVPNProfileIdentity(profile); err != nil {
		return systemVPNObservation{}, err
	}
	configuration, err := buildMacOSPacketTunnelConfiguration(profile, time.Now().UTC())
	if err != nil {
		return systemVPNObservation{}, err
	}
	response, err := h.call(ctx, func() (string, error) { return h.bridge.Start(string(configuration)) })
	observation := response.observation()
	if err == nil && (response.State != guiruntime.SystemVPNConnected || response.ProviderState != guiruntime.SystemVPNConnected || !response.ProviderStatusMatched) {
		err = fmt.Errorf("%w: packet-tunnel provider did not confirm the requested profile", errNativeSystemVPNOperation)
	}
	return observation, err
}

func buildMacOSPacketTunnelConfiguration(raw json.RawMessage, now time.Time) ([]byte, error) {
	var profile runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return nil, fmt.Errorf("decode authoritative system VPN profile: %w", err)
	}
	if err := profile.Validate(now); err != nil {
		return nil, fmt.Errorf("validate authoritative system VPN profile: %w", err)
	}
	profileValidUntil := systemVPNProfileValidUntil(profile)
	host, portText, err := net.SplitHostPort(profile.SocksListen)
	if err != nil {
		return nil, fmt.Errorf("parse authoritative SOCKS listener: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, fmt.Errorf("parse authoritative SOCKS port: %w", err)
	}

	hosts := make(map[string]struct{}, len(profile.Dependencies))
	endpoints := make(map[string]struct{}, len(profile.Dependencies))
	for _, dependency := range profile.Dependencies {
		hosts[dependency.Host] = struct{}{}
		endpoint := dependency.Scheme + "://" + net.JoinHostPort(dependency.Host, strconv.Itoa(dependency.Port))
		endpoints[endpoint] = struct{}{}
	}
	requiredHosts := sortedSet(hosts)
	sourceEndpoints := sortedSet(endpoints)
	routesByHost := make(map[string][]string, len(requiredHosts))
	resolvedCIDRs := make([]string, 0)
	for _, requiredHost := range requiredHosts {
		routes := append([]string(nil), profile.CarrierControlRoutes[requiredHost]...)
		sort.Strings(routes)
		routesByHost[requiredHost] = routes
		resolvedCIDRs = append(resolvedCIDRs, routes...)
	}
	resolvedCIDRs = uniqueSortedStrings(resolvedCIDRs)

	routeMode := "full_tunnel"
	destinationCIDRs := []string(nil)
	userBypassCIDRs := append([]string(nil), profile.UserBypassCIDRs...)
	switch profile.RouteMode {
	case runtimeapi.SystemVPNRouteNone:
	case runtimeapi.SystemVPNRouteBypass:
		userBypassCIDRs = append(userBypassCIDRs, profile.DestinationCIDRs...)
	case runtimeapi.SystemVPNRouteOnly:
		routeMode = "destination_split"
		destinationCIDRs = append(destinationCIDRs, profile.DestinationCIDRs...)
	default:
		return nil, fmt.Errorf("system VPN route mode %q cannot be mapped to macOS", profile.RouteMode)
	}

	configuration := macOSPacketTunnelConfiguration{
		RemoteAddress:     "127.0.0.1",
		DaemonInstanceID:  profile.DaemonInstanceID,
		ProfileRevision:   profile.ProfileRevision,
		ProfileHash:       profile.ProfileHash,
		SessionID:         profile.SessionID,
		ProfileValidUntil: profileValidUntil,
		SocksEndpoint:     macOSSocksEndpoint{Host: host, Port: port},
		RouteMode:         routeMode,
		DestinationCIDRs:  uniqueSortedStrings(destinationCIDRs),
		UserBypassCIDRs:   uniqueSortedStrings(userBypassCIDRs),
		Bypass: macOSBypassSet{
			Authority:           "carrier_control",
			SourceEndpoints:     sourceEndpoints,
			RequiredHosts:       requiredHosts,
			ResolvedCIDRs:       resolvedCIDRs,
			ResolvedCIDRsByHost: routesByHost,
			ResolutionComplete:  true,
		},
		DNS: macOSDNSConfiguration{
			Servers:       append([]string(nil), profile.DNSServers...),
			MatchDomains:  []string{""},
			SearchDomains: []string{},
		},
		MTU:                    profile.MTU,
		TunnelIPv4Address:      "198.18.0.2",
		TunnelIPv4SubnetMask:   "255.255.255.0",
		TunnelIPv6Address:      "fd00:5754:0001::2",
		TunnelIPv6PrefixLength: 64,
	}
	payload, err := json.Marshal(configuration)
	if err != nil {
		return nil, fmt.Errorf("encode macOS packet-tunnel configuration: %w", err)
	}
	return payload, nil
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return sortedSet(set)
}

func (h *nativeSystemVPNHost) Stop(ctx context.Context) (systemVPNObservation, error) {
	response, err := h.call(ctx, h.bridge.Stop)
	return response.observation(), err
}

func (h *nativeSystemVPNHost) Status(ctx context.Context) (systemVPNObservation, error) {
	response, err := h.call(ctx, h.bridge.Status)
	if err == nil && response.State == guiruntime.SystemVPNConnected && (response.ProviderState != guiruntime.SystemVPNConnected || !response.ProviderStatusMatched) {
		err = fmt.Errorf("%w: Network Extension status lacks an exact provider profile match", errNativeSystemVPNOperation)
	}
	return response.observation(), err
}

func (h *nativeSystemVPNHost) Logs(ctx context.Context) ([]guiruntime.LogLine, error) {
	response, err := h.call(ctx, h.bridge.Logs)
	if err != nil {
		return nil, err
	}
	lines := make([]guiruntime.LogLine, 0, len(response.Logs))
	for _, record := range response.Logs {
		message := strings.TrimSpace(record.Message)
		if message == "" {
			message = record.Event
		}
		lines = append(lines, guiruntime.LogLine{
			Timestamp: record.Timestamp,
			Level:     strings.ToLower(strings.TrimSpace(record.Level)),
			Message:   guiruntime.RedactText(message),
			Fields: map[string]string{
				"event":  guiruntime.RedactText(record.Event),
				"source": "macos-network-extension",
			},
		})
	}
	return lines, nil
}

func (h *nativeSystemVPNHost) call(ctx context.Context, operation func() (string, error)) (nativeSystemVPNResponse, error) {
	if h == nil || h.bridge == nil {
		return nativeSystemVPNResponse{}, fmt.Errorf("%w: bridge is unavailable", errNativeSystemVPNOperation)
	}
	operationContext, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	if err := operationContext.Err(); err != nil {
		return nativeSystemVPNResponse{}, err
	}

	// The Swift C ABI is synchronous and cannot be cancelled once entered.
	// Keep ownership until it returns so a timed-out Start cannot complete
	// later, after the caller has already attempted rollback and Stop.
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	if err := operationContext.Err(); err != nil {
		return nativeSystemVPNResponse{}, err
	}
	payload, operationErr := operation()
	if err := operationContext.Err(); err != nil {
		return nativeSystemVPNResponse{}, err
	}
	if operationErr != nil {
		return nativeSystemVPNResponse{}, fmt.Errorf("%w: %v", errNativeSystemVPNOperation, operationErr)
	}
	var response nativeSystemVPNResponse
	if strings.TrimSpace(payload) == "" {
		return response, fmt.Errorf("%w: native bridge returned an empty response", errNativeSystemVPNOperation)
	}
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		return response, fmt.Errorf("%w: decode response: %v", errNativeSystemVPNOperation, err)
	}
	if !validNativeSystemVPNState(response.State) {
		return response, fmt.Errorf("%w: native bridge returned state %q", errNativeSystemVPNOperation, response.State)
	}
	if !response.Success {
		message := guiruntime.RedactText(strings.TrimSpace(response.Error))
		if message == "" {
			message = "native bridge reported failure"
		}
		return response, fmt.Errorf("%w: %s", errNativeSystemVPNOperation, message)
	}
	return response, nil
}

func (response nativeSystemVPNResponse) observation() systemVPNObservation {
	return systemVPNObservation{
		State:             response.State,
		ProviderState:     response.ProviderState,
		DaemonInstanceID:  response.DaemonInstanceID,
		Revision:          response.ProfileRevision,
		SessionID:         response.SessionID,
		ProfileHash:       response.ProfileHash,
		ProfileValidUntil: response.ProfileValidUntil,
	}
}

func validNativeSystemVPNState(state guiruntime.SystemVPNState) bool {
	switch state {
	case guiruntime.SystemVPNDisconnected,
		guiruntime.SystemVPNPermissionRequired,
		guiruntime.SystemVPNConnecting,
		guiruntime.SystemVPNConnected,
		guiruntime.SystemVPNDegraded,
		guiruntime.SystemVPNDisconnecting,
		guiruntime.SystemVPNError,
		guiruntime.SystemVPNUnsupported:
		return true
	default:
		return false
	}
}
