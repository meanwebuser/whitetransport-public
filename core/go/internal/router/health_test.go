package router

import (
	"testing"
)

func TestCarrierHealthRecordAndSnapshot(t *testing.T) {
	h := NewCarrierHealth()

	h.RecordReadSuccess("vk.messages")
	h.RecordReadSuccess("vk.messages")
	h.RecordWriteSuccess("vk.messages")
	h.RecordReadFailure("ok.messages")

	snap := h.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 carriers, got %d", len(snap))
	}

	vk := snap["vk.messages"]
	if !vk.Healthy {
		t.Error("vk.messages should be healthy")
	}
	if vk.ReadSuccesses != 2 {
		t.Errorf("expected 2 read successes, got %d", vk.ReadSuccesses)
	}
	if vk.WriteSuccesses != 1 {
		t.Errorf("expected 1 write success, got %d", vk.WriteSuccesses)
	}

	ok := snap["ok.messages"]
	if ok.Healthy {
		t.Error("ok.messages should be unhealthy (only failures)")
	}
	if ok.ReadFailures != 1 {
		t.Errorf("expected 1 read failure, got %d", ok.ReadFailures)
	}
}

func TestCarrierHealthMetrics(t *testing.T) {
	h := NewCarrierHealth()

	// Unknown carrier returns healthy.
	m := h.Metrics("unknown")
	if !m.Healthy {
		t.Error("unknown carrier should default to healthy")
	}

	h.RecordReadSuccess("vk.docs.1024")
	m = h.Metrics("vk.docs.1024")
	if !m.Healthy {
		t.Error("carrier with only successes should be healthy")
	}
	if m.LastOK.IsZero() {
		t.Error("last success should be set")
	}
}

func TestCarrierHealthUnhealthyWhenMoreFailures(t *testing.T) {
	h := NewCarrierHealth()

	h.RecordReadSuccess("test")
	h.RecordReadFailure("test")
	h.RecordReadFailure("test")

	m := h.Metrics("test")
	if m.Healthy {
		t.Error("carrier with more failures than successes should be unhealthy")
	}
}

func TestCarrierHealthReportsConstructionLifecycle(t *testing.T) {
	h := NewCarrierHealth()
	h.RecordConstructed("yandex.primary")
	h.RecordInitializationFailure("broken.vk", "construction", "credential_missing", true, "broken.vk")

	snap := h.Snapshot()
	if got := snap["yandex.primary"]; !got.Healthy || got.LifecycleState != "constructed" {
		t.Fatalf("expected constructed carrier to be healthy, got %+v", got)
	}
	broken := snap["broken.vk"]
	if broken.Healthy || broken.LifecycleState != "degraded" || broken.FailureStage != "construction" || broken.ErrorCode != "credential_missing" || !broken.Retryable || broken.ResourceGroup != "broken.vk" {
		t.Fatalf("unexpected degraded carrier snapshot: %+v", broken)
	}
	if h.Metrics("broken.vk").Healthy {
		t.Fatal("construction-degraded carrier must be excluded from policy")
	}
}

func TestCarrierHealthReportsRuntimeFailureAndRecovery(t *testing.T) {
	h := NewCarrierHealth()
	h.RecordConstructed("yandex.primary")
	h.RecordRuntimeFailure("yandex.primary")
	failed := h.Snapshot()["yandex.primary"]
	if failed.Healthy || failed.LifecycleState != "degraded" || failed.FailureStage != "runtime" || failed.ErrorCode != "io_failure" {
		t.Fatalf("unexpected runtime failure: %+v", failed)
	}
	h.RecordConstructed("yandex.primary")
	recovered := h.Snapshot()["yandex.primary"]
	if !recovered.Healthy || recovered.LifecycleState != "constructed" || recovered.ErrorCode != "" {
		t.Fatalf("unexpected runtime recovery: %+v", recovered)
	}
}
