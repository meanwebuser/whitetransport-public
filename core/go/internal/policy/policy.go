package policy

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

// DeliveryStrategy controls how the scheduler should use selected carriers.
type DeliveryStrategy string

const (
	DeliverySingle    DeliveryStrategy = "single"
	DeliveryStriped   DeliveryStrategy = "striped"
	DeliveryMirrored  DeliveryStrategy = "mirrored"
	DeliveryHedged    DeliveryStrategy = "hedged"
	DeliveryRedundant DeliveryStrategy = "redundant"
)

// Route expresses which carrier should be preferred for a traffic class.
type Route struct {
	TrafficClass fabric.TrafficClass
	CarrierID    string
	Priority     int
}

// RoutePlan is the scheduler contract for one traffic class. The policy chooses
// carriers; a later chunk scheduler decides exact chunk placement and ACK repair.
type RoutePlan struct {
	TrafficClass       fabric.TrafficClass
	Strategy           DeliveryStrategy
	Primary            carriers.Descriptor
	Parallel           []carriers.Descriptor
	Repair             []carriers.Descriptor
	MirrorCount        int
	HedgeTimeout       time.Duration
	MaxInFlightChunks  int
	AllowEgressMirrors bool
	Score              float64 // primary carrier score (populated when scorer is available)
}

// FailureSeverity represents the severity of a carrier failure
type FailureSeverity int

const (
	FailureNone FailureSeverity = iota
	FailureMinor
	FailureMajor
	FailureCritical
)

// FailureScore tracks carrier failure information for deprioritization
type FailureScore struct {
	LastError              string
	ErrorCount             int
	LastErrorTime          time.Time
	Severity               FailureSeverity
	PriorityOffset         int // Priority adjustment based on failures
	LastReset              time.Time
	AutoDisabledUntil      time.Time
	ConsecutiveSuccesses   int
}

// AutoDisableThreshold is the number of consecutive failures that triggers
// an automatic temporary disable of a carrier.
const AutoDisableThreshold = 5

// AutoDisableDuration is how long an auto-disable lasts before the carrier
// is given another chance by the runtime.
const AutoDisableDuration = 5 * time.Minute

// ClearAutoDisableOn is the number of consecutive successes that clears an
// active auto-disable early.
const ClearAutoDisableOn = 2

// CarrierPolicy is the routing policy shared by clients and nodes. Clients are
// expected to apply local health and platform constraints before selecting.
type CarrierPolicy struct {
	Routes         []Route
	TokenChecker   tokens.TokenHealthChecker // optional token health gate
	FailureTracker *FailureTracker           // optional failure tracker
	Scorer         Scorer                    // optional capability scorer (when set, generates routes dynamically)
}

// FailureTracker tracks carrier failures for intelligent deprioritization
type FailureTracker struct {
	mu          sync.RWMutex
	scores      map[string]*FailureScore
	decayWindow time.Duration // How long failure penalties persist
}

// NewFailureTracker creates a failure tracker with decay window
func NewFailureTracker(decayWindow time.Duration) *FailureTracker {
	return &FailureTracker{
		scores:      make(map[string]*FailureScore),
		decayWindow: decayWindow,
	}
}

// RecordFailure records a carrier failure with error code and context
func (ft *FailureTracker) RecordFailure(carrierID string, errMsg string, errorCode string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	score, exists := ft.scores[carrierID]
	if !exists {
		score = &FailureScore{
			LastReset: time.Now(),
		}
		ft.scores[carrierID] = score
	}

	score.LastError = errMsg
	score.ErrorCount++
	score.LastErrorTime = time.Now()
	score.ConsecutiveSuccesses = 0

	// Determine failure severity based on error code
	switch errorCode {
	case "2001": // Specific error code mentioned in requirements
		score.Severity = FailureCritical
		score.PriorityOffset = 30 // Large priority penalty
	case "5", "6", "401", "403", "429": // Auth/rate limit errors
		score.Severity = FailureMajor
		score.PriorityOffset = 20
	case "timeout", "connection refused": // Transport errors
		score.Severity = FailureMinor
		score.PriorityOffset = 10
	default:
		score.Severity = FailureMinor
		score.PriorityOffset = 5
	}

	// Auto-disable on consecutive failure threshold so the runtime can stop
	// hammering a broken carrier without operator intervention. This is
	// in-memory only; it is cleared by AutoDisableDuration expiry or by
	// ClearAutoDisableOn consecutive successes.
	if score.ErrorCount >= AutoDisableThreshold && score.AutoDisabledUntil.IsZero() {
		score.AutoDisabledUntil = time.Now().Add(AutoDisableDuration)
	}
}

