package runtime

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
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
	if s.apiEndpointOccupied(ctx) {
		err := fmt.Errorf("managed daemon API endpoint %s is already occupied; refusing to claim a foreign process", s.plan.APIURL)
		s.setStatusLocked("error", err.Error(), 0)
		status := s.status
		s.mu.Unlock()
		return status, err
	}
	s.logs.Info("managed daemon starting", map[string]string{
		"binary_path": s.plan.BinaryPath,
		"config_path": s.plan.ConfigPath,
		"api_url":     s.plan.APIURL,
	})
	cmd := newDaemonCommand(ctx, s.plan)
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

	go s.pipeLog("whitetransportd", stdout)
	go s.pipeLog("whitetransportd:err", stderr)
	go s.waitForExit(cmd, exitDone)

	waitCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.waitForHealth(waitCtx); err != nil {
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

// apiEndpointOccupied prevents a stale or foreign daemon from satisfying the
// health check for a child that failed to bind its API/SOCKS ports. A healthy
// pre-existing endpoint cannot be attributed to this supervisor instance, so
// startup fails closed instead of treating foreign health as owned process
// readiness.
func (s *DaemonSupervisor) apiEndpointOccupied(ctx context.Context) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.plan.APIURL+"/v1/status", nil)
	if err != nil {
		return false
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return true
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

func (s *DaemonSupervisor) waitForHealth(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("managed daemon API did not become ready at %s: %w", s.plan.APIURL, ctx.Err())
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

func (s *DaemonSupervisor) pipeLog(prefix string, pipe any) {
	reader, ok := pipe.(interface{ Read([]byte) (int, error) })
	if !ok {
		return
	}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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
