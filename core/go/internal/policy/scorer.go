package policy

import (
	"math"
	"sort"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// Scorer computes a suitability score for a carrier-traffic pair.
// Higher scores indicate better fit. Negative scores mean ineligible.
type Scorer interface {
	Score(desc carriers.Descriptor, traffic fabric.TrafficClass) float64
}

// TrafficRequirements declares what a traffic class needs from a carrier.
type TrafficRequirements struct {
	RequiredCapabilities []carriers.Capability
	RequireAny           bool
	// BonusCapabilities are desirable (not mandatory) properties. Each bonus
	// cap the carrier declares adds +5 to the score, rewarding better-fit
	// carriers without excluding those that lack them.
	BonusCapabilities    []carriers.Capability
	WeightLatency        float64
	WeightBandwidth      float64
	WeightReliability    float64
	WeightCost           float64
	MaxAcceptableLatency time.Duration
}

// CapabilityScorer is the default Scorer implementation.
type CapabilityScorer struct {
	Requirements map[fabric.TrafficClass]TrafficRequirements
	Overrides    map[string]map[fabric.TrafficClass]float64
}

// DefaultTrafficRequirements returns the standard requirements table.
func DefaultTrafficRequirements() map[fabric.TrafficClass]TrafficRequirements {
	return map[fabric.TrafficClass]TrafficRequirements{
		fabric.TrafficBootstrap: {
			RequiredCapabilities: []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox},
			BonusCapabilities:    []carriers.Capability{carriers.CapRetrospective, carriers.CapDurable, carriers.CapPoll},
			WeightLatency: 0.9, WeightBandwidth: 0.1, WeightReliability: 0.8, WeightCost: 0.2,
			MaxAcceptableLatency: 5 * time.Second,
		},
		fabric.TrafficControl: {
			RequiredCapabilities: []carriers.Capability{carriers.CapMailbox},
			BonusCapabilities:    []carriers.Capability{carriers.CapRetrospective, carriers.CapDelete, carriers.CapDurable},
			WeightLatency: 0.8, WeightBandwidth: 0.2, WeightReliability: 0.7, WeightCost: 0.2,
			MaxAcceptableLatency: 5 * time.Second,
		},
		fabric.TrafficAdmin: {
			RequiredCapabilities: []carriers.Capability{carriers.CapMailbox},
			WeightLatency: 0.7, WeightBandwidth: 0.2, WeightReliability: 0.6, WeightCost: 0.3,
			MaxAcceptableLatency: 10 * time.Second,
		},
		fabric.TrafficHealth: {
			RequiredCapabilities: []carriers.Capability{carriers.CapMailbox},
			WeightLatency: 0.7, WeightBandwidth: 0.2, WeightReliability: 0.6, WeightCost: 0.3,
			MaxAcceptableLatency: 10 * time.Second,
		},
		fabric.TrafficLog: {
			RequiredCapabilities: []carriers.Capability{carriers.CapMailbox},
			WeightLatency: 0.3, WeightBandwidth: 0.5, WeightReliability: 0.4, WeightCost: 0.5,
			MaxAcceptableLatency: 30 * time.Second,
		},
		fabric.TrafficStream: {
			RequiredCapabilities: []carriers.Capability{carriers.CapStream, carriers.CapDuplex},
			BonusCapabilities:    []carriers.Capability{carriers.CapEphemeral, carriers.CapOrdered},
			WeightLatency: 0.6, WeightBandwidth: 0.9, WeightReliability: 0.5, WeightCost: 0.3,
			MaxAcceptableLatency: 2 * time.Second,
		},
		fabric.TrafficEgress: {
			// A retained mailbox can carry control envelopes but cannot tunnel a
			// TCP stream. Keep it out of session egress endpoints even when it is
			// the only healthy discovery path.
			RequiredCapabilities: []carriers.Capability{carriers.CapStream, carriers.CapBulk},
			RequireAny:    true,
			// CapStream is intentionally both required (via RequireAny) and bonus:
			// stream carriers earn a +5 preference bump over bulk carriers for
			// sustained egress data flow. This keeps wbstream above vk.docs.1024.
			BonusCapabilities: []carriers.Capability{carriers.CapStream, carriers.CapOrdered, carriers.CapEphemeral},
			WeightLatency: 0.7, WeightBandwidth: 0.8, WeightReliability: 0.6, WeightCost: 0.3,
			MaxAcceptableLatency: 5 * time.Second,
		},
		fabric.TrafficBulk: {
			RequiredCapabilities: []carriers.Capability{carriers.CapBulk},
			BonusCapabilities:    []carriers.Capability{carriers.CapDurable, carriers.CapIdempotentWrite},
			WeightLatency: 0.1, WeightBandwidth: 0.9, WeightReliability: 0.6, WeightCost: 0.7,
			MaxAcceptableLatency: 60 * time.Second,
		},
		fabric.TrafficRepair: {
			RequiredCapabilities: []carriers.Capability{carriers.CapBulk, carriers.CapRetained},
			RequireAny:    true,
			BonusCapabilities: []carriers.Capability{carriers.CapDurable, carriers.CapRetained},
			WeightLatency: 0.2, WeightBandwidth: 0.7, WeightReliability: 0.8, WeightCost: 0.4,
			MaxAcceptableLatency: 30 * time.Second,
		},
	}
}

