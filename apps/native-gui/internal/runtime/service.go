package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

const (
	stepPass = "pass"
	stepFail = "fail"
)

// Client is the daemon API subset required by the native GUI runtime service.
type Client interface {
	Status(ctx context.Context) (runtimeapi.Status, error)
	Nodes(ctx context.Context) ([]runtimeapi.Node, error)
	Connect(ctx context.Context, nodeID string) (runtimeapi.Status, error)
	Disconnect(ctx context.Context) (runtimeapi.Status, error)
	Carriers(ctx context.Context) (map[string]runtimeapi.CarrierSnapshot, error)
	Build(ctx context.Context) (runtimeapi.BuildInfo, error)
	Plan(ctx context.Context, trafficClass string, payloadBytes int) (runtimeapi.RoutePlan, error)
}

// Service exposes product-level runtime operations for the Wails-bound app
// layer. It owns no UI state; every method returns a fresh daemon snapshot.
type Service struct {
	client         Client
	telemetryProbe TelemetryProbe
	now            func() time.Time
}

// NewService creates a native GUI runtime service around a daemon API client.
func NewService(client Client) (*Service, error) {
	return NewServiceWithTelemetryProbe(client, NewHTTPIPTelemetryProbeFromEnv())
}

// NewServiceWithTelemetryProbe creates a service with an explicit telemetry
// probe. Tests and fake runtime use this to avoid live network dependencies.
func NewServiceWithTelemetryProbe(client Client, telemetryProbe TelemetryProbe) (*Service, error) {
	if client == nil {
		return nil, fmt.Errorf("native runtime service requires a client")
	}
	if telemetryProbe == nil {
		return nil, fmt.Errorf("native runtime service requires a telemetry probe")
	}
	return &Service{
		client:         client,
		telemetryProbe: telemetryProbe,
		now:            func() time.Time { return time.Now().UTC() },
	}, nil
}

// NewRuntimeAPIService creates a service backed by a local whitetransportd API.
func NewRuntimeAPIService(baseURL string) (*Service, error) {
	client, err := runtimeapi.NewClient(baseURL)
	if err != nil {
		return nil, err
	}
	return NewService(client)
}

// Status returns the current user-facing desktop status.
func (s *Service) Status(ctx context.Context) (DesktopStatus, error) {
	status, err := s.client.Status(ctx)
	if err != nil {
		return DesktopStatus{}, err
	}
	nodes, err := s.client.Nodes(ctx)
	if err != nil {
		return DesktopStatus{}, err
	}
	carriers, err := s.client.Carriers(ctx)
	if err != nil {
		return DesktopStatus{}, err
	}
	build, err := s.client.Build(ctx)
	if err != nil {
		return DesktopStatus{}, err
	}
	return s.toDesktopStatus(status, nodes, carriers, build), nil
}

// ListServers returns selectable servers for the main native GUI.
func (s *Service) ListServers(ctx context.Context) ([]ServerSummary, error) {
	nodes, err := s.client.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	servers := make([]ServerSummary, 0, len(nodes))
	for _, node := range nodes {
		servers = append(servers, ServerSummary{
			ID:           node.NodeID,
			Label:        displayLabel(node),
			Country:      node.Country,
			Region:       node.Region,
			Available:    node.Available,
			Capabilities: append([]string(nil), node.Capabilities...),
			LastSeenAt:   formatOptionalTime(node.LastSeenAt),
		})
	}
	sortServerSummaries(servers)
	return servers, nil
}

// Connect asks the runtime to connect to the selected server. Empty serverID
// keeps daemon-side auto-selection.
func (s *Service) Connect(ctx context.Context, serverID string) (DesktopStatus, error) {
	status, err := s.client.Connect(ctx, strings.TrimSpace(serverID))
	if err != nil {
		return DesktopStatus{}, err
	}
	return s.statusFromRuntimeStatus(ctx, status)
}

// Disconnect asks the runtime to disconnect the active session.
func (s *Service) Disconnect(ctx context.Context) (DesktopStatus, error) {
	status, err := s.client.Disconnect(ctx)
	if err != nil {
		return DesktopStatus{}, err
	}
	return s.statusFromRuntimeStatus(ctx, status)
}

