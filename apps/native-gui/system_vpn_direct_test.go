package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

func TestBuildDirectHelperConfigMapsAuthoritativeProfile(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	now := time.Now().UTC()
	var profile runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(testNativeSystemVPNProfile(t), &profile); err != nil {
		t.Fatal(err)
	}
	profile.SocksListen = "127.0.0.1:1080"
	profile.RouteMode = runtimeapi.SystemVPNRouteBypass
	profile.DestinationCIDRs = []string{"198.51.100.0/24"}
	profile.UserBypassCIDRs = []string{"10.0.0.0/8"}
	profile.MTU = 1400
	if err := profile.SetHash(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(profile)
	cfg, err := buildDirectHelperConfig(raw, now, filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "bypass" || cfg.SOCKSHost != "127.0.0.1" || cfg.SOCKSPort != 1080 || cfg.MTU != 1400 {
		t.Fatalf("config basics = %#v", cfg)
	}
	wantBypass := []string{"10.0.0.0/8", "198.51.100.0/24", "198.51.100.10/32", "198.51.100.20/32", "198.51.100.30/32"}
	if !reflect.DeepEqual(cfg.BypassCIDRs, wantBypass) {
		t.Fatalf("bypass routes = %#v, want %#v", cfg.BypassCIDRs, wantBypass)
	}
	if cfg.DaemonInstanceID != profile.DaemonInstanceID || cfg.ProfileHash != profile.ProfileHash || !cfg.ProfileValidUntil.Equal(profile.ExpiresAt.UTC().Truncate(time.Second)) {
		t.Fatalf("identity not mapped: %#v", cfg)
	}
	if cfg.Tun2SocksPath != filepath.Join("/Users", "alice", "Library", "Application Support", "WhiteTransport", "bin", "tun2socks") {
		t.Fatalf("tun2socks path = %q; expected console-user Application Support bin", cfg.Tun2SocksPath)
	}
}

func TestBuildDirectHelperConfigMergesCarrierControlRoutesIntoFullBypass(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	now := time.Now().UTC()
	var profile runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(testNativeSystemVPNProfile(t), &profile); err != nil {
		t.Fatal(err)
	}
	profile.RouteMode = runtimeapi.SystemVPNRouteNone
	profile.CarrierControlRoutes["egress.test"] = []string{"198.51.100.30/32", "2001:db8::30/128"}
	profile.DNSSnapshot["egress.test"] = []string{"198.51.100.30", "2001:db8::30"}
	for i := range profile.Dependencies {
		if profile.Dependencies[i].Host == "egress.test" {
			profile.Dependencies[i].Addresses = []string{"198.51.100.30", "2001:db8::30"}
		}
	}
	profile.SortProfileSlices()
	if err := profile.SetHash(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := buildDirectHelperConfig(raw, now, filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.10/32", "198.51.100.20/32", "198.51.100.30/32", "2001:db8::30/128"}
	if !reflect.DeepEqual(cfg.BypassCIDRs, want) {
		t.Fatalf("carrier bypass routes = %#v, want %#v", cfg.BypassCIDRs, want)
	}
}

func TestBuildDirectHelperConfigPreservesOnlyModeWithoutCarrierBypass(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	now := time.Now().UTC()
	var profile runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(testNativeSystemVPNProfile(t), &profile); err != nil {
		t.Fatal(err)
	}
	profile.RouteMode = runtimeapi.SystemVPNRouteOnly
	profile.DestinationCIDRs = []string{"203.0.113.0/24"}
	profile.SortProfileSlices()
	if err := profile.SetHash(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := buildDirectHelperConfig(raw, now, filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "only" || !reflect.DeepEqual(cfg.OnlyCIDRs, []string{"203.0.113.0/24"}) || len(cfg.BypassCIDRs) != 0 {
		t.Fatalf("only mapping = mode=%q only=%#v bypass=%#v", cfg.Mode, cfg.OnlyCIDRs, cfg.BypassCIDRs)
	}
}

func TestDirectHelperBundleResourcesAreCopiedToConsoleBin(t *testing.T) {
	t.Setenv("WT_DIRECT_HELPER_RESOURCES_DIR", t.TempDir())
	resources := os.Getenv("WT_DIRECT_HELPER_RESOURCES_DIR")
	for _, name := range []string{"direct-helper", "tun2socks"} {
		path := filepath.Join(resources, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", t.TempDir())
	host := newDirectSystemVPNHost()
	if err := host.ensureBundledAssets(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"direct-helper", "tun2socks"} {
		path := filepath.Join(filepath.Dir(host.executable), name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("copied %s: %v", name, err)
		}
	}
}

func TestDirectHelperDarwinStatusReadsPublicSnapshotNotPrivateState(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("macos", "direct-helper", "runner_darwin.go"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "func platformStatus(cfg Config) Result {"
	const end = "\nfunc runLogged("
	startIndex := strings.Index(string(data), start)
	endIndex := strings.Index(string(data), end)
	if startIndex < 0 || endIndex <= startIndex {
		t.Fatal("could not locate Darwin direct-helper platformStatus implementation")
	}
	statusSource := string(data)[startIndex:endIndex]
	if !strings.Contains(statusSource, "readStatusSnapshot(cfg.StatusPath)") {
		t.Fatal("routine Darwin helper status must read only the public status snapshot")
	}
	if strings.Contains(statusSource, "readState(") {
		t.Fatal("routine Darwin helper status must never read private state.json")
	}
}

func TestDirectHelperStatusDoesNotRunSetupOrSidecarRefresh(t *testing.T) {
	data, err := os.ReadFile("system_vpn_direct.go")
	if err != nil {
		t.Fatal(err)
	}
	const start = "func (h *directSystemVPNHost) Status(ctx context.Context) (systemVPNObservation, error) {"
	const end = "\nfunc (h *directSystemVPNHost) statusCommandArgs() []string {"
	startIndex := strings.Index(string(data), start)
	endIndex := strings.Index(string(data), end)
	if startIndex < 0 || endIndex <= startIndex {
		t.Fatal("could not locate direct system VPN Status implementation")
	}
	statusSource := string(data)[startIndex:endIndex]
	if strings.Contains(statusSource, "ensure") || strings.Contains(statusSource, "MkdirAll") || strings.Contains(statusSource, "copyBundledExecutable") {
		t.Fatal("routine Status must not prepare or replace the direct-helper sidecar")
	}
}

func TestNewDirectSystemVPNHostRefreshesStaleBundledHelperDuringSetup(t *testing.T) {
	resources := t.TempDir()
	freshHelper := []byte("#!/bin/sh\necho current-helper\n")
	freshTun2Socks := []byte("#!/bin/sh\necho current-tun2socks\n")
	for name, contents := range map[string][]byte{
		"direct-helper": freshHelper,
		"tun2socks":     freshTun2Socks,
	} {
		if err := os.WriteFile(filepath.Join(resources, name), contents, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WT_DIRECT_HELPER_RESOURCES_DIR", resources)
	home := t.TempDir()
	t.Setenv("HOME", home)

	target := filepath.Join(directHelperBinDir(home), "direct-helper")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho stale-helper\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := newDirectSystemVPNHost()
	got, err := os.ReadFile(host.executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, freshHelper) {
		t.Fatal("direct-helper setup did not replace the stale bundled sidecar")
	}
}

func TestDirectHelperStatusDoesNotRefreshHelperAfterSetup(t *testing.T) {
	resources := t.TempDir()
	initialHelper := []byte("#!/bin/sh\necho initial-helper\n")
	updatedHelper := []byte("#!/bin/sh\necho updated-helper\n")
	freshTun2Socks := []byte("#!/bin/sh\necho current-tun2socks\n")
	for name, contents := range map[string][]byte{
		"direct-helper": initialHelper,
		"tun2socks":     freshTun2Socks,
	} {
		if err := os.WriteFile(filepath.Join(resources, name), contents, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WT_DIRECT_HELPER_RESOURCES_DIR", resources)
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(directHelperBinDir(home), "direct-helper")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, initialHelper, 0o755); err != nil {
		t.Fatal(err)
	}

	host := newDirectSystemVPNHost()
	if err := os.WriteFile(filepath.Join(resources, "direct-helper"), updatedHelper, 0o755); err != nil {
		t.Fatal(err)
	}
	host.configStat = func(string) (bool, error) { return true, nil }
	var invocation []string
	host.runner = func(_ context.Context, args ...string) ([]byte, error) {
		invocation = append([]string(nil), args...)
		got, err := os.ReadFile(host.executable)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, initialHelper) {
			t.Fatal("Status unexpectedly refreshed the direct-helper sidecar")
		}
		return []byte(`{"ok":true,"command":"status","message":"stopped"}`), nil
	}

	if _, err := host.Status(context.Background()); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := []string{host.executable, "status", "--config", host.configPath}
	if !reflect.DeepEqual(invocation, want) {
		t.Fatalf("status invocation = %#v, want unprivileged %#v", invocation, want)
	}
}

func TestDirectHelperBundleAssetsKeepMatchingHelperInPlace(t *testing.T) {
	resources := t.TempDir()
	for name, contents := range map[string][]byte{
		"direct-helper": []byte("#!/bin/sh\necho current-helper\n"),
		"tun2socks":     []byte("#!/bin/sh\necho current-tun2socks\n"),
	} {
		if err := os.WriteFile(filepath.Join(resources, name), contents, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WT_DIRECT_HELPER_RESOURCES_DIR", resources)
	t.Setenv("HOME", t.TempDir())

	host := newDirectSystemVPNHost()
	if err := os.MkdirAll(filepath.Dir(host.executable), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"direct-helper", "tun2socks"} {
		contents, err := os.ReadFile(filepath.Join(resources, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(host.executable), name), contents, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(host.executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.ensureBundledAssets(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(host.executable)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("matching direct-helper was unnecessarily replaced")
	}
}

func TestDirectHelperCommandUsesNativeAuthorizationPromptByDefault(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	t.Setenv("WT_DIRECT_HELPER_AUTH_MODE", "")
	host := newDirectSystemVPNHost()
	got := host.commandArgs("start")
	if len(got) != 7 || got[0] != "/usr/bin/osascript" || got[1] != "-e" || got[3] != "--" || got[4] != host.executable || got[5] != "start" || got[6] != host.configPath {
		t.Fatalf("argv = %#v, want osascript prompt with helper command arguments", got)
	}
	if !strings.Contains(got[2], "quoted form") || !strings.Contains(got[2], "with administrator privileges") {
		t.Fatalf("AppleScript = %q, want safely quoted administrator prompt", got[2])
	}
	for _, arg := range got {
		if strings.Contains(strings.ToLower(arg), "password") {
			t.Fatalf("argv contains password handling: %#v", got)
		}
	}
}

func TestDirectHelperCommandAllowsExplicitNonInteractiveTestAuth(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	t.Setenv("WT_DIRECT_HELPER_AUTH_MODE", "sudo-noninteractive")
	t.Setenv("MAC_SUDO", "")
	host := newDirectSystemVPNHost()
	got := host.commandArgs("start")
	want := []string{"sudo", "-S", "-n", host.executable, "start", "--config", host.configPath}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestDirectHelperCommandReadsCredentialFromStdinWithoutNoPromptFlag(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	t.Setenv("WT_DIRECT_HELPER_AUTH_MODE", "sudo-noninteractive")
	t.Setenv("MAC_SUDO", "unit-test-secret")
	host := newDirectSystemVPNHost()
	got := host.commandArgs("start")
	want := []string{"sudo", "-S", host.executable, "start", "--config", host.configPath}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestRunDirectCommandFeedsMacSudoOnlyThroughStdin(t *testing.T) {
	testDir := t.TempDir()
	fakeSudo := filepath.Join(testDir, "sudo")
	script := "#!/bin/sh\nread -r password\n[ \"$password\" = \"unit-test-secret\" ]\n"
	if err := os.WriteFile(fakeSudo, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", testDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAC_SUDO", "unit-test-secret")
	if _, err := runDirectCommand(context.Background(), "sudo", "-S", "-n", "/fake/helper", "status"); err != nil {
		t.Fatalf("runDirectCommand() error = %v, want password supplied on stdin", err)
	}
}

func TestRunDirectCommandKeepsSudoPromptOutOfHelperJSON(t *testing.T) {
	testDir := t.TempDir()
	fakeSudo := filepath.Join(testDir, "sudo")
	script := "#!/bin/sh\nprintf 'Password:' >&2\nread -r password\n[ \"$password\" = \"unit-test-secret\" ] || exit 1\nprintf '{\"ok\":true,\"command\":\"start\"}\\n'\n"
	if err := os.WriteFile(fakeSudo, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", testDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAC_SUDO", "unit-test-secret")
	output, err := runDirectCommand(context.Background(), "sudo", "-S", "/fake/helper", "start")
	if err != nil {
		t.Fatalf("runDirectCommand() error = %v", err)
	}
	var result directHelperResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("helper stdout = %q, want JSON without sudo prompt: %v", output, err)
	}
	if !result.OK || result.Command != "start" {
		t.Fatalf("result = %#v", result)
	}
}

func TestDirectHelperRunMapsSudoPromptOnStderrToPermissionRequired(t *testing.T) {
	host := &directSystemVPNHost{runner: func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("exit status 1: Password:")
	}}
	_, err := host.run(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "permission_required") {
		t.Fatalf("error = %v, want permission_required", err)
	}
}

func TestDirectHelperPermissionUsesSupportedStatusCommand(t *testing.T) {
	var commands [][]string
	host := &directSystemVPNHost{
		executable: "/Users/alice/Library/Application Support/WhiteTransport/bin/direct-helper",
		configPath: "/Users/alice/Library/Application Support/WhiteTransport/direct-helper/config.json",
		authMode:   "sudo-noninteractive",
		runner: func(_ context.Context, args ...string) ([]byte, error) {
			commands = append(commands, append([]string(nil), args...))
			return []byte(`{"ok":true,"command":"status","message":"stopped"}`), nil
		},
	}
	if _, err := host.Permission(context.Background()); err != nil {
		t.Fatalf("Permission() error = %v", err)
	}
	want := []string{host.executable, "status", "--config", host.configPath}
	if len(commands) != 1 || !reflect.DeepEqual(commands[0], want) {
		t.Fatalf("Permission() commands = %#v, want one read-only status command %#v", commands, want)
	}
}

func TestDirectHelperReadOnlyCommandUsesNoAuthorizationWrapper(t *testing.T) {
	host := &directSystemVPNHost{
		executable: "/Users/alice/Library/Application Support/WhiteTransport/bin/direct-helper",
		configPath: "/Users/alice/Library/Application Support/WhiteTransport/direct-helper/config.json",
	}
	want := []string{host.executable, "test", "--config", host.configPath}
	if got := host.readOnlyCommandArgs("test"); !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only argv = %#v, want %#v", got, want)
	}
}

func TestDirectHelperStatusUsesReadOnlyInvocation(t *testing.T) {
	t.Setenv("HOME", "/Users/alice")
	t.Setenv("WT_DIRECT_HELPER_AUTH_MODE", "")
	var got []string
	host := &directSystemVPNHost{
		executable: "/Users/alice/Library/Application Support/WhiteTransport/bin/direct-helper",
		configPath: "/Users/alice/Library/Application Support/WhiteTransport/direct-helper/config.json",
		runner: func(_ context.Context, args ...string) ([]byte, error) {
			got = append([]string(nil), args...)
			return []byte(`{"ok":true,"command":"status","message":"stopped"}`), nil
		},
	}
	if _, err := host.Status(context.Background()); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := []string{host.executable, "status", "--config", host.configPath}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status argv = %#v, want read-only helper invocation %#v", got, want)
	}
}

func TestDirectHelperStatusBeforeFirstStartIsDisconnected(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "direct-helper", "config.json")
	called := false
	host := &directSystemVPNHost{
		executable: filepath.Join(tempDir, "bin", "direct-helper"),
		configPath: configPath,
		configStat: func(string) (bool, error) { return false, nil },
		runner: func(context.Context, ...string) ([]byte, error) {
			called = true
			return nil, errors.New("helper must not be invoked before first start")
		},
	}

	observation, err := host.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v, want disconnected without a pre-existing config", err)
	}
	if called {
		t.Fatal("Status() invoked the helper before its first start")
	}
	if observation.State != guiruntime.SystemVPNDisconnected {
		t.Fatalf("Status() state = %q, want disconnected", observation.State)
	}
}

func TestDirectHelperStopSkipsAuthorizationWhenAlreadyStopped(t *testing.T) {
	var calls [][]string
	host := &directSystemVPNHost{
		executable: "/Users/alice/Library/Application Support/WhiteTransport/bin/direct-helper",
		configPath: "/Users/alice/Library/Application Support/WhiteTransport/direct-helper/config.json",
		runner: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return []byte(`{"ok":true,"command":"status","message":"stopped"}`), nil
		},
	}
	observation, err := host.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if observation.State != guiruntime.SystemVPNDisconnected {
		t.Fatalf("Stop() state = %q, want disconnected", observation.State)
	}
	want := []string{host.executable, "status", "--config", host.configPath}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("Stop() calls = %#v, want one read-only status call %#v", calls, want)
	}
}

func TestDirectHelperRunMapsCanceledMacAuthorizationToPermissionRequired(t *testing.T) {
	host := &directSystemVPNHost{runner: func(context.Context, ...string) ([]byte, error) {
		return []byte("execution error: User canceled. (-128)\n"), errors.New("exit status 1")
	}}
	_, err := host.run(context.Background(), "true")
	if err == nil || !strings.Contains(err.Error(), "permission_required") {
		t.Fatalf("error = %v, want permission_required", err)
	}
	if strings.Contains(err.Error(), "sudo -v") || strings.Contains(strings.ToLower(err.Error()), "password") {
		t.Fatalf("error exposes obsolete credential instructions: %v", err)
	}
	if !strings.Contains(err.Error(), "administrator prompt") {
		t.Fatalf("error = %v, want actionable macOS prompt instruction", err)
	}
}

func TestDirectHelperPermissionMapsSudoFailure(t *testing.T) {
	host := &directSystemVPNHost{runner: func(context.Context, ...string) ([]byte, error) {
		return []byte("sudo: a password is required\n"), context.DeadlineExceeded
	}}
	observation, err := host.Permission(context.Background())
	if observation.State != guiruntime.SystemVPNPermissionRequired {
		t.Fatalf("state = %q", observation.State)
	}
	if err == nil {
		t.Fatal("expected permission error")
	}
}

func TestMacOSVPNBackendDefaultsToDirect(t *testing.T) {
	if got := macOSVPNBackendFromEnv(""); got != "direct" {
		t.Fatalf("empty backend = %q", got)
	}
	if got := macOSVPNBackendFromEnv("networkextension"); got != "networkextension" {
		t.Fatalf("explicit backend = %q", got)
	}
	if got := macOSVPNBackendFromEnv("unknown"); got != "direct" {
		t.Fatalf("unknown backend = %q", got)
	}
}
