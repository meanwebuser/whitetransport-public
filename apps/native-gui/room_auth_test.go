package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

type recordingRoomRuntime struct {
	configJSON  string
	sessionJSON string
	startErr    error
	starts      int
	stops       int
}

func (r *recordingRoomRuntime) StartTransportWithLocalSession(configJSON, sessionJSON string) error {
	r.starts++
	r.configJSON = configJSON
	r.sessionJSON = sessionJSON
	return r.startErr
}

func (r *recordingRoomRuntime) StopTransport() { r.stops++ }

func TestParseRoomAuthPayloadAcceptsCompleteWBStreamSession(t *testing.T) {
	payload, err := parseRoomAuthPayload([]byte(`{"platform":"wbstream","access_token":"local-access","cookie_header":"x_wbaas_token=local-cookie"}`))
	if err != nil {
		t.Fatalf("parseRoomAuthPayload: %v", err)
	}
	if payload.Platform != "wbstream" || payload.AccessToken != "local-access" || payload.CookieHeader == "" {
		t.Fatalf("unexpected normalized payload: %+v", payload)
	}
}

func TestParseRoomAuthPayloadRejectsWrongOrIncompleteProvider(t *testing.T) {
	for _, raw := range []string{
		`{"platform":"dion","access_token":"local-access","cookie_header":"local-cookie"}`,
		`{"platform":"wbstream","access_token":"","cookie_header":"local-cookie"}`,
		`not-json`,
	} {
		if _, err := parseRoomAuthPayload([]byte(raw)); err == nil {
			t.Fatalf("expected invalid payload %q to fail", raw)
		}
	}
}

func TestStartRoomRuntimeUsesSharedMemoryOnlySessionContract(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "runtime")
	configPath := filepath.Join(root, "daemon.json")
	t.Setenv("WT_NATIVE_GUI_CONFIG_DIR", configDir)
	if err := os.WriteFile(configPath, []byte(`{"role":"client","carrier_configs":[{"id":"wbstream","carrier_type":"wbstream","wbstream":{}}]}`), 0o600); err != nil {
		t.Fatalf("write base daemon config: %v", err)
	}
	supervisor := &recordingSupervisor{}
	app, err := newApp(&stubRuntimeService{}, guiruntime.RuntimeResourceSummary{
		Mode: guiruntime.ModeManaged,
		Candidates: []guiruntime.RuntimeResourceCandidate{{
			Kind: guiruntime.ResourceDaemonConfig, Target: configPath, Status: "found", Exists: true,
		}},
	}, newTestLogSink(t), supervisor)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	runtime := &recordingRoomRuntime{}
	app.roomRuntime = runtime

	if err := app.startRoomRuntime(roomAuthPayload{
		Platform: "wbstream", AccessToken: "local-access", CookieHeader: "x_wbaas_token=local-cookie",
	}); err != nil {
		t.Fatalf("startRoomRuntime: %v", err)
	}
	if supervisor.stops != 1 || runtime.starts != 1 || !app.roomRuntimeActive {
		t.Fatalf("unexpected runtime activation: stops=%d starts=%d active=%t", supervisor.stops, runtime.starts, app.roomRuntimeActive)
	}
	if _, err := os.Stat(filepath.Join(configDir, "client-tokens.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("client-tokens.json = %v, want absent", err)
	}
	var session map[string]string
	if err := json.Unmarshal([]byte(runtime.sessionJSON), &session); err != nil {
		t.Fatalf("decode shared session JSON: %v", err)
	}
	if session["platform"] != "wbstream" || session["access_token"] != "local-access" || session["cookie_header"] != "x_wbaas_token=local-cookie" {
		t.Fatalf("shared session = %#v", session)
	}
	if string(runtime.configJSON) == "" || string(runtime.configJSON) == runtime.sessionJSON {
		t.Fatalf("runtime config was not kept separate from the local session")
	}
}

func TestDisconnectClearsInMemoryRoomRuntime(t *testing.T) {
	app, err := NewApp(&stubRuntimeService{}, newTestLogSink(t))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	runtime := &recordingRoomRuntime{}
	app.roomRuntime = runtime
	app.roomRuntimeActive = true

	if _, err := app.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if runtime.stops != 1 || app.roomRuntimeActive {
		t.Fatalf("memory room runtime cleanup = stops=%d active=%t", runtime.stops, app.roomRuntimeActive)
	}
}
