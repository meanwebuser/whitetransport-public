// Package runtimeapi contains the public client and wire models for the local
// whitetransportd runtime API.
package runtimeapi

import (
	"strings"
	"time"
)

// ConnectionPolicy independently restricts the exit and the payload transport.
// Empty values leave that choice to the runtime and its recovery policy.
type ConnectionPolicy struct {
	NodeID    string `json:"node_id,omitempty"`
	Transport string `json:"transport,omitempty"`
}

// EgressTransportFamily identifies a supported desktop transport choice.
func EgressTransportFamily(carrier string) string {
	switch id := strings.ToLower(strings.TrimSpace(carrier)); id {
	case "vk_messages", "vk.messages", "vk.egress":
		return "vk_messages"
	case "vk_documents", "vk.docs", "vk.docs.256", "vk.docs.1024", "vk.docs.egress", "vk-doc-256", "vk-doc-1024":
		return "vk_documents"
	case "telemost", "yandex.telemost", "telemost-video-vp8":
		return "telemost"
	case "dion", "dion.call", "dion.video":
		return "dion"
	case "hysteria2", "hy2":
		return "hysteria2"
	case "vless", "singbox.vless":
		return "vless"
	default:
		if tag, ok := strings.CutPrefix(id, "xray-"); ok && tag != "" {
			return "vless"
		}
		return ""
	}
}