// RecordSuccess records a successful carrier call. It increments the
// consecutive-success counter and clears an active auto-disable once
// ClearAutoDisableOn consecutive successes have been observed.
func (ft *FailureTracker) RecordSuccess(carrierID string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	score, exists := ft.scores[carrierID]
	if !exists {
		return
	}
	score.ConsecutiveSuccesses++
	if !score.AutoDisabledUntil.IsZero() && score.ConsecutiveSuccesses >= ClearAutoDisableOn {
		score.AutoDisabledUntil = time.Time{}
		score.ErrorCount = 0
		score.PriorityOffset = 0
		score.Severity = FailureNone
	}
}

// IsAutoDisabled reports whether a carrier is currently in the auto-disable
// window. Callers should skip such carriers when building bindings or plans.
func (ft *FailureTracker) IsAutoDisabled(carrierID string) bool {
	ft.mu.RLock()
	defer ft.mu.RUnlock()

	score, exists := ft.scores[carrierID]
	if !exists {
		return false
	}
	return !score.AutoDisabledUntil.IsZero() && time.Now().Before(score.AutoDisabledUntil)
}

// GetAdjustedPriority returns the adjusted priority for a carrier based on failures
func (ft *FailureTracker) GetAdjustedPriority(carrierID string, basePriority int) int {
	ft.mu.RLock()
	defer ft.mu.RUnlock()

	score, exists := ft.scores[carrierID]
	if !exists {
		return basePriority
	}

	// Decay old failure penalties
	if time.Since(score.LastErrorTime) > ft.decayWindow {
		// Reduce penalty as failures get older
		age := time.Since(score.LastErrorTime)
		decayFactor := 1.0 - (float64(age) / float64(ft.decayWindow))
		score.PriorityOffset = int(float64(score.PriorityOffset) * decayFactor * 0.5) // Half decay per window
	}

	return basePriority + score.PriorityOffset
}

// ResetFailures manually resets failure scores for a carrier
func (ft *FailureTracker) ResetFailures(carrierID string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if score, exists := ft.scores[carrierID]; exists {
		if time.Since(score.LastReset) > ft.decayWindow*3 { // Reset after 3 decay windows
			delete(ft.scores, carrierID)
		} else {
			score.ErrorCount = 0
			score.PriorityOffset = 0
			score.Severity = FailureNone
		}
	}
}

// RecordCarrierFailure records a failure for a carrier with error parsing
func (p CarrierPolicy) RecordCarrierFailure(carrierID string, errMsg string) {
	if p.FailureTracker != nil {
		// Extract error code from error message
		errorCode := extractErrorCode(errMsg)
		p.FailureTracker.RecordFailure(carrierID, errMsg, errorCode)
	}
}

// extractErrorCode extracts error codes from error messages
func extractErrorCode(errMsg string) string {
	// Check for common error patterns first
	switch {
	case strings.Contains(errMsg, "2001"):
		return "2001"
	case strings.Contains(errMsg, "401"):
		return "401"
	case strings.Contains(errMsg, "403"):
		return "403"
	case strings.Contains(errMsg, "429"):
		return "429"
	case strings.Contains(errMsg, "timeout"):
		return "timeout"
	case strings.Contains(errMsg, "connection refused"):
		return "connection refused"
	}

	// Look for error_code:X pattern
	if strings.Contains(errMsg, "error_code:") {
		// Split by error_code: and take the part after it
		parts := strings.Split(errMsg, "error_code:")
		if len(parts) > 1 {
			codePart := strings.TrimSpace(parts[1])
			// Extract just the code (remove any trailing characters)
			codeEnd := strings.IndexAny(codePart, " ,);")
			if codeEnd != -1 {
				return codePart[:codeEnd]
			}
			return codePart
		}
	}

	// Look for HTTP status codes in parentheses
	if strings.Contains(errMsg, "(") && strings.Contains(errMsg, ")") {
		start := strings.Index(errMsg, "(") + 1
		end := strings.Index(errMsg, ")")
		if end > start {
			code := strings.TrimSpace(errMsg[start:end])
			if len(code) <= 4 && strings.Contains(code, "4") {
				return code
			}
		}
	}

	return ""
}

// DefaultAdaptivePolicy returns the current WhiteTransport routing policy.
// The capability scorer is the single source of truth: routes are generated
// dynamically from AutoRoutes(StandardDescriptors(), DefaultScorer()) and the
// scorer is attached to the policy so the control plane can score carriers
// directly when sorting egress candidates. There is no env-var gate and no
// hardcoded priority table.
func DefaultAdaptivePolicy() CarrierPolicy {
	scorer := DefaultScorer()
	return CarrierPolicy{
		FailureTracker: NewFailureTracker(10 * time.Minute), // Decay penalties after 10 minutes
		Scorer:         scorer,
		Routes:         AutoRoutes(carriers.StandardDescriptors(), scorer),
	}
}

