package main

import (
	"context"
	"encoding/json"
	"sync"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

// fakeSystemVPNHost is a synthetic lifecycle driver for deterministic GUI
// tests. It never opens preferences, requests entitlements, or changes routes.
type fakeSystemVPNHost struct {
	mu          sync.Mutex
	observation systemVPNObservation
}

func newFakeSystemVPNHost() *fakeSystemVPNHost {
	return &fakeSystemVPNHost{observation: systemVPNObservation{
		State:         guiruntime.SystemVPNDisconnected,
		ProviderState: guiruntime.SystemVPNDisconnected,
	}}
}

func (*fakeSystemVPNHost) Supported() bool { return true }

func (h *fakeSystemVPNHost) Permission(ctx context.Context) (systemVPNObservation, error) {
	if err := ctx.Err(); err != nil {
		return systemVPNObservation{}, err
	}
	return h.Status(ctx)
}

func (h *fakeSystemVPNHost) Start(ctx context.Context, profile json.RawMessage) (systemVPNObservation, error) {
	if err := ctx.Err(); err != nil {
		return systemVPNObservation{}, err
	}
	identity, err := decodeSystemVPNProfileIdentity(profile)
	if err != nil {
		return systemVPNObservation{}, err
	}
	observation := systemVPNObservation{
		State:             guiruntime.SystemVPNConnected,
		ProviderState:     guiruntime.SystemVPNConnected,
		DaemonInstanceID:  identity.DaemonInstanceID,
		Revision:          identity.Revision,
		SessionID:         identity.SessionID,
		ProfileHash:       identity.ProfileHash,
		ProfileValidUntil: identity.ProfileValidUntil,
	}
	h.mu.Lock()
	h.observation = observation
	h.mu.Unlock()
	return observation, nil
}

func (h *fakeSystemVPNHost) Stop(ctx context.Context) (systemVPNObservation, error) {
	if err := ctx.Err(); err != nil {
		return systemVPNObservation{}, err
	}
	observation := systemVPNObservation{
		State:         guiruntime.SystemVPNDisconnected,
		ProviderState: guiruntime.SystemVPNDisconnected,
	}
	h.mu.Lock()
	h.observation = observation
	h.mu.Unlock()
	return observation, nil
}

func (h *fakeSystemVPNHost) Status(ctx context.Context) (systemVPNObservation, error) {
	if err := ctx.Err(); err != nil {
		return systemVPNObservation{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.observation, nil
}

func (*fakeSystemVPNHost) Logs(context.Context) ([]guiruntime.LogLine, error) { return nil, nil }
