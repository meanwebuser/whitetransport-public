package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/router"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
)

var _ = carriers.CarrierVKDocs1024 // referenced in status stub below

func TestRuntimeHandlerServesRuntimeSessionEndpoints(t *testing.T) {
	control := &stubRuntimeControl{
		nodes: []runtime.NodeView{{
			NodeID:    "example-exit-node",
			Label:     "Example Exit Node",
			Available: true,
		}},
		status: runtime.StatusView{
			Role:         "client",
			State:        "connected",
			ActiveNodeID: "example-exit-node",
			SessionID:    "sess-1",
			EgressEndpoints: []carriers.Endpoint{{
				ID: carriers.CarrierVKDocs1024,
			}},
		},
	}

	server := httptest.NewServer(NewRuntimeHandler(nil, policy.DefaultAdaptivePolicy(), control, nil))
	defer server.Close()

	response, err := http.Get(server.URL + "/v1/nodes")
	if err != nil {
		t.Fatalf("get nodes: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /v1/nodes, got %d", response.StatusCode)
	}

	var nodes []runtime.NodeView
	if err := json.NewDecoder(response.Body).Decode(&nodes); err != nil {
		t.Fatalf("decode nodes response: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "example-exit-node" || !nodes[0].Available {
		t.Fatalf("unexpected nodes response: %+v", nodes)
	}

	statusResponse, err := http.Get(server.URL + "/v1/status")
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer statusResponse.Body.Close()

	var status runtime.StatusView
	if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if status.ActiveNodeID != "example-exit-node" || len(status.EgressEndpoints) != 1 {
		t.Fatalf("unexpected status response: %+v", status)
	}

	connectBody := bytes.NewBufferString(`{"node_id":"example-exit-node"}`)
	connectResponse, err := http.Post(server.URL+"/v1/session/connect", "application/json", connectBody)
	if err != nil {
		t.Fatalf("post session connect: %v", err)
	}
	defer connectResponse.Body.Close()
	if connectResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from connect, got %d", connectResponse.StatusCode)
	}

	disconnectResponse, err := http.Post(server.URL+"/v1/session/disconnect", "application/json", nil)
	if err != nil {
		t.Fatalf("post session disconnect: %v", err)
	}
	defer disconnectResponse.Body.Close()
	if disconnectResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from disconnect, got %d", disconnectResponse.StatusCode)
	}
}

func TestRuntimeHandlerServesBuildInfo(t *testing.T) {
	server := httptest.NewServer(NewRuntimeHandlerWithBuildInfo(nil, policy.DefaultAdaptivePolicy(), &stubRuntimeControl{}, nil, BuildInfo{
		Version: "dev",
		Commit:  "abc123",
		Date:    "2026-06-13T00:00:00Z",
	}))
	defer server.Close()

	response, err := http.Get(server.URL + "/v1/build")
	if err != nil {
		t.Fatalf("get build info: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /v1/build, got %d", response.StatusCode)
	}

	var build BuildInfo
	if err := json.NewDecoder(response.Body).Decode(&build); err != nil {
		t.Fatalf("decode build info: %v", err)
	}
	if build.Commit != "abc123" || build.Version != "dev" || build.Date == "" {
		t.Fatalf("unexpected build info: %+v", build)
	}
}

func TestRuntimeHandlerSelectsActiveEgressEndpoint(t *testing.T) {
	control := &stubRuntimeControl{status: runtime.StatusView{
		State:        "connected",
		ActiveNodeID: "example-exit-node",
		SessionID:    "sess-xray",
		EgressEndpoints: []carriers.Endpoint{
			{ID: "xray-de-httpupgrade", Carrier: carriers.CarrierSingBoxVLESS},
			{ID: "xray-us-httpupgrade", Carrier: carriers.CarrierSingBoxVLESS},
		},
	}}
	server := httptest.NewServer(NewRuntimeHandler(nil, policy.DefaultAdaptivePolicy(), control, nil))
	defer server.Close()

	response, err := http.Post(
		server.URL+"/v1/session/egress/select",
		"application/json",
		bytes.NewBufferString(`{"egress_endpoint_id":"xray-de-httpupgrade"}`),
	)
	if err != nil {
		t.Fatalf("post select egress endpoint: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("select endpoint status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var status runtime.StatusView
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatalf("decode select endpoint status: %v", err)
	}
	if status.SelectedEgressEndpointID != "xray-de-httpupgrade" {
		t.Fatalf("selected endpoint = %q", status.SelectedEgressEndpointID)
	}
	if control.selectedEgressEndpointID != "xray-de-httpupgrade" {
		t.Fatalf("control selected endpoint = %q", control.selectedEgressEndpointID)
	}
}

type stubRuntimeControl struct {
	nodes                    []runtime.NodeView
	status                   runtime.StatusView
	selectedEgressEndpointID string
}

func (s *stubRuntimeControl) ListNodes() []runtime.NodeView {
	return s.nodes
}

func (s *stubRuntimeControl) Status() runtime.StatusView {
	return s.status
}

func (s *stubRuntimeControl) Connect(ctx context.Context, nodeID string) (runtime.StatusView, error) {
	if nodeID != "" {
		s.status.ActiveNodeID = nodeID
	}
	return s.status, nil
}

func (s *stubRuntimeControl) Disconnect() runtime.StatusView {
	s.status.ActiveNodeID = ""
	s.status.SessionID = ""
	s.status.EgressEndpoints = nil
	return s.status
}

func (s *stubRuntimeControl) SelectEgressEndpoint(endpointID string) (runtime.StatusView, error) {
	for _, endpoint := range s.status.EgressEndpoints {
		if endpoint.ID == endpointID {
			s.selectedEgressEndpointID = endpointID
			s.status.SelectedEgressEndpointID = endpointID
			return s.status, nil
		}
	}
	return s.status, fmt.Errorf("unknown active egress endpoint %q", endpointID)
}

func (s *stubRuntimeControl) CarrierHealthSnapshot() map[string]router.CarrierSnapshot {
	return nil
}
