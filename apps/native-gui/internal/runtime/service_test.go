package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

func TestServiceStatusMapsProductState(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &stubClient{
		status: runtimeapi.Status{
			Role:         "client",
			State:        "connected",
			ActiveNodeID: "node-1",
			SessionID:    "session-1",
			SocksListen:  "127.0.0.1:8809",
		},
		nodes: []runtimeapi.Node{
			{NodeID: "node-1", Label: "Berlin", Available: true},
			{NodeID: "node-2", Label: "Paris", Available: false},
		},
		carriers: map[string]runtimeapi.CarrierSnapshot{
			"wbstream.vp8": {CarrierID: "wbstream.vp8", Healthy: true},
			"vk.messages":  {CarrierID: "vk.messages", Healthy: false},
		},
		build: runtimeapi.BuildInfo{Version: "dev", Commit: "abc123", Date: "2026-06-16"},
	})

	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateDegraded || status.Connected {
		t.Fatalf("state = %s connected=%v, want degraded and disconnected until system VPN is connected", status.State, status.Connected)
	}
	if status.AvailableServers != 1 || status.DiscoveredServers != 2 {
		t.Fatalf("server counts = %d/%d, want 1/2", status.AvailableServers, status.DiscoveredServers)
	}
	if status.HealthyCarriers != 1 || status.UnhealthyCarriers != 1 {
		t.Fatalf("carrier counts = %d/%d, want 1/1", status.HealthyCarriers, status.UnhealthyCarriers)
	}
	if status.RuntimeBuild.Commit != "abc123" {
		t.Fatalf("build = %+v, want commit abc123", status.RuntimeBuild)
	}
	if !strings.Contains(status.Message, "Runtime degraded") {
		t.Fatalf("message = %q, want active node", status.Message)
	}
	if status.TransportState != StateConnected {
		t.Fatalf("transport_state = %s, want connected", status.TransportState)
	}
	if status.SystemVPNState != SystemVPNUnsupported {
		t.Fatalf("system_vpn_state = %s, want unsupported until a host activates the OS VPN", status.SystemVPNState)
	}
}

func TestServiceStatusDoesNotClaimSystemVPNFromTransportSocks(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &stubClient{
		status: runtimeapi.Status{
			Role:        "client",
			State:       "connected",
			SessionID:   "session-transport-only",
			SocksListen: "127.0.0.1:18890",
		},
	})

	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.TransportState != StateConnected || status.Connected {
		t.Fatalf("transport status = %+v, want transport connected but product disconnected", status)
	}
	if status.SystemVPNState == SystemVPNConnected {
		t.Fatalf("system_vpn_state = %s, SOCKS transport alone must not claim OS VPN connected", status.SystemVPNState)
	}
}

