package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

const (
	// DefaultRuntimeAPIURL is the local whitetransportd API endpoint used by
	// the native GUI until the managed daemon supervisor is implemented.
	DefaultRuntimeAPIURL = "http://127.0.0.1:17680"

	ModeFake     = "fake"
	ModeLocalAPI = "local-api"
	ModeManaged  = "managed-daemon"

	ResourceRuntimeAPI   = "runtime-api"
	ResourceDaemonBinary = "daemon-binary"
	ResourceTokenStore   = "token-store"
	ResourceDaemonConfig = "daemon-config"
	ResourceSingBox      = "sing-box"
)

// RuntimeResourceCandidate describes one path or endpoint probed by the
// native GUI. Targets are paths except for runtime-api, where target is a URL.
type RuntimeResourceCandidate struct {
	Kind       string `json:"kind"`
	Source     string `json:"source"`
	Target     string `json:"target"`
	Exists     bool   `json:"exists"`
	Executable bool   `json:"executable"`
	Required   bool   `json:"required"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// RuntimeResourceSummary is the debug-safe startup resource snapshot shown in
// settings and written to the native GUI log.
type RuntimeResourceSummary struct {
	Mode              string                     `json:"mode"`
	RuntimeAPIURL     string                     `json:"runtime_api_url"`
	SupervisorState   string                     `json:"supervisor_state"`
	WorkingDirectory  string                     `json:"working_directory,omitempty"`
	ExecutablePath    string                     `json:"executable_path,omitempty"`
	RepoRoot          string                     `json:"repo_root,omitempty"`
	Candidates        []RuntimeResourceCandidate `json:"candidates"`
	MissingRequired   []string                   `json:"missing_required,omitempty"`
	DiagnosticsNotice string                     `json:"diagnostics_notice,omitempty"`
	// BootstrapSecret is kept out of diagnostics serialization; it is carried
	// from a bundled daemon config into managed-config regeneration in memory.
	BootstrapSecret string `json:"-"`
}

// ResourceResolver resolves debug-safe native runtime paths without reading
// secret contents.
type ResourceResolver struct {
	mode           string
	cwd            string
	executablePath string
	goos           string
	goarch         string
	lookPath       func(string) (string, error)
}

// CurrentRuntimeMode returns the native GUI mode selected by environment.
// When no explicit mode is set, it auto-detects: if the daemon binary is found
// next to the executable (bundled in .app), managed mode is used so the app
// starts and supervises whitetransportd automatically.
func CurrentRuntimeMode() string {
	if os.Getenv("WT_NATIVE_GUI_FAKE_RUNTIME") == "1" {
		return ModeFake
	}
	if os.Getenv("WT_NATIVE_GUI_MANAGE_DAEMON") == "1" {
		return ModeManaged
	}
	if os.Getenv("WT_NATIVE_GUI_LOCAL_API") == "1" {
		return ModeLocalAPI
	}
	// Auto-detect: if whitetransportd binary exists next to the executable,
	// run in managed mode (app manages the daemon lifecycle).
	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		for _, name := range []string{"whitetransportd", "whitetransportd.exe"} {
			path := filepath.Join(execDir, name)
			if info, err := os.Stat(path); err == nil && executableFileForOS(info, path, goruntime.GOOS) {
				return ModeManaged
			}
		}
	}
	return ModeLocalAPI
}

// RuntimeAPIURL returns the configured local runtime API endpoint.
func RuntimeAPIURL() string {
	if value := strings.TrimSpace(os.Getenv("WT_NATIVE_GUI_RUNTIME_API")); value != "" {
		return value
	}
	return DefaultRuntimeAPIURL
}

// NewResourceResolver creates a resolver for the current process.
func NewResourceResolver(mode string) ResourceResolver {
	cwd, _ := os.Getwd()
	executablePath, _ := os.Executable()
	return ResourceResolver{
		mode:           normalizeRuntimeMode(mode),
		cwd:            cwd,
		executablePath: executablePath,
		goos:           goruntime.GOOS,
		goarch:         goruntime.GOARCH,
		lookPath:       exec.LookPath,
	}
}

// ResolveRuntimeResources returns the current process resource summary.
func ResolveRuntimeResources(mode string) RuntimeResourceSummary {
	return NewResourceResolver(mode).Resolve()
}

// Resolve inspects runtime resource candidates and reports their existence.
func (r ResourceResolver) Resolve() RuntimeResourceSummary {
	resolver := r.withDefaults()
	repoRoot := findRepoRoot(resolver.cwd)
	summary := RuntimeResourceSummary{
		Mode:             resolver.mode,
		RuntimeAPIURL:    RuntimeAPIURL(),
		WorkingDirectory: resolver.cwd,
		ExecutablePath:   resolver.executablePath,
		RepoRoot:         repoRoot,
	}
	if resolver.mode == ModeFake {
		summary.SupervisorState = "disabled_fake_runtime"
		summary.DiagnosticsNotice = "Fake runtime mode is active; daemon resources are diagnostics only."
	} else if resolver.mode == ModeManaged {
		summary.SupervisorState = "configured"
		summary.DiagnosticsNotice = "Managed daemon mode is active; whitetransportd starts from the first found daemon binary and config candidates."
	}

	summary.Candidates = append(summary.Candidates, RuntimeResourceCandidate{
		Kind:     ResourceRuntimeAPI,
		Source:   runtimeAPISource(),
		Target:   summary.RuntimeAPIURL,
		Exists:   true,
		Required: resolver.mode == ModeLocalAPI,
		Status:   "configured",
	})
	managedRequired := resolver.mode == ModeManaged
	summary.Candidates = append(summary.Candidates, resolver.fileCandidates(ResourceDaemonBinary, managedRequired, true, daemonBinaryCandidates(resolver, repoRoot))...)
	summary.Candidates = append(summary.Candidates, resolver.fileCandidates(ResourceTokenStore, false, false, tokenStoreCandidates(resolver, repoRoot))...)
	summary.Candidates = append(summary.Candidates, resolver.fileCandidates(ResourceDaemonConfig, managedRequired, false, daemonConfigCandidates(resolver, repoRoot))...)
	summary.BootstrapSecret = resolver.bootstrapSecretFromCandidates(summary.Candidates)
	summary.Candidates = append(summary.Candidates, resolver.fileCandidates(ResourceSingBox, false, true, singBoxCandidates(resolver, repoRoot))...)
	summary.MissingRequired = missingRequiredKinds(summary.Candidates)
	return summary
}

func (r ResourceResolver) bootstrapSecretFromCandidates(candidates []RuntimeResourceCandidate) string {
	for _, candidate := range candidates {
		if candidate.Kind != ResourceDaemonConfig || candidate.Status != "found" {
			continue
		}
		if secret := bootstrapSecretFromConfigPath(candidate.Target); secret != "" {
			return secret
		}
	}
	return ""
}

func bootstrapSecretFromConfigPath(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var config struct {
		BootstrapSecret string `json:"bootstrap_secret"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return ""
	}
	return strings.TrimSpace(config.BootstrapSecret)
}

