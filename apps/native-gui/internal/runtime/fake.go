package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

// NewFakeService creates a deterministic in-memory service for native GUI
// development and smoke tests that must not depend on live carriers.
func NewFakeService() (*Service, error) {
	return NewServiceWithTelemetryProbe(newFakeClient(), fixedTelemetryProbe{
		externalIP: "203.0.113.88",
		latencyMS:  58,
	})
}

type fixedTelemetryProbe struct {
	externalIP string
	latencyMS  int
}

func (p fixedTelemetryProbe) Probe(context.Context, string) (TelemetryProbeResult, error) {
	return TelemetryProbeResult{ExternalIP: p.externalIP, LatencyMS: p.latencyMS}, nil
}

type fakeClient struct {
	mu           sync.Mutex
	status       runtimeapi.Status
	nodes        []runtimeapi.Node
	carriers     map[string]runtimeapi.CarrierSnapshot
	build        runtimeapi.BuildInfo
	connectedSeq int
}

func newFakeClient() *fakeClient {
	now := time.Now().UTC()
	return &fakeClient{
		status: runtimeapi.Status{
			Role:            "client",
			State:           "disconnected",
			SocksListen:     "127.0.0.1:8809",
			DiscoveredNodes: 3,
			AvailableNodes:  2,
		},
		nodes: []runtimeapi.Node{
			{NodeID: "local-fast", Label: "Local fast", Country: "DE", Region: "Frankfurt", Available: true, Capabilities: []string{"egress", "control"}, LastSeenAt: now.Add(-7 * time.Second)},
			{NodeID: "example-node-primary", Label: "Example Node Primary", Country: "RU", Region: "Moscow", Available: true, Capabilities: []string{"egress", "control"}, LastSeenAt: now.Add(-18 * time.Second)},
			{NodeID: "warmup", Label: "Warmup node", Country: "NL", Region: "Amsterdam", Available: false, Capabilities: []string{"control"}, LastSeenAt: now.Add(-2 * time.Minute)},
		},
		carriers: map[string]runtimeapi.CarrierSnapshot{
			"file.mailbox": {CarrierID: "file.mailbox", Healthy: true, Reliability: 1},
			"wbstream.vp8": {CarrierID: "wbstream.vp8", Healthy: true, Reliability: 0.98},
			"vk.messages":  {CarrierID: "vk.messages", Healthy: true, Reliability: 0.96},
		},
		build: runtimeapi.BuildInfo{Version: "native-fast", Commit: "fake", Date: now.Format(time.RFC3339)},
	}
}

func (f *fakeClient) Status(context.Context) (runtimeapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneFakeStatus(f.status), nil
}

func (f *fakeClient) Nodes(context.Context) ([]runtimeapi.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtimeapi.Node(nil), f.nodes...), nil
}

func (f *fakeClient) Connect(_ context.Context, nodeID string) (runtimeapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target := nodeID
	if target == "" {
		for _, node := range f.nodes {
			if node.Available {
				target = node.NodeID
				break
			}
		}
	}
	if target == "" {
		f.status.LastError = "no available servers"
		return f.status, fmt.Errorf("no available servers")
	}
	f.connectedSeq++
	now := time.Now().UTC()
	f.status.State = "connected"
	f.status.ActiveNodeID = target
	f.status.SessionID = fmt.Sprintf("fake-session-%d", f.connectedSeq)
	f.status.LastError = ""
	profile := runtimeapi.SystemVPNProfile{
		SchemaRevision:   runtimeapi.SystemVPNProfileSchemaRevision,
		DaemonInstanceID: "fake-daemon-instance",
		ProfileRevision:  uint64(f.connectedSeq),
		SessionID:        f.status.SessionID,
		SelectedNodeID:   target,
		Ready:            true,
		IssuedAt:         now,
		ExpiresAt:        now.Add(5 * time.Minute),
	}
	if err := profile.SetHash(); err != nil {
		f.status.LastError = err.Error()
		return cloneFakeStatus(f.status), fmt.Errorf("build synthetic system VPN profile: %w", err)
	}
	f.status.SystemVPNProfile = &profile
	f.status.SystemVPNProfileReadiness = &runtimeapi.SystemVPNReadiness{Ready: true, Provenance: "synthetic-gui-test"}
	return cloneFakeStatus(f.status), nil
}

func (f *fakeClient) Disconnect(context.Context) (runtimeapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.State = "disconnected"
	f.status.ActiveNodeID = ""
	f.status.SessionID = ""
	f.status.LastError = ""
	f.status.SystemVPNProfile = nil
	f.status.SystemVPNProfileReadiness = nil
	return cloneFakeStatus(f.status), nil
}

func cloneFakeStatus(status runtimeapi.Status) runtimeapi.Status {
	clone := status
	clone.EgressEndpoints = append([]runtimeapi.Endpoint(nil), status.EgressEndpoints...)
	if status.SystemVPNProfile != nil {
		clone.SystemVPNProfile = status.SystemVPNProfile.Clone()
	}
	if status.SystemVPNProfileReadiness != nil {
		readiness := *status.SystemVPNProfileReadiness
		clone.SystemVPNProfileReadiness = &readiness
	}
	return clone
}

func (f *fakeClient) Carriers(context.Context) (map[string]runtimeapi.CarrierSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]runtimeapi.CarrierSnapshot, len(f.carriers))
	for key, value := range f.carriers {
		out[key] = value
	}
	return out, nil
}

func (f *fakeClient) Build(context.Context) (runtimeapi.BuildInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.build, nil
}

func (f *fakeClient) Plan(_ context.Context, trafficClass string, _ int) (runtimeapi.RoutePlan, error) {
	return runtimeapi.RoutePlan{
		TrafficClass:      trafficClass,
		Strategy:          "single",
		Primary:           runtimeapi.DescriptorView{ID: "file.mailbox", Provider: "file", Mode: "mailbox", Healthy: true},
		MirrorCount:       1,
		HedgeTimeoutMs:    0,
		MaxInFlightChunks: 4,
	}, nil
}
