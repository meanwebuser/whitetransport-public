package runtime

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

const (
	egressRecoveryTickInterval = 15 * time.Second
	egressRecoveryProbeTimeout = 10 * time.Second
	nodeLivenessProbeTimeout   = 4 * time.Second
	nodeReselectionTimeout     = 12 * time.Second
)

func (c *ControlPlane) startEgressRecoveryLoop(ctx context.Context) {
	c.mu.Lock()
	if c.recoveryCancel != nil {
		c.mu.Unlock()
		return
	}
	recoveryCtx, cancel := context.WithCancel(ctx)
	c.recoveryCancel = cancel
	c.mu.Unlock()

	c.recoveryWG.Add(1)
	go func() {
		defer c.recoveryWG.Done()
		ticker := time.NewTicker(egressRecoveryTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-recoveryCtx.Done():
				return
			case <-c.stopCh:
				return
			case <-ticker.C:
				c.recoverOneEgressEndpoint(recoveryCtx)
			}
		}
	}()
}

// recoverOneEgressEndpoint checks at most one quarantined, higher-ranked
// endpoint. It never opens a foreground egress connection and only accepts an
// explicit safe-probe capability, so DION/WB ProviderCarrier cannot create a
// room or call merely because a fallback is active.
func (c *ControlPlane) recoverOneEgressEndpoint(ctx context.Context) bool {
	c.mu.Lock()
	active := c.active
	tracker := c.egressRecovery
	if active == nil || active.SelectedEgressEndpointID != "" || active.AutomaticEgressEndpointID == "" || tracker == nil {
		c.mu.Unlock()
		return false
	}
	ranked := c.rankedEgressEndpoints(active.EgressEndpoints)
	keys := make([]string, 0, len(ranked))
	endpoints := make(map[string]carriers.Endpoint, len(ranked))
	for _, endpoint := range ranked {
		if endpoint.ID == active.AutomaticEgressEndpointID {
			break
		}
		key := policy.EgressEndpointKey(endpoint)
		keys = append(keys, key)
		endpoints[key] = endpoint
	}
	key, due := tracker.ClaimDueProbe(keys)
	if !due {
		c.mu.Unlock()
		return false
	}
	endpoint := endpoints[key]
	sessionID := active.SessionID
	c.mu.Unlock()

	probeCtx, cancel := context.WithTimeout(ctx, egressRecoveryProbeTimeout)
	err := c.safeRecoveryProbe(probeCtx, endpoint)
	cancel()

	// Always release ClaimDueProbe's reservation, even when the session changed
	// while the bounded probe was in flight.
	tracker.RecordProbeResult(key, err == nil)
	quarantined := tracker.IsQuarantined(key)
	recovered := err == nil && !quarantined
	dbg("egress recovery probe endpoint=%s carrier=%s err=%v quarantined=%t recovered=%t", endpoint.ID, endpoint.Carrier, err, quarantined, recovered)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.SessionID != sessionID || c.egressRecovery != tracker {
		return true
	}
	if c.policy.FailureTracker != nil {
		if err != nil {
			c.policy.RecordCarrierFailure(endpoint.ID, err.Error())
		} else {
			c.policy.FailureTracker.RecordSuccess(endpoint.ID)
		}
	}
	if recovered && c.carrierHealth != nil {
		c.carrierHealth.RecordConstructed(endpoint.ID)
	}
	return true
}

func (c *ControlPlane) safeRecoveryProbe(ctx context.Context, endpoint carriers.Endpoint) error {
	binding, _ := c.findBindingByCarrier(endpoint.Carrier)
	if binding.Carrier == nil {
		return fmt.Errorf("no local carrier binding for recovery candidate %q", endpoint.ID)
	}
	// ProviderCarrier.Probe can send bytes through an active session, so it is
	// never a generic background-recovery capability. Some providers expose a
	// narrower, audited identity preflight; unwrap only that explicit opt-in.
	if bridge, ok := binding.Carrier.(*carriers.ProviderCarrier); ok {
		prober, ok := bridge.GetProvider().(provider.SafeEgressRecoveryProber)
		if !ok {
			return fmt.Errorf("provider carrier %q has no safe autonomous recovery probe", endpoint.Carrier)
		}
		return prober.SafeEgressRecoveryProbe(ctx)
	}
	prober, ok := binding.Carrier.(carriers.SafeEgressRecoveryProber)
	if !ok {
		return fmt.Errorf("carrier %q has no safe autonomous recovery probe", endpoint.Carrier)
	}
	metrics, err := prober.SafeEgressRecoveryProbe(ctx, endpoint)
	if err != nil {
		return err
	}
	if !metrics.Healthy {
		return fmt.Errorf("recovery probe for %q reported unhealthy", endpoint.ID)
	}
	return nil
}

