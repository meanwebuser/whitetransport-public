package adminreporter

import (
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/router"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
)

type fakeStatusProvider struct {
	status    runtime.StatusView
	health    map[string]router.CarrierSnapshot
	socksAddr string
}

func (f fakeStatusProvider) Status() runtime.StatusView {
	return f.status
}

func (f fakeStatusProvider) CarrierHealthSnapshot() map[string]router.CarrierSnapshot {
	return f.health
}

func (f fakeStatusProvider) GetSocksAddr() string {
	return f.socksAddr
}

func TestBuildPayloadUsesDerivedDefaults(t *testing.T) {
	provider := fakeStatusProvider{
		status: runtime.StatusView{State: "connected"},
		health: map[string]router.CarrierSnapshot{
			"vk.messages": {},
			"ok.docs":     {},
		},
		socksAddr: "127.0.0.1:1080",
	}

	r := Reporter{
		cfg: config.AdminReporterConfig{},
		node: config.Config{
			NodeID:      "node-1",
			DisplayName: "example-exit-node",
			ListenAPI:   "127.0.0.1:17680",
			Role:        config.RoleNode,
			Country:     "Russia",
			Region:      "Moscow",
		},
		status:    provider,
		version:   "1.2.3",
		startedAt: time.Now().Add(-5 * time.Second),
	}

	p := r.buildPayload()
	if p.NodeID != "node-1" {
		t.Fatalf("expected node id, got %q", p.NodeID)
	}
	if p.Name != "example-exit-node" {
		t.Fatalf("expected display name, got %q", p.Name)
	}
	if p.APIHost != "127.0.0.1" {
		t.Fatalf("expected derived api host, got %q", p.APIHost)
	}
	if p.APIPort != 17680 {
		t.Fatalf("expected derived api port 17680, got %d", p.APIPort)
	}
	if p.IP != "127.0.0.1" {
		t.Fatalf("expected fallback ip from api host, got %q", p.IP)
	}
	if p.SocksPort != 1080 {
		t.Fatalf("expected socks port 1080, got %d", p.SocksPort)
	}
	if p.Status != "online" {
		t.Fatalf("expected online status, got %q", p.Status)
	}
	if p.HealthStatus != "healthy" {
		t.Fatalf("expected healthy status, got %q", p.HealthStatus)
	}
	if p.Version != "1.2.3" {
		t.Fatalf("expected build version fallback, got %q", p.Version)
	}
	if p.Uptime < 4 {
		t.Fatalf("expected uptime to be derived from start time, got %f", p.Uptime)
	}
	if len(p.Carriers) != 2 {
		t.Fatalf("expected 2 carriers, got %d", len(p.Carriers))
	}
	seen := map[string]bool{}
	for _, carrierID := range p.Carriers {
		seen[carrierID] = true
	}
	if !seen["vk.messages"] || !seen["ok.docs"] {
		t.Fatalf("expected both carriers in payload, got %#v", p.Carriers)
	}
}

func TestBuildPayloadUsesReporterOverrides(t *testing.T) {
	r := Reporter{
		cfg: config.AdminReporterConfig{
			IP:          "node.example.invalid",
			APIHost:     "10.0.0.5",
			APIPort:     19090,
			NodeVersion: "9.9.9",
		},
		node: config.Config{
			NodeID:    "node-2",
			ListenAPI: "127.0.0.1:17680",
			Role:      config.RoleNode,
		},
		status: fakeStatusProvider{
			status: runtime.StatusView{State: "disconnected", LastError: "carrier timeout"},
		},
		version:   "1.0.0",
		startedAt: time.Now(),
	}

	p := r.buildPayload()
	if p.Name != "node-2" {
		t.Fatalf("expected node id as fallback name, got %q", p.Name)
	}
	if p.IP != "node.example.invalid" {
		t.Fatalf("expected configured ip override, got %q", p.IP)
	}
	if p.APIHost != "10.0.0.5" || p.APIPort != 19090 {
		t.Fatalf("expected configured api endpoint override, got %s:%d", p.APIHost, p.APIPort)
	}
	if p.Version != "9.9.9" {
		t.Fatalf("expected explicit node version override, got %q", p.Version)
	}
	if p.Status != "online" {
		t.Fatalf("expected disconnected runtime state to map to online, got %q", p.Status)
	}
	if p.HealthStatus != "degraded" {
		t.Fatalf("expected degraded health with last error, got %q", p.HealthStatus)
	}
	if p.LastError != "carrier timeout" {
		t.Fatalf("expected last error passthrough, got %q", p.LastError)
	}
}

func TestResolveEndpoint(t *testing.T) {
	r := Reporter{cfg: config.AdminReporterConfig{AdminURL: "https://admin.example.com/root/"}}

	endpoint, err := r.resolveEndpoint("/api/nodes/register")
	if err != nil {
		t.Fatalf("resolve endpoint: %v", err)
	}
	if endpoint != "https://admin.example.com/api/nodes/register" {
		t.Fatalf("unexpected endpoint %q", endpoint)
	}
}

func TestAddressHelpers(t *testing.T) {
	if got := hostPart("127.0.0.1:17680"); got != "127.0.0.1" {
		t.Fatalf("unexpected host part %q", got)
	}
	if got := hostPart("node.example.invalid"); got != "node.example.invalid" {
		t.Fatalf("unexpected raw host part %q", got)
	}
	if got := portPart("127.0.0.1:1080", 42); got != 1080 {
		t.Fatalf("unexpected port part %d", got)
	}
	if got := portPart("invalid", 42); got != 42 {
		t.Fatalf("expected fallback port, got %d", got)
	}
}
