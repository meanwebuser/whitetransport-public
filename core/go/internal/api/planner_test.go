package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

func TestPlannerAPIReturnsStripedBulkSchedule(t *testing.T) {
	server := NewPlannerServer(carriers.StandardDescriptors(), policy.DefaultAdaptivePolicy())
	request := httptest.NewRequest(http.MethodGet, "/v1/plan?traffic=bulk&payload_bytes=1048576", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var body policy.RoutePlanView
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Strategy != string(policy.DeliveryStriped) {
		t.Fatalf("expected striped plan, got %+v", body)
	}
	seen := map[string]bool{}
	for _, placement := range body.Placements {
		seen[placement.CarrierID] = true
	}
	for _, id := range []string{carriers.CarrierVKDocs1024, carriers.CarrierVKDocs256, carriers.CarrierOKDocs256} {
		if !seen[id] {
			t.Fatalf("expected striped placement for %s, got %v", id, seen)
		}
	}
}

func TestPlannerAPIRejectsBadPayloadBytes(t *testing.T) {
	server := NewPlannerServer(carriers.StandardDescriptors(), policy.DefaultAdaptivePolicy())
	request := httptest.NewRequest(http.MethodGet, "/v1/plan?traffic=bulk&payload_bytes=-1", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestPlannerAPIReportsHealth(t *testing.T) {
	server := NewPlannerServer(carriers.StandardDescriptors(), policy.DefaultAdaptivePolicy())
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}
