package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/runtime"
)

func TestRuntimeStatusResponsePreservesEgressDiagnostics(t *testing.T) {
	const bindingLabel = "v1:0123456789abcdef0123456789abcdef"
	response := runtimeStatusResponse(runtime.StatusView{
		AutomaticEgressEndpointID: "yandex.primary",
		LastDialEgressEndpointID:  "vk-docs-egress",
		EgressBindingLabel:        bindingLabel,
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
	if !strings.Contains(string(payload), `"last_dial_egress_endpoint_id":"vk-docs-egress"`) ||
		!strings.Contains(string(payload), `"egress_binding_label":"`+bindingLabel+`"`) {
		t.Fatalf("runtime status JSON omitted egress diagnostics: %s", payload)
	}
}
