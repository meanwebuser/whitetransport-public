package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

func TestTransportExplorationIsDisabledByDefault(t *testing.T) {
	control := &ControlPlane{cfg: config.Config{Role: config.RoleClient}}
	control.startTransportExplorationLoop(context.Background())
	if control.explorationCancel != nil {
		t.Fatal("exploration started without the runtime flag")
	}
}

func TestTransportExplorationRecordsMeasuredTCPAndPosteriorEvidence(t *testing.T) {
	base := newMemoryCarrierWithDescriptor(t, "exploration-memory", carriers.CarrierSSHTCP)
	descriptor := base.Descriptor()
	descriptor.Evidence = carriers.NewEvidenceState()
	base.descriptor = descriptor
	carrier := &explorationCarrier{memoryCarrier: base}
	endpoint := carriers.Endpoint{ID: "exploration-endpoint", Carrier: carriers.CarrierSSHTCP, Address: "memory://exploration"}
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient, Exploration: config.ExplorationConfig{
			Enabled:             true,
			TCPProbeAddress:     "example.test:443",
			MaxConcurrency:      1,
			ProbeTimeoutSeconds: 1,
			PersistPath:         filepath.Join(t.TempDir(), "exploration.json"),
		}},
		egress: []carrierRef{{ID: endpoint.ID, Descriptor: descriptor, Binding: policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}}},
		policy: policy.DefaultAdaptivePolicy(),
	}
	control.runTransportExploration(context.Background())
	snapshot := control.explorationStatus()
	if len(snapshot.Measurements) != 1 || !snapshot.Measurements[0].TCPHealthy {
		t.Fatalf("exploration snapshot = %+v", snapshot)
	}
	if snapshot.SelectedCarrier == "" {
		t.Fatal("exploration did not produce a provider-neutral selection")
	}
	latest, ok := carrier.Descriptor().Evidence.Latest()
	if !ok || latest.Source != carriers.MetricSourceProbe {
		t.Fatalf("posterior evidence = %+v/%v", latest, ok)
	}
	persisted, err := os.ReadFile(control.cfg.Exploration.PersistPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(control.cfg.Exploration.PersistPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("exploration receipt mode = %o, want 600", got)
	}
	if !strings.Contains(string(persisted), "prior_latency_ms") || !strings.Contains(string(persisted), "observed_source") || strings.Contains(string(persisted), "example.test:443") {
		t.Fatalf("persisted exploration receipt did not preserve evidence boundaries: %s", persisted)
	}
}

func TestTransportExplorationRestoresPosteriorAsStartupPrior(t *testing.T) {
	persistPath := filepath.Join(t.TempDir(), "exploration.json")
	endpoint := carriers.Endpoint{ID: "persisted-endpoint", Carrier: carriers.CarrierSSHTCP, Address: "memory://persisted"}

	firstBase := newMemoryCarrierWithDescriptor(t, "persisted-carrier", carriers.CarrierSSHTCP)
	firstDescriptor := firstBase.Descriptor()
	firstDescriptor.Evidence = carriers.NewEvidenceState()
	firstBase.descriptor = firstDescriptor
	firstCarrier := &explorationCarrier{memoryCarrier: firstBase}
	firstControl := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient, Exploration: config.ExplorationConfig{
			Enabled:             true,
			TCPProbeAddress:     "example.test:443",
			MaxConcurrency:      1,
			ProbeTimeoutSeconds: 1,
			PersistPath:         persistPath,
		}},
		egress: []carrierRef{{ID: endpoint.ID, Descriptor: firstDescriptor, Binding: policy.CarrierBinding{Carrier: firstCarrier, Endpoint: endpoint}}},
		policy: policy.DefaultAdaptivePolicy(),
	}
	firstControl.runTransportExploration(context.Background())

	secondBase := newMemoryCarrierWithDescriptor(t, "persisted-carrier", carriers.CarrierSSHTCP)
	secondDescriptor := secondBase.Descriptor()
	secondDescriptor.Evidence = carriers.NewEvidenceState()
	secondBase.descriptor = secondDescriptor
	secondCarrier := &explorationCarrier{memoryCarrier: secondBase}
	secondControl := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient, Exploration: config.ExplorationConfig{
			Enabled:     true,
			PersistPath: persistPath,
		}},
		egress: []carrierRef{{ID: endpoint.ID, Descriptor: secondDescriptor, Binding: policy.CarrierBinding{Carrier: secondCarrier, Endpoint: endpoint}}},
	}
	if err := secondControl.loadExplorationSnapshot(); err != nil {
		t.Fatalf("load exploration receipt: %v", err)
	}

	snapshot := secondControl.explorationStatus()
	if !snapshot.Enabled || snapshot.Running || snapshot.LastRunAt.IsZero() {
		t.Fatalf("restored exploration status = %+v", snapshot)
	}
	prior, ok := secondCarrier.Descriptor().Evidence.Prior()
	if !ok {
		t.Fatal("persisted posterior was not promoted to a startup prior")
	}
	if prior.Source != carriers.MetricSourceCatalog || !prior.Metrics.Healthy || prior.Metrics.Latency <= 0 || prior.Metrics.BandwidthBPS != 0 || prior.Metrics.CostPerMB != 0 || prior.Metrics.QuotaRemaining != 0 {
		t.Fatalf("startup prior = %+v", prior)
	}
	if _, ok := secondCarrier.Descriptor().Evidence.Latest(); ok {
		t.Fatal("restored startup prior must not masquerade as a current-process observation")
	}
	effective := secondCarrier.Descriptor().EffectiveMetrics()
	if !effective.Healthy || effective.Latency <= 0 || effective.BandwidthBPS <= 0 {
		t.Fatalf("policy baseline did not use startup prior: %+v", effective)
	}
	if len(snapshot.Measurements) != 1 || !prior.ObservedAt.Equal(snapshot.Measurements[0].ObservedAt) {
		t.Fatalf("startup prior time does not match receipt: prior=%s snapshot=%+v", prior.ObservedAt, snapshot.Measurements)
	}
}