func (r ResourceResolver) withDefaults() ResourceResolver {
	out := r
	out.mode = normalizeRuntimeMode(out.mode)
	if strings.TrimSpace(out.goos) == "" {
		out.goos = goruntime.GOOS
	}
	if strings.TrimSpace(out.goarch) == "" {
		out.goarch = goruntime.GOARCH
	}
	if out.lookPath == nil {
		out.lookPath = exec.LookPath
	}
	return out
}

func (r ResourceResolver) fileCandidates(kind string, required bool, requireExecutable bool, candidates []pathCandidate) []RuntimeResourceCandidate {
	out := make([]RuntimeResourceCandidate, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		target := strings.TrimSpace(candidate.target)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, inspectCandidate(kind, candidate.source, target, required, requireExecutable, r.goos))
	}
	return out
}

type pathCandidate struct {
	source string
	target string
}

func daemonBinaryCandidates(r ResourceResolver, repoRoot string) []pathCandidate {
	var candidates []pathCandidate
	candidates = appendEnvCandidate(candidates, "WT_NATIVE_GUI_DAEMON_BINARY")
	execDir := filepath.Dir(r.executablePath)
	daemonName := "whitetransportd"
	if r.goos == "windows" {
		daemonName += ".exe"
	}
	candidates = append(candidates,
		pathCandidate{source: "bundle", target: filepath.Join(execDir, daemonName)},
		pathCandidate{source: "bundle:resources", target: filepath.Join(execDir, "resources", daemonName)},
		pathCandidate{source: "cwd", target: filepath.Join(r.cwd, daemonName)},
	)
	if repoRoot != "" {
		candidates = append(candidates,
			pathCandidate{source: "repo:artifacts", target: filepath.Join(repoRoot, "artifacts", "desktop", daemonArtifactName(r.goos, r.goarch))},
			pathCandidate{source: "repo:core-go", target: filepath.Join(repoRoot, "core", "go", "whitetransportd")},
		)
	}
	return candidates
}

