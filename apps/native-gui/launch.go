package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

var launchTestNodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// launchOptions configures the explicit, non-interactive GUI smoke path.
// It is intentionally opt-in so a normal desktop launch never changes state.
type launchOptions struct {
	testConnectNodeID string
	testResultPath    string
	testSudoFile      string
	testExit          bool
}

var (
	launchTestDiscoveryTimeout      = 30 * time.Second
	launchTestDiscoveryPollInterval = 250 * time.Millisecond
)

// launchConnectResult is a secret-safe artifact for package smoke runners.
type launchConnectResult struct {
	Mode                      string                    `json:"mode"`
	ProofBoundary             string                    `json:"proofBoundary,omitempty"`
	TargetNodeID              string                    `json:"targetNodeId"`
	ActiveNodeID              string                    `json:"activeNodeId,omitempty"`
	TransportState            guiruntime.ProductState   `json:"transportState,omitempty"`
	SystemVPNState            guiruntime.SystemVPNState `json:"systemVpnState,omitempty"`
	ExternalIP                string                    `json:"externalIp,omitempty"`
	SystemRouteProbeRequested bool                      `json:"systemRouteProbeRequested,omitempty"`
	SystemRouteProbePassed    bool                      `json:"systemRouteProbePassed,omitempty"`
	SystemRouteProbeTarget    string                    `json:"systemRouteProbeTarget,omitempty"`
	SystemRouteProbeResponse  string                    `json:"systemRouteProbeResponse,omitempty"`
	SystemRouteIP             string                    `json:"systemRouteIp,omitempty"`
	SystemRouteProbeMarker    string                    `json:"systemRouteProbeMarker,omitempty"`
	SystemRouteProbeError     string                    `json:"systemRouteProbeError,omitempty"`
	BypassRouteProbeRequested bool                      `json:"bypassRouteProbeRequested,omitempty"`
	BypassRouteProbePassed    bool                      `json:"bypassRouteProbePassed,omitempty"`
	BypassRouteProbeTarget    string                    `json:"bypassRouteProbeTarget,omitempty"`
	BypassRouteProbeResponse  string                    `json:"bypassRouteProbeResponse,omitempty"`
	BypassRouteIP             string                    `json:"bypassRouteIp,omitempty"`
	BypassRouteProbeMarker    string                    `json:"bypassRouteProbeMarker,omitempty"`
	BypassRouteProbeError     string                    `json:"bypassRouteProbeError,omitempty"`
	Passed                    bool                      `json:"passed"`
	Error                     string                    `json:"error,omitempty"`
	CompletedAt               string                    `json:"completedAt"`
	LogPath                   string                    `json:"logPath,omitempty"`
	ResultPath                string                    `json:"resultPath,omitempty"`
}

// Provider-backed system routes can take several seconds to deliver the first
// response while ICE/data-channel state settles. Keep the acceptance probe
// bounded, but do not turn transient carrier latency into a false failure.
const systemRouteProbeTimeout = 30 * time.Second