func TestServiceStatusProjectsActiveEgress(t *testing.T) {
	t.Parallel()

	const selectedID = "private-selected-endpoint"
	const automaticID = "private-automatic-endpoint"
	const address = "vless://private-address-token@example.invalid:443"
	const metadataToken = "private-metadata-token"

	tests := []struct {
		name          string
		active        bool
		selected      string
		automatic     string
		carrier       string
		wantCarrier   string
		wantSelection string
	}{
		{"manual takes precedence", true, selectedID, automaticID, " VK.DOCS.256 ", "vk_documents", "manual"},
		{"VK Documents egress alias", true, selectedID, automaticID, "vk.docs.egress", "vk_documents", "manual"},
		{"documents variant", true, selectedID, "", "vk.docs.1024", "vk_documents", "manual"},
		{"VK Messages", true, selectedID, "", "vk.messages", "vk_messages", "manual"},
		{"telemost alias", true, selectedID, "", "yandex.telemost", "telemost", "manual"},
		{"dion alias", true, selectedID, "", "dion.call", "dion", "manual"},
		{"vless", true, selectedID, "", "singbox.vless", "vless", "manual"},
		{"auto-discovered vless", true, selectedID, "", "xray-private-profile", "vless", "manual"},
		{"unverified xray prefix", true, selectedID, "", "xray.private-profile", "other", "manual"},
		{"empty xray tag", true, selectedID, "", "xray-", "other", "manual"},
		{"hysteria2", true, selectedID, "", "hysteria2", "hysteria2", "manual"},
		{"unknown carrier", true, selectedID, automaticID, "private-unknown-carrier", "other", "manual"},
		{"VK Messages automatic egress alias", true, "", automaticID, "telemost", "vk_messages", "automatic"},
		{"stale manual ID", true, "private-stale-endpoint", automaticID, "telemost", "vk_messages", "automatic"},
		{"no ID match", true, "private-stale-endpoint", "", "telemost", "", ""},
		{"unselected", true, "", "", "telemost", "", ""},
		{"inactive session", false, selectedID, automaticID, "telemost", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newTestService(t, &stubClient{status: runtimeapi.Status{
				State:                     "connected",
				ActiveNodeID:              "fixture-node",
				SessionID:                 "fixture-session",
				SessionActive:             tt.active,
				SelectedEgressEndpointID:  tt.selected,
				AutomaticEgressEndpointID: tt.automatic,
				EgressEndpoints: []runtimeapi.Endpoint{
					{Carrier: "dion"},
					{ID: automaticID, Carrier: "vk.egress"},
					{ID: selectedID, Carrier: tt.carrier, Address: address, Metadata: map[string]string{"token": metadataToken}},
				},
			}})

			status, err := service.Status(context.Background())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			payload, err := json.Marshal(status)
			if err != nil {
				t.Fatalf("marshal renderer status: %v", err)
			}
			var wire struct {
				SessionActive   *bool  `json:"session_active"`
				EgressCarrier   string `json:"egress_carrier"`
				EgressSelection string `json:"egress_selection"`
			}
			if err := json.Unmarshal(payload, &wire); err != nil {
				t.Fatalf("decode renderer status: %v", err)
			}
			if wire.SessionActive == nil || *wire.SessionActive != tt.active {
				t.Fatalf("session_active must equal %t in renderer status: %s", tt.active, payload)
			}
			if wire.EgressCarrier != tt.wantCarrier || wire.EgressSelection != tt.wantSelection {
				t.Fatalf("egress = %q/%q, want %q/%q", wire.EgressCarrier, wire.EgressSelection, tt.wantCarrier, tt.wantSelection)
			}
			if tt.wantCarrier == "" && (strings.Contains(string(payload), `"egress_carrier"`) || strings.Contains(string(payload), `"egress_selection"`)) {
				t.Fatalf("renderer status included egress fields without a matched active route: %s", payload)
			}
			for _, hidden := range []string{selectedID, automaticID, "private-stale-endpoint", address, metadataToken, "private-unknown-carrier", "private-profile"} {
				if strings.Contains(string(payload), hidden) {
					t.Fatalf("renderer status exposed raw route data: %s", payload)
				}
			}
		})
	}
}

