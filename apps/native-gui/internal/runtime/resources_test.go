package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResourceResolverReportsConfiguredAPIAndCandidates(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "core", "go"), 0o700); err != nil {
		t.Fatalf("mkdir core/go: %v", err)
	}
	tokenStore := filepath.Join(root, "secrets", "token-store.json")
	if err := os.MkdirAll(filepath.Dir(tokenStore), 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(tokenStore, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write token store: %v", err)
	}

	resolver := ResourceResolver{
		mode:           ModeLocalAPI,
		cwd:            filepath.Join(root, "apps", "native-gui"),
		executablePath: filepath.Join(root, "artifacts", "native-gui", "WhiteTransport-native-gui"),
		goos:           "linux",
		goarch:         "amd64",
		lookPath:       func(string) (string, error) { return "", errors.New("not found") },
	}
	summary := resolver.Resolve()
	if summary.Mode != ModeLocalAPI || summary.RuntimeAPIURL != DefaultRuntimeAPIURL {
		t.Fatalf("summary = %+v, want local api default", summary)
	}
	if summary.RepoRoot != root {
		t.Fatalf("RepoRoot = %q, want %q", summary.RepoRoot, root)
	}
	if len(summary.MissingRequired) != 0 {
		t.Fatalf("MissingRequired = %+v, want none", summary.MissingRequired)
	}
	if !hasCandidate(summary.Candidates, ResourceTokenStore, tokenStore, "found") {
		t.Fatalf("token-store candidates = %+v, want repo token store found", summary.Candidates)
	}
	if !hasCandidate(summary.Candidates, ResourceDaemonBinary, filepath.Join(root, "artifacts", "desktop", "whitetransportd-linux-x64"), "missing") {
		t.Fatalf("daemon candidates = %+v, want linux x64 artifact candidate", summary.Candidates)
	}
}

func TestResourceResolverCarriesBundledBootstrapSecretToManagedConfig(t *testing.T) {
	root := t.TempDir()
	execDir := filepath.Join(root, "Contents", "MacOS")
	resourcesDir := filepath.Join(root, "Contents", "Resources")
	if err := os.MkdirAll(execDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// macOS packaging stores the credentialed daemon config beside MacOS,
	// under Contents/Resources, rather than next to the executable.
	configPath := filepath.Join(resourcesDir, "daemon.json")
	if err := os.WriteFile(configPath, []byte(`{"role":"client","bootstrap_secret":"fixture-bootstrap-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := ResourceResolver{
		mode:           ModeManaged,
		cwd:            root,
		executablePath: filepath.Join(execDir, "WhiteTransport-native-gui"),
		goos:           "darwin",
		goarch:         "arm64",
		lookPath:       func(string) (string, error) { return "", errors.New("not found") },
	}
	summary := resolver.Resolve()
	if !hasCandidate(summary.Candidates, ResourceDaemonConfig, configPath, "found") {
		t.Fatalf("daemon config candidates = %+v, want macOS Contents/Resources config", summary.Candidates)
	}
	if summary.BootstrapSecret != "fixture-bootstrap-secret" {
		t.Fatalf("BootstrapSecret = %q, want bundled daemon config value", summary.BootstrapSecret)
	}
}

func TestResourceResolverHonorsEnvironmentOverrides(t *testing.T) {
	tokenStore := filepath.Join(t.TempDir(), "token-store.json")
	if err := os.WriteFile(tokenStore, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write token store: %v", err)
	}
	t.Setenv("WT_NATIVE_GUI_RUNTIME_API", "http://127.0.0.1:19090")
	t.Setenv("WT_NATIVE_GUI_TOKEN_STORE", tokenStore)

	summary := ResourceResolver{
		mode:           ModeLocalAPI,
		cwd:            t.TempDir(),
		executablePath: filepath.Join(t.TempDir(), "WhiteTransport-native-gui"),
		lookPath:       func(string) (string, error) { return "", errors.New("not found") },
	}.Resolve()
	if summary.RuntimeAPIURL != "http://127.0.0.1:19090" {
		t.Fatalf("RuntimeAPIURL = %q, want override", summary.RuntimeAPIURL)
	}
	if !hasCandidate(summary.Candidates, ResourceTokenStore, tokenStore, "found") {
		t.Fatalf("candidates = %+v, want env token store", summary.Candidates)
	}
}

func TestCurrentRuntimeModeHonorsFakeRuntime(t *testing.T) {
	t.Setenv("WT_NATIVE_GUI_FAKE_RUNTIME", "1")
	if mode := CurrentRuntimeMode(); mode != ModeFake {
		t.Fatalf("CurrentRuntimeMode = %q, want fake", mode)
	}
}

func TestCurrentRuntimeModeHonorsManagedRuntime(t *testing.T) {
	t.Setenv("WT_NATIVE_GUI_MANAGE_DAEMON", "1")
	if mode := CurrentRuntimeMode(); mode != ModeManaged {
		t.Fatalf("CurrentRuntimeMode = %q, want managed", mode)
	}
}

func TestManagedModeRequiresDaemonBinaryAndConfig(t *testing.T) {
	summary := ResourceResolver{
		mode:           ModeManaged,
		cwd:            t.TempDir(),
		executablePath: filepath.Join(t.TempDir(), "WhiteTransport-native-gui"),
		lookPath:       func(string) (string, error) { return "", errors.New("not found") },
	}.Resolve()
	if summary.SupervisorState != "configured" {
		t.Fatalf("SupervisorState = %q, want configured", summary.SupervisorState)
	}
	if !containsString(summary.MissingRequired, ResourceDaemonBinary) || !containsString(summary.MissingRequired, ResourceDaemonConfig) {
		t.Fatalf("MissingRequired = %+v, want daemon binary and config", summary.MissingRequired)
	}
}

func TestRepoDevConfigDoesNotSatisfyManagedRequirement(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "core", "go"), 0o700); err != nil {
		t.Fatalf("mkdir core/go: %v", err)
	}
	devConfig := filepath.Join(root, "config", "dev", "local-client-enhanced-simple.json")
	if err := os.MkdirAll(filepath.Dir(devConfig), 0o700); err != nil {
		t.Fatalf("mkdir config/dev: %v", err)
	}
	if err := os.WriteFile(devConfig, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write dev config: %v", err)
	}
	summary := ResourceResolver{
		mode:           ModeManaged,
		cwd:            filepath.Join(root, "apps", "native-gui"),
		executablePath: filepath.Join(root, "artifacts", "native-gui", "WhiteTransport-native-gui"),
		lookPath:       func(string) (string, error) { return "", errors.New("not found") },
	}.Resolve()
	if !hasCandidate(summary.Candidates, ResourceDaemonConfig, devConfig, "found") {
		t.Fatalf("candidates = %+v, want dev config diagnostic", summary.Candidates)
	}
	if !containsString(summary.MissingRequired, ResourceDaemonConfig) {
		t.Fatalf("MissingRequired = %+v, want daemon config still missing", summary.MissingRequired)
	}
}

func hasCandidate(candidates []RuntimeResourceCandidate, kind string, target string, status string) bool {
	for _, candidate := range candidates {
		if candidate.Kind == kind && candidate.Target == target && candidate.Status == status {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
