package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

// directHelperConfig is the small, secret-free contract written by the GUI.
// All paths are absolute because the helper runs as root via sudo.
type directHelperConfig struct {
	SOCKSHost         string    `json:"socks_host"`
	SOCKSPort         int       `json:"socks_port"`
	Mode              string    `json:"mode"`
	BypassCIDRs       []string  `json:"bypass_cidrs,omitempty"`
	OnlyCIDRs         []string  `json:"only_cidrs,omitempty"`
	Tun2SocksPath     string    `json:"tun2socks_path"`
	MTU               int       `json:"mtu"`
	StatePath         string    `json:"state_path"`
	LogPath           string    `json:"log_path"`
	TestResultPath    string    `json:"test_result_path"`
	DaemonInstanceID  string    `json:"daemon_instance_id"`
	ProfileRevision   uint64    `json:"profile_revision"`
	ProfileHash       string    `json:"profile_hash"`
	SessionID         string    `json:"session_id"`
	ProfileValidUntil time.Time `json:"profile_valid_until"`
}

type directHelperState struct {
	PID    int                `json:"pid"`
	Config directHelperConfig `json:"config"`
}

type directHelperResult struct {
	OK      bool               `json:"ok"`
	Command string             `json:"command"`
	Message string             `json:"message"`
	State   *directHelperState `json:"state"`
	Error   string             `json:"error"`
}

type directCommandRunner func(context.Context, ...string) ([]byte, error)

type directSystemVPNHost struct {
	executable string
	configPath string
	authMode   string
	runner     directCommandRunner
}

const directHelperApplicationSupport = "Library/Application Support/WhiteTransport"

const directHelperAuthorizationScript = `on run argv
if (count argv) is not 3 then error "invalid direct-helper argument count"
set helperPath to item 1 of argv
set helperCommand to item 2 of argv
set configPath to item 3 of argv
set commandLine to quoted form of helperPath & " " & quoted form of helperCommand & " --config " & quoted form of configPath
do shell script commandLine with administrator privileges
end run`

func directHelperBinDir(home string) string {
	return filepath.Join(home, directHelperApplicationSupport, "bin")
}

func directHelperBundleResourcesDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("WT_DIRECT_HELPER_RESOURCES_DIR")); override != "" {
		return filepath.Clean(override), nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve WhiteTransport executable: %w", err)
	}
	// Wails places the application executable under Contents/MacOS and app
	// resources under the sibling Contents/Resources directory.
	return filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "Resources")), nil
}

func (h *directSystemVPNHost) ensureBundledAssets() error {
	if h == nil || strings.TrimSpace(h.executable) == "" {
		return fmt.Errorf("direct-helper is unavailable")
	}
	resourcesDir, err := directHelperBundleResourcesDir()
	if err != nil {
		return err
	}
	installDir := filepath.Dir(h.executable)
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		return fmt.Errorf("create direct-helper bin directory: %w", err)
	}
	for _, name := range []string{"direct-helper", "tun2socks"} {
		source := filepath.Join(resourcesDir, name)
		target := filepath.Join(installDir, name)
		if info, statErr := os.Stat(target); statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			continue
		}
		if err := copyBundledExecutable(source, target); err != nil {
			return err
		}
	}
	return nil
}