// DefaultScorer returns a CapabilityScorer with standard requirements and no overrides.
func DefaultScorer() *CapabilityScorer {
	return &CapabilityScorer{Requirements: DefaultTrafficRequirements()}
}

// DefaultScorerWithOverrides returns a scorer with legacy-compatible overrides.
func DefaultScorerWithOverrides() *CapabilityScorer {
	return &CapabilityScorer{
		Requirements: DefaultTrafficRequirements(),
		Overrides:    DefaultOverrides(),
	}
}

// DefaultOverrides reproduces the current DefaultRoutes() priority ordering.
func DefaultOverrides() map[string]map[fabric.TrafficClass]float64 {
	return map[string]map[fabric.TrafficClass]float64{
		carriers.CarrierVKMessages:    {fabric.TrafficBootstrap: 90, fabric.TrafficControl: 90, fabric.TrafficAdmin: 80, fabric.TrafficHealth: 80, fabric.TrafficLog: 70, fabric.TrafficEgress: 30},
		carriers.CarrierOKMessages:    {fabric.TrafficBootstrap: 80, fabric.TrafficControl: 80, fabric.TrafficAdmin: 70, fabric.TrafficHealth: 70, fabric.TrafficLog: 60, fabric.TrafficEgress: 25},
		carriers.CarrierWBStreamVP8:   {fabric.TrafficStream: 90, fabric.TrafficEgress: 70},
		carriers.CarrierSingBoxVLESS:  {fabric.TrafficEgress: 90},
		carriers.CarrierSSHTCP:        {fabric.TrafficEgress: 80},
		carriers.CarrierVKDocs1024:    {fabric.TrafficBulk: 90, fabric.TrafficRepair: 60},
		carriers.CarrierVKDocs256:     {fabric.TrafficBulk: 80, fabric.TrafficRepair: 90},
		carriers.CarrierOKDocs256:     {fabric.TrafficBulk: 70, fabric.TrafficRepair: 80},
		carriers.CarrierOKPhotos:      {fabric.TrafficBulk: 60, fabric.TrafficRepair: 70},
		carriers.CarrierVKPhotos:      {fabric.TrafficRepair: 60},
		carriers.CarrierYandexDisk:    {fabric.TrafficBulk: 85, fabric.TrafficRepair: 75, fabric.TrafficEgress: 50},
	}
}

