package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultSupervisorTimeout = 15 * time.Second

// DaemonSupervisorStatus describes the managed daemon process state.
type DaemonSupervisorStatus struct {
	State        string `json:"state"`
	Message      string `json:"message"`
	PID          int    `json:"pid,omitempty"`
	APIURL       string `json:"api_url,omitempty"`
	BinaryPath   string `json:"binary_path,omitempty"`
	ConfigPath   string `json:"config_path,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	LastHealthAt string `json:"last_health_at,omitempty"`
}

func newDaemonCommand(ctx context.Context, plan SupervisorPlan) *exec.Cmd {
	cmd := exec.CommandContext(ctx, plan.BinaryPath, "--config", plan.ConfigPath, "--serve")
	// Bundled sidecars may resolve related resources relative to the daemon.
	// Running beside the binary keeps a copied desktop bundle relocatable.
	cmd.Dir = filepath.Dir(plan.BinaryPath)
	return cmd
}

// SupervisorPlan is the concrete command plan for managed whitetransportd.
type SupervisorPlan struct {
	BinaryPath string
	ConfigPath string
	APIURL     string
}

// DaemonSupervisor owns an optional whitetransportd child process.
type DaemonSupervisor struct {
	plan       SupervisorPlan
	logs       *LogSink
	httpClient *http.Client
	timeout    time.Duration

	mu           sync.Mutex
	cmd          *exec.Cmd
	exitDone     chan error
	status       DaemonSupervisorStatus
	startedAt    time.Time
	lastHealth   time.Time
	shuttingDown bool
	ready        chan string
}

// NewSupervisorPlan creates a managed daemon command plan from diagnostics.
func NewSupervisorPlan(resources RuntimeResourceSummary) (SupervisorPlan, error) {
	binary, ok := resources.FirstFoundCandidate(ResourceDaemonBinary)
	if !ok {
		return SupervisorPlan{}, fmt.Errorf("managed daemon requires a found %s candidate", ResourceDaemonBinary)
	}
	config, ok := firstSupervisorCandidate(resources, ResourceDaemonConfig)
	if !ok {
		return SupervisorPlan{}, fmt.Errorf("managed daemon requires a found %s candidate", ResourceDaemonConfig)
	}
	return SupervisorPlan{
		BinaryPath: binary.Target,
		ConfigPath: config.Target,
		APIURL:     strings.TrimRight(resources.RuntimeAPIURL, "/"),
	}, nil
}

func firstSupervisorCandidate(resources RuntimeResourceSummary, kind string) (RuntimeResourceCandidate, bool) {
	for _, candidate := range resources.Candidates {
		if candidate.Kind == kind && candidateCanSatisfyRequired(candidate) {
			return candidate, true
		}
	}
	return RuntimeResourceCandidate{}, false
}

// NewDaemonSupervisor creates a daemon supervisor from resolved resources.
func NewDaemonSupervisor(resources RuntimeResourceSummary, logs *LogSink) (*DaemonSupervisor, error) {
	plan, err := NewSupervisorPlan(resources)
	if err != nil {
		return nil, err
	}
	if logs == nil {
		logs = NewDisabledLogSink()
	}
	timeout := defaultSupervisorTimeout
	if configured := parsePositiveDurationMillis(os.Getenv("WT_NATIVE_GUI_DAEMON_START_TIMEOUT_MS")); configured > 0 {
		timeout = configured
	}
	return &DaemonSupervisor{
		plan:       plan,
		logs:       logs,
		httpClient: &http.Client{Timeout: time.Second},
		timeout:    timeout,
		status: DaemonSupervisorStatus{
			State:      "stopped",
			Message:    "Managed daemon not started",
			APIURL:     plan.APIURL,
			BinaryPath: plan.BinaryPath,
			ConfigPath: plan.ConfigPath,
		},
	}, nil
}

// Start launches whitetransportd and waits for /v1/status.
func (s *DaemonSupervisor) Start(ctx context.Context) (DaemonSupervisorStatus, error) {
	s.mu.Lock()
	if s.cmd != nil && s.cmd.Process != nil {
		status := s.status
		s.mu.Unlock()
		return status, nil
	}
	s.shuttingDown = false
	s.setStatusLocked("starting", "Starting managed whitetransportd", 0)
	launchPlan, cleanup, err := dynamicLaunchPlan(s.plan)
	if err != nil {
		s.mu.Unlock()
		return s.Status(), err
	}
	defer cleanup()
	s.ready = make(chan string, 1)
	ready := s.ready
	s.plan.APIURL = ""
	s.logs.Info("managed daemon starting", map[string]string{
		"binary_path": s.plan.BinaryPath,
		"config_path": s.plan.ConfigPath,
		"api_url":     s.plan.APIURL,
	})
	cmd := newDaemonCommand(ctx, launchPlan)
	cmd.Env = daemonEnvironment(os.Environ(), launchdSSHAgentSocket())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.setStatusLocked("error", err.Error(), 0)
		s.mu.Unlock()
		return s.status, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.setStatusLocked("error", err.Error(), 0)
		s.mu.Unlock()
		return s.status, err
	}
	if err := cmd.Start(); err != nil {
		s.setStatusLocked("error", err.Error(), 0)
		s.mu.Unlock()
		return s.status, err
	}
	s.cmd = cmd
	exitDone := make(chan error, 1)
	s.exitDone = exitDone
	s.startedAt = time.Now().UTC()
	s.setStatusLocked("starting", "Waiting for daemon API", cmd.Process.Pid)
	s.mu.Unlock()

	go s.pipeLog("whitetransportd", stdout, ready)
	go s.pipeLog("whitetransportd:err", stderr)
	go s.waitForExit(cmd, exitDone)

	waitCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	select {
	case address := <-ready:
		s.mu.Lock()
		s.plan.APIURL = address
		s.status.APIURL = address
		s.mu.Unlock()
	case <-exitDone:
		return s.Status(), fmt.Errorf("managed daemon exited before reporting its API address")
	case <-waitCtx.Done():
		_ = s.Stop(context.Background())
		return s.Status(), fmt.Errorf("managed daemon did not report its API address: %w", waitCtx.Err())
	}
	if err := s.waitForHealth(waitCtx, exitDone); err != nil {
		_ = s.Stop(context.Background())
		return s.Status(), err
	}

	s.mu.Lock()
	s.setStatusLocked("running", "Managed daemon running", cmd.Process.Pid)
	status := s.status
	s.mu.Unlock()
	s.logs.Info("managed daemon running", map[string]string{"pid": fmt.Sprintf("%d", cmd.Process.Pid), "api_url": s.plan.APIURL})
	return status, nil
}

// dynamicLaunchPlan preserves the operator config and lets the owned process
// bind both listeners atomically through the operating system.
func dynamicLaunchPlan(plan SupervisorPlan) (SupervisorPlan, func(), error) {
	raw, err := os.ReadFile(plan.ConfigPath)
	if err != nil {
		return plan, func() {}, err
	}
	var config map[string]json.RawMessage
	if err = json.Unmarshal(raw, &config); err != nil {
		return plan, func() {}, err
	}
	if config == nil {
		return plan, func() {}, fmt.Errorf("managed daemon config must be an object")
	}
	config["listen_api"] = json.RawMessage(`"127.0.0.1:0"`)
	config["socks_listen"] = json.RawMessage(`"127.0.0.1:0"`)
	raw, err = json.Marshal(config)
	if err != nil {
		return plan, func() {}, err
	}
	dir, err := DefaultRuntimeConfigDir()
	if err != nil {
		return plan, func() {}, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return plan, func() {}, err
	}
	file, err := os.CreateTemp(dir, "daemon-launch-*.json")
	if err != nil {
		return plan, func() {}, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	_, err = file.Write(raw)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return plan, func() {}, err
	}
	plan.ConfigPath = file.Name()
	return plan, cleanup, nil
}

func announcedAPI(line string) string {
	const prefix = "whitetransportd api listening on "
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	address := strings.TrimPrefix(line, prefix)
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	ip := net.ParseIP(parsed.Hostname())
	port, err := strconv.Atoi(parsed.Port())
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return ""
	}
	return address
}

// daemonEnvironment preserves the caller environment and supplies macOS's
// per-user SSH agent to a GUI-launched daemon, which otherwise has no shell
// environment from which to inherit SSH_AUTH_SOCK.
func daemonEnvironment(base []string, launchdAgentSocket string) []string {
	environment := append([]string(nil), base...)
	hasAgentSocket := false
	for _, entry := range environment {
		if strings.HasPrefix(entry, "SSH_AUTH_SOCK=") && strings.TrimSpace(strings.TrimPrefix(entry, "SSH_AUTH_SOCK=")) != "" {
			hasAgentSocket = true
			break
		}
	}
	if !hasAgentSocket && strings.TrimSpace(launchdAgentSocket) != "" {
		environment = append(environment, "SSH_AUTH_SOCK="+strings.TrimSpace(launchdAgentSocket))
	}
	return append(environment, "WT_DEBUG=1")
}

// launchdSSHAgentSocket returns the user agent socket exposed by macOS.
// Non-macOS desktop targets keep their inherited environment unchanged.
func launchdSSHAgentSocket() string {
	if goruntime.GOOS != "darwin" {
		return ""
	}
	output, err := exec.Command("launchctl", "getenv", "SSH_AUTH_SOCK").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// Stop terminates the managed daemon process.
func (s *DaemonSupervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	cmd := s.cmd
	exitDone := s.exitDone
	if cmd == nil || cmd.Process == nil {
		s.setStatusLocked("stopped", "Managed daemon stopped", 0)
		s.mu.Unlock()
		return nil
	}
	s.shuttingDown = true
	s.setStatusLocked("stopping", "Stopping managed daemon", cmd.Process.Pid)
	s.mu.Unlock()

	s.logs.Info("managed daemon stopping", map[string]string{"pid": fmt.Sprintf("%d", cmd.Process.Pid)})
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
	}

	select {
	case <-exitDone:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-exitDone
	}

	s.mu.Lock()
	s.cmd = nil
	s.exitDone = nil
	s.setStatusLocked("stopped", "Managed daemon stopped", 0)
	s.mu.Unlock()
	return nil
}

// Restart applies a newly generated managed configuration by stopping and
// starting the daemon under the same supervisor plan.
func (s *DaemonSupervisor) Restart(ctx context.Context) (DaemonSupervisorStatus, error) {
	if err := s.Stop(ctx); err != nil {
		return s.Status(), fmt.Errorf("stop managed daemon for restart: %w", err)
	}
	status, err := s.Start(ctx)
	if err != nil {
		return status, fmt.Errorf("start managed daemon after restart: %w", err)
	}
	return status, nil
}

// Status returns the latest supervisor state.
func (s *DaemonSupervisor) Status() DaemonSupervisorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *DaemonSupervisor) waitForHealth(ctx context.Context, exitDone <-chan error) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("managed daemon API did not become ready at %s: %w", s.plan.APIURL, ctx.Err())
		case <-exitDone:
			return fmt.Errorf("managed daemon exited before becoming ready")
		case <-ticker.C:
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.plan.APIURL+"/v1/status", nil)
			if err != nil {
				return err
			}
			response, err := s.httpClient.Do(request)
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 300 {
					s.mu.Lock()
					s.lastHealth = time.Now().UTC()
					s.status.LastHealthAt = s.lastHealth.Format(time.RFC3339Nano)
					s.mu.Unlock()
					return nil
				}
			}
		}
	}
}

func (s *DaemonSupervisor) pipeLog(prefix string, pipe any, ready ...chan string) {
	reader, ok := pipe.(interface{ Read([]byte) (int, error) })
	if !ok {
		return
	}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if prefix == "whitetransportd" && len(ready) > 0 {
			if address := announcedAPI(line); address != "" {
				select {
				case ready[0] <- address:
				default:
				}
			}
		}
		if line == "" {
			continue
		}
		level := "info"
		message := prefix
		stream := "stdout"
		if strings.HasSuffix(prefix, ":err") {
			// The daemon writes verbose diagnostics to stderr as well as actual
			// failures. Keep the stream identity in fields, but do not expose every
			// debug line as a misleading `whitetransportd:err` product error.
			message = strings.TrimSuffix(prefix, ":err")
			stream = "stderr"
			level = daemonStderrLogLevel(line)
		}
		_ = s.logs.Write(level, message, map[string]string{"line": line, "stream": stream})
	}
}

func daemonStderrLogLevel(line string) string {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, marker := range []string{
		"invalid_token",
		"unauthenticated",
		"permission denied",
		" panic",
		" fatal",
		" error",
		" failed",
		"status 4",
	} {
		if strings.Contains(lower, marker) {
			return "error"
		}
	}
	return "info"
}

func (s *DaemonSupervisor) waitForExit(cmd *exec.Cmd, exitDone chan<- error) {
	err := cmd.Wait()
	exitDone <- err
	close(exitDone)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cmd != s.cmd {
		return
	}
	s.cmd = nil
	if s.shuttingDown {
		s.setStatusLocked("stopped", "Managed daemon stopped", 0)
		return
	}
	message := "Managed daemon exited"
	if err != nil {
		message = err.Error()
	}
	s.setStatusLocked("exited", message, 0)
	s.logs.Error("managed daemon exited", err, nil)
}

func (s *DaemonSupervisor) setStatusLocked(state string, message string, pid int) {
	s.status.State = state
	s.status.Message = message
	s.status.PID = pid
	s.status.APIURL = s.plan.APIURL
	s.status.BinaryPath = s.plan.BinaryPath
	s.status.ConfigPath = s.plan.ConfigPath
	if !s.startedAt.IsZero() {
		s.status.StartedAt = s.startedAt.Format(time.RFC3339Nano)
	}
	if !s.lastHealth.IsZero() {
		s.status.LastHealthAt = s.lastHealth.Format(time.RFC3339Nano)
	}
}

func parsePositiveDurationMillis(raw string) time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	value, err := time.ParseDuration(trimmed + "ms")
	if err != nil || value <= 0 {
		return 0
	}
	return value
}
