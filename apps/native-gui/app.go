package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

type runtimeService interface {
	Status(ctx context.Context) (guiruntime.DesktopStatus, error)
	ListServers(ctx context.Context) ([]guiruntime.ServerSummary, error)
	Connect(ctx context.Context, serverID string) (guiruntime.DesktopStatus, error)
	Disconnect(ctx context.Context) (guiruntime.DesktopStatus, error)
	Telemetry(ctx context.Context) (guiruntime.DesktopTelemetry, error)
	RunDiagnostics(ctx context.Context) guiruntime.DiagnosticResult
}

// App is the Wails-bound product API for the native desktop client.
type App struct {
	ctx                      context.Context
	service                  runtimeService
	logs                     *guiruntime.LogSink
	resources                guiruntime.RuntimeResourceSummary
	supervisor               daemonSupervisor
	credentialMu             sync.Mutex
	credentialRefreshMessage string
	roomAuthMu               sync.Mutex
	roomAuth                 roomAuthStatus
	roomRuntime              localSessionRuntime
	roomRuntimeActive        bool
	systemVPN                systemVPNHost
	lifecycleMu              sync.Mutex
	activeSystemVPNProfile   systemVPNProfileIdentity
}

type nativeCapabilities struct {
	Host                 string `json:"host"`
	Transport            bool   `json:"transport"`
	Endpoints            bool   `json:"endpoints"`
	Logs                 bool   `json:"logs"`
	SplitRouting         bool   `json:"splitRouting"`
	ProxyRouting         bool   `json:"proxyRouting"`
	SystemVPN            bool   `json:"systemVpn"`
	RequestVPNPermission bool   `json:"requestVpnPermission"`
	StartSystemVPN       bool   `json:"startSystemVpn"`
	StopSystemVPN        bool   `json:"stopSystemVpn"`
	SmokeTest            bool   `json:"smokeTest"`
}

type daemonSupervisor interface {
	Stop(ctx context.Context) error
	Restart(ctx context.Context) (guiruntime.DaemonSupervisorStatus, error)
	Status() guiruntime.DaemonSupervisorStatus
}

// NewApp creates the Wails-bound application API.
func NewApp(service runtimeService, logSinks ...*guiruntime.LogSink) (*App, error) {
	if service == nil {
		return nil, fmt.Errorf("native app requires a runtime service")
	}
	logSink := guiruntime.NewDisabledLogSink()
	if len(logSinks) > 0 {
		if logSinks[0] == nil {
			return nil, fmt.Errorf("native app requires a non-nil log sink")
		}
		logSink = logSinks[0]
	}
	resources := guiruntime.ResolveRuntimeResources(guiruntime.CurrentRuntimeMode())
	return newApp(service, resources, logSink, newDisabledSupervisor(resources))
}

func newApp(service runtimeService, resources guiruntime.RuntimeResourceSummary, logSink *guiruntime.LogSink, supervisor daemonSupervisor) (*App, error) {
	if service == nil {
		return nil, fmt.Errorf("native app requires a runtime service")
	}
	if logSink == nil {
		return nil, fmt.Errorf("native app requires a non-nil log sink")
	}
	if supervisor == nil {
		supervisor = newDisabledSupervisor(resources)
	}
	systemVPN := newSystemVPNHost()
	if resources.Mode == guiruntime.ModeFake {
		systemVPN = newFakeSystemVPNHost()
	}
	return &App{
		service: service, logs: logSink, resources: resources, supervisor: supervisor,
		roomRuntime: newEmbeddedRoomRuntime(), systemVPN: systemVPN,
	}, nil
}

// NewDefaultApp creates the default runtime-backed app. Set
// WT_NATIVE_GUI_FAKE_RUNTIME=1 for deterministic local GUI smoke.
func NewDefaultApp() (*App, error) {
	logSink, err := guiruntime.NewDefaultLogSink()
	if err != nil {
		return nil, err
	}
	logSink.Info("native gui startup", map[string]string{"log_path": logSink.Path()})
	mode := guiruntime.CurrentRuntimeMode()
	resources := guiruntime.ResolveRuntimeResources(mode)
	resources, _, configErr := guiruntime.EnsureManagedDaemonConfig(resources, logSink)
	logRuntimeResources(logSink, resources)
	if configErr != nil {
		logSink.Error("managed daemon config generation failed", configErr, nil)
		resources.SupervisorState = "error"
		return newApp(newStartupErrorService(configErr), resources, logSink, newDisabledSupervisor(resources))
	}
	service, supervisor, resources := newDefaultRuntimeService(logSink, resources)
	if service == nil {
		return nil, fmt.Errorf("native app did not create a runtime service")
	}
	return newApp(service, resources, logSink, supervisor)
}