func tokenStoreCandidates(r ResourceResolver, repoRoot string) []pathCandidate {
	var candidates []pathCandidate
	candidates = appendEnvCandidate(candidates, "WT_NATIVE_GUI_TOKEN_STORE")
	execDir := filepath.Dir(r.executablePath)
	candidates = append(candidates,
		pathCandidate{source: "bundle", target: filepath.Join(execDir, "token-store.json")},
		pathCandidate{source: "bundle:resources", target: filepath.Join(execDir, "resources", "token-store.json")},
		pathCandidate{source: "cwd", target: filepath.Join(r.cwd, "secrets", "token-store.json")},
	)
	if repoRoot != "" {
		candidates = append(candidates, pathCandidate{source: "repo:secrets", target: filepath.Join(repoRoot, "secrets", "token-store.json")})
	}
	return candidates
}

func daemonConfigCandidates(r ResourceResolver, repoRoot string) []pathCandidate {
	var candidates []pathCandidate
	candidates = appendEnvCandidate(candidates, "WT_NATIVE_GUI_DAEMON_CONFIG")
	execDir := filepath.Dir(r.executablePath)
	candidates = append(candidates,
		pathCandidate{source: "bundle", target: filepath.Join(execDir, "daemon.json")},
		pathCandidate{source: "bundle:config", target: filepath.Join(execDir, "config", "daemon.json")},
		pathCandidate{source: "bundle:resources", target: filepath.Join(execDir, "resources", "daemon.json")},
		pathCandidate{source: "cwd", target: filepath.Join(r.cwd, "daemon.json")},
	)
	if r.goos == "darwin" {
		// Wails places credentialed bundle data in Contents/Resources while the
		// executable runs from Contents/MacOS.
		candidates = append(candidates, pathCandidate{
			source: "bundle:contents-resources",
			target: filepath.Join(execDir, "..", "Resources", "daemon.json"),
		})
	}
	if repoRoot != "" {
		candidates = append(candidates, pathCandidate{source: "repo:dev-config", target: filepath.Join(repoRoot, "config", "dev", "local-client-enhanced-simple.json")})
	}
	return candidates
}

func singBoxCandidates(r ResourceResolver, repoRoot string) []pathCandidate {
	var candidates []pathCandidate
	candidates = appendEnvCandidate(candidates, "WT_NATIVE_GUI_SING_BOX")
	execDir := filepath.Dir(r.executablePath)
	singBoxName := "sing-box"
	if r.goos == "windows" {
		singBoxName += ".exe"
	}
	candidates = append(candidates,
		pathCandidate{source: "bundle", target: filepath.Join(execDir, singBoxName)},
		pathCandidate{source: "bundle:resources", target: filepath.Join(execDir, "resources", singBoxName)},
	)
	if repoRoot != "" {
		candidates = append(candidates, pathCandidate{source: "repo:artifacts", target: filepath.Join(repoRoot, "artifacts", "desktop", singBoxArtifactName(r.goos, r.goarch))})
	}
	if found, err := r.lookPath("sing-box"); err == nil && strings.TrimSpace(found) != "" {
		candidates = append(candidates, pathCandidate{source: "PATH", target: found})
	}
	return candidates
}

func appendEnvCandidate(candidates []pathCandidate, envName string) []pathCandidate {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return append(candidates, pathCandidate{source: "env:" + envName, target: value})
	}
	return candidates
}

