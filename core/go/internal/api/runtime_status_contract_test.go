package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

func TestRuntimeStatusUsesPublicEndpointJSONContract(t *testing.T) {
	control := &stubRuntimeControl{status: runtime.StatusView{
		State: "connected",
		EgressEndpoints: []carriers.Endpoint{{
			ID:       "xray-de-httpupgrade",
			Carrier:  carriers.CarrierSingBoxVLESS,
			Address:  "de.example.test:443",
			Metadata: map[string]string{"network": "httpupgrade"},
		}},
	}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()

	NewRuntimeHandler(nil, policy.DefaultAdaptivePolicy(), control, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode status fields: %v", err)
	}
	if _, ok := fields["session_active"]; !ok {
		t.Fatalf("status response is missing required session_active: %s", response.Body.String())
	}
	var body struct {
		EgressEndpoints []map[string]json.RawMessage `json:"egress_endpoints"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if len(body.EgressEndpoints) != 1 {
		t.Fatalf("egress endpoint count = %d, want 1", len(body.EgressEndpoints))
	}
	endpoint := body.EgressEndpoints[0]
	for _, field := range []string{"id", "carrier", "address", "metadata"} {
		if _, ok := endpoint[field]; !ok {
			t.Errorf("public endpoint is missing lowercase field %q: %s", field, response.Body.String())
		}
	}
	for _, leakedField := range []string{"ID", "Carrier", "Address", "Metadata"} {
		if _, ok := endpoint[leakedField]; ok {
			t.Errorf("internal protocol field %q leaked into public endpoint: %s", leakedField, response.Body.String())
		}
	}
}

func TestRuntimeStatusPublishesAtomicSystemVPNProfileContract(t *testing.T) {
	control := &stubRuntimeControl{status: runtime.StatusView{
		State: "connected",
		SystemVPNProfile: &runtimeapi.SystemVPNProfile{
			SchemaRevision:   runtimeapi.SystemVPNProfileSchemaRevision,
			DaemonInstanceID: "daemon-1",
			ProfileRevision:  9,
			ProfileHash:      "hash",
			SessionID:        "session-1",
			SelectedNodeID:   "node-1",
			Ready:            true,
		},
		SystemVPNProfileReadiness: &runtimeapi.SystemVPNReadiness{Ready: true, Provenance: "test"},
	}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()

	NewRuntimeHandler(nil, policy.DefaultAdaptivePolicy(), control, nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	var body struct {
		Profile   map[string]json.RawMessage `json:"system_vpn_profile"`
		Readiness map[string]json.RawMessage `json:"system_vpn_profile_readiness"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	for _, field := range []string{"daemon_instance_id", "profile_revision", "profile_hash", "session_id", "ready"} {
		if _, ok := body.Profile[field]; !ok {
			t.Errorf("profile missing %q: %s", field, response.Body.String())
		}
	}
	if _, ok := body.Readiness["ready"]; !ok {
		t.Fatalf("readiness missing ready: %s", response.Body.String())
	}
}

func TestRuntimeStatusReportsActiveSession(t *testing.T) {
	control := &stubRuntimeControl{status: runtime.StatusView{
		Role:          config.RoleNode,
		State:         "running",
		SessionID:     "node-session-1",
		SessionActive: true,
	}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()

	NewRuntimeHandler(nil, policy.DefaultAdaptivePolicy(), control, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	var body struct {
		SessionActive bool   `json:"session_active"`
		SessionID     string `json:"session_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !body.SessionActive {
		t.Fatalf("session_active = false for active node status: %s", response.Body.String())
	}
	if body.SessionID != "node-session-1" {
		t.Fatalf("session_id = %q, want %q", body.SessionID, "node-session-1")
	}
}