func newDefaultRuntimeService(logSink *guiruntime.LogSink, resources guiruntime.RuntimeResourceSummary) (runtimeService, daemonSupervisor, guiruntime.RuntimeResourceSummary) {
	// Upgrade to managed mode if the daemon binary was found anywhere in resource
	// candidates, even if CurrentRuntimeMode() returned local-api (e.g. when
	// os.Executable() returns a temp path inside a macOS .app bundle).
	// Fake mode is an explicit test contract. Do not let nearby development
	// artifacts silently turn its deterministic in-memory service into a real
	// managed daemon.
	if shouldAutoUpgradeToManaged(resources) {
		resources.Mode = guiruntime.ModeManaged
		logSink.Info("auto-upgrading to managed mode", map[string]string{"reason": "daemon binary + token store found"})
	}
	if resources.Mode == guiruntime.ModeManaged {
		// Ensure daemon config is generated from token-store before creating supervisor.
		var configErr error
		resources, _, configErr = guiruntime.EnsureManagedDaemonConfig(resources, logSink)
		if configErr != nil {
			logSink.Error("managed daemon config generation failed", configErr, nil)
			resources.SupervisorState = "error"
			return newStartupErrorService(configErr), newDisabledSupervisor(resources), resources
		}
		supervisor, err := guiruntime.NewDaemonSupervisor(resources, logSink)
		if err != nil {
			logSink.Error("managed daemon supervisor initialization failed", err, nil)
			resources.SupervisorState = "error"
			return newStartupErrorService(err), newDisabledSupervisor(resources), resources
		}
		status, err := supervisor.Start(context.Background())
		resources.SupervisorState = status.State
		if err != nil {
			logSink.Error("managed daemon startup failed", err, nil)
			return newStartupErrorService(err), supervisor, resources
		}
		service, err := guiruntime.NewRuntimeAPIService(guiruntime.RuntimeAPIURL())
		if err != nil {
			logSink.Error("runtime api service initialization failed", err, nil)
			return newStartupErrorService(err), supervisor, resources
		}
		return service, supervisor, resources
	}

	service, err := newDefaultRuntimeAPIService(logSink)
	if err != nil {
		logSink.Error("runtime service initialization failed", err, nil)
		return newStartupErrorService(err), newDisabledSupervisor(resources), resources
	}
	return service, newDisabledSupervisor(resources), resources
}

func shouldAutoUpgradeToManaged(resources guiruntime.RuntimeResourceSummary) bool {
	if resources.Mode == guiruntime.ModeManaged || resources.Mode == guiruntime.ModeFake {
		return false
	}
	// Explicit local API mode attaches to an already-running daemon. Nearby repo
	// artifacts must not silently replace that externally owned process.
	if os.Getenv("WT_NATIVE_GUI_LOCAL_API") == "1" {
		return false
	}
	if _, ok := resources.FirstFoundCandidate(guiruntime.ResourceDaemonBinary); !ok {
		return false
	}
	_, ok := resources.FirstFoundCandidate(guiruntime.ResourceTokenStore)
	return ok
}

func newDefaultRuntimeAPIService(logSink *guiruntime.LogSink) (runtimeService, error) {
	if guiruntime.CurrentRuntimeMode() == guiruntime.ModeFake {
		logSink.Info("runtime service selected", map[string]string{"mode": "fake"})
		return guiruntime.NewFakeService()
	}
	apiURL := guiruntime.RuntimeAPIURL()
	logSink.Info("runtime service selected", map[string]string{"mode": "local-api", "api_url": apiURL})
	return guiruntime.NewRuntimeAPIService(apiURL)
}

// startup stores the Wails context for future runtime event emission.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.logs.Info("wails startup", nil)
}

// shutdown stops an owned daemon process when the Wails app exits.
func (a *App) shutdown(ctx context.Context) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.systemVPN != nil && a.systemVPN.Supported() {
		if _, err := a.systemVPN.Stop(ctx); err != nil {
			a.logs.Error("system VPN shutdown failed", err, nil)
		}
		a.activeSystemVPNProfile = systemVPNProfileIdentity{}
	}
	if a.supervisor == nil {
		if a.roomRuntime != nil && a.roomRuntimeActive {
			a.roomRuntime.StopTransport()
			a.roomRuntimeActive = false
		}
		return
	}
	if err := a.supervisor.Stop(ctx); err != nil {
		a.logs.Error("managed daemon shutdown failed", err, nil)
	}
	if a.roomRuntime != nil && a.roomRuntimeActive {
		a.roomRuntime.StopTransport()
		a.roomRuntimeActive = false
	}
}

