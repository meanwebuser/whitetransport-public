package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// statusSnapshot is deliberately separate from State. State is private root
// input for stop; this sidecar is a read-only status contract for the GUI and
// contains no route list or credentials.
type statusSnapshot struct {
	PID               int    `json:"pid"`
	Interface         string `json:"interface"`
	DaemonInstanceID  string `json:"daemon_instance_id"`
	ProfileRevision   uint64 `json:"profile_revision"`
	ProfileHash       string `json:"profile_hash"`
	SessionID         string `json:"session_id"`
	ProfileValidUntil string `json:"profile_valid_until"`
}

func statusSnapshotFromState(state State) statusSnapshot {
	return statusSnapshot{
		PID:               state.PID,
		Interface:         state.Interface,
		DaemonInstanceID:  state.Config.DaemonInstanceID,
		ProfileRevision:   state.Config.ProfileRevision,
		ProfileHash:       state.Config.ProfileHash,
		SessionID:         state.Config.SessionID,
		ProfileValidUntil: state.Config.ProfileValidUntil.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func (snapshot statusSnapshot) state() State {
	return State{
		PID:       snapshot.PID,
		Interface: snapshot.Interface,
		Config: Config{
			DaemonInstanceID:  snapshot.DaemonInstanceID,
			ProfileRevision:   snapshot.ProfileRevision,
			ProfileHash:       snapshot.ProfileHash,
			SessionID:         snapshot.SessionID,
			ProfileValidUntil: parseStatusTime(snapshot.ProfileValidUntil),
		},
	}
}

func parseStatusTime(value string) (resultTime time.Time) {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func writeStatusSnapshot(path string, state State) error {
	if path == "" {
		return errors.New("status snapshot path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create status snapshot directory: %w", err)
	}
	data, err := json.MarshalIndent(statusSnapshotFromState(state), "", "  ")
	if err != nil {
		return fmt.Errorf("encode status snapshot: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".status-*.tmp")
	if err != nil {
		return fmt.Errorf("create status snapshot temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set status snapshot permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write status snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync status snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close status snapshot: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace status snapshot: %w", err)
	}
	return nil
}

func readStatusSnapshot(path string) (statusSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return statusSnapshot{}, err
	}
	var snapshot statusSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return statusSnapshot{}, fmt.Errorf("decode status snapshot: %w", err)
	}
	return snapshot, nil
}

func removeStatusSnapshot(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