func TestServiceStatusProjectsCredentialFreeErrorCode(t *testing.T) {
	t.Parallel()

	const private = "private-error-detail"
	tests := []struct {
		lastError string
		wantCode  string
	}{
		{"", ""},
		{"no available nodes", "no_available_nodes"},
		{"no active carrier session", "no_active_session"},
		{"no locally supported egress endpoint in active session: " + private, "no_compatible_egress"},
		{"carrier routes exhausted for node " + private + "; selecting another node", "carrier_routes_exhausted"},
		{"tunnel liveness check failed 3 times", "tunnel_unhealthy"},
		{"context deadline exceeded", "timeout"},
		{"upload failed: " + private, "runtime_error"},
	}
	for _, tt := range tests {
		service := newTestService(t, &stubClient{status: runtimeapi.Status{State: "degraded", LastError: tt.lastError}})
		status, err := service.Status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		payload, err := json.Marshal(status)
		if err != nil {
			t.Fatalf("marshal renderer status: %v", err)
		}
		var wire struct {
			ErrorCode string `json:"error_code"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil {
			t.Fatalf("decode renderer status: %v", err)
		}
		if strings.Contains(string(payload), "last_runtime_error") || strings.Contains(string(payload), private) {
			t.Fatalf("renderer status exposed raw runtime error: %s", payload)
		}
		if wire.ErrorCode != tt.wantCode {
			t.Errorf("error_code for %q = %q, want %q", tt.lastError, wire.ErrorCode, tt.wantCode)
		}
	}
}

func TestServiceStatusCarriesExactSystemVPNProfileOnlyToNativeHost(t *testing.T) {
	t.Parallel()
	profile := &runtimeapi.SystemVPNProfile{
		SchemaRevision:   runtimeapi.SystemVPNProfileSchemaRevision,
		DaemonInstanceID: "daemon-a",
		ProfileRevision:  9,
		ProfileHash:      "hash-a",
		SessionID:        "session-a",
		SelectedNodeID:   "node-a",
		Ready:            true,
	}
	service := newTestService(t, &stubClient{status: runtimeapi.Status{
		State:            "connected",
		ActiveNodeID:     "node-a",
		SessionID:        "session-a",
		SystemVPNProfile: profile,
	}})

	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var got runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(status.SystemVPNProfile, &got); err != nil {
		t.Fatalf("decode native profile: %v", err)
	}
	if got.DaemonInstanceID != profile.DaemonInstanceID || got.ProfileRevision != profile.ProfileRevision || got.ProfileHash != profile.ProfileHash || got.SessionID != profile.SessionID {
		t.Fatalf("native profile = %+v, want exact identity %+v", got, profile)
	}
	rendererPayload, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal renderer status: %v", err)
	}
	if strings.Contains(string(rendererPayload), "system_vpn_profile") || strings.Contains(string(rendererPayload), "daemon-a") {
		t.Fatalf("renderer payload exposed native-only profile: %s", rendererPayload)
	}
}

func TestNativeWireStatesCoverMacLifecycle(t *testing.T) {
	if StateUnsupported != "unsupported" || StateDisconnecting != "disconnecting" {
		t.Fatalf("product wire states unsupported=%q disconnecting=%q", StateUnsupported, StateDisconnecting)
	}
	if SystemVPNDisconnecting != "disconnecting" {
		t.Fatalf("system VPN disconnecting wire state = %q", SystemVPNDisconnecting)
	}
	if state := overallProductState(StateConnected, SystemVPNDisconnecting); state != StateDisconnecting {
		t.Fatalf("overall state = %q, want disconnecting", state)
	}
}

func TestServiceListServersReturnsProductSummaries(t *testing.T) {
	t.Parallel()

	lastSeenAt := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	service := newTestService(t, &stubClient{
		nodes: []runtimeapi.Node{
			{
				NodeID:       "node-1",
				Label:        "",
				Country:      "DE",
				Region:       "Berlin",
				Available:    true,
				Capabilities: []string{"egress", "control"},
				LastSeenAt:   lastSeenAt,
			},
		},
	})

	servers, err := service.ListServers(context.Background())
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("len(servers) = %d, want 1", len(servers))
	}
	server := servers[0]
	if server.ID != "node-1" || server.Label != "node-1" || !server.Available {
		t.Fatalf("unexpected server: %+v", server)
	}
	if server.Country != "DE" || server.Region != "Berlin" || server.LastSeenAt != lastSeenAt.Format(time.RFC3339Nano) {
		t.Fatalf("server metadata not preserved: %+v", server)
	}
	if server.LatencyMS != nil {
		t.Fatalf("latency = %v, want nil until runtime provides a real measurement", *server.LatencyMS)
	}
	if len(server.Capabilities) != 2 || server.Capabilities[0] != "egress" {
		t.Fatalf("capabilities = %+v, want copied capabilities", server.Capabilities)
	}
}

func TestServiceListServersSortsAvailableServersByFreshness(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &stubClient{
		nodes: []runtimeapi.Node{
			{NodeID: "example-runtime-canary", Label: "example-runtime-canary", Available: true, LastSeenAt: time.Date(2026, 9, 2, 11, 39, 0, 0, time.UTC)},
			{NodeID: "offline-node", Label: "alpha-offline", Available: false},
			{NodeID: "primary-test-node", Label: "primary-test-node", Available: true, LastSeenAt: time.Date(2026, 9, 2, 11, 40, 0, 0, time.UTC)},
		},
	})

	servers, err := service.ListServers(context.Background())
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	gotIDs := []string{servers[0].ID, servers[1].ID, servers[2].ID}
	wantIDs := []string{"primary-test-node", "example-runtime-canary", "offline-node"}
	for index := range wantIDs {
		if gotIDs[index] != wantIDs[index] {
			t.Fatalf("server order = %+v, want %+v", gotIDs, wantIDs)
		}
	}
}

func TestServiceConnectUsesSelectedServer(t *testing.T) {
	t.Parallel()

	client := &stubClient{
		status: runtimeapi.Status{State: "disconnected"},
		nodes:  []runtimeapi.Node{{NodeID: "node-2", Label: "Selected", Available: true}},
		carriers: map[string]runtimeapi.CarrierSnapshot{
			"wbstream.vp8": {CarrierID: "wbstream.vp8", Healthy: true},
		},
	}
	service := newTestService(t, client)

	status, err := service.Connect(context.Background(), " node-2 ")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if client.connectedNodeID != "node-2" {
		t.Fatalf("connectedNodeID = %q, want node-2", client.connectedNodeID)
	}
	if status.State != StateDegraded || status.Connected || status.ActiveNodeID != "node-2" {
		t.Fatalf("unexpected transport-only status: %+v", status)
	}
}

func TestServiceTelemetryProbesConnectedRuntime(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &stubClient{
		status: runtimeapi.Status{
			State:        "connected",
			ActiveNodeID: "primary-test-node",
			SocksListen:  "127.0.0.1:18890",
		},
	})
	service.telemetryProbe = &stubTelemetryProbe{
		result: TelemetryProbeResult{ExternalIP: "203.0.113.9", LatencyMS: 73},
	}

	telemetry, err := service.Telemetry(context.Background())
	if err != nil {
		t.Fatalf("Telemetry: %v", err)
	}
	if telemetry.ActiveNodeID != "primary-test-node" || telemetry.ExternalIP != "203.0.113.9" {
		t.Fatalf("telemetry = %+v, want node and external IP", telemetry)
	}
	if telemetry.LatencyMS == nil || *telemetry.LatencyMS != 73 {
		t.Fatalf("latency = %v, want 73", telemetry.LatencyMS)
	}
	if telemetry.MeasuredAt == "" {
		t.Fatalf("measured_at is empty: %+v", telemetry)
	}
	if service.telemetryProbe.(*stubTelemetryProbe).socksListen != "127.0.0.1:18890" {
		t.Fatalf("probe socks = %q", service.telemetryProbe.(*stubTelemetryProbe).socksListen)
	}
}

func TestServiceTelemetrySkipsDisconnectedRuntime(t *testing.T) {
	t.Parallel()

	probe := &stubTelemetryProbe{}
	service := newTestService(t, &stubClient{
		status: runtimeapi.Status{State: "disconnected", SocksListen: "127.0.0.1:18890"},
	})
	service.telemetryProbe = probe

	telemetry, err := service.Telemetry(context.Background())
	if err != nil {
		t.Fatalf("Telemetry: %v", err)
	}
	if probe.called {
		t.Fatal("telemetry probe was called while disconnected")
	}
	if telemetry.ExternalIP != "" || telemetry.LatencyMS != nil {
		t.Fatalf("telemetry = %+v, want empty while disconnected", telemetry)
	}
}

func TestServiceTelemetryReturnsProbeErrorAsProductTelemetry(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &stubClient{
		status: runtimeapi.Status{State: "connected", ActiveNodeID: "node-1", SocksListen: "127.0.0.1:18890"},
	})
	service.telemetryProbe = &stubTelemetryProbe{err: errors.New("SOCKS timeout")}

	telemetry, err := service.Telemetry(context.Background())
	if err != nil {
		t.Fatalf("Telemetry returned method error: %v", err)
	}
	if !strings.Contains(telemetry.Error, "SOCKS timeout") {
		t.Fatalf("telemetry error = %q, want SOCKS timeout", telemetry.Error)
	}
	if telemetry.ExternalIP != "" || telemetry.LatencyMS != nil {
		t.Fatalf("telemetry = %+v, want no successful probe data", telemetry)
	}
}

func TestServicePropagatesRuntimeError(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &stubClient{statusErr: errors.New("daemon unavailable")})
	_, err := service.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "daemon unavailable") {
		t.Fatalf("Status error = %v, want daemon unavailable", err)
	}
}

func TestServiceDiagnosticsReturnsStructuredFailures(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &stubClient{
		status:   runtimeapi.Status{State: "disconnected"},
		nodes:    []runtimeapi.Node{},
		carriers: map[string]runtimeapi.CarrierSnapshot{},
		planErr:  errors.New("planner unavailable"),
	})

	result := service.RunDiagnostics(context.Background())
	if result.Passed {
		t.Fatal("diagnostics passed, want failure")
	}
	if len(result.Steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3", len(result.Steps))
	}
	if result.Steps[0].Status != stepPass {
		t.Fatalf("status step = %+v, want pass", result.Steps[0])
	}
	if result.Steps[1].Status != stepFail || !strings.Contains(result.Steps[1].Error, "planner unavailable") {
		t.Fatalf("plan step = %+v, want planner failure", result.Steps[1])
	}
}

func TestServiceDiagnosticsStatusErrorIncludesExplicitTransportAndSystemVPNState(t *testing.T) {
	t.Parallel()

	const private = "private-status-endpoint"
	service := newTestService(t, &stubClient{statusErr: fmt.Errorf("poll %s: %w", private, context.DeadlineExceeded)})
	result := service.RunDiagnostics(context.Background())

	if result.Status.State != StateError {
		t.Fatalf("diagnostic status = %+v, want product error", result.Status)
	}
	if result.Status.TransportState != StateError {
		t.Fatalf("transport_state = %q, want error", result.Status.TransportState)
	}
	if result.Status.SystemVPNState != SystemVPNUnsupported {
		t.Fatalf("system_vpn_state = %q, want unsupported when daemon status is unavailable", result.Status.SystemVPNState)
	}
	if result.Status.Connected {
		t.Fatal("diagnostic status claims connected after daemon status failure")
	}
	if result.Status.ErrorCode != "timeout" {
		t.Fatalf("error_code = %q, want timeout", result.Status.ErrorCode)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal renderer diagnostics: %v", err)
	}
	if strings.Contains(string(payload), private) {
		t.Fatalf("renderer diagnostics exposed status error details: %s", payload)
	}
}

func TestNewServiceRequiresClient(t *testing.T) {
	t.Parallel()

	if _, err := NewService(nil); err == nil {
		t.Fatal("NewService(nil) succeeded, want error")
	}
}

func newTestService(t *testing.T, client *stubClient) *Service {
	t.Helper()
	if client.build == (runtimeapi.BuildInfo{}) {
		client.build = runtimeapi.BuildInfo{Version: "test", Commit: "test", Date: "test"}
	}
	if client.carriers == nil {
		client.carriers = map[string]runtimeapi.CarrierSnapshot{}
	}
	service, err := NewService(client)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	baseTime := time.Date(2026, 6, 16, 15, 0, 0, 0, time.UTC)
	step := 0
	service.now = func() time.Time {
		step++
		return baseTime.Add(time.Duration(step) * time.Millisecond)
	}
	return service
}

type stubClient struct {
	status          runtimeapi.Status
	statusErr       error
	nodes           []runtimeapi.Node
	nodesErr        error
	carriers        map[string]runtimeapi.CarrierSnapshot
	carriersErr     error
	build           runtimeapi.BuildInfo
	buildErr        error
	planErr         error
	connectedNodeID string
}

type stubTelemetryProbe struct {
	result      TelemetryProbeResult
	err         error
	called      bool
	socksListen string
}

func (p *stubTelemetryProbe) Probe(_ context.Context, socksListen string) (TelemetryProbeResult, error) {
	p.called = true
	p.socksListen = socksListen
	if p.err != nil {
		return TelemetryProbeResult{}, p.err
	}
	if p.result == (TelemetryProbeResult{}) {
		return TelemetryProbeResult{}, fmt.Errorf("stub telemetry result not configured")
	}
	return p.result, nil
}

func (f *stubClient) Status(context.Context) (runtimeapi.Status, error) {
	if f.statusErr != nil {
		return runtimeapi.Status{}, f.statusErr
	}
	return f.status, nil
}

func (f *stubClient) Nodes(context.Context) ([]runtimeapi.Node, error) {
	if f.nodesErr != nil {
		return nil, f.nodesErr
	}
	return append([]runtimeapi.Node(nil), f.nodes...), nil
}

func (f *stubClient) Connect(_ context.Context, nodeID string) (runtimeapi.Status, error) {
	f.connectedNodeID = nodeID
	return runtimeapi.Status{
		State:        "connected",
		ActiveNodeID: nodeID,
		SessionID:    "session-" + nodeID,
		SocksListen:  "127.0.0.1:8809",
	}, nil
}

func (f *stubClient) Disconnect(context.Context) (runtimeapi.Status, error) {
	return runtimeapi.Status{State: "disconnected", SocksListen: "127.0.0.1:8809"}, nil
}

func (f *stubClient) Carriers(context.Context) (map[string]runtimeapi.CarrierSnapshot, error) {
	if f.carriersErr != nil {
		return nil, f.carriersErr
	}
	out := make(map[string]runtimeapi.CarrierSnapshot, len(f.carriers))
	for key, value := range f.carriers {
		out[key] = value
	}
	return out, nil
}

func (f *stubClient) Build(context.Context) (runtimeapi.BuildInfo, error) {
	if f.buildErr != nil {
		return runtimeapi.BuildInfo{}, f.buildErr
	}
	return f.build, nil
}

func (f *stubClient) Plan(_ context.Context, trafficClass string, _ int) (runtimeapi.RoutePlan, error) {
	if f.planErr != nil {
		return runtimeapi.RoutePlan{}, f.planErr
	}
	return runtimeapi.RoutePlan{
		TrafficClass: trafficClass,
		Strategy:     "single",
		Primary:      runtimeapi.DescriptorView{ID: "file.mailbox", Healthy: true},
	}, nil
}

// A managed restart may complete while a status request is in flight. All
// fields of that response must still describe the same daemon instance.
func TestServiceStatusKeepsClientSnapshotAcrossRebind(t *testing.T) {
	old := &rebindingStatusClient{stubClient: &stubClient{nodes: []runtimeapi.Node{{NodeID: "old-node", Available: true}}}}
	service, err := NewService(old)
	if err != nil {
		t.Fatal(err)
	}
	old.rebind = func() {
		if err := service.RebindAPI("http://127.0.0.1:1"); err != nil {
			t.Fatal(err)
		}
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.DiscoveredServers != 1 {
		t.Fatalf("mixed daemon response: %+v", status)
	}
	if service.apiClient() == old {
		t.Fatal("new address was not installed")
	}
}

type rebindingStatusClient struct {
	*stubClient
	rebind func()
}

func (c *rebindingStatusClient) Status(ctx context.Context) (runtimeapi.Status, error) {
	c.rebind()
	return c.stubClient.Status(ctx)
}