// DefaultRoutes is intentionally removed. The capability scorer
// (DefaultScorer / AutoRoutes) is now the only routing source. Callers that
// previously relied on a hardcoded priority table should construct a
// CarrierPolicy with Routes: AutoRoutes(..., DefaultScorer()) instead.

// Plan returns the full delivery plan for a traffic class. It supports
// multipath striping and small-message reliability without mirroring egress.
func (p CarrierPolicy) Plan(traffic fabric.TrafficClass, available []carriers.Descriptor) (RoutePlan, error) {
	selected := p.healthyRoutes(traffic, available)
	if len(selected) == 0 {
		return RoutePlan{}, errors.New("no healthy carrier route")
	}
	plan := RoutePlan{
		TrafficClass:      traffic,
		Strategy:          DeliverySingle,
		Primary:           selected[0],
		MaxInFlightChunks: 1,
	}
	if p.Scorer != nil {
		plan.Score = p.Scorer.Score(selected[0], traffic)
	}

	switch traffic {
	case fabric.TrafficBootstrap, fabric.TrafficControl:
		plan.Strategy = DeliveryMirrored
		plan.MirrorCount = minInt(2, len(selected))
		plan.HedgeTimeout = 750 * time.Millisecond
		plan.MaxInFlightChunks = plan.MirrorCount
		plan.Parallel = cloneDescriptors(selected[1:plan.MirrorCount])
	case fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog:
		plan.Strategy = DeliveryHedged
		plan.MirrorCount = 1
		plan.HedgeTimeout = time.Second
		plan.MaxInFlightChunks = 1
		plan.Parallel = cloneDescriptors(selected[1:minInt(2, len(selected))])
	case fabric.TrafficBulk:
		plan.Strategy = DeliveryStriped
		plan.MaxInFlightChunks = minInt(8, len(selected)*2)
		plan.Parallel = cloneDescriptors(selected[1:])
		plan.Repair = cloneDescriptors(selected[1:])
	case fabric.TrafficRepair:
		plan.Strategy = DeliveryStriped
		plan.MirrorCount = minInt(2, len(selected))
		plan.MaxInFlightChunks = minInt(4, len(selected))
		plan.Parallel = cloneDescriptors(selected[1:])
		plan.Repair = cloneDescriptors(selected[1:])
	case fabric.TrafficStream:
		plan.Strategy = DeliverySingle
		plan.MirrorCount = 1
		plan.MaxInFlightChunks = 1
	case fabric.TrafficEgress:
		plan.Strategy = DeliveryHedged
		plan.MirrorCount = 1
		plan.HedgeTimeout = 2 * time.Second
		plan.MaxInFlightChunks = minInt(4, len(selected))
		plan.Parallel = cloneDescriptors(selected[1:])
		plan.Repair = cloneDescriptors(selected[1:])
	}
	return plan, nil
}

// Select returns the best healthy carrier for a traffic class.
func (p CarrierPolicy) Select(traffic fabric.TrafficClass, available []carriers.Descriptor) (carriers.Descriptor, error) {
	plan, err := p.Plan(traffic, available)
	if err != nil {
		return carriers.Descriptor{}, err
	}
	return plan.Primary, nil
}

func (p CarrierPolicy) healthyRoutes(traffic fabric.TrafficClass, available []carriers.Descriptor) []carriers.Descriptor {
	byID := map[string]carriers.Descriptor{}
	for _, desc := range available {
		byID[desc.ID] = desc
	}
	routes := make([]Route, 0, len(p.Routes))
	for i := range p.Routes {
		route := &p.Routes[i]
		if route.TrafficClass != traffic {
			continue
		}
		desc, ok := byID[route.CarrierID]
		if !ok || !desc.Metrics.Healthy {
			continue
		}
		// Gate on token health when a checker is attached.
		if p.TokenChecker != nil && !p.TokenChecker.IsCarrierHealthy(route.CarrierID) {
			continue
		}
		routes = append(routes, *route)
	}

	// Apply failure-based deprioritization
	if p.FailureTracker != nil {
		sort.SliceStable(routes, func(i, j int) bool {
			priorityI := p.FailureTracker.GetAdjustedPriority(routes[i].CarrierID, routes[i].Priority)
			priorityJ := p.FailureTracker.GetAdjustedPriority(routes[j].CarrierID, routes[j].Priority)
			return priorityI < priorityJ
		})
	} else {
		sort.SliceStable(routes, func(i, j int) bool {
			return routes[i].Priority < routes[j].Priority
		})
	}

	selected := make([]carriers.Descriptor, 0, len(routes))
	for _, route := range routes {
		selected = append(selected, byID[route.CarrierID])
	}
	return selected
}

func cloneDescriptors(in []carriers.Descriptor) []carriers.Descriptor {
	out := make([]carriers.Descriptor, len(in))
	copy(out, in)
	return out
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
