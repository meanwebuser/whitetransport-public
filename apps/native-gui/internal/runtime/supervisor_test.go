package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNewSupervisorPlanUsesFirstFoundBinaryAndConfig(t *testing.T) {
	resources := RuntimeResourceSummary{
		RuntimeAPIURL: "http://127.0.0.1:19090",
		Candidates: []RuntimeResourceCandidate{
			{Kind: ResourceDaemonBinary, Target: "/missing/daemon", Status: "missing"},
			{Kind: ResourceDaemonBinary, Target: "/bin/daemon", Status: "found"},
			{Kind: ResourceDaemonConfig, Target: filepath.Join(t.TempDir(), "daemon.json"), Status: "found"},
		},
	}
	plan, err := NewSupervisorPlan(resources)
	if err != nil {
		t.Fatalf("NewSupervisorPlan: %v", err)
	}
	if plan.BinaryPath != "/bin/daemon" || plan.APIURL != "http://127.0.0.1:19090" {
		t.Fatalf("plan = %+v, want first found daemon and api", plan)
	}
}

func TestNewDaemonCommandRunsFromBundledBinaryDirectory(t *testing.T) {
	binaryDir := filepath.Join(t.TempDir(), "WhiteTransport.app", "Contents", "MacOS")
	plan := SupervisorPlan{
		BinaryPath: filepath.Join(binaryDir, "whitetransportd"),
		ConfigPath: filepath.Join(binaryDir, "..", "Resources", "daemon.json"),
	}
	cmd := newDaemonCommand(context.Background(), plan)
	if cmd.Dir != binaryDir {
		t.Fatalf("daemon command dir = %q, want %q", cmd.Dir, binaryDir)
	}
}

func TestNewSupervisorPlanIgnoresRepoDevConfig(t *testing.T) {
	_, err := NewSupervisorPlan(RuntimeResourceSummary{
		RuntimeAPIURL: "http://127.0.0.1:19090",
		Candidates: []RuntimeResourceCandidate{
			{Kind: ResourceDaemonBinary, Target: "/bin/daemon", Status: "found"},
			{Kind: ResourceDaemonConfig, Source: "repo:dev-config", Target: "/repo/config/dev/local-client-enhanced-simple.json", Status: "found"},
		},
	})
	if err == nil {
		t.Fatal("NewSupervisorPlan succeeded with repo dev config, want explicit config error")
	}
}

func TestNewSupervisorPlanRequiresBinaryAndConfig(t *testing.T) {
	_, err := NewSupervisorPlan(RuntimeResourceSummary{
		RuntimeAPIURL: "http://127.0.0.1:19090",
		Candidates: []RuntimeResourceCandidate{
			{Kind: ResourceDaemonBinary, Target: "/bin/daemon", Status: "found"},
		},
	})
	if err == nil {
		t.Fatal("NewSupervisorPlan succeeded without config, want error")
	}
}

func TestParsePositiveDurationMillis(t *testing.T) {
	if got := parsePositiveDurationMillis("250"); got.String() != "250ms" {
		t.Fatalf("duration = %s, want 250ms", got)
	}
	if got := parsePositiveDurationMillis("-1"); got != 0 {
		t.Fatalf("duration = %s, want zero", got)
	}
}

func TestDaemonEnvironmentAddsLaunchdAgentSocketWhenInheritedEnvironmentLacksIt(t *testing.T) {
	env := daemonEnvironment([]string{"PATH=/usr/bin"}, "/private/tmp/launchd-agent.sock")
	if !slices.Contains(env, "SSH_AUTH_SOCK=/private/tmp/launchd-agent.sock") {
		t.Fatalf("daemon environment = %v, want launchd SSH agent socket", env)
	}
	if !slices.Contains(env, "WT_DEBUG=1") {
		t.Fatalf("daemon environment = %v, want WT_DEBUG", env)
	}
}

func TestDaemonEnvironmentPreservesInheritedAgentSocket(t *testing.T) {
	env := daemonEnvironment([]string{"SSH_AUTH_SOCK=/tmp/inherited.sock"}, "/private/tmp/launchd-agent.sock")
	if !slices.Contains(env, "SSH_AUTH_SOCK=/tmp/inherited.sock") {
		t.Fatalf("daemon environment = %v, want inherited socket", env)
	}
	if slices.Contains(env, "SSH_AUTH_SOCK=/private/tmp/launchd-agent.sock") {
		t.Fatalf("daemon environment overwrote inherited socket: %v", env)
	}
}

func TestDaemonStderrLogClassifiesProviderErrorsWithoutErrStreamLabel(t *testing.T) {
	logs, err := NewLogSink(filepath.Join(t.TempDir(), "WhiteTransport.log"))
	if err != nil {
		t.Fatalf("NewLogSink: %v", err)
	}
	supervisor := &DaemonSupervisor{logs: logs}
	supervisor.pipeLog("whitetransportd:err", strings.NewReader("[DBG] cursor: set binding=vk.messages cursor=1\n[DBG] create-room: status 401 invalid_token\n"))
	entries, err := logs.ReadLines(10)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %#v, want two daemon lines", entries)
	}
	if entries[0].Level != "error" || entries[0].Message != "whitetransportd" || entries[0].Fields["stream"] != "stderr" {
		t.Fatalf("provider error entry = %#v, want error without err stream label", entries[0])
	}
	if entries[1].Level != "info" || entries[1].Message != "whitetransportd" || entries[1].Fields["stream"] != "stderr" {
		t.Fatalf("debug entry = %#v, want info without err stream label", entries[1])
	}
}

func TestDaemonSupervisorRejectsHealthyPreexistingAPI(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-started")
	binary := filepath.Join(t.TempDir(), "fake-daemon.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf started > %q\nexit 1\n", marker)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake daemon: %v", err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	logs := NewDisabledLogSink()
	supervisor, err := NewDaemonSupervisor(RuntimeResourceSummary{
		RuntimeAPIURL: api.URL,
		Candidates: []RuntimeResourceCandidate{
			{Kind: ResourceDaemonBinary, Target: binary, Status: "found", Exists: true},
			{Kind: ResourceDaemonConfig, Target: filepath.Join(t.TempDir(), "daemon.json"), Status: "found", Exists: true},
		},
	}, logs)
	if err != nil {
		t.Fatalf("NewDaemonSupervisor: %v", err)
	}
	if _, err := supervisor.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded against healthy preexisting API; stale daemon could be mistaken for owned child")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("Start launched child despite preexisting API occupancy")
	}
}