func copyBundledExecutable(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("read bundled runtime %q: %w", filepath.Base(source), err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("bundled runtime %q is not an executable file", filepath.Base(source))
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read bundled runtime %q: %w", filepath.Base(source), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".WhiteTransport-runtime-*")
	if err != nil {
		return fmt.Errorf("create bundled runtime temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write bundled runtime %q: %w", filepath.Base(source), err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("install bundled runtime %q: %w", filepath.Base(source), err)
	}
	return nil
}

func macOSVPNBackendFromEnv(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "networkextension") {
		return "networkextension"
	}
	return "direct"
}

func newDirectSystemVPNHost() *directSystemVPNHost {
	home, _ := os.UserHomeDir()
	configPath := os.Getenv("WT_DIRECT_HELPER_CONFIG")
	if configPath == "" {
		configPath = filepath.Join(home, "Library", "Application Support", "WhiteTransport", "direct-helper", "config.json")
	}
	executable := os.Getenv("WT_DIRECT_HELPER_BIN")
	if executable == "" {
		executable = filepath.Join(home, "Library", "Application Support", "WhiteTransport", "bin", "direct-helper")
	}
	return &directSystemVPNHost{executable: executable, configPath: configPath, authMode: strings.TrimSpace(os.Getenv("WT_DIRECT_HELPER_AUTH_MODE")), runner: runDirectCommand}
}

func runDirectCommand(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	if args[0] == "sudo" && slicesContains(args[1:], "-S") {
		// Test automation supplies the operator-approved password through the
		// environment; keep it off argv and logs by writing it only to sudo's
		// stdin. Production's native authorization path does not use this mode.
		if password := os.Getenv("MAC_SUDO"); password != "" {
			command.Stdin = strings.NewReader(password + "\n")
		}
	}
	return command.CombinedOutput()
}

func slicesContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (h *directSystemVPNHost) Supported() bool { return true }

func (h *directSystemVPNHost) commandArgs(command string) []string {
	if h.authMode == "sudo-noninteractive" {
		// A supplied test credential must be accepted from stdin; sudo's -n
		// flag rejects stdin authentication. Without a credential retain the
		// fail-fast behavior so an unattended run never prompts.
		if os.Getenv("MAC_SUDO") != "" {
			return []string{"sudo", "-S", h.executable, command, "--config", h.configPath}
		}
		return []string{"sudo", "-S", "-n", h.executable, command, "--config", h.configPath}
	}
	return []string{"/usr/bin/osascript", "-e", directHelperAuthorizationScript, "--", h.executable, command, h.configPath}
}

func (h *directSystemVPNHost) Permission(ctx context.Context) (systemVPNObservation, error) {
	result, err := h.run(ctx, "status")
	if err != nil {
		return systemVPNObservation{State: guiruntime.SystemVPNPermissionRequired, ProviderState: guiruntime.SystemVPNPermissionRequired}, permissionRequiredError(err)
	}
	return directResultObservation(result, "status"), nil
}

func (h *directSystemVPNHost) Start(ctx context.Context, raw json.RawMessage) (systemVPNObservation, error) {
	if _, err := decodeSystemVPNProfileIdentity(raw); err != nil {
		return systemVPNObservation{}, err
	}
	cfg, err := buildDirectHelperConfig(raw, time.Now().UTC(), h.configPath)
	if err != nil {
		return systemVPNObservation{}, err
	}
	if err := h.ensureBundledAssets(); err != nil {
		return systemVPNObservation{}, err
	}
	if err := writeDirectHelperConfig(h.configPath, cfg); err != nil {
		return systemVPNObservation{}, err
	}
	if _, err := h.run(ctx, "test"); err != nil {
		return directErrorObservation(err), err
	}
	result, err := h.run(ctx, "start")
	if err != nil {
		return directResultObservation(result, "start"), err
	}
	return directResultObservation(result, "start"), nil
}

func (h *directSystemVPNHost) Stop(ctx context.Context) (systemVPNObservation, error) {
	result, err := h.run(ctx, "stop")
	return directResultObservation(result, "stop"), err
}

func (h *directSystemVPNHost) Status(ctx context.Context) (systemVPNObservation, error) {
	result, err := h.run(ctx, "status")
	return directResultObservation(result, "status"), err
}

func (h *directSystemVPNHost) Logs(ctx context.Context) ([]guiruntime.LogLine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(h.logPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read direct-helper logs: %w", err)
	}
	lines := make([]guiruntime.LogLine, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, guiruntime.LogLine{Level: "info", Message: guiruntime.RedactText(line), Fields: map[string]string{"source": "macos-direct-helper"}})
	}
	return lines, nil
}

func (h *directSystemVPNHost) logPath() string {
	data, err := os.ReadFile(h.configPath)
	if err == nil {
		var cfg directHelperConfig
		if json.Unmarshal(data, &cfg) == nil && cfg.LogPath != "" {
			return cfg.LogPath
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "WhiteTransport", "direct-helper.log")
}

func (h *directSystemVPNHost) run(ctx context.Context, command string) (directHelperResult, error) {
	if h == nil || h.runner == nil {
		return directHelperResult{}, fmt.Errorf("direct-helper is unavailable")
	}
	output, err := h.runner(ctx, h.commandArgs(command)...)
	var result directHelperResult
	if decodeErr := json.Unmarshal(output, &result); decodeErr != nil {
		if err != nil && isPermissionOutput(output) {
			return result, permissionRequiredError(err)
		}
		if err != nil {
			return result, fmt.Errorf("direct-helper %s failed: %w (%s)", command, err, strings.TrimSpace(string(output)))
		}
		return result, fmt.Errorf("decode direct-helper %s response: %w", command, decodeErr)
	}
	if !result.OK {
		message := strings.TrimSpace(result.Error)
		if isPermissionOutput(output) || strings.Contains(strings.ToLower(message), "permission") {
			return result, permissionRequiredError(errors.New(message))
		}
		if message == "" {
			message = result.Message
		}
		return result, fmt.Errorf("direct-helper %s: %s", command, message)
	}
	return result, nil
}

func isPermissionOutput(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "a password is required") || strings.Contains(text, "no tty present") || strings.Contains(text, "permission denied") || strings.Contains(text, "user canceled") || strings.Contains(text, "user cancelled")
}

