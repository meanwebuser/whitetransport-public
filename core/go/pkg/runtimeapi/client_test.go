package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientRuntimeEndpoints(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/runtime/v1/status", func(w http.ResponseWriter, r *http.Request) {
		requireMethod(t, r, http.MethodGet)
		writeTestJSON(t, w, Status{Role: "client", State: "disconnected", SocksListen: "127.0.0.1:8809", DiscoveredNodes: 2, AvailableNodes: 1})
	})
	mux.HandleFunc("/runtime/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		requireMethod(t, r, http.MethodGet)
		writeTestJSON(t, w, []Node{{NodeID: "node-1", Label: "Server 1", Available: true, Capabilities: []string{"stream"}}})
	})
	mux.HandleFunc("/runtime/v1/session/connect", func(w http.ResponseWriter, r *http.Request) {
		requireMethod(t, r, http.MethodPost)
		var body struct {
			NodeID string `json:"node_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode connect body: %v", err)
		}
		if body.NodeID != "node-1" {
			t.Fatalf("node_id = %q, want node-1", body.NodeID)
		}
		writeTestJSON(t, w, Status{Role: "client", State: "connected", ActiveNodeID: "node-1", SocksListen: "127.0.0.1:8809"})
	})
	mux.HandleFunc("/runtime/v1/session/disconnect", func(w http.ResponseWriter, r *http.Request) {
		requireMethod(t, r, http.MethodPost)
		writeTestJSON(t, w, Status{Role: "client", State: "disconnected"})
	})
	mux.HandleFunc("/runtime/v1/carriers", func(w http.ResponseWriter, r *http.Request) {
		requireMethod(t, r, http.MethodGet)
		writeTestJSON(t, w, map[string]CarrierSnapshot{"vk.messages": {CarrierID: "vk.messages", Healthy: true, Reliability: 1}})
	})
	mux.HandleFunc("/runtime/v1/build", func(w http.ResponseWriter, r *http.Request) {
		requireMethod(t, r, http.MethodGet)
		writeTestJSON(t, w, BuildInfo{Version: "dev", Commit: "abc123", Date: "2026-06-16"})
	})
	mux.HandleFunc("/runtime/v1/health/detailed", func(w http.ResponseWriter, r *http.Request) {
		requireMethod(t, r, http.MethodGet)
		writeTestJSON(t, w, DetailedHealth{
			Status:        Status{Role: "client", State: "connected"},
			UptimeSeconds: 42,
			Carriers:      map[string]CarrierSnapshot{"vk.messages": {CarrierID: "vk.messages", Healthy: true}},
			Sessions:      SessionHealth{Active: true, ActiveNodeID: "node-1"},
			Memory:        MemoryHealth{Goroutines: 12},
		})
	})
	mux.HandleFunc("/runtime/v1/plan", func(w http.ResponseWriter, r *http.Request) {
		requireMethod(t, r, http.MethodGet)
		if got := r.URL.Query().Get("traffic"); got != "egress" {
			t.Fatalf("traffic query = %q, want egress", got)
		}
		if got := r.URL.Query().Get("payload_bytes"); got != "4096" {
			t.Fatalf("payload_bytes query = %q, want 4096", got)
		}
		writeTestJSON(t, w, RoutePlan{
			TrafficClass:      "egress",
			Strategy:          "single",
			Primary:           DescriptorView{ID: "wbstream.vp8", Provider: "wbstream", Mode: "stream", Healthy: true},
			MirrorCount:       1,
			HedgeTimeoutMs:    200,
			MaxInFlightChunks: 8,
		})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL + "/runtime/")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	status, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.SocksListen != "127.0.0.1:8809" || status.AvailableNodes != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}

	nodes, err := client.Nodes(ctx)
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "node-1" || !nodes[0].Available {
		t.Fatalf("unexpected nodes: %+v", nodes)
	}

	connected, err := client.Connect(ctx, "node-1")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if connected.State != "connected" || connected.ActiveNodeID != "node-1" {
		t.Fatalf("unexpected connected status: %+v", connected)
	}

	disconnected, err := client.Disconnect(ctx)
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if disconnected.State != "disconnected" {
		t.Fatalf("unexpected disconnected status: %+v", disconnected)
	}

	carriers, err := client.Carriers(ctx)
	if err != nil {
		t.Fatalf("Carriers: %v", err)
	}
	if !carriers["vk.messages"].Healthy {
		t.Fatalf("unexpected carriers: %+v", carriers)
	}

	build, err := client.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if build.Commit != "abc123" {
		t.Fatalf("unexpected build info: %+v", build)
	}

	health, err := client.DetailedHealth(ctx)
	if err != nil {
		t.Fatalf("DetailedHealth: %v", err)
	}
	if !health.Sessions.Active || health.Memory.Goroutines != 12 {
		t.Fatalf("unexpected detailed health: %+v", health)
	}

	plan, err := client.Plan(ctx, "egress", 4096)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Primary.ID != "wbstream.vp8" || plan.MaxInFlightChunks != 8 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestClientReportsAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeTestJSON(t, w, map[string]string{"error": "runtime unavailable"})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Status(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Status error = %T %v, want APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable || apiErr.Message != "runtime unavailable" {
		t.Fatalf("unexpected APIError: %+v", apiErr)
	}
}

func TestClientReportsInvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{"))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode GET /v1/status response") {
		t.Fatalf("Status error = %v, want decode error", err)
	}
}

func TestClientHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		writeTestJSON(t, w, Status{State: "late"})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, WithTimeout(time.Second))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	_, err = client.Status(ctx)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("Status error = %v, want context deadline", err)
	}
}

func TestClientConnectUsesSessionTimeoutInsteadOfDefaultRequestTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/session/connect" {
			t.Fatalf("path = %s, want /v1/session/connect", r.URL.Path)
		}
		time.Sleep(30 * time.Millisecond)
		writeTestJSON(t, w, Status{State: "connected", ActiveNodeID: "node-1"})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, WithTimeout(5*time.Millisecond))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	status, err := client.Connect(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if status.State != "connected" || status.ActiveNodeID != "node-1" {
		t.Fatalf("status = %+v", status)
	}
}

func TestSessionConnectTimeoutCoversSlowProviderWindow(t *testing.T) {
	if sessionConnectTimeout < 5*time.Minute {
		t.Fatalf("session connect timeout = %v, want at least five minutes for delayed control carriers", sessionConnectTimeout)
	}
}

func TestNewClientValidation(t *testing.T) {
	t.Parallel()

	tests := []string{"", "127.0.0.1:17680", "ftp://127.0.0.1:17680", "http:///missing-host"}
	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			if _, err := NewClient(rawURL); err == nil {
				t.Fatalf("NewClient(%q) succeeded, want error", rawURL)
			}
		})
	}

	if _, err := NewClient("http://127.0.0.1:17680", WithHTTPClient(nil)); err == nil {
		t.Fatal("NewClient with nil HTTP client succeeded, want error")
	}
	if _, err := NewClient("http://127.0.0.1:17680", WithTimeout(0)); err == nil {
		t.Fatal("NewClient with zero timeout succeeded, want error")
	}
}

func requireMethod(t *testing.T, r *http.Request, expected string) {
	t.Helper()
	if r.Method != expected {
		t.Fatalf("method = %s, want %s", r.Method, expected)
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
