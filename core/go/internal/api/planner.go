package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

// PlannerServer exposes the local runtime planning API used by desktop,
// mobile bindings, and creator/orchestrator processes.
type PlannerServer struct {
	carriers []carriers.Descriptor
	policy   policy.CarrierPolicy
}

// NewPlannerServer creates a local planner API using configured carriers.
func NewPlannerServer(carrierDescriptors []carriers.Descriptor, carrierPolicy policy.CarrierPolicy) PlannerServer {
	return PlannerServer{
		carriers: append([]carriers.Descriptor(nil), carrierDescriptors...),
		policy:   carrierPolicy,
	}
}

// Handler returns the HTTP handler for local runtime API routes.
func (s PlannerServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/plan", s.handlePlan)
	return mux
}

func (s PlannerServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"carriers": len(s.carriers),
	})
}

func (s PlannerServer) handlePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := r.URL.Query()
	traffic := fabric.TrafficClass(query.Get("traffic"))
	if traffic == "" {
		traffic = fabric.TrafficControl
	}
	payloadBytes := 0
	if raw := query.Get("payload_bytes"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "payload_bytes must be a non-negative integer")
			return
		}
		payloadBytes = parsed
	}
	view, err := BuildPlanView(s.policy, s.carriers, traffic, payloadBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// BuildPlanView is the shared API/CLI planner entry point.
func BuildPlanView(carrierPolicy policy.CarrierPolicy, carrierDescriptors []carriers.Descriptor, traffic fabric.TrafficClass, payloadBytes int) (policy.RoutePlanView, error) {
	routePlan, err := carrierPolicy.Plan(traffic, carrierDescriptors)
	if err != nil {
		return policy.RoutePlanView{}, err
	}
	var placements []policy.ChunkPlacement
	if payloadBytes > 0 {
		placements, err = policy.SchedulePayload(routePlan, payloadBytes)
		if err != nil {
			return policy.RoutePlanView{}, err
		}
	}
	return policy.ToRoutePlanView(routePlan, placements), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(fmt.Sprintf("write json response: %v", err))
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": message,
	})
}
