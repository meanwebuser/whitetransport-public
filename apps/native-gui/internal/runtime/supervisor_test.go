package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
