package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusSnapshotRoundTripKeepsOnlyReadOnlyIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	state := State{
		PID:       1234,
		Interface: "utun99",
		Gateway:   "192.0.2.1",
		Routes:    []Route{{CIDR: "0.0.0.0/1", Via: "utun", Kind: "full"}},
		Config: Config{
			DaemonInstanceID:  "daemon-1",
			ProfileRevision:   7,
			ProfileHash:       "profile-hash",
			SessionID:         "session-1",
			ProfileValidUntil: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
			StatePath:         "/private/state.json",
		},
	}
	if err := writeStatusSnapshot(path, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("status snapshot mode = %o, want 644", got)
	}
	got, err := readStatusSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != state.PID || got.Interface != state.Interface || got.DaemonInstanceID != state.Config.DaemonInstanceID || got.ProfileRevision != state.Config.ProfileRevision || got.ProfileHash != state.Config.ProfileHash || got.SessionID != state.Config.SessionID {
		t.Fatalf("status snapshot = %#v, want identity from %#v", got, state)
	}
	restored := got.state()
	if len(restored.Routes) != 0 || restored.Config.StatePath != "" {
		t.Fatalf("status snapshot restored privileged state: %#v", restored)
	}
}
