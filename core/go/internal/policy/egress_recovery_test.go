package policy

import (
	"testing"
	"time"
)

func TestEgressRecoveryTrackerQuarantineIsImmediate(t *testing.T) {
	tracker := NewEgressRecoveryTracker(EgressRecoveryConfig{FailureThreshold: 99}, nil)
	tracker.Quarantine("primary")
	if tracker.CanDial("primary") {
		t.Fatal("quarantine waited for gradual failure threshold")
	}
}

func TestDefaultEgressRecoveryFitsShortSessionLease(t *testing.T) {
	now := time.Date(2026, time.August, 2, 20, 0, 0, 0, time.UTC)
	config := DefaultEgressRecoveryConfig()
	config.ProbeJitter = 0
	tracker := NewEgressRecoveryTracker(config, func() time.Time { return now })
	primary := "mail.imap_smtp\x00mail.primary\x00egress"

	tracker.Quarantine(primary)
	now = now.Add(config.InitialProbeDelay)
	if _, ok := tracker.ClaimDueProbe([]string{primary}); !ok {
		t.Fatal("first default recovery probe was not due")
	}
	tracker.RecordProbeResult(primary, true)
	if _, ok := tracker.ClaimDueProbe([]string{primary}); !ok {
		t.Fatal("next-tick confirmation probe was not due")
	}
	tracker.RecordProbeResult(primary, true)
	now = now.Add(config.FailbackCooldown)
	if !tracker.CanDial(primary) {
		t.Fatal("default recovered route remained unavailable after cooldown")
	}
	// Runtime executes the immediately-due confirmation on its next 15-second
	// bounded recovery tick.
	if elapsed := config.InitialProbeDelay + 15*time.Second + config.FailbackCooldown; elapsed >= 2*time.Minute {
		t.Fatalf("default recovery elapsed=%s cannot fit the protocol session lease", elapsed)
	}
}

func TestEgressRecoveryTrackerDegradesAndProbesWithBackoff(t *testing.T) {
	now := time.Date(2026, time.August, 1, 20, 0, 0, 0, time.UTC)
	tracker := NewEgressRecoveryTracker(EgressRecoveryConfig{
		FailureThreshold:  3,
		InitialProbeDelay: time.Minute,
		MaxProbeDelay:     4 * time.Minute,
		ProbeJitter:       0,
	}, func() time.Time { return now })
	primary := "dion.call/dion-primary"

	for i := 0; i < 2; i++ {
		tracker.RecordDialFailure(primary)
		if !tracker.CanDial(primary) {
			t.Fatalf("primary disabled before threshold after failure %d", i+1)
		}
	}
	tracker.RecordDialFailure(primary)
	if tracker.CanDial(primary) {
		t.Fatal("primary remains eligible after threshold failure")
	}
	if _, ok := tracker.ClaimDueProbe([]string{primary}); ok {
		t.Fatal("recovery probe claimed before its due time")
	}

	now = now.Add(time.Minute)
	if got, ok := tracker.ClaimDueProbe([]string{primary}); !ok || got != primary {
		t.Fatalf("due probe = %q, %v; want %q, true", got, ok, primary)
	}
	tracker.RecordProbeResult(primary, false)

	now = now.Add(time.Minute)
	if _, ok := tracker.ClaimDueProbe([]string{primary}); ok {
		t.Fatal("second probe was not backed off after a failed probe")
	}
	now = now.Add(time.Minute)
	if got, ok := tracker.ClaimDueProbe([]string{primary}); !ok || got != primary {
		t.Fatalf("backed-off probe = %q, %v; want %q, true", got, ok, primary)
	}
}

func TestEgressRecoveryTrackerFailsBackOnlyAfterHysteresisAndCooldown(t *testing.T) {
	now := time.Date(2026, time.August, 1, 20, 0, 0, 0, time.UTC)
	tracker := NewEgressRecoveryTracker(EgressRecoveryConfig{
		FailureThreshold:  1,
		InitialProbeDelay: time.Minute,
		MaxProbeDelay:     4 * time.Minute,
		ProbeSuccesses:    2,
		FailbackCooldown:  30 * time.Second,
		ProbeJitter:       0,
	}, func() time.Time { return now })
	primary := "dion.call/dion-primary"

	tracker.RecordDialFailure(primary)
	now = now.Add(time.Minute)
	if _, ok := tracker.ClaimDueProbe([]string{primary}); !ok {
		t.Fatal("first recovery probe was not due")
	}
	tracker.RecordProbeResult(primary, true)
	if tracker.CanDial(primary) {
		t.Fatal("one successful probe bypassed hysteresis")
	}

	if _, ok := tracker.ClaimDueProbe([]string{primary}); !ok {
		t.Fatal("hysteresis confirmation was not due on the next recovery tick")
	}
	tracker.RecordProbeResult(primary, true)
	if tracker.CanDial(primary) {
		t.Fatal("primary became eligible before failback cooldown")
	}
	now = now.Add(30 * time.Second)
	if !tracker.CanDial(primary) {
		t.Fatal("primary did not become eligible after stable probes and cooldown")
	}
}

func TestEgressRecoveryTrackerGuardsAgainstFailbackFlap(t *testing.T) {
	now := time.Date(2026, time.August, 1, 20, 0, 0, 0, time.UTC)
	tracker := NewEgressRecoveryTracker(EgressRecoveryConfig{
		FailureThreshold:  3,
		InitialProbeDelay: time.Minute,
		MaxProbeDelay:     4 * time.Minute,
		ProbeSuccesses:    1,
		FailbackCooldown:  0,
		FailbackGuard:     time.Minute,
		ProbeJitter:       0,
	}, func() time.Time { return now })
	primary := "dion.call/dion-primary"

	for i := 0; i < 3; i++ {
		tracker.RecordDialFailure(primary)
	}
	now = now.Add(time.Minute)
	if _, ok := tracker.ClaimDueProbe([]string{primary}); !ok {
		t.Fatal("recovery probe was not due")
	}
	tracker.RecordProbeResult(primary, true)
	if !tracker.CanDial(primary) {
		t.Fatal("primary should be eligible after the configured single probe")
	}

	tracker.RecordDialFailure(primary)
	if tracker.CanDial(primary) {
		t.Fatal("post-failback failure did not immediately re-disable the flapping route")
	}
}