// probeDirectRoute performs a direct HTTP(S) request with proxy use disabled.
// The target returned to callers is limited to scheme and host so credentials
// or sensitive path/query values never enter the result artifact or logs.
// Either an expected IP, a body marker, or both may define the observer
// contract. Body markers let off-host probes correlate a unique request
// without requiring the observer to pretend that a response body is an IP.
func probeDirectRoute(ctx context.Context, probeName, endpoint, expectedIP, expectedBody string) (target, responseSummary, externalIP string, err error) {
	expectedIP = strings.TrimSpace(expectedIP)
	expectedBody = strings.TrimSpace(expectedBody)
	if expectedIP == "" && expectedBody == "" {
		return "", "", "", fmt.Errorf("%s route probe requires an expected IP or body marker", probeName)
	}
	if expectedIP != "" && net.ParseIP(expectedIP) == nil {
		return "", "", "", fmt.Errorf("%s route probe expected IP must be valid", probeName)
	}
	parsed, parseErr := url.Parse(strings.TrimSpace(endpoint))
	if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", "", fmt.Errorf("%s route probe URL must be an absolute http or https URL", probeName)
	}
	target = parsed.Scheme + "://" + parsed.Host
	probeCtx, cancel := context.WithTimeout(ctx, systemRouteProbeTimeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil}
	if probeName == "system" {
		// Test-only off-host fixtures can pin the TCP destination while keeping
		// the URL hostname for HTTP Host and TLS SNI semantics.
		if connectAddress := strings.TrimSpace(os.Getenv("WT_GUI_TEST_SYSTEM_ROUTE_CONNECT_ADDRESS")); connectAddress != "" {
			dialer := &net.Dialer{}
			transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", connectAddress)
			}
		}
		if os.Getenv("WT_GUI_TEST_SYSTEM_ROUTE_INSECURE_TLS") == "1" {
			// This opt-in exists only for numeric-IP test fixtures whose TLS
			// certificate cannot contain the pinned address.
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only acceptance override
		}
	}
	defer transport.CloseIdleConnections()
	request, requestErr := http.NewRequestWithContext(probeCtx, http.MethodGet, parsed.String(), nil)
	if requestErr != nil {
		return target, "", "", fmt.Errorf("create %s route probe request: %w", probeName, requestErr)
	}
	if probeName == "system" {
		// Test-only off-host fixtures may pin a numeric address while retaining
		// the observer hostname required for virtual-host routing.
		if hostOverride := strings.TrimSpace(os.Getenv("WT_GUI_TEST_SYSTEM_ROUTE_HOST")); hostOverride != "" {
			request.Host = hostOverride
		}
	}
	response, requestErr := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(request)
	if requestErr != nil {
		return target, "", "", fmt.Errorf("%s route probe request: %w", probeName, requestErr)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 128))
	responseSummary = fmt.Sprintf("http-%d", response.StatusCode)
	if candidate := strings.TrimSpace(string(body)); net.ParseIP(candidate) != nil {
		externalIP = candidate
		responseSummary = "ip"
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return target, responseSummary, externalIP, fmt.Errorf("%s route probe endpoint returned HTTP %d", probeName, response.StatusCode)
	}
	bodyText := strings.TrimSpace(string(body))
	if expectedBody != "" {
		if !strings.Contains(bodyText, expectedBody) {
			return target, responseSummary, externalIP, fmt.Errorf("%s route probe body marker mismatch", probeName)
		}
		responseSummary = "body-marker"
		if externalIP != "" {
			responseSummary = "ip+body-marker"
		}
	}
	if expectedIP != "" && (externalIP == "" || externalIP != expectedIP) {
		return target, responseSummary, externalIP, fmt.Errorf("%s route probe IP mismatch", probeName)
	}
	return target, responseSummary, externalIP, nil
}

// parseLaunchOptions accepts only the automation flags owned by this GUI and
// leaves ordinary Wails/macOS launch arguments untouched.
func parseLaunchOptions(args []string) (launchOptions, error) {
	options := launchOptions{}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		value := func(name string) (string, bool, error) {
			if argument == name {
				if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
					return "", true, fmt.Errorf("%s requires a value", name)
				}
				index++
				return args[index], true, nil
			}
			if strings.HasPrefix(argument, name+"=") {
				return strings.TrimPrefix(argument, name+"="), true, nil
			}
			return "", false, nil
		}
		if value, matched, err := value("--wt-test-connect"); matched {
			if err != nil {
				return launchOptions{}, err
			}
			options.testConnectNodeID = strings.TrimSpace(value)
			continue
		}
		if value, matched, err := value("--wt-test-result"); matched {
			if err != nil {
				return launchOptions{}, err
			}
			options.testResultPath = strings.TrimSpace(value)
			continue
		}
		if value, matched, err := value("--wt-test-sudo-file"); matched {
			if err != nil {
				return launchOptions{}, err
			}
			options.testSudoFile = strings.TrimSpace(value)
			continue
		}
		if argument == "--wt-test-exit" {
			options.testExit = true
		}
	}
	if options.testConnectNodeID == "" {
		if options.testResultPath != "" || options.testExit {
			return launchOptions{}, fmt.Errorf("--wt-test-result and --wt-test-exit require --wt-test-connect")
		}
		return options, nil
	}
	if options.testResultPath == "" {
		return launchOptions{}, fmt.Errorf("--wt-test-connect requires --wt-test-result")
	}
	if options.testSudoFile != "" && !filepath.IsAbs(options.testSudoFile) {
		return launchOptions{}, fmt.Errorf("--wt-test-sudo-file must be an absolute path")
	}
	if !launchTestNodeIDPattern.MatchString(options.testConnectNodeID) {
		return launchOptions{}, fmt.Errorf("--wt-test-connect must be a node identifier, not a URI or credential")
	}
	if !filepath.IsAbs(options.testResultPath) {
		return launchOptions{}, fmt.Errorf("--wt-test-result must be an absolute path")
	}
	return options, nil
}