// GetStatus returns the current product-level runtime status.
func (a *App) GetStatus() (guiruntime.DesktopStatus, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	status, err := a.service.Status(a.context())
	if err != nil {
		a.logs.Error("status refresh failed", err, nil)
		if a.systemVPN != nil && a.systemVPN.Supported() && !a.activeSystemVPNProfile.Ready {
			_, _ = a.systemVPN.Stop(a.context())
			a.activeSystemVPNProfile = systemVPNProfileIdentity{}
		}
		status.Connected = false
		return status, err
	}
	if a.systemVPN == nil || !a.systemVPN.Supported() {
		return status, nil
	}
	observation, err := a.systemVPN.Status(a.context())
	if err != nil {
		a.logs.Error("system VPN status refresh failed", err, nil)
		return applySystemVPNObservation(status, systemVPNObservation{State: guiruntime.SystemVPNError, ProviderState: guiruntime.SystemVPNError}), err
	}
	if a.isSystemVPNRecoveryGap(status) {
		// Keep the user's route while the same daemon searches for a new path.
		// The provider still enforces the existing profile's validity deadline.
		status.SystemVPNState = observation.State
		status.State = guiruntime.StateConnecting
		status.Connected = false
		return status, nil
	}
	if observation.State != guiruntime.SystemVPNConnected && a.activeSystemVPNProfile.Ready {
		identity, identityErr := decodeSystemVPNProfileIdentity(status.SystemVPNProfile)
		if identityErr == nil && validateSystemVPNServerSelection(status, identity, "") == nil &&
			canReplaceActiveSystemVPNProfile(a.activeSystemVPNProfile, identity) {
			return a.replaceActiveSystemVPNProfile(status, identity)
		}
	}
	if observation.State == guiruntime.SystemVPNConnected {
		identity, identityErr := decodeSystemVPNProfileIdentity(status.SystemVPNProfile)
		selectionErr := validateSystemVPNServerSelection(status, identity, "")
		if identityErr != nil || selectionErr != nil || !observationMatchesProfile(observation, identity) {
			if identityErr == nil && selectionErr == nil && canReplaceActiveSystemVPNProfile(a.activeSystemVPNProfile, identity) && observationMatchesProfile(observation, a.activeSystemVPNProfile) {
				replacement, replacementErr := a.replaceActiveSystemVPNProfile(status, identity)
				if replacementErr == nil {
					return replacement, nil
				}
				a.logs.Error("system VPN profile replacement failed", replacementErr, nil)
				return replacement, replacementErr
			}
			_, _ = a.systemVPN.Stop(a.context())
			a.activeSystemVPNProfile = systemVPNProfileIdentity{}
			mismatchErr := fmt.Errorf("system VPN observation does not match the active daemon profile")
			a.logs.Error("system VPN identity mismatch", mismatchErr, nil)
			return applySystemVPNObservation(status, systemVPNObservation{State: guiruntime.SystemVPNError, ProviderState: guiruntime.SystemVPNError}), mismatchErr
		}
	}
	return applySystemVPNObservation(status, observation), nil
}

func (a *App) isSystemVPNRecoveryGap(status guiruntime.DesktopStatus) bool {
	if !a.activeSystemVPNProfile.Ready || status.TransportState == guiruntime.StateConnected {
		return false
	}
	if len(status.SystemVPNProfile) == 0 {
		return true
	}
	var profile systemVPNProfileIdentity
	if json.Unmarshal(status.SystemVPNProfile, &profile) != nil {
		return false
	}
	return !profile.Ready && (profile.DaemonInstanceID == "" || profile.DaemonInstanceID == a.activeSystemVPNProfile.DaemonInstanceID)
}

func canReplaceActiveSystemVPNProfile(active, next systemVPNProfileIdentity) bool {
	if !active.Ready || !next.Ready || active.DaemonInstanceID != next.DaemonInstanceID {
		return false
	}
	if active.SessionID != next.SessionID || active.SelectedNodeID != next.SelectedNodeID {
		return next.Revision > active.Revision && next.ProfileHash != active.ProfileHash
	}
	changedRouteSnapshot := next.Revision > active.Revision && next.ProfileHash != active.ProfileHash
	renewedLease := next.Revision == active.Revision && next.ProfileHash == active.ProfileHash &&
		!next.ProfileValidUntil.Equal(active.ProfileValidUntil)
	return changedRouteSnapshot || renewedLease
}

// replaceActiveSystemVPNProfile applies a changed route snapshot using the
// only proven native transition: provider-confirmed stop followed by start.
// Network Extension preferences are not treated as live-reconfigurable.
func (a *App) replaceActiveSystemVPNProfile(status guiruntime.DesktopStatus, identity systemVPNProfileIdentity) (guiruntime.DesktopStatus, error) {
	stopped, stopErr := a.systemVPN.Stop(a.context())
	if stopErr == nil && stopped.State != guiruntime.SystemVPNDisconnected {
		stopErr = fmt.Errorf("system VPN replacement stop returned state %q", stopped.State)
	}
	if stopErr != nil {
		return applySystemVPNObservation(status, systemVPNObservation{State: guiruntime.SystemVPNError, ProviderState: guiruntime.SystemVPNError}), stopErr
	}

	started, startErr := a.systemVPN.Start(a.context(), status.SystemVPNProfile)
	if startErr == nil && !observationMatchesProfile(started, identity) {
		startErr = fmt.Errorf("replacement system VPN did not confirm the refreshed daemon profile")
	}
	if startErr != nil {
		_, cleanupErr := a.systemVPN.Stop(a.context())
		// Preserve the last authorized identity so the next status poll can
		// retry provider activation without canceling the recovered transport.
		return applySystemVPNObservation(status, systemVPNObservation{State: guiruntime.SystemVPNError, ProviderState: guiruntime.SystemVPNError}), errors.Join(startErr, cleanupErr)
	}
	a.activeSystemVPNProfile = identity
	a.logs.Info("system VPN profile replaced", map[string]string{"session_id": identity.SessionID})
	return applySystemVPNObservation(status, started), nil
}

