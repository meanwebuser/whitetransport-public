package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

func (c *ControlPlane) startTransportExplorationLoop(ctx context.Context) {
	if err := c.loadExplorationSnapshot(); err != nil {
		log.Printf("warning: failed to load transport exploration receipt: %v", err)
	}
	c.resortEgressAfterExplorationRestore()
	if !c.cfg.Exploration.Enabled {
		c.explorationMu.Lock()
		c.explorationSnapshot.Enabled = false
		c.explorationSnapshot.Running = false
		c.explorationMu.Unlock()
		return
	}
	c.mu.Lock()
	if c.explorationCancel != nil {
		c.mu.Unlock()
		return
	}
	explorationCtx, cancel := context.WithCancel(ctx)
	c.explorationCancel = cancel
	c.mu.Unlock()

	c.explorationMu.Lock()
	c.explorationSnapshot.Enabled = true
	c.explorationSnapshot.Running = true
	c.explorationMu.Unlock()

	c.explorationWG.Add(1)
	go func() {
		defer c.explorationWG.Done()
		interval := time.Duration(c.cfg.Exploration.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = time.Minute
		}
		for {
			c.runTransportExploration(explorationCtx)
			timer := time.NewTimer(interval)
			select {
			case <-explorationCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				c.explorationMu.Lock()
				c.explorationSnapshot.Running = false
				c.explorationMu.Unlock()
				return
			case <-timer.C:
			}
		}
	}()
}

func (c *ControlPlane) resortEgressAfterExplorationRestore() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.policy.Scorer != nil {
		sortEgressRefsScored(c.egress, c.policy.Scorer)
		return
	}
	sortEgressRefs(c.egress)
}

func (c *ControlPlane) runTransportExploration(ctx context.Context) {
	refs := c.explorationRefs()
	maxConcurrency := c.cfg.Exploration.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 2
	}
	if maxConcurrency > 8 {
		maxConcurrency = 8
	}
	probeTimeout := time.Duration(c.cfg.Exploration.ProbeTimeoutSeconds) * time.Second
	if probeTimeout <= 0 {
		probeTimeout = 5 * time.Second
	}
	if probeTimeout > 30*time.Second {
		probeTimeout = 30 * time.Second
	}

	measurements := make([]runtimeapi.TransportMeasurement, 0, len(refs))
	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for _, ref := range refs {
		ref := ref
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			measurement := c.measureCarrier(ctx, ref, probeTimeout)
			<-sem
			mu.Lock()
			measurements = append(measurements, measurement)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(measurements, func(i, j int) bool {
		if measurements[i].CarrierID != measurements[j].CarrierID {
			return measurements[i].CarrierID < measurements[j].CarrierID
		}
		return measurements[i].EndpointID < measurements[j].EndpointID
	})

	selected := c.explorationSelection(refs)
	persistPath := c.explorationPersistPath()
	snapshot := runtimeapi.TransportExploration{
		Enabled:         true,
		Running:         true,
		LastRunAt:       time.Now().UTC(),
		SelectedCarrier: selected,
		PersistedPath:   persistPath,
		Measurements:    measurements,
	}
	c.explorationMu.Lock()
	c.explorationSnapshot = snapshot
	c.explorationMu.Unlock()
	if persistPath != "" {
		persisted := snapshot
		persisted.Running = false
		if err := persistExploration(persistPath, persisted); err != nil {
			log.Printf("warning: failed to persist transport exploration receipt: %v", err)
		}
	}
}

func (c *ControlPlane) explorationPersistPath() string {
	if path := strings.TrimSpace(c.cfg.Exploration.PersistPath); path != "" {
		return path
	}
	if path := strings.TrimSpace(c.cfg.StateFile); path != "" {
		return path + ".exploration.json"
	}
	return ".tmp/transport-exploration.json"
}

// loadExplorationSnapshot restores the last redacted exploration receipt and
// promotes its newest posterior observations to the configured carriers'
// startup baseline. The runtime flag remains the source of truth for whether
// exploration is active; a receipt never enables the loop by itself.
func (c *ControlPlane) loadExplorationSnapshot() error {
	path := c.explorationPersistPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var snapshot runtimeapi.TransportExploration
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	c.restoreExplorationPrior(snapshot.Measurements)
	snapshot.Enabled = true
	snapshot.Running = false
	snapshot.PersistedPath = path
	c.explorationMu.Lock()
	c.explorationSnapshot = snapshot
	c.explorationMu.Unlock()
	return nil
}