func inspectCandidate(kind string, source string, target string, required bool, requireExecutable bool, goos ...string) RuntimeResourceCandidate {
	candidate := RuntimeResourceCandidate{Kind: kind, Source: source, Target: target, Required: required}
	info, err := os.Stat(target)
	if err != nil {
		candidate.Status = "missing"
		if !os.IsNotExist(err) {
			candidate.Error = err.Error()
		}
		return candidate
	}
	candidate.Exists = true
	if info.IsDir() {
		candidate.Status = "directory"
		candidate.Error = "expected file, got directory"
		return candidate
	}
	platform := goruntime.GOOS
	if len(goos) > 0 {
		platform = goos[0]
	}
	candidate.Executable = executableFileForOS(info, target, platform)
	if requireExecutable && !candidate.Executable {
		candidate.Status = "not-executable"
		return candidate
	}
	candidate.Status = "found"
	return candidate
}

func executableFileForOS(info os.FileInfo, target, goos string) bool {
	return info.Mode()&0o111 != 0 || (goos == "windows" && strings.EqualFold(filepath.Ext(target), ".exe"))
}

func missingRequiredKinds(candidates []RuntimeResourceCandidate) []string {
	requiredByKind := map[string]bool{}
	foundByKind := map[string]bool{}
	for _, candidate := range candidates {
		if !candidate.Required {
			continue
		}
		requiredByKind[candidate.Kind] = true
		if candidateCanSatisfyRequired(candidate) {
			foundByKind[candidate.Kind] = true
		}
	}
	missing := make([]string, 0)
	for kind := range requiredByKind {
		if !foundByKind[kind] {
			missing = append(missing, kind)
		}
	}
	return missing
}

func candidateCanSatisfyRequired(candidate RuntimeResourceCandidate) bool {
	if candidate.Status != "configured" && candidate.Status != "found" {
		return false
	}
	// Repo dev configs are useful diagnostics, but they can use different
	// listen ports and test-only assumptions. Managed startup requires an
	// explicit env, cwd, or bundled config until config generation exists.
	if candidate.Kind == ResourceDaemonConfig && candidate.Source == "repo:dev-config" {
		return false
	}
	return true
}

// WithGeneratedDaemonConfig replaces the previous managed config candidate and
// recomputes required resource state. Replacing rather than appending keeps the
// supervisor plan tied to the newest client-owned credential import.
func (s RuntimeResourceSummary) WithGeneratedDaemonConfig(path string) RuntimeResourceSummary {
	candidates := make([]RuntimeResourceCandidate, 0, len(s.Candidates)+1)
	for _, candidate := range s.Candidates {
		if candidate.Kind == ResourceDaemonConfig && (candidate.Source == "generated" || strings.HasPrefix(candidate.Source, "bundle")) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	s.Candidates = append(candidates, inspectCandidate(ResourceDaemonConfig, "generated", path, s.Mode == ModeManaged, false))
	if secret := bootstrapSecretFromConfigPath(path); secret != "" {
		s.BootstrapSecret = secret
	}
	s.MissingRequired = missingRequiredKinds(s.Candidates)
	return s
}

func runtimeAPISource() string {
	if strings.TrimSpace(os.Getenv("WT_NATIVE_GUI_RUNTIME_API")) != "" {
		return "env:WT_NATIVE_GUI_RUNTIME_API"
	}
	return "default"
}

func normalizeRuntimeMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case ModeFake:
		return ModeFake
	case ModeManaged:
		return ModeManaged
	default:
		return ModeLocalAPI
	}
}

// FirstFoundCandidate returns the first usable candidate for a resource kind.
func (s RuntimeResourceSummary) FirstFoundCandidate(kind string) (RuntimeResourceCandidate, bool) {
	for _, candidate := range s.Candidates {
		if candidate.Kind == kind && (candidate.Status == "found" || candidate.Status == "configured") {
			return candidate, true
		}
	}
	return RuntimeResourceCandidate{}, false
}

func findRepoRoot(start string) string {
	current := strings.TrimSpace(start)
	for current != "" {
		if fileExists(filepath.Join(current, "package.json")) && dirExists(filepath.Join(current, "core", "go")) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func daemonArtifactName(goos string, goarch string) string {
	return fmt.Sprintf("whitetransportd-%s-%s", goos, artifactArch(goarch))
}

func singBoxArtifactName(goos string, goarch string) string {
	return fmt.Sprintf("sing-box-%s-%s", goos, artifactArch(goarch))
}

func artifactArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	default:
		return goarch
	}
}
