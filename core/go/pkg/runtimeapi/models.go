// Package runtimeapi contains the public client and wire models for the local
// whitetransportd runtime API.
package runtimeapi

import "time"

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
	Role                      string              `json:"role"`
	State                     string              `json:"state"`
	NodeID                    string              `json:"node_id,omitempty"`
	ActiveNodeID              string              `json:"active_node_id,omitempty"`
	SessionID                 string              `json:"session_id,omitempty"`
	SessionActive             bool                `json:"session_active"`
	SocksListen               string              `json:"socks_listen,omitempty"`
	EgressEndpoints           []Endpoint          `json:"egress_endpoints,omitempty"`
	SelectedEgressEndpointID  string              `json:"selected_egress_endpoint_id,omitempty"`
	UpstreamProxy             string              `json:"upstream_proxy,omitempty"`
	DiscoveredNodes           int                 `json:"discovered_nodes"`
	AvailableNodes            int                 `json:"available_nodes"`
	ReconnectAttempts         int                 `json:"reconnect_attempts,omitempty"`
	LastError                 string              `json:"last_error,omitempty"`
	SystemVPNProfile          *SystemVPNProfile   `json:"system_vpn_profile,omitempty"`
	SystemVPNProfileReadiness *SystemVPNReadiness `json:"system_vpn_profile_readiness,omitempty"`
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
