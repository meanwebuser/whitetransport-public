package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/runtime"
)

func TestRuntimeStatusResponsePreservesAutomaticEgressEndpoint(t *testing.T) {
	response := runtimeStatusResponse(runtime.StatusView{
		AutomaticEgressEndpointID: "yandex.primary",
	})
	if response.AutomaticEgressEndpointID != "yandex.primary" {
		t.Fatalf("automatic egress endpoint = %q, want yandex.primary", response.AutomaticEgressEndpointID)
	}

	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal runtime status: %v", err)
	}
	if !strings.Contains(string(payload), `"automatic_egress_endpoint_id":"yandex.primary"`) {
		t.Fatalf("runtime status JSON omitted automatic endpoint: %s", payload)
	}
}
