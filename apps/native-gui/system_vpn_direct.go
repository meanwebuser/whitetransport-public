package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	StatusPath        string    `json:"status_path"`
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
	configStat func(string) (bool, error)
	setupErr   error
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
	return h.ensureBundledAssetNames("direct-helper", "tun2socks")
}

// ensureBundledDirectHelper refreshes the status sidecar once during host setup;
// tun2socks remains part of the privileged start path.
func (h *directSystemVPNHost) ensureBundledDirectHelper() error {
	return h.ensureBundledAssetNames("direct-helper")
}

// ensureBundledAssetNames installs a bundled executable only when its SHA-256
// provenance differs from the console-user copy or the target is unusable.
func (h *directSystemVPNHost) ensureBundledAssetNames(names ...string) error {
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
	for _, name := range names {
		source := filepath.Join(resourcesDir, name)
		target := filepath.Join(installDir, name)
		matches, err := bundledExecutableMatches(source, target)
		if err != nil {
			return err
		}
		if matches {
			continue
		}
		if err := copyBundledExecutable(source, target); err != nil {
			return err
		}
	}
	return nil
}

// bundledExecutableMatches reports whether target is an executable copy of the
// exact bundled source, without replacing a current console-user sidecar.
func bundledExecutableMatches(source, target string) (bool, error) {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return false, fmt.Errorf("read bundled runtime %q: %w", filepath.Base(source), err)
	}
	if !sourceInfo.Mode().IsRegular() || sourceInfo.Mode().Perm()&0o111 == 0 {
		return false, fmt.Errorf("bundled runtime %q is not an executable file", filepath.Base(source))
	}
	targetInfo, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect installed runtime %q: %w", filepath.Base(target), err)
	}
	if !targetInfo.Mode().IsRegular() || targetInfo.Mode().Perm()&0o111 == 0 {
		return false, nil
	}
	sourceDigest, err := executableSHA256(source)
	if err != nil {
		return false, fmt.Errorf("hash bundled runtime %q: %w", filepath.Base(source), err)
	}
	targetDigest, err := executableSHA256(target)
	if err != nil {
		return false, fmt.Errorf("hash installed runtime %q: %w", filepath.Base(target), err)
	}
	return sourceDigest == targetDigest, nil
}

// executableSHA256 returns the content digest used to establish sidecar
// provenance without reading or logging credential-bearing configuration.
func executableSHA256(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
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
	host := &directSystemVPNHost{
		executable: executable,
		configPath: configPath,
		authMode:   strings.TrimSpace(os.Getenv("WT_DIRECT_HELPER_AUTH_MODE")),
		runner:     runDirectCommand,
		configStat: func(path string) (bool, error) {
			_, err := os.Stat(path)
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return err == nil, err
		},
	}
	host.setupErr = host.ensureBundledDirectHelper()
	return host
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
	// The helper's stdout is a JSON-only protocol. In the acceptance-only sudo
	// mode, sudo writes its password prompt to stderr even after authentication
	// succeeds. CombinedOutput would prepend that prompt to a valid JSON result
	// and make a successful helper start look like a decoder failure.
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return output, err
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
	// Permission is a read-only capability probe. Calling the privileged command
	// wrapper here would open an administrator prompt during every capability or
	// onboarding refresh, even when the helper is already stopped.
	observation, err := h.Status(ctx)
	if err != nil {
		return directErrorObservation(err), err
	}
	return observation, nil
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
	// Validation only reads the user-owned config and writes a diagnostic
	// artifact; it must not consume a second administrator authorization.
	if _, err := h.runWithArgs(ctx, "test", h.readOnlyCommandArgs("test")...); err != nil {
		return directErrorObservation(err), err
	}
	result, err := h.run(ctx, "start")
	if err != nil {
		return directResultObservation(result, "start"), err
	}
	return directResultObservation(result, "start"), nil
}

func (h *directSystemVPNHost) Stop(ctx context.Context) (systemVPNObservation, error) {
	// Stop is idempotent and should not authorize a route mutation when no helper
	// process exists. This read-only probe also prevents the failed-start rollback
	// path from opening a second administrator prompt after Start already failed.
	statusObservation, statusErr := h.Status(ctx)
	if statusErr != nil {
		return statusObservation, statusErr
	}
	if statusObservation.State != guiruntime.SystemVPNConnected {
		return statusObservation, nil
	}
	result, err := h.run(ctx, "stop")
	return directResultObservation(result, "stop"), err
}

func (h *directSystemVPNHost) Status(ctx context.Context) (systemVPNObservation, error) {
	// Status is read-only: invoking the helper directly avoids opening an
	// administrator prompt every time the GUI refreshes its status. Start and
	// stop remain privileged because they mutate utun/routes.
	// A fresh installation has no helper config yet. Treat that state as the
	// expected disconnected baseline instead of trying to execute a helper that
	// Start has not needed to copy into Application Support.
	if h != nil && h.configStat != nil && strings.TrimSpace(h.configPath) != "" {
		exists, err := h.configStat(h.configPath)
		if err != nil {
			return directErrorObservation(err), fmt.Errorf("inspect direct-helper config: %w", err)
		}
		if !exists {
			return systemVPNObservation{State: guiruntime.SystemVPNDisconnected, ProviderState: guiruntime.SystemVPNDisconnected}, nil
		}
	}
	if h.setupErr != nil {
		return directErrorObservation(h.setupErr), fmt.Errorf("prepare direct-helper runtime: %w", h.setupErr)
	}
	result, err := h.runWithArgs(ctx, "status", h.statusCommandArgs()...)
	return directResultObservation(result, "status"), err
}

func (h *directSystemVPNHost) statusCommandArgs() []string {
	return h.readOnlyCommandArgs("status")
}

func (h *directSystemVPNHost) readOnlyCommandArgs(command string) []string {
	return []string{h.executable, command, "--config", h.configPath}
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
	return h.runWithArgs(ctx, command, h.commandArgs(command)...)
}

func (h *directSystemVPNHost) runWithArgs(ctx context.Context, command string, args ...string) (directHelperResult, error) {
	if h == nil || h.runner == nil {
		return directHelperResult{}, fmt.Errorf("direct-helper is unavailable")
	}
	output, err := h.runner(ctx, args...)
	var result directHelperResult
	if decodeErr := json.Unmarshal(output, &result); decodeErr != nil {
		if err != nil && (isPermissionOutput(output) || isPermissionOutput([]byte(err.Error()))) {
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
	return strings.Contains(text, "a password is required") || strings.Contains(text, "password:") || strings.Contains(text, "no tty present") || strings.Contains(text, "permission denied") || strings.Contains(text, "user canceled") || strings.Contains(text, "user cancelled")
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
	return directHelperConfig{SOCKSHost: host, SOCKSPort: port, Mode: mode, BypassCIDRs: uniqueSortedStrings(bypass), OnlyCIDRs: uniqueSortedStrings(only), Tun2SocksPath: filepath.Join(directHelperBinDir(home), "tun2socks"), MTU: profile.MTU, StatePath: filepath.Join(base, "state.json"), StatusPath: filepath.Join(base, "status.json"), LogPath: filepath.Join(home, "Library", "Logs", "WhiteTransport", "direct-helper.log"), TestResultPath: filepath.Join(base, "test-result.json"), DaemonInstanceID: profile.DaemonInstanceID, ProfileRevision: profile.ProfileRevision, ProfileHash: profile.ProfileHash, SessionID: profile.SessionID, ProfileValidUntil: systemVPNProfileValidUntil(profile)}, nil
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