// GetCapabilities returns host-backed capabilities rather than assuming every
// Wails build can activate a system VPN.
func (a *App) GetCapabilities() nativeCapabilities {
	supported := a.systemVPN != nil && a.systemVPN.Supported()
	splitRouting := supported && (a.resources.Mode == guiruntime.ModeManaged || a.resources.Mode == guiruntime.ModeFake)
	return nativeCapabilities{
		Host:                 "wails",
		Transport:            true,
		Endpoints:            true,
		Logs:                 true,
		SplitRouting:         splitRouting,
		ProxyRouting:         true,
		SystemVPN:            supported,
		RequestVPNPermission: supported,
		StartSystemVPN:       supported,
		StopSystemVPN:        supported,
		SmokeTest:            true,
	}
}

// ListServers returns selectable servers for the main GUI.
func (a *App) ListServers() ([]guiruntime.ServerSummary, error) {
	servers, err := a.service.ListServers(a.context())
	if err != nil {
		a.logs.Error("server list refresh failed", err, nil)
	}
	return servers, err
}

// Connect turns on the runtime path through the selected server.
func (a *App) Connect(serverID string) (guiruntime.DesktopStatus, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.logs.Info("connect requested", map[string]string{"server_id": serverID})
	status, err := a.service.Connect(a.context(), serverID)
	if err != nil {
		a.logs.Error("connect failed", err, map[string]string{"server_id": serverID})
		return status, err
	}
	if a.systemVPN != nil && a.systemVPN.Supported() {
		status, err = a.startSystemVPNForStatus(status, serverID)
		if err != nil {
			a.logs.Error("connect system VPN failed", err, map[string]string{"server_id": serverID})
			return status, err
		}
	}
	a.logs.Info("connect completed", map[string]string{"server_id": serverID, "active_node_id": status.ActiveNodeID, "state": string(status.State)})
	return status, nil
}

// Disconnect turns off the active runtime path.
func (a *App) Disconnect() (guiruntime.DesktopStatus, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.logs.Info("disconnect requested", nil)
	var stopObservation systemVPNObservation
	var stopErr error
	if a.systemVPN != nil && a.systemVPN.Supported() {
		stopObservation, stopErr = a.systemVPN.Stop(a.context())
		if stopErr == nil && stopObservation.State != guiruntime.SystemVPNDisconnected {
			stopErr = fmt.Errorf("system VPN stop returned state %q", stopObservation.State)
		}
	}
	status, transportErr := a.service.Disconnect(a.context())
	if a.roomRuntime != nil && a.roomRuntimeActive {
		a.roomRuntime.StopTransport()
		a.roomRuntimeActive = false
	}
	a.activeSystemVPNProfile = systemVPNProfileIdentity{}
	if a.systemVPN != nil && a.systemVPN.Supported() {
		status = applySystemVPNObservation(status, stopObservation)
	}
	if err := errors.Join(stopErr, transportErr); err != nil {
		a.logs.Error("disconnect failed", err, nil)
		return status, err
	}
	a.logs.Info("disconnect completed", map[string]string{"state": string(status.State)})
	return status, nil
}

// RequestSystemVPNPermission asks the native host to load/create its VPN
// manager without changing the daemon session.
func (a *App) RequestSystemVPNPermission() (guiruntime.SystemVPNState, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.systemVPN == nil || !a.systemVPN.Supported() {
		return guiruntime.SystemVPNUnsupported, fmt.Errorf("system VPN is unsupported on this host")
	}
	observation, err := a.systemVPN.Permission(a.context())
	return observation.State, err
}

// StartSystemVPN starts the host from the daemon's current exact profile. The
// primary Connect method already performs this transaction automatically.
func (a *App) StartSystemVPN() (guiruntime.DesktopStatus, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.systemVPN == nil || !a.systemVPN.Supported() {
		return guiruntime.DesktopStatus{SystemVPNState: guiruntime.SystemVPNUnsupported}, fmt.Errorf("system VPN is unsupported on this host")
	}
	status, err := a.service.Status(a.context())
	if err != nil {
		return status, err
	}
	return a.startSystemVPNForStatus(status, "")
}

// StopSystemVPN stops only the OS route; Disconnect remains the normal product
// operation that also tears down the daemon session.
func (a *App) StopSystemVPN() (guiruntime.SystemVPNState, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.systemVPN == nil || !a.systemVPN.Supported() {
		return guiruntime.SystemVPNUnsupported, fmt.Errorf("system VPN is unsupported on this host")
	}
	observation, err := a.systemVPN.Stop(a.context())
	if err == nil {
		a.activeSystemVPNProfile = systemVPNProfileIdentity{}
	}
	return observation.State, err
}