func (c *ControlPlane) restoreExplorationPrior(measurements []runtimeapi.TransportMeasurement) {
	type measurementKey struct {
		carrierID  string
		endpointID string
	}
	latest := make(map[measurementKey]runtimeapi.TransportMeasurement, len(measurements))
	for _, measurement := range measurements {
		if strings.TrimSpace(measurement.ObservedSource) == "" || measurement.ObservedAt.IsZero() {
			continue
		}
		key := measurementKey{carrierID: measurement.CarrierID, endpointID: measurement.EndpointID}
		previous, ok := latest[key]
		if !ok || measurement.ObservedAt.After(previous.ObservedAt) {
			latest[key] = measurement
		}
	}

	c.mu.RLock()
	refs := append([]carrierRef(nil), c.egress...)
	c.mu.RUnlock()
	for _, ref := range refs {
		measurement, ok := latest[measurementKey{carrierID: ref.Descriptor.ID, endpointID: ref.ID}]
		if !ok {
			continue
		}
		observed := carriers.Metrics{
			Healthy:        measurement.ObservedHealthy,
			Latency:        time.Duration(measurement.ObservedLatencyMs) * time.Millisecond,
			BandwidthBPS:   measurement.ObservedBandwidthBPS,
			Reliability:    measurement.ObservedReliability,
			CostPerMB:      measurement.ObservedCostPerMB,
			QuotaRemaining: measurement.ObservedQuota,
			FailureReason:  measurement.ObservedFailure,
		}
		if measurement.ObservedHealthy {
			observed.LastOK = measurement.ObservedAt
		} else if observed.FailureReason == "" {
			observed.FailureReason = measurement.ErrorCode
			if observed.FailureReason == "" {
				observed.FailureReason = "exploration_unhealthy"
			}
		}
		ref.Binding.Carrier.Descriptor().AdoptPrior(observed, measurement.ObservedAt)
	}
}

func (c *ControlPlane) explorationRefs() []carrierRef {
	c.mu.RLock()
	refs := append([]carrierRef(nil), c.egress...)
	active := c.active
	c.mu.RUnlock()
	if active != nil {
		// Session aliases are already represented by the configured refs for
		// static transports. Dynamic profiles remain covered by the normal
		// recovery path until they expose an explicit safe probe.
	}
	return refs
}

