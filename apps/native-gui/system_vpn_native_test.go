package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

type stubNativeSystemVPNBridge struct {
	permission string
	start      string
	stop       string
	status     string
	logs       string
	err        error
	started    string
	blockStart <-chan struct{}
}

func (s *stubNativeSystemVPNBridge) Permission() (string, error) { return s.permission, s.err }
func (s *stubNativeSystemVPNBridge) Start(profile string) (string, error) {
	s.started = profile
	if s.blockStart != nil {
		<-s.blockStart
	}
	return s.start, s.err
}
func (s *stubNativeSystemVPNBridge) Stop() (string, error)   { return s.stop, s.err }
func (s *stubNativeSystemVPNBridge) Status() (string, error) { return s.status, s.err }
func (s *stubNativeSystemVPNBridge) Logs() (string, error)   { return s.logs, s.err }

func TestNativeSystemVPNHostPassesExactProfileAndRequiresProviderMatch(t *testing.T) {
	profile := testNativeSystemVPNProfile(t)
	identity, err := decodeSystemVPNProfileIdentity(profile)
	if err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	bridge := &stubNativeSystemVPNBridge{start: connectedNativeResponse(identity, true)}
	host := newNativeSystemVPNHost(bridge, time.Second)

	observation, err := host.Start(context.Background(), profile)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var extensionConfig map[string]any
	if err := json.Unmarshal([]byte(bridge.started), &extensionConfig); err != nil {
		t.Fatalf("decode extension config: %v", err)
	}
	if extensionConfig["daemon_instance_id"] != identity.DaemonInstanceID || extensionConfig["profile_hash"] != identity.ProfileHash {
		t.Fatalf("extension config lost exact profile identity: %+v", extensionConfig)
	}
	if extensionConfig["route_mode"] != "full_tunnel" || extensionConfig["socks_endpoint"] == nil || extensionConfig["bypass"] == nil || extensionConfig["dns"] == nil {
		t.Fatalf("extension config does not match Swift packet-tunnel schema: %+v", extensionConfig)
	}
	if !observationMatchesProfile(observation, identity) {
		t.Fatalf("native observation = %+v, want exact provider match", observation)
	}
	if identity.ProfileValidUntil.IsZero() || !observation.ProfileValidUntil.Equal(identity.ProfileValidUntil) {
		t.Fatalf("native lease = %+v, want exact non-zero profile deadline", observation)
	}

	bridge.start = connectedNativeResponse(identity, false)
	if _, err := host.Start(context.Background(), profile); err == nil {
		t.Fatal("Start accepted connected NE state without matching provider status")
	}
}

func TestObservationRejectsDifferentProfileValidityDeadline(t *testing.T) {
	identity, err := decodeSystemVPNProfileIdentity(testNativeSystemVPNProfile(t))
	if err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	observation := systemVPNObservation{
		State:             "connected",
		ProviderState:     "connected",
		DaemonInstanceID:  identity.DaemonInstanceID,
		Revision:          identity.Revision,
		SessionID:         identity.SessionID,
		ProfileHash:       identity.ProfileHash,
		ProfileValidUntil: identity.ProfileValidUntil.Add(time.Second),
	}
	if observationMatchesProfile(observation, identity) {
		t.Fatal("observation accepted a different profile validity deadline")
	}
}

func TestBuildMacOSPacketTunnelConfigurationUsesEarliestProfileValidityDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var profile runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(testNativeSystemVPNProfile(t), &profile); err != nil {
		t.Fatalf("decode profile fixture: %v", err)
	}
	profile.IssuedAt = now.Add(-time.Minute)
	profile.ExpiresAt = now.Add(4 * time.Minute)
	profile.Dependencies[0].DNSExpiresAt = now.Add(3 * time.Minute)
	profile.Dependencies[1].DNSExpiresAt = now.Add(90*time.Second + 987*time.Millisecond)
	profile.Dependencies[2].DNSExpiresAt = now.Add(3 * time.Minute)
	if err := profile.SetHash(); err != nil {
		t.Fatalf("SetHash: %v", err)
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}

	payload, err := buildMacOSPacketTunnelConfiguration(raw, now)
	if err != nil {
		t.Fatalf("build configuration: %v", err)
	}
	var configuration struct {
		ProfileValidUntil time.Time `json:"profile_valid_until"`
	}
	if err := json.Unmarshal(payload, &configuration); err != nil {
		t.Fatalf("decode configuration: %v", err)
	}
	want := profile.Dependencies[1].DNSExpiresAt.UTC().Truncate(time.Second)
	if !configuration.ProfileValidUntil.Equal(want) {
		t.Fatalf("profile_valid_until = %s, want earliest dependency deadline %s", configuration.ProfileValidUntil, want)
	}
	var wire map[string]any
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("decode wire configuration: %v", err)
	}
	if wire["profile_valid_until"] != want.Format(time.RFC3339) {
		t.Fatalf("profile_valid_until wire = %v, want canonical whole-second %q", wire["profile_valid_until"], want.Format(time.RFC3339))
	}
}

