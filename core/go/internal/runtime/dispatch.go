package runtime

import (
	"context"
	"fmt"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

// DispatchRequest is the runtime contract for sending one payload through the
// adaptive carrier fabric.
type DispatchRequest struct {
	ID          string
	Traffic     fabric.TrafficClass
	PayloadType string
	Payload     []byte
}

// DispatchReport returns the selected plan, chunk placements, and write result.
type DispatchReport struct {
	Plan       policy.RoutePlan
	Placements []policy.ChunkPlacement
	Result     policy.DispatchResult
}

// DispatchPayload plans, schedules, and writes a payload through configured
// carrier bindings. ACK and repair loops can continue from the returned report.
func DispatchPayload(
	ctx context.Context,
	carrierPolicy policy.CarrierPolicy,
	bindings map[string]policy.CarrierBinding,
	request DispatchRequest,
) (DispatchReport, error) {
	if request.ID == "" {
		return DispatchReport{}, fmt.Errorf("dispatch request id is required")
	}
	if request.Traffic == "" {
		return DispatchReport{}, fmt.Errorf("dispatch request traffic is required")
	}
	if request.PayloadType == "" {
		return DispatchReport{}, fmt.Errorf("dispatch request payload type is required")
	}
	descriptors := BindingDescriptors(bindings)
	if len(descriptors) == 0 {
		return DispatchReport{}, fmt.Errorf("at least one carrier binding is required")
	}
	plan, err := carrierPolicy.Plan(request.Traffic, descriptors)
	if err != nil {
		return DispatchReport{}, err
	}
	placements, err := policy.SchedulePayload(plan, len(request.Payload))
	if err != nil {
		return DispatchReport{}, err
	}
	base := fabric.NewEnvelope(request.ID, request.Traffic, request.PayloadType, request.Payload)
	result, err := policy.DispatchScheduledPayload(ctx, base, placements, bindings)
	if err != nil {
		return DispatchReport{}, err
	}
	return DispatchReport{Plan: plan, Placements: placements, Result: result}, nil
}

// BindingDescriptors returns healthy descriptors exposed by executable
// bindings. The policy engine uses descriptors and remains independent of
// concrete adapter implementations.
func BindingDescriptors(bindings map[string]policy.CarrierBinding) []carriers.Descriptor {
	descriptors := make([]carriers.Descriptor, 0, len(bindings))
	for _, binding := range bindings {
		descriptors = append(descriptors, binding.Carrier.Descriptor())
	}
	return descriptors
}
