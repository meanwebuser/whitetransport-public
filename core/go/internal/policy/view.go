package policy

import (
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

// DescriptorView is the stable JSON shape for carrier catalog output.
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

// RoutePlanView is the stable JSON shape exposed to clients and orchestrators.
type RoutePlanView struct {
	TrafficClass      string               `json:"traffic_class"`
	Strategy          string               `json:"strategy"`
	Primary           DescriptorView       `json:"primary"`
	Parallel          []DescriptorView     `json:"parallel"`
	Repair            []DescriptorView     `json:"repair"`
	MirrorCount       int                  `json:"mirror_count"`
	HedgeTimeoutMs    int64                `json:"hedge_timeout_ms"`
	MaxInFlightChunks int                  `json:"max_in_flight_chunks"`
	Placements        []ChunkPlacementView `json:"placements,omitempty"`
	Score             float64              `json:"score,omitempty"`
}

// ChunkPlacementView is the stable JSON shape for scheduled payload chunks.
type ChunkPlacementView struct {
	Index            int      `json:"index"`
	Offset           int      `json:"offset"`
	Size             int      `json:"size"`
	CarrierID        string   `json:"carrier_id"`
	MirrorCarrierIDs []string `json:"mirror_carrier_ids,omitempty"`
	HedgeCarrierIDs  []string `json:"hedge_carrier_ids,omitempty"`
	HedgeAfterMs     int64    `json:"hedge_after_ms,omitempty"`
}

// ToRoutePlanView converts a route plan and optional placements to JSON-safe
// fields for CLI/API consumers.
func ToRoutePlanView(plan RoutePlan, placements []ChunkPlacement) RoutePlanView {
	return RoutePlanView{
		TrafficClass:      string(plan.TrafficClass),
		Strategy:          string(plan.Strategy),
		Primary:           ToDescriptorView(plan.Primary),
		Parallel:          ToDescriptorViews(plan.Parallel),
		Repair:            ToDescriptorViews(plan.Repair),
		MirrorCount:       plan.MirrorCount,
		HedgeTimeoutMs:    milliseconds(plan.HedgeTimeout),
		MaxInFlightChunks: plan.MaxInFlightChunks,
		Placements:        ToChunkPlacementViews(placements),
		Score:             plan.Score,
	}
}

// ToDescriptorView converts a descriptor into stable JSON-safe fields.
func ToDescriptorView(desc carriers.Descriptor) DescriptorView {
	return DescriptorView{
		ID:                desc.ID,
		Provider:          desc.Provider,
		Mode:              string(desc.Mode),
		MaxPayloadBytes:   desc.Limits.MaxPayloadBytes,
		ChunkPayloadBytes: desc.Limits.ChunkPayloadBytes,
		BandwidthBPS:      desc.Metrics.BandwidthBPS,
		Healthy:           desc.Metrics.Healthy,
		Reliability:       desc.Metrics.Reliability,
		CostPerMB:         desc.Metrics.CostPerMB,
	}
}

// ToDescriptorViews converts descriptors into stable JSON-safe fields.
func ToDescriptorViews(descriptors []carriers.Descriptor) []DescriptorView {
	views := make([]DescriptorView, 0, len(descriptors))
	for _, desc := range descriptors {
		views = append(views, ToDescriptorView(desc))
	}
	return views
}

// ToChunkPlacementViews converts chunk placements into stable JSON-safe fields.
func ToChunkPlacementViews(placements []ChunkPlacement) []ChunkPlacementView {
	views := make([]ChunkPlacementView, 0, len(placements))
	for _, placement := range placements {
		views = append(views, ChunkPlacementView{
			Index:            placement.Index,
			Offset:           placement.Offset,
			Size:             placement.Size,
			CarrierID:        placement.CarrierID,
			MirrorCarrierIDs: cloneStrings(placement.MirrorCarrierIDs),
			HedgeCarrierIDs:  cloneStrings(placement.HedgeCarrierIDs),
			HedgeAfterMs:     milliseconds(placement.HedgeAfter),
		})
	}
	return views
}

func milliseconds(value time.Duration) int64 {
	return int64(value / time.Millisecond)
}