func permissionRequiredError(cause error) error {
	return fmt.Errorf("permission_required: approve the macOS administrator prompt, then retry: %w", cause)
}

func directErrorObservation(err error) systemVPNObservation {
	if strings.Contains(err.Error(), "permission_required") {
		return systemVPNObservation{State: guiruntime.SystemVPNPermissionRequired, ProviderState: guiruntime.SystemVPNPermissionRequired}
	}
	return systemVPNObservation{State: guiruntime.SystemVPNError, ProviderState: guiruntime.SystemVPNError}
}

func directResultObservation(result directHelperResult, command string) systemVPNObservation {
	if result.State == nil {
		return systemVPNObservation{State: guiruntime.SystemVPNDisconnected, ProviderState: guiruntime.SystemVPNDisconnected}
	}
	cfg := result.State.Config
	state := guiruntime.SystemVPNDisconnected
	if command == "start" || (command == "status" && result.Message == "running") {
		state = guiruntime.SystemVPNConnected
	}
	return systemVPNObservation{State: state, ProviderState: state, DaemonInstanceID: cfg.DaemonInstanceID, Revision: cfg.ProfileRevision, SessionID: cfg.SessionID, ProfileHash: cfg.ProfileHash, ProfileValidUntil: cfg.ProfileValidUntil}
}

func buildDirectHelperConfig(raw json.RawMessage, now time.Time, configPath string) (directHelperConfig, error) {
	var profile runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return directHelperConfig{}, fmt.Errorf("decode authoritative system VPN profile: %w", err)
	}
	if err := profile.Validate(now); err != nil {
		return directHelperConfig{}, fmt.Errorf("validate authoritative system VPN profile: %w", err)
	}
	host, portText, err := net.SplitHostPort(profile.SocksListen)
	if err != nil {
		return directHelperConfig{}, fmt.Errorf("parse authoritative SOCKS listener: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return directHelperConfig{}, fmt.Errorf("parse authoritative SOCKS port: %w", err)
	}
	mode := "full"
	bypass, only := []string(nil), []string(nil)
	switch profile.RouteMode {
	case runtimeapi.SystemVPNRouteNone:
	case runtimeapi.SystemVPNRouteBypass:
		mode = "bypass"
		bypass = append(bypass, profile.UserBypassCIDRs...)
		bypass = append(bypass, profile.DestinationCIDRs...)
	case runtimeapi.SystemVPNRouteOnly:
		mode = "only"
		only = append(only, profile.DestinationCIDRs...)
	default:
		return directHelperConfig{}, fmt.Errorf("system VPN route mode %q cannot be mapped to direct-helper", profile.RouteMode)
	}
	// Full and bypass modes install /1 routes. Keep every authoritative
	// carrier/control endpoint outside those routes or the provider's own
	// sockets recurse through the TUN and the SOCKS dataplane cannot connect.
	if profile.RouteMode != runtimeapi.SystemVPNRouteOnly {
		for _, routes := range profile.CarrierControlRoutes {
			bypass = append(bypass, routes...)
		}
	}
	base := filepath.Dir(configPath)
	home, _ := os.UserHomeDir()
	return directHelperConfig{SOCKSHost: host, SOCKSPort: port, Mode: mode, BypassCIDRs: uniqueSortedStrings(bypass), OnlyCIDRs: uniqueSortedStrings(only), Tun2SocksPath: filepath.Join(directHelperBinDir(home), "tun2socks"), MTU: profile.MTU, StatePath: filepath.Join(base, "state.json"), LogPath: filepath.Join(home, "Library", "Logs", "WhiteTransport", "direct-helper.log"), TestResultPath: filepath.Join(base, "test-result.json"), DaemonInstanceID: profile.DaemonInstanceID, ProfileRevision: profile.ProfileRevision, ProfileHash: profile.ProfileHash, SessionID: profile.SessionID, ProfileValidUntil: systemVPNProfileValidUntil(profile)}, nil
}

func writeDirectHelperConfig(path string, cfg directHelperConfig) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("direct-helper config path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create direct-helper config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode direct-helper config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create direct-helper config temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace direct-helper config: %w", err)
	}
	return nil
}