func (c *ControlPlane) measureCarrier(ctx context.Context, ref carrierRef, timeout time.Duration) runtimeapi.TransportMeasurement {
	started := time.Now().UTC()
	prior := ref.Binding.Carrier.Descriptor().EffectiveMetrics()
	result := runtimeapi.TransportMeasurement{
		CarrierID:         ref.Descriptor.ID,
		EndpointID:        ref.ID,
		Source:            string(carriers.MetricSourceProbe),
		StartedAt:         started,
		PriorLatencyMs:    prior.Latency.Milliseconds(),
		PriorBandwidthBPS: prior.BandwidthBPS,
		PriorReliability:  prior.Reliability,
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var firstErr error
	var observed carriers.Metrics
	observedSet := false
	if prober, ok := ref.Binding.Carrier.(carriers.SafeEgressRecoveryProber); ok {
		metrics, err := prober.SafeEgressRecoveryProbe(probeCtx, ref.Binding.Endpoint)
		if err != nil {
			firstErr = err
			observed = carriers.Metrics{Healthy: false, FailureReason: "safe_probe_failed"}
			observedSet = true
			result.TCPHealthy = false
			result.FailureStage = "safe_probe"
			result.ErrorCode = "unhealthy"
		} else {
			observed = metrics
			// Safe probes provide availability/latency evidence. Catalog
			// bandwidth, cost and quota are priors and must not be written as
			// measured posterior values by a one-shot health check.
			observed.BandwidthBPS = 0
			observed.CostPerMB = 0
			observed.QuotaRemaining = 0
			if metrics.Healthy {
				observed.Reliability = 1
				observed.FailureReason = ""
			} else {
				observed.Reliability = 0
				observed.FailureReason = "probe_unhealthy"
				firstErr = fmt.Errorf("safe probe reported unhealthy")
				result.FailureStage = "safe_probe"
				result.ErrorCode = "unhealthy"
			}
			observedSet = true
			result.TCPHealthy = metrics.Healthy
		}
	} else {
		firstErr = fmt.Errorf("safe probe is not implemented")
		observed = carriers.Metrics{Healthy: false, FailureReason: "safe_probe_unsupported"}
		observedSet = true
		result.TCPHealthy = false
		result.FailureStage = "safe_probe"
		result.ErrorCode = "unsupported"
	}

	if address := strings.TrimSpace(c.cfg.Exploration.TCPProbeAddress); address != "" {
		dialStarted := time.Now()
		var conn net.Conn
		var err error
		canDial := false
		if c.tunnel != nil && c.tunnel.SupportsEndpoint(ref.Binding.Endpoint) {
			// Let the managed tunnel own sidecar startup and lifecycle. This
			// is required for idle exploration of static Hysteria2/VLESS
			// bindings whose carrier StreamDialFunc is wired on first use.
			canDial = true
			conn, err = c.tunnel.DialContext(probeCtx, ref.Binding.Endpoint, address)
		} else if dialer, ok := ref.Binding.Carrier.(carriers.StreamDialer); ok {
			canDial = true
			conn, err = dialer.DialStream(probeCtx, ref.Binding.Endpoint, address)
		}
		if canDial {
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				result.TCPHealthy = false
				if result.FailureStage == "" {
					result.FailureStage = "tcp_dial"
					result.ErrorCode = "unreachable"
				}
				observed.Healthy = false
				observed.FailureReason = "tcp_unreachable"
				observedSet = true
			} else {
				result.TCPHealthy = true
				dialDelay := time.Since(dialStarted)
				result.TCPDelayMs = dialDelay.Milliseconds()
				observed.Healthy = true
				if result.TCPDelayMs > 0 {
					observed.Latency = dialDelay
				}
				observed.LastOK = time.Now().UTC()
				observed.Reliability = 1
				observed.FailureReason = ""
				// A successful real stream dial is the strongest result for a
				// stream adapter; an optional/unsupported safe probe must not
				// quarantine it.
				firstErr = nil
				result.FailureStage = ""
				result.ErrorCode = ""
				observedSet = true
				_ = conn.Close()
			}
		}
	}
	if udp, ok := ref.Binding.Carrier.(carriers.DatagramProber); ok {
		latency, err := udp.ProbeUDP(probeCtx)
		if err != nil {
			if firstErr == nil && !result.TCPHealthy {
				firstErr = err
			}
			if result.FailureStage == "" {
				result.FailureStage = "udp_probe"
				result.ErrorCode = "unreachable"
			}
		} else {
			result.UDPHealthy = true
			result.UDPDelayMs = latency.Milliseconds()
		}
	}
	if firstErr != nil && result.FailureStage == "" {
		result.FailureStage = "probe"
		result.ErrorCode = "unhealthy"
	}
	result.CompletedAt = time.Now().UTC()
	if observedSet {
		observedAt := result.CompletedAt
		if observed.Healthy {
			observed.FailureReason = ""
		} else if observed.FailureReason == "" {
			observed.FailureReason = "probe_unhealthy"
		}
		ref.Binding.Carrier.Descriptor().Observe(carriers.MetricSourceProbe, observed, observedAt)
		result.ObservedHealthy = observed.Healthy
		result.ObservedLatencyMs = observed.Latency.Milliseconds()
		result.ObservedBandwidthBPS = observed.BandwidthBPS
		result.ObservedReliability = observed.Reliability
		result.ObservedCostPerMB = observed.CostPerMB
		result.ObservedQuota = observed.QuotaRemaining
		result.ObservedFailure = observed.FailureReason
		result.ObservedSource = string(carriers.MetricSourceProbe)
		result.ObservedAt = observedAt
	}
	return result
}

func (c *ControlPlane) explorationSelection(refs []carrierRef) string {
	descriptors := make([]carriers.Descriptor, 0, len(refs))
	for _, ref := range refs {
		descriptors = append(descriptors, ref.Binding.Carrier.Descriptor())
	}
	if len(descriptors) == 0 || c.policy.Scorer == nil {
		return ""
	}
	selected, err := c.policy.Select(fabric.TrafficEgress, descriptors)
	if err != nil {
		return ""
	}
	return selected.ID
}

func (c *ControlPlane) explorationStatus() *runtimeapi.TransportExploration {
	c.explorationMu.RLock()
	defer c.explorationMu.RUnlock()
	snapshot := c.explorationSnapshot
	snapshot.Measurements = append([]runtimeapi.TransportMeasurement(nil), snapshot.Measurements...)
	return &snapshot
}

func persistExploration(path string, snapshot runtimeapi.TransportExploration) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".exploration-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