func (a *App) startSystemVPNForStatus(status guiruntime.DesktopStatus, requestedServerID string) (guiruntime.DesktopStatus, error) {
	identity, err := decodeSystemVPNProfileIdentity(status.SystemVPNProfile)
	if err != nil {
		_, rollbackErr := a.service.Disconnect(a.context())
		return applySystemVPNObservation(status, systemVPNObservation{State: guiruntime.SystemVPNError, ProviderState: guiruntime.SystemVPNError}), errors.Join(err, rollbackErr)
	}
	if err := validateSystemVPNServerSelection(status, identity, requestedServerID); err != nil {
		_, rollbackErr := a.service.Disconnect(a.context())
		return applySystemVPNObservation(status, systemVPNObservation{State: guiruntime.SystemVPNError, ProviderState: guiruntime.SystemVPNError}), errors.Join(err, rollbackErr)
	}
	observation, startErr := a.systemVPN.Start(a.context(), status.SystemVPNProfile)
	if startErr == nil && !observationMatchesProfile(observation, identity) {
		startErr = fmt.Errorf("system VPN did not reach connected state for the requested daemon profile")
	}
	if startErr != nil {
		if observation.State == guiruntime.SystemVPNPermissionRequired && strings.Contains(startErr.Error(), "permission_required") {
			return applySystemVPNObservation(status, observation), nil
		}
		_, stopErr := a.systemVPN.Stop(a.context())
		_, rollbackErr := a.service.Disconnect(a.context())
		a.activeSystemVPNProfile = systemVPNProfileIdentity{}
		return applySystemVPNObservation(status, systemVPNObservation{State: guiruntime.SystemVPNError, ProviderState: guiruntime.SystemVPNError}), errors.Join(startErr, stopErr, rollbackErr)
	}
	a.activeSystemVPNProfile = identity
	return applySystemVPNObservation(status, observation), nil
}

func validateSystemVPNServerSelection(status guiruntime.DesktopStatus, identity systemVPNProfileIdentity, requestedServerID string) error {
	if status.ActiveNodeID == "" {
		return fmt.Errorf("selected server is absent from connected transport status")
	}
	if requestedServerID != "" && status.ActiveNodeID != requestedServerID {
		return fmt.Errorf("selected server mismatch: requested %q, transport activated %q", requestedServerID, status.ActiveNodeID)
	}
	if identity.SelectedNodeID != status.ActiveNodeID {
		return fmt.Errorf("selected server mismatch: transport activated %q, profile confirms %q", status.ActiveNodeID, identity.SelectedNodeID)
	}
	return nil
}

func applySystemVPNObservation(status guiruntime.DesktopStatus, observation systemVPNObservation) guiruntime.DesktopStatus {
	status.SystemVPNState = observation.State
	status.Connected = false
	if status.TransportState != guiruntime.StateConnected {
		if observation.State == guiruntime.SystemVPNConnected {
			status.State = guiruntime.StateError
		} else if status.TransportState != "" {
			status.State = status.TransportState
		}
		return status
	}
	switch observation.State {
	case guiruntime.SystemVPNConnected:
		status.State = guiruntime.StateConnected
		status.Connected = true
	case guiruntime.SystemVPNConnecting:
		status.State = guiruntime.StateConnecting
	case guiruntime.SystemVPNDisconnecting:
		status.State = guiruntime.StateDisconnecting
	case guiruntime.SystemVPNError:
		status.State = guiruntime.StateError
	case guiruntime.SystemVPNDegraded:
		status.State = guiruntime.StateDegraded
	default:
		status.State = guiruntime.StateDegraded
	}
	return status
}

// GetTelemetry returns post-connect product telemetry such as external IP.
func (a *App) GetTelemetry() (guiruntime.DesktopTelemetry, error) {
	telemetry, err := a.service.Telemetry(a.context())
	if err != nil {
		a.logs.Error("telemetry refresh failed", err, nil)
		return telemetry, err
	}
	fields := map[string]string{"active_node_id": telemetry.ActiveNodeID}
	if telemetry.LatencyMS != nil {
		fields["latency_ms"] = fmt.Sprintf("%d", *telemetry.LatencyMS)
	}
	if telemetry.Error != "" {
		fields["error"] = telemetry.Error
	}
	a.logs.Info("telemetry refreshed", fields)
	return telemetry, nil
}

// RunDiagnostics returns structured diagnostics for the debug panel.
func (a *App) RunDiagnostics() guiruntime.DiagnosticResult {
	result := a.service.RunDiagnostics(a.context())
	a.logs.Info("diagnostics completed", map[string]string{"passed": fmt.Sprintf("%t", result.Passed)})
	return result
}

// GetRuntimeResources returns debug-safe startup resource diagnostics.
func (a *App) GetRuntimeResources() guiruntime.RuntimeResourceSummary {
	return a.resources
}

// GetRuntimeSupervisorStatus returns the current managed daemon status.
func (a *App) GetRuntimeSupervisorStatus() guiruntime.DaemonSupervisorStatus {
	return a.supervisor.Status()
}

// GetLogFilePath returns the persistent native GUI log file location.
func (a *App) GetLogFilePath() string {
	return a.logs.Path()
}

// ListClientCredentials returns locally-stored platform credentials (masked).
// Credentials never leave the device.
func (a *App) ListClientCredentials() ([]guiruntime.ClientCredentialSummary, error) {
	creds, err := guiruntime.LoadClientCredentials()
	if err != nil {
		a.logs.Error("list client credentials failed", err, nil)
		return nil, err
	}
	return guiruntime.SummarizeClientCredentials(creds), nil
}

