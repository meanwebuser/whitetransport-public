package policy

import (
	"hash/fnv"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

// EgressRecoveryConfig bounds background recovery work for routes that have
// failed enough foreground dials to be temporarily removed from selection.
// A zero value field receives the production default in NewEgressRecoveryTracker.
type EgressRecoveryConfig struct {
	FailureThreshold  int
	InitialProbeDelay time.Duration
	MaxProbeDelay     time.Duration
	ProbeSuccesses    int
	FailbackCooldown  time.Duration
	FailbackGuard     time.Duration
	ProbeJitter       time.Duration
}

// DefaultEgressRecoveryConfig avoids constant provider traffic while allowing
// a recovered route to rejoin the next-dial candidate set promptly.
func DefaultEgressRecoveryConfig() EgressRecoveryConfig {
	return EgressRecoveryConfig{
		FailureThreshold:  AutoDisableThreshold,
		InitialProbeDelay: time.Minute,
		MaxProbeDelay:     15 * time.Minute,
		ProbeSuccesses:    ClearAutoDisableOn,
		FailbackCooldown:  15 * time.Second,
		FailbackGuard:     2 * time.Minute,
		ProbeJitter:       10 * time.Second,
	}
}

// EgressRecoveryTracker keeps route-specific recovery state. It deliberately
// keys by endpoint rather than carrier class: two sing-box or SSH profiles can
// fail independently and must never suppress each other.
type EgressRecoveryTracker struct {
	mu     sync.Mutex
	config EgressRecoveryConfig
	now    func() time.Time
	routes map[string]*egressRecoveryState
}

type egressRecoveryState struct {
	Failures          int
	ProbeFailures     int
	ProbeSuccesses    int
	Disabled          bool
	ProbeInFlight     bool
	NextProbeAt       time.Time
	EligibleAt        time.Time
	FailbackGuardTill time.Time
}

// NewEgressRecoveryTracker builds a deterministic tracker when now is
// supplied. Production callers pass nil and use wall-clock time.
func NewEgressRecoveryTracker(config EgressRecoveryConfig, now func() time.Time) *EgressRecoveryTracker {
	defaults := DefaultEgressRecoveryConfig()
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = defaults.FailureThreshold
	}
	if config.InitialProbeDelay <= 0 {
		config.InitialProbeDelay = defaults.InitialProbeDelay
	}
	if config.MaxProbeDelay <= 0 {
		config.MaxProbeDelay = defaults.MaxProbeDelay
	}
	if config.MaxProbeDelay < config.InitialProbeDelay {
		config.MaxProbeDelay = config.InitialProbeDelay
	}
	if config.ProbeSuccesses <= 0 {
		config.ProbeSuccesses = defaults.ProbeSuccesses
	}
	if config.FailbackCooldown < 0 {
		config.FailbackCooldown = 0
	}
	if config.FailbackGuard < 0 {
		config.FailbackGuard = 0
	}
	if config.ProbeJitter < 0 {
		config.ProbeJitter = 0
	}
	if now == nil {
		now = time.Now
	}
	return &EgressRecoveryTracker{config: config, now: now, routes: make(map[string]*egressRecoveryState)}
}

// EgressEndpointKey is stable for the duration of one negotiated session and
// distinguishes profiles that share a carrier implementation.
func EgressEndpointKey(endpoint carriers.Endpoint) string {
	return endpoint.Carrier + "\x00" + endpoint.ID + "\x00" + endpoint.Address
}

// CanDial reports whether a foreground connection may use this route. A route
// that has recovered waits through the cooldown before it can steal traffic
// back from the verified fallback.
func (t *EgressRecoveryTracker) CanDial(routeKey string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.routes[routeKey]
	if state == nil {
		return true
	}
	if state.Disabled {
		return false
	}
	return state.EligibleAt.IsZero() || !t.now().Before(state.EligibleAt)
}

// IsQuarantined reports whether a route still needs successful background
// probes. It intentionally ignores the post-recovery failback cooldown.
func (t *EgressRecoveryTracker) IsQuarantined(routeKey string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.routes[routeKey]
	return state != nil && state.Disabled
}

