package policy

import (
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestFailureBasedDeprioritization(t *testing.T) {
	policy := DefaultAdaptivePolicy()
	
	// Check what routes are available for TrafficControl
	var controlRoutes []Route
	for _, route := range policy.Routes {
		if route.TrafficClass == fabric.TrafficControl {
			controlRoutes = append(controlRoutes, route)
		}
	}
	t.Logf("Control routes: %v", controlRoutes)
	
	// Create test carriers that exist in the routes
	vkMessagesCarrier := carriers.Descriptor{
		ID:             "vk.messages",
		Metrics:        carriers.Metrics{Healthy: true},
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficControl},
	}
	
	okMessagesCarrier := carriers.Descriptor{
		ID:             "ok.messages", 
		Metrics:        carriers.Metrics{Healthy: true},
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficControl},
	}
	
	available := []carriers.Descriptor{vkMessagesCarrier, okMessagesCarrier}
	
	// Initially, both should be selected since they exist in routes for TrafficControl
	selected := policy.healthyRoutes(fabric.TrafficControl, available)
	t.Logf("Selected carriers: %v", func() []string { ids := make([]string, len(selected)); for i, c := range selected { ids[i] = c.ID }; return ids }())
	
	if len(selected) != 2 {
		t.Fatalf("Expected 2 healthy carriers, got %d", len(selected))
	}
	
	// Record error 2001 for ok.messages (this should deprioritize it)
	t.Logf("Recording error for ok.messages...")
	policy.RecordCarrierFailure("ok.messages", "operation failed with error_code:2001")
	
	// Now ok.messages should be deprioritized
	selected = policy.healthyRoutes(fabric.TrafficControl, available)
	t.Logf("Selected carriers after failure: %v", func() []string { ids := make([]string, len(selected)); for i, c := range selected { ids[i] = c.ID }; return ids }())
	
	if selected[0].ID != "vk.messages" {
		t.Errorf("Expected vk.messages to be first, got %s", selected[0].ID)
	}
	if selected[1].ID != "ok.messages" {
		t.Errorf("Expected ok.messages to be deprioritized to last, got %s", selected[1].ID)
	}
}

func TestFailureDecay(t *testing.T) {
	policy := DefaultAdaptivePolicy()
	policy.FailureTracker.decayWindow = 2 * time.Minute // Short decay for testing
	
	healthyCarrier := carriers.Descriptor{
		ID:             "vk.messages",
		Metrics:        carriers.Metrics{Healthy: true},
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficControl},
	}
	
	problematicCarrier := carriers.Descriptor{
		ID:             "ok.messages",
		Metrics:        carriers.Metrics{Healthy: true},
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficControl},
	}
	
	available := []carriers.Descriptor{healthyCarrier, problematicCarrier}
	
	// Record failure
	policy.RecordCarrierFailure("ok.messages", "error_code:2001")
	
	// Both carriers should be selected (failure doesn't remove, just deprioritizes)
	selected := policy.healthyRoutes(fabric.TrafficControl, available)
	if len(selected) != 2 {
		t.Fatalf("Expected 2 carriers, got %d", len(selected))
	}
	
	// Reset failure scores - this should not change the order since both have same base priority
	policy.FailureTracker.ResetFailures("ok.messages")
	
	// After reset, carriers should still be selected in same order (same priority)
	selected = policy.healthyRoutes(fabric.TrafficControl, available)
	if len(selected) != 2 {
		t.Fatalf("Expected 2 carriers after reset, got %d", len(selected))
	}
}

func TestErrorCodeExtraction(t *testing.T) {
	tests := []struct {
		errMsg      string
		expectedCode string
	}{
		{"error_code:2001", "2001"},
		{"error_code:5", "5"},
		{"HTTP 429: too many requests", "429"},
		{"(401) unauthorized", "401"},
		{"timeout error", "timeout"},
		{"connection refused", "connection refused"},
		{"some other error", ""},
	}

	for _, test := range tests {
		result := extractErrorCode(test.errMsg)
		if result != test.expectedCode {
			t.Errorf("For errMsg '%s', expected '%s', got '%s'", test.errMsg, test.expectedCode, result)
		}
	}
}

// TestFailureTrackerAutoDisable verifies that consecutive failures trip the
// auto-disable threshold and that consecutive successes clear it.
func TestFailureTrackerAutoDisable(t *testing.T) {
	tracker := NewFailureTracker(10 * time.Minute)
	carrierID := "auto-disable.test"

	if tracker.IsAutoDisabled(carrierID) {
		t.Fatal("unknown carrier should not be auto-disabled")
	}

	// Four failures are below the threshold and must NOT auto-disable.
	for i := 0; i < AutoDisableThreshold-1; i++ {
		tracker.RecordFailure(carrierID, "error_code:2001", "2001")
	}
	if tracker.IsAutoDisabled(carrierID) {
		t.Fatalf("carrier auto-disabled before threshold reached (%d failures)", AutoDisableThreshold-1)
	}

	// The fifth failure trips the auto-disable.
	tracker.RecordFailure(carrierID, "error_code:2001", "2001")
	if !tracker.IsAutoDisabled(carrierID) {
		t.Fatalf("carrier should be auto-disabled after %d failures", AutoDisableThreshold)
	}

	// One success is not enough — ClearAutoDisableOn consecutive successes
	// are required before the auto-disable is cleared.
	tracker.RecordSuccess(carrierID)
	if !tracker.IsAutoDisabled(carrierID) {
		t.Fatal("carrier should still be auto-disabled after one success (ClearAutoDisableOn = 2)")
	}
	tracker.RecordSuccess(carrierID)
	if tracker.IsAutoDisabled(carrierID) {
		t.Fatal("carrier auto-disable should be cleared after ClearAutoDisableOn consecutive successes")
	}
}

// TestFailureTrackerAutoDisableExpireViaDuration verifies the time-based
// recovery path: even without intervening successes, an auto-disable expires
// once AutoDisableDuration has passed.
func TestFailureTrackerAutoDisableExpireViaDuration(t *testing.T) {
	tracker := NewFailureTracker(10 * time.Minute)
	carrierID := "auto-disable-expire.test"

	for i := 0; i < AutoDisableThreshold; i++ {
		tracker.RecordFailure(carrierID, "error_code:5", "5")
	}
	if !tracker.IsAutoDisabled(carrierID) {
		t.Fatal("carrier should be auto-disabled after threshold failures")
	}

	// Manually rewind the auto-disable window so we can prove IsAutoDisabled
	// returns false once the deadline is in the past without sleeping.
	tracker.mu.Lock()
	tracker.scores[carrierID].AutoDisabledUntil = time.Now().Add(-time.Second)
	tracker.mu.Unlock()

	if tracker.IsAutoDisabled(carrierID) {
		t.Fatal("carrier should not be auto-disabled after the AutoDisableDuration window has elapsed")
	}
}