package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

type systemVPNProfileIdentity struct {
	DaemonInstanceID  string    `json:"daemon_instance_id"`
	Revision          uint64    `json:"profile_revision"`
	SessionID         string    `json:"session_id"`
	SelectedNodeID    string    `json:"selected_node_id"`
	ProfileHash       string    `json:"profile_hash"`
	Ready             bool      `json:"ready"`
	ProfileValidUntil time.Time `json:"-"`
}

type systemVPNObservation struct {
	State             guiruntime.SystemVPNState `json:"state"`
	ProviderState     guiruntime.SystemVPNState `json:"provider_state"`
	DaemonInstanceID  string                    `json:"daemon_instance_id"`
	Revision          uint64                    `json:"profile_revision"`
	SessionID         string                    `json:"session_id"`
	ProfileHash       string                    `json:"profile_hash"`
	ProfileValidUntil time.Time                 `json:"profile_valid_until"`
}

type systemVPNHost interface {
	Supported() bool
	Permission(ctx context.Context) (systemVPNObservation, error)
	Start(ctx context.Context, profile json.RawMessage) (systemVPNObservation, error)
	Stop(ctx context.Context) (systemVPNObservation, error)
	Status(ctx context.Context) (systemVPNObservation, error)
	Logs(ctx context.Context) ([]guiruntime.LogLine, error)
}

func decodeSystemVPNProfileIdentity(profile json.RawMessage) (systemVPNProfileIdentity, error) {
	if len(profile) == 0 || !json.Valid(profile) {
		return systemVPNProfileIdentity{}, fmt.Errorf("system VPN profile is absent or invalid")
	}
	var authoritative runtimeapi.SystemVPNProfile
	if err := json.Unmarshal(profile, &authoritative); err != nil {
		return systemVPNProfileIdentity{}, fmt.Errorf("decode system VPN profile identity: %w", err)
	}
	identity := systemVPNProfileIdentity{
		DaemonInstanceID:  authoritative.DaemonInstanceID,
		Revision:          authoritative.ProfileRevision,
		SessionID:         authoritative.SessionID,
		SelectedNodeID:    authoritative.SelectedNodeID,
		ProfileHash:       authoritative.ProfileHash,
		Ready:             authoritative.Ready,
		ProfileValidUntil: systemVPNProfileValidUntil(authoritative),
	}
	if !identity.Ready || identity.DaemonInstanceID == "" || identity.Revision == 0 || identity.SessionID == "" || identity.SelectedNodeID == "" || identity.ProfileHash == "" {
		return systemVPNProfileIdentity{}, fmt.Errorf("system VPN profile is not ready or lacks identity")
	}
	if identity.ProfileValidUntil.IsZero() {
		return systemVPNProfileIdentity{}, fmt.Errorf("system VPN profile lacks a validity deadline")
	}
	return identity, nil
}

// systemVPNProfileValidUntil returns the exact lease deadline understood by
// the macOS provider. Its wire codecs preserve only whole UTC seconds.
func systemVPNProfileValidUntil(profile runtimeapi.SystemVPNProfile) time.Time {
	validUntil := profile.ExpiresAt
	for _, dependency := range profile.Dependencies {
		if !dependency.DNSExpiresAt.IsZero() && (validUntil.IsZero() || dependency.DNSExpiresAt.Before(validUntil)) {
			validUntil = dependency.DNSExpiresAt
		}
	}
	return validUntil.UTC().Truncate(time.Second)
}

func observationMatchesProfile(observation systemVPNObservation, identity systemVPNProfileIdentity) bool {
	return observation.State == guiruntime.SystemVPNConnected &&
		observation.ProviderState == guiruntime.SystemVPNConnected &&
		observation.DaemonInstanceID == identity.DaemonInstanceID &&
		observation.Revision == identity.Revision &&
		observation.SessionID == identity.SessionID &&
		observation.ProfileHash == identity.ProfileHash &&
		observation.ProfileValidUntil.Equal(identity.ProfileValidUntil)
}

type unsupportedSystemVPNHost struct{}

func (unsupportedSystemVPNHost) Supported() bool { return false }

func (unsupportedSystemVPNHost) Permission(context.Context) (systemVPNObservation, error) {
	return systemVPNObservation{State: guiruntime.SystemVPNUnsupported, ProviderState: guiruntime.SystemVPNUnsupported}, nil
}

func (unsupportedSystemVPNHost) Start(context.Context, json.RawMessage) (systemVPNObservation, error) {
	return systemVPNObservation{State: guiruntime.SystemVPNUnsupported, ProviderState: guiruntime.SystemVPNUnsupported}, fmt.Errorf("system VPN is unsupported on this host")
}

func (unsupportedSystemVPNHost) Stop(context.Context) (systemVPNObservation, error) {
	return systemVPNObservation{State: guiruntime.SystemVPNUnsupported, ProviderState: guiruntime.SystemVPNUnsupported}, nil
}

func (unsupportedSystemVPNHost) Status(context.Context) (systemVPNObservation, error) {
	return systemVPNObservation{State: guiruntime.SystemVPNUnsupported, ProviderState: guiruntime.SystemVPNUnsupported}, nil
}

func (unsupportedSystemVPNHost) Logs(context.Context) ([]guiruntime.LogLine, error) { return nil, nil }
