package policy

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestRoutePlanViewExposesStableSchedulerShape(t *testing.T) {
	plan, err := DefaultAdaptivePolicy().Plan(fabric.TrafficControl, carriers.StandardDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	placements, err := SchedulePayload(plan, 4096)
	if err != nil {
		t.Fatal(err)
	}

	view := ToRoutePlanView(plan, placements)
	if view.TrafficClass != string(fabric.TrafficControl) || view.Strategy != string(DeliveryMirrored) {
		t.Fatalf("unexpected plan view identity: %+v", view)
	}
	if view.Primary.ID != carriers.CarrierVKMessages {
		t.Fatalf("expected VK primary in view, got %+v", view.Primary)
	}
	if view.HedgeTimeoutMs != 750 {
		t.Fatalf("expected hedge timeout milliseconds, got %d", view.HedgeTimeoutMs)
	}
	if len(view.Placements) != 2 {
		t.Fatalf("expected scheduled placements in view, got %d", len(view.Placements))
	}
	if len(view.Placements[0].MirrorCarrierIDs) != 1 || view.Placements[0].MirrorCarrierIDs[0] != carriers.CarrierOKMessages {
		t.Fatalf("expected OK mirror in placement view, got %+v", view.Placements[0])
	}
}
