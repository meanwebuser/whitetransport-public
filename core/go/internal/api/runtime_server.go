package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/router"
	"github.com/meanwebuser/whitetransport/core/internal/runtime"
)

// RuntimeControl is the runtime control surface exposed over the local API.
type RuntimeControl interface {
	ListNodes() []runtime.NodeView
	Status() runtime.StatusView
	Connect(ctx context.Context, nodeID string) (runtime.StatusView, error)
	Disconnect() runtime.StatusView
	CarrierHealthSnapshot() map[string]router.CarrierSnapshot
}

// EgressEndpointSelector is an optional local-only diagnostic capability.
// It pins one active route so a failed carrier can be inspected without the
// normal adaptive failover masking the failure with a different endpoint.
type EgressEndpointSelector interface {
	SelectEgressEndpoint(endpointID string) (runtime.StatusView, error)
}

// BuildInfo describes the daemon binary/config contract used by remote smoke
// tests before they spend a live session attempt.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// NewRuntimeHandler wraps the planner API with runtime session endpoints.
func NewRuntimeHandler(
	carrierDescriptors []carriers.Descriptor,
	carrierPolicy policy.CarrierPolicy,
	control RuntimeControl,
	logf func(format string, args ...any),
) http.Handler {
	return NewRuntimeHandlerWithBuildInfo(carrierDescriptors, carrierPolicy, control, logf, BuildInfo{})
}

// NewRuntimeHandlerWithBuildInfo wraps the planner API with runtime session
// endpoints and exposes build metadata for deploy contract checks.
func NewRuntimeHandlerWithBuildInfo(
	carrierDescriptors []carriers.Descriptor,
	carrierPolicy policy.CarrierPolicy,
	control RuntimeControl,
	logf func(format string, args ...any),
	buildInfo BuildInfo,
) http.Handler {
	return runtimeHandler{
		planner:   NewPlannerServer(carrierDescriptors, carrierPolicy).Handler(),
		control:   control,
		logf:      logf,
		buildInfo: buildInfo,
		startedAt: time.Now().UTC(),
	}
}

type runtimeHandler struct {
	planner   http.Handler
	control   RuntimeControl
	logf      func(format string, args ...any)
	buildInfo BuildInfo
	startedAt time.Time
}

func (h runtimeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/nodes":
		h.handleNodes(w, r)
	case "/v1/status":
		h.handleStatus(w, r)
	case "/v1/session/connect":
		h.handleSessionConnect(w, r)
	case "/v1/session/disconnect":
		h.handleSessionDisconnect(w, r)
	case "/v1/session/egress/select":
		h.handleSessionEgressSelect(w, r)
	case "/v1/carriers":
		h.handleCarriers(w, r)
	case "/v1/health/detailed":
		h.handleDetailedHealth(w, r)
	case "/v1/build":
		h.handleBuild(w, r)
	default:
		h.planner.ServeHTTP(w, r)
	}
}

func (h runtimeHandler) handleBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, h.buildInfo)
}

func (h runtimeHandler) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.control == nil {
		writeError(w, http.StatusNotImplemented, "runtime control unavailable")
		return
	}
	writeJSON(w, http.StatusOK, h.control.ListNodes())
}

func (h runtimeHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.control == nil {
		writeError(w, http.StatusNotImplemented, "runtime control unavailable")
		return
	}
	writeJSON(w, http.StatusOK, runtimeStatusResponse(h.control.Status()))
}

func (h runtimeHandler) handleSessionConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.control == nil {
		writeError(w, http.StatusNotImplemented, "runtime control unavailable")
		return
	}

	var body struct {
		NodeID string `json:"node_id"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("decode session connect request: %v", err))
			return
		}
	}

	status, err := h.control.Connect(r.Context(), body.NodeID)
	if err != nil {
		resp := map[string]any{
			"error":  err.Error(),
			"status": runtimeStatusResponse(status),
		}
		// Add retry hint when node is busy.
		if strings.Contains(err.Error(), "busy") {
			resp["retryable"] = true
			resp["retry_after_seconds"] = 5
		}
		code := http.StatusBadGateway
		if resp["retryable"] == true {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, resp)
		return
	}
	writeJSON(w, http.StatusOK, runtimeStatusResponse(status))
}

func (h runtimeHandler) handleSessionDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.control == nil {
		writeError(w, http.StatusNotImplemented, "runtime control unavailable")
		return
	}
	writeJSON(w, http.StatusOK, runtimeStatusResponse(h.control.Disconnect()))
}

func (h runtimeHandler) handleSessionEgressSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.control == nil {
		writeError(w, http.StatusNotImplemented, "runtime control unavailable")
		return
	}
	selector, ok := h.control.(EgressEndpointSelector)
	if !ok {
		writeError(w, http.StatusNotImplemented, "runtime does not support explicit egress selection")
		return
	}
	var body struct {
		EgressEndpointID string `json:"egress_endpoint_id"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("decode egress selection request: %v", err))
			return
		}
	}
	if strings.TrimSpace(body.EgressEndpointID) == "" {
		writeError(w, http.StatusBadRequest, "egress_endpoint_id is required")
		return
	}
	status, err := selector.SelectEgressEndpoint(body.EgressEndpointID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runtimeStatusResponse(status))
}

func (h runtimeHandler) logger(format string, args ...any) {
	if h.logf != nil {
		h.logf(format, args...)
	}
}

func (h runtimeHandler) handleCarriers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.control == nil {
		writeError(w, http.StatusNotImplemented, "runtime control unavailable")
		return
	}
	snap := h.control.CarrierHealthSnapshot()
	if snap == nil {
		snap = map[string]router.CarrierSnapshot{}
	}
	writeJSON(w, http.StatusOK, snap)
}

func (h runtimeHandler) handleDetailedHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.control == nil {
		writeError(w, http.StatusNotImplemented, "runtime control unavailable")
		return
	}

	status := h.control.Status()
	carriers := h.control.CarrierHealthSnapshot()
	if carriers == nil {
		carriers = map[string]router.CarrierSnapshot{}
	}

	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)

	type memoryView struct {
		AllocBytes     uint64 `json:"alloc_bytes"`
		SysBytes       uint64 `json:"sys_bytes"`
		HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
		HeapSysBytes   uint64 `json:"heap_sys_bytes"`
		NumGC          uint32 `json:"num_gc"`
		Goroutines     int    `json:"goroutines"`
	}
	type sessionView struct {
		Active            bool   `json:"active"`
		ActiveNodeID      string `json:"active_node_id,omitempty"`
		SessionID         string `json:"session_id,omitempty"`
		ReconnectAttempts int    `json:"reconnect_attempts"`
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":         runtimeStatusResponse(status),
		"uptime_seconds": int(time.Since(h.startedAt).Seconds()),
		"started_at":     h.startedAt,
		"carriers":       carriers,
		"sessions": sessionView{
			Active:            status.SessionActive,
			ActiveNodeID:      status.ActiveNodeID,
			SessionID:         status.SessionID,
			ReconnectAttempts: status.ReconnectAttempts,
		},
		"memory": memoryView{
			AllocBytes:     mem.Alloc,
			SysBytes:       mem.Sys,
			HeapAllocBytes: mem.HeapAlloc,
			HeapSysBytes:   mem.HeapSys,
			NumGC:          mem.NumGC,
			Goroutines:     goruntime.NumGoroutine(),
		},
	})
}