// Score computes a suitability score for a carrier-traffic pair.
func (s *CapabilityScorer) Score(desc carriers.Descriptor, traffic fabric.TrafficClass) float64 {
	req, ok := s.Requirements[traffic]
	if !ok {
		return -1
	}

	// When overrides are active, they are authoritative: only carriers with
	// an explicit override for this traffic class are eligible. This
	// guarantees exact behavioral match with DefaultRoutes() during migration.
	if s.Overrides != nil {
		if overrides, carrierOverridden := s.Overrides[desc.ID]; carrierOverridden {
			if score, trafficOverridden := overrides[traffic]; trafficOverridden {
				// Override found — still gate on health.
				if !desc.Metrics.Healthy {
					return -1
				}
				return score
			}
		}
		// No override for this (carrier, traffic) pair → ineligible during
		// migration. This prevents new/unconfigured carriers from appearing.
		return -1
	}

	// Pure scoring path (no overrides).
	if !desc.Metrics.Healthy {
		return -1
	}
	if desc.Metrics.QuotaRemaining == 0 {
		return -1 // quota explicitly exhausted
	}
	if !meetsCapabilityRequirements(desc, req) {
		return -1
	}
	if req.MaxAcceptableLatency > 0 && desc.Metrics.Latency >= req.MaxAcceptableLatency {
		return -1
	}

	score := 0.0
	if desc.Metrics.Latency > 0 {
		latencySec := float64(desc.Metrics.Latency) / float64(time.Second)
		score += req.WeightLatency * (1.0 / (1.0 + latencySec)) * 100
	} else {
		score += req.WeightLatency * 0.8 * 100
	}
	if desc.Metrics.BandwidthBPS > 0 {
		bwScore := math.Log10(float64(desc.Metrics.BandwidthBPS)) / 7.0
		if bwScore > 1.0 {
			bwScore = 1.0
		}
		score += req.WeightBandwidth * bwScore * 100
	}
	reliability := desc.Metrics.Reliability
	if reliability == 0 {
		reliability = 1.0
	}
	score += req.WeightReliability * reliability * 100
	if desc.Metrics.CostPerMB > 0 {
		score += req.WeightCost * (1.0 / (1.0 + desc.Metrics.CostPerMB)) * 100
	} else {
		score += req.WeightCost * 100
	}
	// Bonus scoring: reward carriers that declare desirable properties.
	for _, bc := range req.BonusCapabilities {
		if carriers.HasCapability(desc, bc) {
			score += 5
		}
	}
	return score
}

func meetsCapabilityRequirements(desc carriers.Descriptor, req TrafficRequirements) bool {
	if len(req.RequiredCapabilities) == 0 {
		return true
	}
	if req.RequireAny {
		for _, c := range req.RequiredCapabilities {
			if carriers.HasCapability(desc, c) {
				return true
			}
		}
		return false
	}
	for _, c := range req.RequiredCapabilities {
		if !carriers.HasCapability(desc, c) {
			return false
		}
	}
	return true
}

// AutoRoutes generates Route entries by scoring descriptors against all traffic classes.
func AutoRoutes(descriptors []carriers.Descriptor, scorer Scorer) []Route {
	allTraffic := []fabric.TrafficClass{
		fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin,
		fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficStream,
		fabric.TrafficEgress, fabric.TrafficBulk, fabric.TrafficRepair,
	}
	sort.Slice(allTraffic, func(i, j int) bool { return allTraffic[i] < allTraffic[j] })

	var routes []Route
	for _, tc := range allTraffic {
		type scored struct {
			id    string
			score float64
		}
		var candidates []scored
		for _, desc := range descriptors {
			sc := scorer.Score(desc, tc)
			if sc < 0 {
				continue
			}
			candidates = append(candidates, scored{id: desc.ID, score: sc})
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].score != candidates[j].score {
				return candidates[i].score > candidates[j].score
			}
			return candidates[i].id < candidates[j].id
		})
		for rank, c := range candidates {
			routes = append(routes, Route{
				TrafficClass: tc,
				CarrierID:    c.id,
				Priority:     rank * 10,
			})
		}
	}
	return routes
}