// AddClientCredential stores a platform credential locally.
// The credential is saved to ~/.whitetransport/client-tokens.json and
// never transmitted to the admin panel or daemon.
func (a *App) AddClientCredential(platform, label, token, cookie, extra string) ([]guiruntime.ClientCredentialSummary, error) {
	cred := guiruntime.ClientCredential{
		Platform: platform,
		Label:    label,
		Token:    token,
		Cookie:   cookie,
		Extra:    extra,
	}
	creds, err := guiruntime.AddClientCredential(cred)
	if err != nil {
		a.logs.Error("add client credential failed", err, map[string]string{"platform": platform})
		return nil, err
	}
	a.logs.Info("client credential added", map[string]string{"platform": platform, "id": cred.ID})
	return guiruntime.SummarizeClientCredentials(creds), nil
}

// RemoveClientCredential deletes a locally-stored credential by ID.
func (a *App) RemoveClientCredential(id string) ([]guiruntime.ClientCredentialSummary, error) {
	creds, err := guiruntime.RemoveClientCredential(id)
	if err != nil {
		a.logs.Error("remove client credential failed", err, map[string]string{"id": id})
		return nil, err
	}
	a.logs.Info("client credential removed", map[string]string{"id": id})
	return guiruntime.SummarizeClientCredentials(creds), nil
}

// GetSupportedPlatforms returns the list of platforms that support local credentials.
func (a *App) GetSupportedPlatforms() []string {
	return guiruntime.SupportedPlatforms()
}

// ImportBrowserExport parses a browser export file (cookies + localStorage JSON)
// and atomically replaces older local credentials for every imported platform.
// Returns the updated credential list after import.
func (a *App) ImportBrowserExport(fileContent string) ([]guiruntime.ClientCredentialSummary, error) {
	a.logs.Info("browser export import requested", nil)

	// Write to temp file for parser
	tmpFile, err := os.CreateTemp("", "wt-import-*.json")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(fileContent); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	creds, err := guiruntime.ParseBrowserExport(tmpPath)
	if err != nil {
		a.logs.Error("browser export import failed", err, nil)
		return nil, err
	}

	allCreds, err := guiruntime.ReplaceClientCredentialsForPlatforms(creds)
	if err != nil {
		a.logs.Error("browser export credential replacement failed", err, nil)
		return nil, err
	}
	a.refreshManagedConfigAfterBrowserImport()

	a.logs.Info("browser export import completed", map[string]string{"imported": fmt.Sprintf("%d", len(creds))})
	return guiruntime.SummarizeClientCredentials(allCreds), nil
}

// GetClientCredentialRefreshMessage returns the non-sensitive outcome of the
// latest browser credential import and managed-daemon refresh attempt.
func (a *App) GetClientCredentialRefreshMessage() string {
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	return a.credentialRefreshMessage
}

// refreshManagedConfigAfterBrowserImport refreshes only the daemon config
// generated by this client. It never overwrites an operator-owned config and
// avoids interrupting an active tunnel just to apply refreshed browser tokens.
func (a *App) refreshManagedConfigAfterBrowserImport() {
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()

	if a.resources.Mode != guiruntime.ModeManaged {
		a.credentialRefreshMessage = "Учётные данные сохранены локально. Перезапустите WhiteTransport, чтобы применить их к внешнему демону."
		return
	}
	resources, generated, err := guiruntime.EnsureManagedDaemonConfig(a.resources, a.logs)
	if err != nil {
		a.credentialRefreshMessage = "Учётные данные сохранены, но конфигурацию демона обновить не удалось. Перезапустите WhiteTransport и проверьте диагностику."
		a.logs.Error("managed daemon config refresh after browser import failed", err, nil)
		return
	}
	a.resources = resources
	if generated.Path == "" {
		a.credentialRefreshMessage = "Учётные данные сохранены, но используется явная конфигурация демона. Она не изменена; добавьте учётные данные в неё вручную и перезапустите клиент."
		return
	}

	supervisorStatus := a.supervisor.Status()
	if supervisorStatus.State != "running" {
		a.credentialRefreshMessage = "Учётные данные и локальная конфигурация обновлены. Они применятся при следующем запуске демона."
		return
	}
	runtimeStatus, err := a.service.Status(a.context())
	if err != nil || (runtimeStatus.State != guiruntime.StateOff && runtimeStatus.State != guiruntime.StateError) {
		a.credentialRefreshMessage = "Учётные данные и локальная конфигурация обновлены. Отключитесь от туннеля и перезапустите клиент, чтобы применить их без обрыва сессии."
		return
	}
	status, err := a.supervisor.Restart(context.Background())
	a.resources.SupervisorState = status.State
	if err != nil {
		a.credentialRefreshMessage = "Учётные данные и локальная конфигурация обновлены, но автоматический перезапуск демона не удался. Перезапустите WhiteTransport."
		a.logs.Error("managed daemon restart after browser import failed", err, nil)
		return
	}
	a.credentialRefreshMessage = "Учётные данные импортированы: локальная конфигурация обновлена, демон перезапущен."
	a.logs.Info("managed daemon restarted after browser credential import", nil)
}

// HasClientRoomCredentials returns true if local video-tunnel credentials exist,
// enabling the role-reversal flow (client creates room, node joins as guest).
func (a *App) HasClientRoomCredentials() (bool, error) {
	creds, err := guiruntime.LoadClientCredentials()
	if err != nil {
		return false, err
	}
	return guiruntime.HasClientRoomCredentials(creds), nil
}

type routingSettings struct {
	Mode      string `json:"mode"`
	LANAccess bool   `json:"lan_access"`
}

