// Package runtime adapts whitetransportd's local API into product-level models
// for the native desktop GUI.
package runtime

import "encoding/json"

// ProductState is the user-facing connection state used by the native GUI.
type ProductState string

const (
	StateOff           ProductState = "off"
	StateConnecting    ProductState = "connecting"
	StateConnected     ProductState = "connected"
	StateDegraded      ProductState = "degraded"
	StateDisconnecting ProductState = "disconnecting"
	StateUnsupported   ProductState = "unsupported"
	StateError         ProductState = "error"
)

// SystemVPNState describes the host-owned OS VPN lifecycle separately from
// the daemon transport lifecycle. A native runtime package cannot claim that
// a SOCKS listener activated a system VPN, so it reports unsupported until a
// platform host supplies a real Network Extension/VpnService state.
type SystemVPNState string

const (
	SystemVPNDisconnected       SystemVPNState = "disconnected"
	SystemVPNPermissionRequired SystemVPNState = "permission_required"
	SystemVPNConnecting         SystemVPNState = "connecting"
	SystemVPNConnected          SystemVPNState = "connected"
	SystemVPNDegraded           SystemVPNState = "degraded"
	SystemVPNDisconnecting      SystemVPNState = "disconnecting"
	SystemVPNError              SystemVPNState = "error"
	SystemVPNUnsupported        SystemVPNState = "unsupported"
)

// BuildSummary is daemon build metadata shown only in diagnostics.
type BuildSummary struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// DesktopStatus is the main runtime status returned to Wails. State and
// Connected are overall product semantics: a connected transport is degraded
// until a host-owned system VPN reports connected. TransportState remains the
// daemon lifecycle for diagnostics and host integration.
type DesktopStatus struct {
	State                ProductState   `json:"state"`
	Connected            bool           `json:"connected"`
	TransportState       ProductState   `json:"transport_state"`
	SystemVPNState       SystemVPNState `json:"system_vpn_state"`
	Message              string         `json:"message"`
	RuntimeState         string         `json:"runtime_state"`
	ActiveNodeID         string         `json:"active_node_id,omitempty"`
	SessionID            string         `json:"session_id,omitempty"`
	SocksListen          string         `json:"socks_listen,omitempty"`
	DiscoveredServers    int            `json:"discovered_servers"`
	AvailableServers     int            `json:"available_servers"`
	HealthyCarriers      int            `json:"healthy_carriers"`
	UnhealthyCarriers    int            `json:"unhealthy_carriers"`
	ReconnectAttempts    int            `json:"reconnect_attempts,omitempty"`
	LastRuntimeError     string         `json:"last_runtime_error,omitempty"`
	RuntimeBuild         BuildSummary   `json:"runtime_build"`
	DiagnosticsAvailable bool           `json:"diagnostics_available"`
	// SystemVPNProfile is daemon-confirmed host input. It is intentionally not
	// exposed to the renderer; App passes its exact JSON only to the native host.
	SystemVPNProfile json.RawMessage `json:"-"`
}

// DesktopTelemetry contains post-connect measurements that are expensive or
// network-bound enough to keep outside the regular status polling path.
type DesktopTelemetry struct {
	ExternalIP   string `json:"external_ip,omitempty"`
	LatencyMS    *int   `json:"latency_ms,omitempty"`
	ActiveNodeID string `json:"active_node_id,omitempty"`
	MeasuredAt   string `json:"measured_at,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ServerSummary is one selectable server in the main native GUI.
type ServerSummary struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Country      string   `json:"country,omitempty"`
	Region       string   `json:"region,omitempty"`
	Available    bool     `json:"available"`
	LatencyMS    *int     `json:"latency_ms,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	LastSeenAt   string   `json:"last_seen_at,omitempty"`
}

// DiagnosticStep is one diagnostics probe result.
type DiagnosticStep struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

// DiagnosticResult is a structured debug result that can be rendered or saved
// without scraping logs.
type DiagnosticResult struct {
	Passed bool             `json:"passed"`
	Steps  []DiagnosticStep `json:"steps"`
	Status DesktopStatus    `json:"status"`
}
