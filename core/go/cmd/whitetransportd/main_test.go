package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
)

func TestResolveConfigPathUsesExplicitPath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, diag, err := resolveConfigPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != configPath || diag.SelectedPath != configPath {
		t.Fatalf("expected explicit config path, got resolved=%q diag=%+v", resolved, diag)
	}
	if len(diag.Candidates) != 1 || diag.Candidates[0].Source != "--config" || !diag.Candidates[0].Exists {
		t.Fatalf("unexpected explicit diagnostics: %+v", diag.Candidates)
	}
}

func TestResolveConfigPathUsesEnvironmentPath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "env-daemon.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_CONFIG", configPath)
	t.Setenv("WT_CONFIG_PATH", "")
	t.Setenv("WHITETRANSPORTD_CONFIG", "")

	resolved, diag, err := resolveConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != configPath || diag.SelectedPath != configPath {
		t.Fatalf("expected env config path, got resolved=%q diag=%+v", resolved, diag)
	}
}

func TestDefaultConfigCandidatesContainOnlyGenericTopology(t *testing.T) {
	t.Setenv("WT_CONFIG", "")
	t.Setenv("WT_CONFIG_PATH", "")
	t.Setenv("WHITETRANSPORTD_CONFIG", "")

	candidates := defaultConfigCandidates()
	wantPaths := map[string]bool{
		"/etc/whitetransport/config.json":                 false,
		"/opt/white-transport/config-managed/config.json": false,
	}
	for _, candidate := range candidates {
		path := filepath.ToSlash(candidate.Path)
		lowerPath := strings.ToLower(path)
		for _, privateLabel := range []string{"private-node", "/ops/white-transport/"} {
			if strings.Contains(lowerPath, privateLabel) {
				t.Fatalf("default config candidate exposes deployment topology %q: %s", privateLabel, path)
			}
		}
		if _, required := wantPaths[path]; required {
			wantPaths[path] = true
		}
	}
	for path, found := range wantPaths {
		if !found {
			t.Errorf("generic config candidate %q is missing", path)
		}
	}
}

func TestWriteConfigDiagnosticsIncludesCandidates(t *testing.T) {
	diag := configDiagnostics{
		Candidates: []configCandidate{{
			Source: "default",
			Path:   "/missing/config.json",
			Exists: false,
		}},
	}
	var output bytes.Buffer

	writeConfigDiagnostics(&output, diag)

	text := output.String()
	if !strings.Contains(text, "config selected: (none)") || !strings.Contains(text, "/missing/config.json") {
		t.Fatalf("diagnostics did not include selected state and candidate path: %q", text)
	}
}

func TestLoadDispatchPayloadRequiresOneSource(t *testing.T) {
	if _, err := loadDispatchPayload("", "", ""); err == nil {
		t.Fatal("expected missing payload source error")
	}
	if _, err := loadDispatchPayload("hello", "aGVsbG8=", ""); err == nil {
		t.Fatal("expected multiple payload source error")
	}
	payload, err := loadDispatchPayload("", "aGVsbG8=", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "hello" {
		t.Fatalf("expected decoded payload, got %q", string(payload))
	}
}

func TestLoadDispatchPayloadReadsFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "payload-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("file-payload"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	payload, err := loadDispatchPayload("", "", file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "file-payload" {
		t.Fatalf("expected file payload, got %q", string(payload))
	}
}

func TestWriteDispatchSummaryDoesNotIncludePayloadBytes(t *testing.T) {
	plan := policy.RoutePlan{
		TrafficClass: fabric.TrafficControl,
		Strategy:     policy.DeliveryMirrored,
		Primary:      descriptor(t, carriers.CarrierVKMessages),
	}
	report := runtime.DispatchReport{
		Plan: plan,
		Placements: []policy.ChunkPlacement{{
			Index:            0,
			Offset:           0,
			Size:             5,
			CarrierID:        carriers.CarrierVKMessages,
			MirrorCarrierIDs: []string{carriers.CarrierOKMessages},
		}},
		Result: policy.DispatchResult{ImmediateWrites: 2},
	}
	var output bytes.Buffer

	if err := writeDispatchSummary(&output, "dispatch-1", 5, report); err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["id"] != "dispatch-1" || decoded["strategy"] != string(policy.DeliveryMirrored) {
		t.Fatalf("bad summary: %+v", decoded)
	}
	if _, ok := decoded["payload"]; ok {
		t.Fatalf("dispatch summary must not print payload: %s", output.String())
	}
	if decoded["payload_bytes"].(float64) != 5 {
		t.Fatalf("expected payload size only, got %+v", decoded)
	}
}

func descriptor(t *testing.T, id string) carriers.Descriptor {
	t.Helper()
	desc, err := carriers.FindStandardDescriptor(id)
	if err != nil {
		t.Fatal(err)
	}
	return desc
}
