package api

import (
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

// runtimeStatusResponse converts internal session endpoints to the stable
// lowercase local-API contract without changing their protocol JSON shape.
func runtimeStatusResponse(status runtime.StatusView) runtimeapi.Status {
	endpoints := make([]runtimeapi.Endpoint, 0, len(status.EgressEndpoints))
	for _, endpoint := range status.EgressEndpoints {
		endpoints = append(endpoints, runtimeapi.Endpoint{
			ID:       endpoint.ID,
			Carrier:  endpoint.Carrier,
			Address:  endpoint.Address,
			Metadata: endpoint.Metadata,
		})
	}

	response := runtimeapi.Status{
		Role:                     string(status.Role),
		State:                    status.State,
		NodeID:                   status.NodeID,
		ActiveNodeID:             status.ActiveNodeID,
		SessionID:                status.SessionID,
		SessionActive:            status.SessionActive,
		SocksListen:              status.SocksListen,
		EgressEndpoints:          endpoints,
		SelectedEgressEndpointID: status.SelectedEgressEndpointID,
		UpstreamProxy:            status.UpstreamProxy,
		DiscoveredNodes:          status.DiscoveredNodes,
		AvailableNodes:           status.AvailableNodes,
		ReconnectAttempts:        status.ReconnectAttempts,
		LastError:                status.LastError,
	}
	if status.SystemVPNProfile != nil {
		response.SystemVPNProfile = status.SystemVPNProfile.Clone()
	}
	if status.SystemVPNProfileReadiness != nil {
		readiness := *status.SystemVPNProfileReadiness
		response.SystemVPNProfileReadiness = &readiness
	}
	return response
}