// Endpoint is a JSON-safe carrier endpoint returned in runtime status.
type Endpoint struct {
	ID       string            `json:"id"`
	Carrier  string            `json:"carrier"`
	Address  string            `json:"address"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Status is the daemon runtime status returned by /v1/status and session
// connect/disconnect calls.
type Status struct {
	TransportExploration      *TransportExploration `json:"transport_exploration,omitempty"`
	Role                      string                `json:"role"`
	State                     string                `json:"state"`
	NodeID                    string                `json:"node_id,omitempty"`
	ActiveNodeID              string                `json:"active_node_id,omitempty"`
	SessionID                 string                `json:"session_id,omitempty"`
	SessionActive             bool                  `json:"session_active"`
	SocksListen               string                `json:"socks_listen,omitempty"`
	EgressEndpoints           []Endpoint            `json:"egress_endpoints,omitempty"`
	SelectedEgressEndpointID  string                `json:"selected_egress_endpoint_id,omitempty"`
	AutomaticEgressEndpointID string                `json:"automatic_egress_endpoint_id,omitempty"`
	LastDialEgressEndpointID  string                `json:"last_dial_egress_endpoint_id,omitempty"`
	EgressBindingLabel        string                `json:"egress_binding_label,omitempty"`
	UpstreamProxy             string                `json:"upstream_proxy,omitempty"`
	DiscoveredNodes           int                   `json:"discovered_nodes"`
	AvailableNodes            int                   `json:"available_nodes"`
	ReconnectAttempts         int                   `json:"reconnect_attempts,omitempty"`
	LastError                 string                `json:"last_error,omitempty"`
	SystemVPNProfile          *SystemVPNProfile     `json:"system_vpn_profile,omitempty"`
	SystemVPNProfileReadiness *SystemVPNReadiness   `json:"system_vpn_profile_readiness,omitempty"`
}

// TransportMeasurement is a redacted headless exploration result. It carries
// no target address, credential or payload; it is sufficient to compare
// transports and identify the failing stage.
type TransportMeasurement struct {
	CarrierID            string    `json:"carrier_id"`
	EndpointID           string    `json:"endpoint_id"`
	Source               string    `json:"source"`
	StartedAt            time.Time `json:"started_at"`
	CompletedAt          time.Time `json:"completed_at"`
	TCPHealthy           bool      `json:"tcp_healthy"`
	TCPDelayMs           int64     `json:"tcp_delay_ms,omitempty"`
	UDPHealthy           bool      `json:"udp_healthy"`
	UDPDelayMs           int64     `json:"udp_delay_ms,omitempty"`
	FailureStage         string    `json:"failure_stage,omitempty"`
	ErrorCode            string    `json:"error_code,omitempty"`
	PriorLatencyMs       int64     `json:"prior_latency_ms,omitempty"`
	PriorBandwidthBPS    int64     `json:"prior_bandwidth_bps,omitempty"`
	PriorReliability     float64   `json:"prior_reliability,omitempty"`
	ObservedHealthy      bool      `json:"observed_healthy"`
	ObservedLatencyMs    int64     `json:"observed_latency_ms,omitempty"`
	ObservedBandwidthBPS int64     `json:"observed_bandwidth_bps,omitempty"`
	ObservedReliability  float64   `json:"observed_reliability,omitempty"`
	ObservedCostPerMB    float64   `json:"observed_cost_per_mb,omitempty"`
	ObservedQuota        int64     `json:"observed_quota_remaining,omitempty"`
	ObservedFailure      string    `json:"observed_failure,omitempty"`
	ObservedSource       string    `json:"observed_source,omitempty"`
	ObservedAt           time.Time `json:"observed_at,omitempty"`
}

// TransportExploration is the current bounded exploration receipt exposed by
// the local runtime API.
type TransportExploration struct {
	Enabled         bool                   `json:"enabled"`
	Running         bool                   `json:"running"`
	LastRunAt       time.Time              `json:"last_run_at,omitempty"`
	SelectedCarrier string                 `json:"selected_carrier,omitempty"`
	PersistedPath   string                 `json:"persisted_path,omitempty"`
	Measurements    []TransportMeasurement `json:"measurements,omitempty"`
}

// Node is a discovered runtime node returned by /v1/nodes.
type Node struct {
	NodeID       string    `json:"node_id"`
	Label        string    `json:"label"`
	Country      string    `json:"country,omitempty"`
	Region       string    `json:"region,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"`
	Available    bool      `json:"available"`
	LastSeenAt   time.Time `json:"last_seen_at,omitempty"`
}

// CarrierSnapshot is one carrier health entry returned by /v1/carriers.
type CarrierSnapshot struct {
	CarrierID      string    `json:"carrier_id"`
	Healthy        bool      `json:"healthy"`
	LifecycleState string    `json:"lifecycle_state,omitempty"`
	FailureStage   string    `json:"failure_stage,omitempty"`
	ErrorCode      string    `json:"error_code,omitempty"`
	Retryable      bool      `json:"retryable,omitempty"`
	ResourceGroup  string    `json:"resource_group,omitempty"`
	ReadSuccesses  int64     `json:"read_successes"`
	ReadFailures   int64     `json:"read_failures"`
	WriteSuccesses int64     `json:"write_successes"`
	WriteFailures  int64     `json:"write_failures"`
	Reliability    float64   `json:"reliability"`
	LastSuccessAt  time.Time `json:"last_success_at,omitempty"`
	LastFailureAt  time.Time `json:"last_failure_at,omitempty"`
}

// BuildInfo is daemon build metadata returned by /v1/build.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// DescriptorView is a stable carrier descriptor returned by /v1/plan.
type DescriptorView struct {
	ID                string  `json:"id"`
	Provider          string  `json:"provider"`
	Mode              string  `json:"mode"`
	MaxPayloadBytes   int     `json:"max_payload_bytes"`
	ChunkPayloadBytes int     `json:"chunk_payload_bytes"`
	BandwidthBPS      int64   `json:"bandwidth_bps"`
	Healthy           bool    `json:"healthy"`
	Reliability       float64 `json:"reliability,omitempty"`
	CostPerMB         float64 `json:"cost_per_mb,omitempty"`
	Score             float64 `json:"score,omitempty"`
}

// ChunkPlacement is a stable scheduled payload chunk returned by /v1/plan.
type ChunkPlacement struct {
	Index            int      `json:"index"`
	Offset           int      `json:"offset"`
	Size             int      `json:"size"`
	CarrierID        string   `json:"carrier_id"`
	MirrorCarrierIDs []string `json:"mirror_carrier_ids,omitempty"`
	HedgeCarrierIDs  []string `json:"hedge_carrier_ids,omitempty"`
	HedgeAfterMs     int64    `json:"hedge_after_ms,omitempty"`
}

// RoutePlan is the stable planning response returned by /v1/plan.
type RoutePlan struct {
	TrafficClass      string           `json:"traffic_class"`
	Strategy          string           `json:"strategy"`
	Primary           DescriptorView   `json:"primary"`
	Parallel          []DescriptorView `json:"parallel"`
	Repair            []DescriptorView `json:"repair"`
	MirrorCount       int              `json:"mirror_count"`
	HedgeTimeoutMs    int64            `json:"hedge_timeout_ms"`
	MaxInFlightChunks int              `json:"max_in_flight_chunks"`
	Placements        []ChunkPlacement `json:"placements,omitempty"`
	Score             float64          `json:"score,omitempty"`
}

// DetailedHealth is the expanded runtime health response used by diagnostics.
type DetailedHealth struct {
	Status        Status                     `json:"status"`
	UptimeSeconds int                        `json:"uptime_seconds"`
	StartedAt     time.Time                  `json:"started_at"`
	Carriers      map[string]CarrierSnapshot `json:"carriers"`
	Sessions      SessionHealth              `json:"sessions"`
	Memory        MemoryHealth               `json:"memory"`
}

// SessionHealth is the session subsection of DetailedHealth.
type SessionHealth struct {
	Active            bool   `json:"active"`
	ActiveNodeID      string `json:"active_node_id,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	ReconnectAttempts int    `json:"reconnect_attempts"`
}

// MemoryHealth is the memory subsection of DetailedHealth.
type MemoryHealth struct {
	AllocBytes     uint64 `json:"alloc_bytes"`
	SysBytes       uint64 `json:"sys_bytes"`
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	HeapSysBytes   uint64 `json:"heap_sys_bytes"`
	NumGC          uint32 `json:"num_gc"`
	Goroutines     int    `json:"goroutines"`
}