// RecordDialFailure feeds gradual foreground degradation into recovery. A
// failure during the short post-failback guard immediately re-disables the
// route, preventing one recovered route from flapping every new connection.
func (t *EgressRecoveryTracker) RecordDialFailure(routeKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.stateLocked(routeKey)
	now := t.now()
	if !state.FailbackGuardTill.IsZero() && now.Before(state.FailbackGuardTill) {
		t.disableLocked(routeKey, state, now)
		return
	}
	if state.Disabled {
		return
	}
	state.Failures++
	if state.Failures >= t.config.FailureThreshold {
		t.disableLocked(routeKey, state, now)
	}
}

// Quarantine immediately removes a route after a concrete open-stream or
// generation failure. Unlike gradual dial scoring, it must not wait for the
// foreground failure threshold.
func (t *EgressRecoveryTracker) Quarantine(routeKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.stateLocked(routeKey)
	if state.Disabled {
		return
	}
	t.disableLocked(routeKey, state, t.now())
}

// RecordDialSuccess clears gradual foreground failures for a healthy route.
// Disabled routes may rejoin only through successful background probes.
func (t *EgressRecoveryTracker) RecordDialSuccess(routeKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.routes[routeKey]
	if state == nil || state.Disabled {
		return
	}
	state.Failures = 0
}

// ClaimDueProbe reserves one due route. The caller must later report the
// result through RecordProbeResult. Reservation makes parallel loop ticks and
// long-running provider calls incapable of creating probe storms.
func (t *EgressRecoveryTracker) ClaimDueProbe(routeKeys []string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for _, routeKey := range routeKeys {
		state := t.routes[routeKey]
		if state == nil || !state.Disabled || state.ProbeInFlight || now.Before(state.NextProbeAt) {
			continue
		}
		state.ProbeInFlight = true
		return routeKey, true
	}
	return "", false
}

// RecordProbeResult records a bounded background Carrier.Probe result. Only
// the configured number of consecutive successes makes a route eligible, and
// even then FailbackCooldown delays its return to foreground selection.
func (t *EgressRecoveryTracker) RecordProbeResult(routeKey string, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.stateLocked(routeKey)
	now := t.now()
	state.ProbeInFlight = false
	if !state.Disabled {
		return
	}
	if !success {
		state.ProbeSuccesses = 0
		state.ProbeFailures++
		state.NextProbeAt = now.Add(t.probeDelay(routeKey, state.ProbeFailures))
		return
	}
	state.ProbeSuccesses++
	if state.ProbeSuccesses < t.config.ProbeSuccesses {
		// A successful probe is evidence that the provider is reachable again,
		// so request the hysteresis confirmation on the next bounded recovery
		// tick. Reapplying InitialProbeDelay here can make two-probe recovery
		// impossible inside the protocol's deliberately short session lease.
		state.NextProbeAt = now
		return
	}
	state.Disabled = false
	state.Failures = 0
	state.ProbeFailures = 0
	state.ProbeSuccesses = 0
	state.NextProbeAt = time.Time{}
	state.EligibleAt = now.Add(t.config.FailbackCooldown)
	state.FailbackGuardTill = state.EligibleAt.Add(t.config.FailbackGuard)
}

func (t *EgressRecoveryTracker) stateLocked(routeKey string) *egressRecoveryState {
	state := t.routes[routeKey]
	if state == nil {
		state = &egressRecoveryState{}
		t.routes[routeKey] = state
	}
	return state
}

func (t *EgressRecoveryTracker) disableLocked(routeKey string, state *egressRecoveryState, now time.Time) {
	state.Disabled = true
	state.Failures = 0
	state.ProbeSuccesses = 0
	state.ProbeInFlight = false
	state.EligibleAt = time.Time{}
	state.FailbackGuardTill = time.Time{}
	state.NextProbeAt = now.Add(t.probeDelay(routeKey, state.ProbeFailures))
}

func (t *EgressRecoveryTracker) probeDelay(routeKey string, failures int) time.Duration {
	delay := t.config.InitialProbeDelay
	for i := 0; i < failures && delay < t.config.MaxProbeDelay; i++ {
		delay *= 2
		if delay > t.config.MaxProbeDelay {
			delay = t.config.MaxProbeDelay
		}
	}
	if t.config.ProbeJitter == 0 {
		return delay
	}
	window := t.config.ProbeJitter
	if window > delay/2 {
		window = delay / 2
	}
	if window == 0 {
		return delay
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(routeKey))
	offsetRange := int64(window) * 2
	offset := time.Duration(int64(h.Sum64()%uint64(offsetRange+1)) - int64(window))
	if delay+offset <= 0 {
		return time.Millisecond
	}
	return delay + offset
}