type splitRoutingSettings struct {
	Mode         string   `json:"mode"`
	LANAccess    bool     `json:"lan_access"`
	Destinations []string `json:"destinations,omitempty"`
}

func (a *App) GetRoutingSettings() (routingSettings, error) {
	dir, err := guiruntime.DefaultRuntimeConfigDir()
	if err != nil {
		return routingSettings{Mode: "all_proxy"}, nil
	}
	rc := guiruntime.LoadRoutingSettings(dir)
	return routingSettings{Mode: rc.Mode, LANAccess: rc.LANAccess}, nil
}

func (a *App) SetRoutingSettings(mode string, lanAccess bool) (routingSettings, error) {
	normalizedMode, err := guiruntime.NormalizeRoutingMode(mode)
	if err != nil {
		return routingSettings{}, err
	}
	dir, err := guiruntime.DefaultRuntimeConfigDir()
	if err != nil {
		return routingSettings{}, err
	}
	if err := guiruntime.SaveRoutingSettings(dir, normalizedMode, lanAccess); err != nil {
		a.logs.Error("save routing settings failed", err, nil)
		return routingSettings{}, err
	}
	a.logs.Info("routing settings updated", map[string]string{"mode": normalizedMode, "lan_access": fmt.Sprintf("%t", lanAccess)})
	return routingSettings{Mode: normalizedMode, LANAccess: lanAccess}, nil
}

// GetSplitRoutingSettings returns the destination-based system VPN route
// policy that will be embedded in the next daemon-confirmed profile.
func (a *App) GetSplitRoutingSettings() (splitRoutingSettings, error) {
	dir, err := guiruntime.DefaultRuntimeConfigDir()
	if err != nil {
		return splitRoutingSettings{}, err
	}
	settings, err := guiruntime.LoadSystemVPNSplitSettings(dir)
	if err != nil {
		return splitRoutingSettings{}, err
	}
	return splitRoutingSettings{
		Mode:         string(settings.Mode),
		LANAccess:    settings.LANAccess,
		Destinations: append([]string(nil), settings.Destinations...),
	}, nil
}

// SetSplitRoutingSettings persists an exact IP-route policy only while the
// transport is disconnected. The daemon must publish it in one atomic profile
// before the host is allowed to start Network Extension.
func (a *App) SetSplitRoutingSettings(mode string, lanAccess bool, destinations []string) (splitRoutingSettings, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	status, err := a.service.Status(a.context())
	if err != nil {
		return splitRoutingSettings{}, fmt.Errorf("check transport before split routing update: %w", err)
	}
	if transportSessionActive(status) {
		return splitRoutingSettings{}, fmt.Errorf("disconnect the VPN before changing split routing")
	}
	dir, err := guiruntime.DefaultRuntimeConfigDir()
	if err != nil {
		return splitRoutingSettings{}, err
	}
	settings := guiruntime.SystemVPNSplitSettings{
		Mode:         guiruntime.SystemVPNSplitMode(mode),
		LANAccess:    lanAccess,
		Destinations: append([]string(nil), destinations...),
	}
	if err := guiruntime.SaveSystemVPNSplitSettings(dir, settings); err != nil {
		return splitRoutingSettings{}, err
	}
	if err := a.refreshManagedRuntimeAfterSplitRoutingChange(); err != nil {
		a.logs.Error("managed daemon split routing refresh failed", err, nil)
		return splitRoutingSettings{}, err
	}
	a.logs.Info("system VPN split routing updated", map[string]string{
		"mode":              mode,
		"lan_access":        fmt.Sprintf("%t", lanAccess),
		"destination_count": fmt.Sprintf("%d", len(destinations)),
	})
	return a.GetSplitRoutingSettings()
}

func (a *App) refreshManagedRuntimeAfterSplitRoutingChange() error {
	if a.resources.Mode != guiruntime.ModeManaged {
		return nil
	}
	resources, generated, err := guiruntime.EnsureManagedDaemonConfig(a.resources, a.logs)
	if err != nil {
		return fmt.Errorf("regenerate managed daemon config: %w", err)
	}
	if generated.Path == "" {
		return fmt.Errorf("managed daemon uses an explicit config; GUI split routing cannot replace it")
	}
	a.resources = resources
	if a.supervisor.Status().State != "running" {
		return nil
	}
	status, err := a.supervisor.Restart(a.context())
	a.resources.SupervisorState = status.State
	if err != nil {
		return fmt.Errorf("restart managed daemon after split routing update: %w", err)
	}
	return nil
}

func transportSessionActive(status guiruntime.DesktopStatus) bool {
	if status.ActiveNodeID != "" || status.SessionID != "" {
		return true
	}
	switch status.TransportState {
	case guiruntime.StateConnecting, guiruntime.StateConnected, guiruntime.StateDegraded, guiruntime.StateDisconnecting:
		return true
	default:
		return false
	}
}

type disabledSupervisor struct {
	status guiruntime.DaemonSupervisorStatus
}