func (options launchOptions) enabled() bool {
	return options.testConnectNodeID != ""
}

// loadTestSudoCredential consumes the one-shot credential file used only by
// off-host GUI acceptance. The password never enters argv or an acceptance
// artifact; the file is removed immediately after it is read.
func loadTestSudoCredential(options launchOptions) error {
	if options.testSudoFile == "" {
		return nil
	}
	data, err := os.ReadFile(options.testSudoFile)
	if err != nil {
		return fmt.Errorf("read test sudo credential: %w", err)
	}
	if err := os.Remove(options.testSudoFile); err != nil {
		return fmt.Errorf("remove test sudo credential: %w", err)
	}
	password := strings.TrimRight(string(data), "\r\n")
	if password == "" {
		return fmt.Errorf("test sudo credential is empty")
	}
	if err := os.Setenv("MAC_SUDO", password); err != nil {
		return fmt.Errorf("set test sudo credential: %w", err)
	}
	return nil
}

// runLaunchConnectTest connects using the same bound API as the rendered GUI,
// records telemetry, and writes one stable JSON artifact for external runners.
func (a *App) runLaunchConnectTest(options launchOptions) launchConnectResult {
	result := launchConnectResult{
		Mode:         "gui-launch-connect",
		TargetNodeID: options.testConnectNodeID,
		CompletedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		LogPath:      a.logs.Path(),
		ResultPath:   options.testResultPath,
	}
	connectedByTest := false
	finish := func(current launchConnectResult) launchConnectResult {
		if connectedByTest {
			if _, err := a.Disconnect(); err != nil && current.Error == "" {
				current.Error = fmt.Sprintf("disconnect test session: %v", err)
				current.Passed = false
			}
		}
		if a.resources.Mode == guiruntime.ModeManaged && a.supervisor != nil {
			if err := a.supervisor.Stop(a.context()); err != nil && current.Error == "" {
				current.Error = fmt.Sprintf("stop owned managed daemon: %v", err)
				current.Passed = false
			}
		}
		return a.finishLaunchConnectTest(current, options.testResultPath)
	}
	a.logs.Info("launch connect test started", map[string]string{"server_id": options.testConnectNodeID, "result_path": options.testResultPath})
	if err := a.waitForLaunchTestNode(options.testConnectNodeID); err != nil {
		result.Error = err.Error()
		return finish(result)
	}
	status, err := a.Connect(options.testConnectNodeID)
	if err != nil {
		result.Error = err.Error()
		return finish(result)
	}
	connectedByTest = true
	result.ActiveNodeID = status.ActiveNodeID
	result.TransportState = status.TransportState
	result.SystemVPNState = status.SystemVPNState
	// Telemetry is measured through the selected SOCKS endpoint. A host status
	// flag alone is not evidence that the OS default route used the VPN.
	result.ProofBoundary = "transport-only"
	if a.resources.Mode == guiruntime.ModeFake {
		// Fake mode proves renderer, Wails bindings, lifecycle ordering, artifact
		// writing, and log plumbing only. It never claims an OS route changed.
		result.ProofBoundary = "synthetic-system-vpn"
	}
	if status.TransportState != guiruntime.StateConnected || status.ActiveNodeID != options.testConnectNodeID {
		result.Error = fmt.Sprintf("transport state %q on %q, expected connected transport on %q", status.TransportState, status.ActiveNodeID, options.testConnectNodeID)
		return finish(result)
	}
	if status.SystemVPNState == guiruntime.SystemVPNConnected && !status.Connected {
		result.Error = "system VPN reports connected but product connection is not active"
		return finish(result)
	}
	systemRouteEndpoint := strings.TrimSpace(os.Getenv("WT_GUI_TEST_SYSTEM_ROUTE_URL"))
	skipTelemetry := os.Getenv("WT_GUI_TEST_SKIP_TELEMETRY") == "1" && systemRouteEndpoint != ""
	if !skipTelemetry {
		telemetry, err := a.GetTelemetry()
		if err != nil {
			result.Error = fmt.Sprintf("read telemetry: %v", err)
			return finish(result)
		}
		result.ExternalIP = telemetry.ExternalIP
		if telemetry.Error != "" {
			result.Error = telemetry.Error
			return finish(result)
		}
	}
	if delaySeconds, parseErr := strconv.Atoi(strings.TrimSpace(os.Getenv("WT_GUI_TEST_PRE_PROBE_HOLD_SECONDS"))); parseErr == nil && delaySeconds > 0 {
		if delaySeconds > 60 {
			delaySeconds = 60
		}
		timer := time.NewTimer(time.Duration(delaySeconds) * time.Second)
		select {
		case <-timer.C:
		case <-a.context().Done():
			if !timer.Stop() {
				<-timer.C
			}
			result.Error = "GUI test pre-probe hold canceled"
			return finish(result)
		}
	}
	if endpoint := systemRouteEndpoint; endpoint != "" {
		result.SystemRouteProbeRequested = true
		expectedIP := strings.TrimSpace(os.Getenv("WT_GUI_TEST_SYSTEM_ROUTE_EXPECTED_IP"))
		expectedBody := strings.TrimSpace(os.Getenv("WT_GUI_TEST_SYSTEM_ROUTE_EXPECTED_BODY"))
		if expectedIP == "" && expectedBody == "" {
			result.SystemRouteProbeError = "system route probe requires EXPECTED_IP or EXPECTED_BODY"
			result.Error = result.SystemRouteProbeError
			return finish(result)
		}
		if expectedIP != "" && net.ParseIP(expectedIP) == nil {
			result.SystemRouteProbeError = "system route probe expected IP must be valid"
			result.Error = result.SystemRouteProbeError
			return finish(result)
		}
		if status.SystemVPNState != guiruntime.SystemVPNConnected {
			result.SystemRouteProbeError = "system route probe requires an active system VPN"
			result.Error = result.SystemRouteProbeError
			return finish(result)
		}
		result.SystemRouteProbeMarker = expectedBody
		target, responseSummary, externalIP, probeErr := probeDirectRoute(a.context(), "system", endpoint, expectedIP, expectedBody)
		result.SystemRouteProbeTarget = target
		result.SystemRouteProbeResponse = responseSummary
		result.SystemRouteIP = externalIP
		if result.ExternalIP == "" {
			result.ExternalIP = externalIP
		}
		if probeErr != nil {
			result.SystemRouteProbeError = probeErr.Error()
			result.Error = result.SystemRouteProbeError
			return finish(result)
		}
		result.SystemRouteProbePassed = true
		result.ProofBoundary = "system-route"
	}
	if endpoint := strings.TrimSpace(os.Getenv("WT_GUI_TEST_BYPASS_ROUTE_URL")); endpoint != "" {
		result.BypassRouteProbeRequested = true
		if !result.SystemRouteProbePassed {
			result.BypassRouteProbeError = "bypass route probe requires a successful system route probe"
			result.Error = result.BypassRouteProbeError
			return finish(result)
		}
		expectedIP := strings.TrimSpace(os.Getenv("WT_GUI_TEST_BYPASS_ROUTE_EXPECTED_IP"))
		expectedBody := strings.TrimSpace(os.Getenv("WT_GUI_TEST_BYPASS_ROUTE_EXPECTED_BODY"))
		if expectedIP == "" && expectedBody == "" {
			result.BypassRouteProbeError = "bypass route probe requires EXPECTED_IP or EXPECTED_BODY"
			result.Error = result.BypassRouteProbeError
			return finish(result)
		}
		if expectedIP != "" && net.ParseIP(expectedIP) == nil {
			result.BypassRouteProbeError = "bypass route probe expected IP must be valid"
			result.Error = result.BypassRouteProbeError
			return finish(result)
		}
		if status.SystemVPNState != guiruntime.SystemVPNConnected {
			result.BypassRouteProbeError = "bypass route probe requires an active system VPN"
			result.Error = result.BypassRouteProbeError
			return finish(result)
		}
		result.BypassRouteProbeMarker = expectedBody
		target, responseSummary, externalIP, probeErr := probeDirectRoute(a.context(), "bypass", endpoint, expectedIP, expectedBody)
		result.BypassRouteProbeTarget = target
		result.BypassRouteProbeResponse = responseSummary
		result.BypassRouteIP = externalIP
		if probeErr != nil {
			result.BypassRouteProbeError = probeErr.Error()
			result.Error = result.BypassRouteProbeError
			return finish(result)
		}
		result.BypassRouteProbePassed = true
		result.ProofBoundary = "split-system-route"
	}
	if holdSeconds, parseErr := strconv.Atoi(strings.TrimSpace(os.Getenv("WT_GUI_TEST_HOLD_SECONDS"))); parseErr == nil && holdSeconds > 0 {
		hold := time.NewTimer(time.Duration(holdSeconds) * time.Second)
		select {
		case <-hold.C:
		case <-a.context().Done():
			if !hold.Stop() {
				<-hold.C
			}
			result.Error = fmt.Sprintf("system route test hold interrupted: %v", a.context().Err())
			return finish(result)
		}
	}
	result.Passed = true
	return finish(result)
}