func TestNativeSystemVPNHostFailsExplicitlyOnBridgeErrorsMalformedJSONAndTimeout(t *testing.T) {
	profile := testNativeSystemVPNProfile(t)
	bridge := &stubNativeSystemVPNBridge{start: `not-json`}
	host := newNativeSystemVPNHost(bridge, time.Second)
	if _, err := host.Start(context.Background(), profile); err == nil {
		t.Fatal("Start accepted malformed native response")
	}
	bridge.start = `{"success":false,"state":"error","provider_state":"error","provider_status_matched":false,"error":"native fixture failed"}`
	if _, err := host.Start(context.Background(), profile); err == nil || !errors.Is(err, errNativeSystemVPNOperation) {
		t.Fatalf("Start error = %v, want explicit native operation error", err)
	}

	blocked := make(chan struct{})
	timeoutHost := newNativeSystemVPNHost(&stubNativeSystemVPNBridge{blockStart: blocked}, 20*time.Millisecond)
	finished := make(chan error, 1)
	go func() {
		_, startErr := timeoutHost.Start(context.Background(), profile)
		finished <- startErr
	}()
	select {
	case startErr := <-finished:
		t.Fatalf("Start returned while uncancellable native operation was still running: %v", startErr)
	case <-time.After(50 * time.Millisecond):
	}
	close(blocked)
	if startErr := <-finished; !errors.Is(startErr, context.DeadlineExceeded) {
		t.Fatalf("timeout Start error after native completion = %v, want deadline exceeded", startErr)
	}
}

func testNativeSystemVPNProfile(t *testing.T) json.RawMessage {
	t.Helper()
	now := time.Now().UTC()
	profile := runtimeapi.SystemVPNProfile{
		SchemaRevision:   runtimeapi.SystemVPNProfileSchemaRevision,
		DaemonInstanceID: "daemon-a",
		ProfileRevision:  4,
		SessionID:        "session-a",
		SelectedNodeID:   "node-a",
		Ready:            true,
		IssuedAt:         now.Add(-time.Second),
		ExpiresAt:        now.Add(time.Minute),
		SocksListen:      "127.0.0.1:41723",
		RouteMode:        runtimeapi.SystemVPNRouteNone,
		CarrierControlOrigins: []string{
			"https://api.ok.test", "https://api.vk.test", "wss://egress.test",
		},
		CarrierControlRoutes: map[string][]string{
			"api.ok.test": {"198.51.100.20/32"}, "api.vk.test": {"198.51.100.10/32"}, "egress.test": {"198.51.100.30/32"},
		},
		DNSSnapshot: map[string][]string{
			"api.ok.test": {"198.51.100.20"}, "api.vk.test": {"198.51.100.10"}, "egress.test": {"198.51.100.30"},
		},
		DNSServers: []string{"1.1.1.1"},
		Dependencies: []runtimeapi.SystemVPNDependency{
			{Purpose: runtimeapi.SystemVPNDependencyControl, Carrier: "ok.messages", Scheme: "https", Host: "api.ok.test", Port: 443, Addresses: []string{"198.51.100.20"}, DNSExpiresAt: now.Add(time.Minute)},
			{Purpose: runtimeapi.SystemVPNDependencyDiscovery, Carrier: "vk.messages", Scheme: "https", Host: "api.vk.test", Port: 443, Addresses: []string{"198.51.100.10"}, DNSExpiresAt: now.Add(time.Minute)},
			{Purpose: runtimeapi.SystemVPNDependencyEgress, Carrier: "wbstream", Scheme: "wss", Host: "egress.test", Port: 443, Addresses: []string{"198.51.100.30"}, DNSExpiresAt: now.Add(time.Minute)},
		},
		MTU:       1500,
		Readiness: runtimeapi.SystemVPNReadiness{Ready: true, Provenance: "test"},
	}
	profile.SortProfileSlices()
	if err := profile.SetHash(); err != nil {
		t.Fatalf("SetHash: %v", err)
	}
	payload, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	return payload
}

func connectedNativeResponse(identity systemVPNProfileIdentity, matched bool) string {
	return fmt.Sprintf(`{"success":true,"state":"connected","provider_state":"connected","provider_status_matched":%t,"daemon_instance_id":%q,"profile_revision":%d,"profile_hash":%q,"session_id":%q,"profile_valid_until":%q}`,
		matched, identity.DaemonInstanceID, identity.Revision, identity.ProfileHash, identity.SessionID, identity.ProfileValidUntil.Format(time.RFC3339))
}

func TestNativeSystemVPNHostMapsRedactedProviderLogs(t *testing.T) {
	bridge := &stubNativeSystemVPNBridge{logs: `{
  "success":true,"state":"connected","provider_state":"connected","provider_status_matched":true,
  "logs":[{"schema_version":2,"timestamp":"2026-07-20T09:00:00Z","level":"info","event":"packet_flow_started","message":"started token=fixture-secret","metadata":{}}]
}`}
	host := newNativeSystemVPNHost(bridge, time.Second)
	lines, err := host.Logs(context.Background())
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(lines) != 1 || lines[0].Level != "info" || lines[0].Fields["event"] != "packet_flow_started" {
		t.Fatalf("mapped logs = %+v", lines)
	}
	if lines[0].Message == "started token=fixture-secret" || lines[0].Message == "" {
		t.Fatalf("provider log message was not defensively redacted: %q", lines[0].Message)
	}
	if lines[0].Timestamp != "2026-07-20T09:00:00Z" || lines[0].Fields["source"] != "macos-network-extension" {
		t.Fatalf("provider log metadata = %+v", lines[0])
	}
}