// Telemetry returns post-connect measurements through the daemon SOCKS path.
// Probe failures are returned as product telemetry errors instead of breaking
// the main connection state.
func (s *Service) Telemetry(ctx context.Context) (DesktopTelemetry, error) {
	status, err := s.client.Status(ctx)
	if err != nil {
		return DesktopTelemetry{}, err
	}
	out := DesktopTelemetry{ActiveNodeID: status.ActiveNodeID}
	if productState(status.State) != StateConnected || strings.TrimSpace(status.SocksListen) == "" {
		return out, nil
	}
	probe, err := s.telemetryProbe.Probe(ctx, status.SocksListen)
	out.MeasuredAt = s.now().Format(time.RFC3339Nano)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.ExternalIP = probe.ExternalIP
	out.LatencyMS = intPtr(probe.LatencyMS)
	return out, nil
}

// RunDiagnostics executes cheap API-level probes for the native debug view.
func (s *Service) RunDiagnostics(ctx context.Context) DiagnosticResult {
	result := DiagnosticResult{Passed: true}

	status, _ := s.addStatusStep(ctx, &result)
	// Preserve an explicit error status when the first probe fails; a zero
	// value would erase the transport/system-VPN boundary in diagnostics.
	result.Status = status
	s.addPlanStep(ctx, &result, "control", 4096)
	s.addPlanStep(ctx, &result, "egress", 8192)
	return result
}

func intPtr(value int) *int {
	return &value
}

func (s *Service) statusFromRuntimeStatus(ctx context.Context, status runtimeapi.Status) (DesktopStatus, error) {
	nodes, err := s.client.Nodes(ctx)
	if err != nil {
		return DesktopStatus{}, err
	}
	carriers, err := s.client.Carriers(ctx)
	if err != nil {
		return DesktopStatus{}, err
	}
	build, err := s.client.Build(ctx)
	if err != nil {
		return DesktopStatus{}, err
	}
	return s.toDesktopStatus(status, nodes, carriers, build), nil
}

func (s *Service) toDesktopStatus(status runtimeapi.Status, nodes []runtimeapi.Node, carriers map[string]runtimeapi.CarrierSnapshot, build runtimeapi.BuildInfo) DesktopStatus {
	transportState := productState(status.State)
	systemVPNState := SystemVPNUnsupported
	productState := overallProductState(transportState, systemVPNState)
	availableNodes := 0
	for _, node := range nodes {
		if node.Available {
			availableNodes++
		}
	}
	healthyCarriers := 0
	unhealthyCarriers := 0
	for _, carrier := range carriers {
		if carrier.Healthy {
			healthyCarriers++
		} else {
			unhealthyCarriers++
		}
	}
	return DesktopStatus{
		State:                productState,
		Connected:            productState == StateConnected,
		TransportState:       transportState,
		SystemVPNState:       systemVPNState,
		Message:              statusMessage(status, productState, availableNodes, healthyCarriers),
		RuntimeState:         status.State,
		ActiveNodeID:         status.ActiveNodeID,
		SessionID:            status.SessionID,
		SocksListen:          status.SocksListen,
		DiscoveredServers:    len(nodes),
		AvailableServers:     availableNodes,
		HealthyCarriers:      healthyCarriers,
		UnhealthyCarriers:    unhealthyCarriers,
		ReconnectAttempts:    status.ReconnectAttempts,
		LastRuntimeError:     status.LastError,
		RuntimeBuild:         BuildSummary(build),
		DiagnosticsAvailable: true,
		SystemVPNProfile:     marshalSystemVPNProfile(status.SystemVPNProfile),
	}
}

func marshalSystemVPNProfile(profile *runtimeapi.SystemVPNProfile) json.RawMessage {
	if profile == nil {
		return nil
	}
	payload, err := json.Marshal(profile)
	if err != nil {
		return nil
	}
	return payload
}

// overallProductState combines daemon transport readiness with the host-owned
// VPN lifecycle. A SOCKS transport alone is degraded until a native host
// reports that the system VPN is connected.
func overallProductState(transportState ProductState, systemVPNState SystemVPNState) ProductState {
	if transportState != StateConnected {
		return transportState
	}
	switch systemVPNState {
	case SystemVPNConnected:
		return StateConnected
	case SystemVPNConnecting:
		return StateConnecting
	case SystemVPNDisconnecting:
		return StateDisconnecting
	case SystemVPNError:
		return StateError
	default:
		return StateDegraded
	}
}