// waitForLaunchTestNode gives the daemon's asynchronous discovery loop time to
// publish the requested endpoint before the test attempts to connect. A GUI
// test must exercise the same discovered-server path as the Home screen, not
// race the daemon immediately after startup and report a false failure.
func (a *App) waitForLaunchTestNode(nodeID string) error {
	deadline := time.Now().Add(launchTestDiscoveryTimeout)
	var lastErr error
	for {
		servers, err := a.service.ListServers(a.context())
		if err != nil {
			lastErr = err
		} else {
			for _, server := range servers {
				if server.ID == nodeID && server.Available {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("wait for server discovery: %w", lastErr)
			}
			return fmt.Errorf("wait for server discovery timed out for %q", nodeID)
		}
		timer := time.NewTimer(launchTestDiscoveryPollInterval)
		select {
		case <-timer.C:
		case <-a.context().Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("wait for server discovery: %w", a.context().Err())
		}
	}
}

// RunTestMode executes the bounded connect/telemetry/cleanup path used by the
// explicit launch test and persists a redacted result beside the GUI log.
func (a *App) RunTestMode(serverID string) (launchConnectResult, error) {
	target := strings.TrimSpace(serverID)
	if !launchTestNodeIDPattern.MatchString(target) {
		return launchConnectResult{}, fmt.Errorf("test mode requires a valid server identifier")
	}
	logPath := strings.TrimSpace(a.logs.Path())
	if logPath == "" {
		return launchConnectResult{}, fmt.Errorf("test mode requires a persistent GUI log path")
	}
	resultPath := filepath.Join(filepath.Dir(logPath), "whitetransport-test-result.json")
	return a.runLaunchConnectTest(launchOptions{testConnectNodeID: target, testResultPath: resultPath}), nil
}

func (a *App) finishLaunchConnectTest(result launchConnectResult, resultPath string) launchConnectResult {
	result.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result.Error = guiruntime.RedactText(result.Error)
	result.SystemRouteProbeError = guiruntime.RedactText(result.SystemRouteProbeError)
	if err := writeLaunchConnectResult(resultPath, result); err != nil {
		if result.Error == "" {
			result.Error = fmt.Sprintf("write result: %v", err)
		}
		result.Passed = false
		a.logs.Error("launch connect test result write failed", err, map[string]string{"result_path": resultPath})
		return result
	}
	fields := map[string]string{
		"server_id":        result.TargetNodeID,
		"active_node_id":   result.ActiveNodeID,
		"proof_boundary":   result.ProofBoundary,
		"transport_state":  string(result.TransportState),
		"system_vpn_state": string(result.SystemVPNState),
		"passed":           fmt.Sprintf("%t", result.Passed),
		"result_path":      resultPath,
	}
	if result.SystemRouteProbeRequested {
		fields["system_route_probe_requested"] = "true"
		fields["system_route_probe_passed"] = fmt.Sprintf("%t", result.SystemRouteProbePassed)
		fields["system_route_probe_target"] = result.SystemRouteProbeTarget
		fields["system_route_probe_response"] = result.SystemRouteProbeResponse
		if result.SystemRouteIP != "" {
			fields["system_route_ip"] = result.SystemRouteIP
		}
	}
	if result.BypassRouteProbeRequested {
		fields["bypass_route_probe_requested"] = "true"
		fields["bypass_route_probe_passed"] = fmt.Sprintf("%t", result.BypassRouteProbePassed)
		fields["bypass_route_probe_target"] = result.BypassRouteProbeTarget
		fields["bypass_route_probe_response"] = result.BypassRouteProbeResponse
		if result.BypassRouteIP != "" {
			fields["bypass_route_ip"] = result.BypassRouteIP
		}
	}
	if result.Error != "" {
		fields["error"] = result.Error
		a.logs.Error("launch connect test completed", fmt.Errorf("%s", result.Error), fields)
	} else {
		a.logs.Info("launch connect test completed", fields)
	}
	return result
}

func writeLaunchConnectResult(path string, result launchConnectResult) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("launch test result path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode launch result: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wt-launch-result-*")
	if err != nil {
		return fmt.Errorf("create result file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set result mode: %w", err)
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write result: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close result: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace result: %w", err)
	}
	return nil
}
