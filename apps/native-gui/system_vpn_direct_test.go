package main

import (
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

func TestDirectHelperPermissionUsesSupportedStatusCommand(t *testing.T) {
	var commands []string
	host := &directSystemVPNHost{
		executable: "/Users/alice/Library/Application Support/WhiteTransport/bin/direct-helper",
		configPath: "/Users/alice/Library/Application Support/WhiteTransport/direct-helper/config.json",
		authMode:   "sudo-noninteractive",
		runner: func(_ context.Context, args ...string) ([]byte, error) {
			commands = append(commands, args[4])
			return []byte(`{"ok":true,"command":"status","message":"stopped"}`), nil
		},
	}
	if _, err := host.Permission(context.Background()); err != nil {
		t.Fatalf("Permission() error = %v", err)
	}
	if len(commands) != 1 || commands[0] != "status" {
		t.Fatalf("Permission() commands = %#v, want one supported status command", commands)
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