func sortServerSummaries(servers []ServerSummary) {
	sort.SliceStable(servers, func(leftIndex int, rightIndex int) bool {
		left := servers[leftIndex]
		right := servers[rightIndex]
		if left.Available != right.Available {
			return left.Available
		}
		if left.LastSeenAt != right.LastSeenAt {
			return left.LastSeenAt > right.LastSeenAt
		}
		leftLabel := strings.ToLower(strings.TrimSpace(left.Label))
		rightLabel := strings.ToLower(strings.TrimSpace(right.Label))
		if leftLabel != rightLabel {
			return leftLabel < rightLabel
		}
		return left.ID < right.ID
	})
}

func (s *Service) addStatusStep(ctx context.Context, result *DiagnosticResult) (DesktopStatus, bool) {
	startedAt := s.now()
	status, err := s.Status(ctx)
	step := DiagnosticStep{
		Name:      "runtime-status",
		StartedAt: startedAt.Format(time.RFC3339Nano),
		EndedAt:   s.now().Format(time.RFC3339Nano),
	}
	if err != nil {
		step.Status = stepFail
		step.Error = err.Error()
		result.Passed = false
		result.Steps = append(result.Steps, step)
		return diagnosticErrorStatus(err), false
	}
	step.Status = stepPass
	step.Detail = fmt.Sprintf("%s, servers=%d, carriers=%d", status.RuntimeState, status.AvailableServers, status.HealthyCarriers)
	result.Steps = append(result.Steps, step)
	return status, true
}

// diagnosticErrorStatus preserves the transport/system-VPN boundary when the
// daemon status probe itself fails.
func diagnosticErrorStatus(err error) DesktopStatus {
	message := err.Error()
	return DesktopStatus{
		State:                StateError,
		TransportState:       StateError,
		SystemVPNState:       SystemVPNUnsupported,
		Message:              message,
		LastRuntimeError:     message,
		DiagnosticsAvailable: false,
	}
}

func (s *Service) addPlanStep(ctx context.Context, result *DiagnosticResult, trafficClass string, payloadBytes int) {
	startedAt := s.now()
	plan, err := s.client.Plan(ctx, trafficClass, payloadBytes)
	step := DiagnosticStep{
		Name:      "plan-" + trafficClass,
		StartedAt: startedAt.Format(time.RFC3339Nano),
		EndedAt:   s.now().Format(time.RFC3339Nano),
	}
	if err != nil {
		step.Status = stepFail
		step.Error = err.Error()
		result.Passed = false
		result.Steps = append(result.Steps, step)
		return
	}
	step.Status = stepPass
	step.Detail = fmt.Sprintf("%s via %s", plan.Strategy, plan.Primary.ID)
	result.Steps = append(result.Steps, step)
}

func productState(runtimeState string) ProductState {
	switch strings.ToLower(strings.TrimSpace(runtimeState)) {
	case "", "disconnected", "stopped":
		return StateOff
	case "connecting", "reconnecting", "starting", "discovering":
		return StateConnecting
	case "connected", "running":
		return StateConnected
	case "degraded", "unhealthy":
		return StateDegraded
	case "disconnecting", "stopping":
		return StateDisconnecting
	case "unsupported":
		return StateUnsupported
	default:
		return StateError
	}
}

func statusMessage(status runtimeapi.Status, state ProductState, availableNodes int, healthyCarriers int) string {
	if strings.TrimSpace(status.LastError) != "" {
		return status.LastError
	}
	switch state {
	case StateConnected:
		if status.ActiveNodeID != "" {
			return fmt.Sprintf("Connected via %s", status.ActiveNodeID)
		}
		return "Connected"
	case StateConnecting:
		return "Connecting"
	case StateDegraded:
		return "Runtime degraded"
	case StateDisconnecting:
		return "Disconnecting"
	case StateUnsupported:
		return "Runtime unsupported"
	case StateError:
		return fmt.Sprintf("Unexpected runtime state: %s", status.State)
	default:
		if availableNodes == 0 {
			return "No available servers"
		}
		if healthyCarriers == 0 {
			return "Runtime ready, carriers warming up"
		}
		return "Ready"
	}
}

func displayLabel(node runtimeapi.Node) string {
	if strings.TrimSpace(node.Label) != "" {
		return node.Label
	}
	return node.NodeID
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