func TestTransportExplorationRestoresPriorWhenBackgroundLoopIsDisabled(t *testing.T) {
	persistPath := filepath.Join(t.TempDir(), "exploration.json")
	base := newMemoryCarrierWithDescriptor(t, "disabled-restore", carriers.CarrierSSHTCP)
	descriptor := base.Descriptor()
	descriptor.Evidence = carriers.NewEvidenceState()
	base.descriptor = descriptor
	endpoint := carriers.Endpoint{ID: "disabled-endpoint", Carrier: carriers.CarrierSSHTCP, Address: "memory://disabled"}
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient, Exploration: config.ExplorationConfig{
			PersistPath: persistPath,
		}},
		egress: []carrierRef{{ID: endpoint.ID, Descriptor: descriptor, Binding: policy.CarrierBinding{Carrier: &explorationCarrier{memoryCarrier: base}, Endpoint: endpoint}}},
	}
	if err := persistExploration(persistPath, runtimeapi.TransportExploration{
		Enabled:       true,
		LastRunAt:     time.Unix(30, 0).UTC(),
		PersistedPath: persistPath,
		Measurements: []runtimeapi.TransportMeasurement{{
			CarrierID:            carriers.CarrierSSHTCP,
			EndpointID:           endpoint.ID,
			ObservedHealthy:      true,
			ObservedLatencyMs:    42,
			ObservedBandwidthBPS: 1234,
			ObservedReliability:  0.8,
			ObservedSource:       string(carriers.MetricSourceProbe),
			ObservedAt:           time.Unix(30, 0).UTC(),
		}},
	}); err != nil {
		t.Fatalf("write receipt: %v", err)
	}
	control.startTransportExplorationLoop(context.Background())
	if control.explorationCancel != nil {
		t.Fatal("disabled exploration unexpectedly started")
	}
	status := control.explorationStatus()
	if status.Enabled || status.Running || len(status.Measurements) != 1 {
		t.Fatalf("disabled exploration status = %+v", status)
	}
	prior, ok := base.Descriptor().Evidence.Prior()
	if !ok || prior.Metrics.Latency != 42*time.Millisecond || prior.Metrics.BandwidthBPS != 1234 {
		t.Fatalf("disabled-loop startup prior = %+v/%v", prior, ok)
	}
}

func TestTransportExplorationPersistsSafeProbeFailureWithoutRawError(t *testing.T) {
	persistPath := filepath.Join(t.TempDir(), "exploration.json")
	base := newMemoryCarrierWithDescriptor(t, "failed-exploration", carriers.CarrierSSHTCP)
	descriptor := base.Descriptor()
	descriptor.Evidence = carriers.NewEvidenceState()
	base.descriptor = descriptor
	carrier := &failingExplorationCarrier{memoryCarrier: base}
	endpoint := carriers.Endpoint{ID: "failed-endpoint", Carrier: carriers.CarrierSSHTCP, Address: "memory://failed"}
	control := &ControlPlane{
		cfg:    config.Config{Role: config.RoleClient, Exploration: config.ExplorationConfig{PersistPath: persistPath}},
		egress: []carrierRef{{ID: endpoint.ID, Descriptor: descriptor, Binding: policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}}},
		policy: policy.DefaultAdaptivePolicy(),
	}
	control.runTransportExploration(context.Background())
	snapshot := control.explorationStatus()
	if len(snapshot.Measurements) != 1 || snapshot.Measurements[0].ObservedHealthy {
		t.Fatalf("failed exploration snapshot = %+v", snapshot)
	}
	measurement := snapshot.Measurements[0]
	if measurement.ObservedFailure != "safe_probe_failed" || strings.Contains(measurement.ObservedFailure, "private-target") {
		t.Fatalf("unsafe failure receipt = %+v", measurement)
	}
	data, err := os.ReadFile(persistPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-target") {
		t.Fatalf("raw probe error leaked into receipt: %s", data)
	}
	if got := carrier.Descriptor().EffectiveMetrics(); got.Healthy {
		t.Fatalf("failed probe did not update effective health: %+v", got)
	}
}

type explorationCarrier struct{ *memoryCarrier }

type failingExplorationCarrier struct{ *memoryCarrier }

func (c *failingExplorationCarrier) SafeEgressRecoveryProbe(context.Context, carriers.Endpoint) (carriers.Metrics, error) {
	return carriers.Metrics{}, fmt.Errorf("probe failed for private-target.example:443")
}

var _ carriers.SafeEgressRecoveryProber = (*failingExplorationCarrier)(nil)

func (c *explorationCarrier) SafeEgressRecoveryProbe(context.Context, carriers.Endpoint) (carriers.Metrics, error) {
	return carriers.Metrics{Healthy: true, Latency: 150 * time.Millisecond, LastOK: time.Now().UTC(), BandwidthBPS: 777, CostPerMB: 0.12, QuotaRemaining: -1, Reliability: 1}, nil
}

func (c *explorationCarrier) DialStream(context.Context, carriers.Endpoint, string) (net.Conn, error) {
	client, peer := net.Pipe()
	_ = peer.Close()
	return client, nil
}

var _ carriers.StreamDialer = (*explorationCarrier)(nil)
var _ carriers.SafeEgressRecoveryProber = (*explorationCarrier)(nil)