func newDisabledSupervisor(resources guiruntime.RuntimeResourceSummary) disabledSupervisor {
	state := "disabled"
	message := "Managed daemon supervisor disabled"
	if resources.Mode == guiruntime.ModeFake {
		state = "disabled_fake_runtime"
		message = "Fake runtime mode does not start a daemon"
	} else if resources.Mode == guiruntime.ModeLocalAPI {
		state = "disabled_local_api"
		message = "Local API mode expects an already running daemon"
	}
	return disabledSupervisor{status: guiruntime.DaemonSupervisorStatus{
		State:   state,
		Message: message,
		APIURL:  resources.RuntimeAPIURL,
	}}
}

func (s disabledSupervisor) Stop(context.Context) error {
	return nil
}

func (s disabledSupervisor) Restart(context.Context) (guiruntime.DaemonSupervisorStatus, error) {
	return s.status, nil
}

func (s disabledSupervisor) Status() guiruntime.DaemonSupervisorStatus {
	return s.status
}

type startupErrorService struct {
	err error
}

func newStartupErrorService(err error) startupErrorService {
	return startupErrorService{err: err}
}

func (s startupErrorService) Status(context.Context) (guiruntime.DesktopStatus, error) {
	message := s.err.Error()
	return guiruntime.DesktopStatus{
		State:                guiruntime.StateError,
		Connected:            false,
		TransportState:       guiruntime.StateError,
		SystemVPNState:       guiruntime.SystemVPNUnsupported,
		Message:              message,
		RuntimeState:         "startup-error",
		LastRuntimeError:     message,
		DiagnosticsAvailable: true,
	}, nil
}

func (s startupErrorService) ListServers(context.Context) ([]guiruntime.ServerSummary, error) {
	return []guiruntime.ServerSummary{}, nil
}

func (s startupErrorService) Connect(context.Context, string) (guiruntime.DesktopStatus, error) {
	status, _ := s.Status(context.Background())
	return status, s.err
}

func (s startupErrorService) Disconnect(context.Context) (guiruntime.DesktopStatus, error) {
	status, _ := s.Status(context.Background())
	return status, nil
}

func (s startupErrorService) Telemetry(context.Context) (guiruntime.DesktopTelemetry, error) {
	return guiruntime.DesktopTelemetry{Error: s.err.Error()}, nil
}

func (s startupErrorService) RunDiagnostics(context.Context) guiruntime.DiagnosticResult {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	status, _ := s.Status(context.Background())
	return guiruntime.DiagnosticResult{
		Passed: false,
		Status: status,
		Steps: []guiruntime.DiagnosticStep{{
			Name:      "startup",
			Status:    "fail",
			Error:     s.err.Error(),
			StartedAt: now,
			EndedAt:   now,
		}},
	}
}

// ReadLogs returns the newest persistent native GUI log entries first.
func (a *App) ReadLogs(limit int) ([]guiruntime.LogLine, error) {
	lines, err := a.logs.ReadLines(limit)
	if err != nil {
		return nil, err
	}
	a.lifecycleMu.Lock()
	activeProvider := a.systemVPN != nil && a.systemVPN.Supported() && a.activeSystemVPNProfile.DaemonInstanceID != ""
	if activeProvider {
		providerLines, providerErr := a.systemVPN.Logs(a.context())
		if providerErr != nil {
			a.lifecycleMu.Unlock()
			return lines, fmt.Errorf("read active system VPN provider logs: %w", providerErr)
		}
		lines = append(lines, providerLines...)
	}
	a.lifecycleMu.Unlock()
	sort.SliceStable(lines, func(left, right int) bool { return lines[left].Timestamp > lines[right].Timestamp })
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, nil
}

// CopyLogs returns all log lines as formatted text for clipboard copy.
func (a *App) CopyLogs() (string, error) {
	lines, err := a.ReadLogs(500)
	if err != nil {
		return "", err
	}
	var buf []byte
	for _, line := range lines {
		ts := line.Timestamp
		if len(ts) > 15 {
			ts = ts[len(ts)-15:]
		}
		fields := ""
		if line.Fields != nil {
			for k, v := range line.Fields {
				fields += " " + k + "=" + v
			}
		}
		buf = append(buf, fmt.Sprintf("%s %s %s%s\n", ts, strings.ToUpper(line.Level), line.Message, fields)...)
	}
	return string(buf), nil
}

func (a *App) context() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func logRuntimeResources(logSink *guiruntime.LogSink, resources guiruntime.RuntimeResourceSummary) {
	logSink.Info("runtime resources resolved", map[string]string{
		"mode":               resources.Mode,
		"runtime_api_url":    resources.RuntimeAPIURL,
		"supervisor_state":   resources.SupervisorState,
		"repo_root":          resources.RepoRoot,
		"missing_required":   strings.Join(resources.MissingRequired, ","),
		"diagnostics_notice": resources.DiagnosticsNotice,
	})
	for _, candidate := range resources.Candidates {
		logSink.Info("runtime resource candidate", map[string]string{
			"kind":       candidate.Kind,
			"source":     candidate.Source,
			"target":     candidate.Target,
			"status":     candidate.Status,
			"required":   fmt.Sprintf("%t", candidate.Required),
			"exists":     fmt.Sprintf("%t", candidate.Exists),
			"executable": fmt.Sprintf("%t", candidate.Executable),
			"error":      candidate.Error,
		})
	}
}