func endpointByID(endpoints []carriers.Endpoint, id string) (carriers.Endpoint, bool) {
	for _, endpoint := range endpoints {
		if endpoint.ID == id {
			return endpoint, true
		}
	}
	return carriers.Endpoint{}, false
}

func (c *ControlPlane) initializeAutomaticEgressRouteLocked() {
	if c.active == nil || c.active.SelectedEgressEndpointID != "" {
		return
	}
	ranked := c.rankedEgressEndpoints(c.active.EgressEndpoints)
	if len(ranked) == 0 {
		return
	}
	if c.egressRecovery == nil {
		c.egressRecovery = policy.NewEgressRecoveryTracker(policy.DefaultEgressRecoveryConfig(), nil)
	}
	c.active.AutomaticEgressEndpointID = ranked[0].ID
}

// promoteRecoveredEgressLocked changes only the automatic preference for a
// future connection. Existing connections stay on their already-open fallback.
func (c *ControlPlane) promoteRecoveredEgressLocked() {
	if c.active == nil || c.active.SelectedEgressEndpointID != "" || c.egressRecovery == nil {
		return
	}
	for _, endpoint := range c.rankedEgressEndpoints(c.active.EgressEndpoints) {
		if c.egressRecovery.CanDial(policy.EgressEndpointKey(endpoint)) {
			if c.egressRouteStreams != nil {
				c.egressRouteStreams.restore(sessionRouteKey(c.active.SessionID, endpoint))
			}
			if endpoint.ID == c.active.AutomaticEgressEndpointID {
				return
			}
			c.active.AutomaticEgressEndpointID = endpoint.ID
			c.profileRevision++
			return
		}
	}
}

func (c *ControlPlane) recordAutomaticEgressFailure(sessionID string, endpoint carriers.Endpoint, dialErr error) {
	if dialErr == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.SessionID != sessionID || c.active.AutomaticEgressEndpointID != endpoint.ID {
		return
	}
	if c.egressRecovery != nil {
		c.egressRecovery.RecordDialFailure(policy.EgressEndpointKey(endpoint))
	}
	if c.policy.FailureTracker != nil {
		c.policy.RecordCarrierFailure(endpoint.ID, dialErr.Error())
	}
}

func (c *ControlPlane) recordQuarantinedEgressFailure(sessionID string, endpoint carriers.Endpoint, dialErr error) {
	if dialErr == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.SessionID != sessionID {
		return
	}
	if c.egressRecovery != nil {
		c.egressRecovery.Quarantine(policy.EgressEndpointKey(endpoint))
	}
	if c.carrierHealth != nil {
		c.carrierHealth.RecordRuntimeFailure(endpoint.ID)
	}
	if c.policy.FailureTracker != nil {
		c.policy.RecordCarrierFailure(endpoint.ID, dialErr.Error())
	}
}

// recordQuarantinedEgressStreamFailure records late stream health only. It
// must not create a background replacement: the next automatic DialEgress
// performs the bounded liveness probe and replacement synchronously before
// SOCKS receives an upstream connection.
func (c *ControlPlane) recordQuarantinedEgressStreamFailure(sessionID string, endpoint carriers.Endpoint, dialErr error) {
	c.recordQuarantinedEgressFailure(sessionID, endpoint, dialErr)
}

func (c *ControlPlane) rankedEgressEndpoints(endpoints []carriers.Endpoint) []carriers.Endpoint {
	type scoredEndpoint struct {
		endpoint carriers.Endpoint
		score    float64
	}
	scored := make([]scoredEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		score := -1.0
		if c.policy.Scorer != nil {
			descriptor, err := egressDescriptor(endpoint)
			if err != nil {
				// Session answers preserve configured aliases (mail.primary,
				// git.primary, etc.). Resolve that stable identity through the
				// local binding before treating a real carrier as unknown.
				if binding, _ := c.findBindingByCarrier(endpoint.Carrier); binding.Carrier != nil {
					descriptor = binding.Carrier.Descriptor()
					err = nil
				}
			}
			if err == nil {
				score = c.policy.Scorer.Score(descriptor, fabric.TrafficEgress)
				if descriptor.ID == carriers.CarrierFileMailbox {
					score = -0.5
				}
			}
		}
		scored = append(scored, scoredEndpoint{endpoint: endpoint, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	ranked := make([]carriers.Endpoint, 0, len(scored))
	for _, endpoint := range scored {
		ranked = append(ranked, endpoint.endpoint)
	}
	return ranked
}
