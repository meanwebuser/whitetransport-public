package main

import (
	"context"
	"encoding/json"
	"testing"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

func TestFakeSystemVPNHostLifecycleIsExplicitAndDeterministic(t *testing.T) {
	host := newFakeSystemVPNHost()
	if !host.Supported() {
		t.Fatal("fake system VPN host must explicitly advertise synthetic support")
	}
	profile := json.RawMessage(`{"daemon_instance_id":"fake-daemon","profile_revision":3,"profile_hash":"fake-hash","session_id":"fake-session","selected_node_id":"fake-node","ready":true,"expires_at":"2099-01-01T00:00:00Z"}`)

	permission, err := host.Permission(context.Background())
	if err != nil || permission.State != guiruntime.SystemVPNDisconnected {
		t.Fatalf("Permission = (%+v, %v), want disconnected synthetic host", permission, err)
	}
	started, err := host.Start(context.Background(), profile)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	identity, err := decodeSystemVPNProfileIdentity(profile)
	if err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	if !observationMatchesProfile(started, identity) {
		t.Fatalf("started observation = %+v, want exact profile match", started)
	}
	status, err := host.Status(context.Background())
	if err != nil || status != started {
		t.Fatalf("Status = (%+v, %v), want %+v", status, err, started)
	}
	stopped, err := host.Stop(context.Background())
	if err != nil || stopped.State != guiruntime.SystemVPNDisconnected || stopped.ProviderState != guiruntime.SystemVPNDisconnected {
		t.Fatalf("Stop = (%+v, %v), want disconnected", stopped, err)
	}
}

func TestNewAppSelectsSyntheticSystemVPNOnlyForFakeRuntime(t *testing.T) {
	resources := guiruntime.RuntimeResourceSummary{Mode: guiruntime.ModeFake}
	app, err := newApp(&stubRuntimeService{}, resources, newTestLogSink(t), newDisabledSupervisor(resources))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if _, ok := app.systemVPN.(*fakeSystemVPNHost); !ok {
		t.Fatalf("fake runtime system VPN host = %T, want *fakeSystemVPNHost", app.systemVPN)
	}
}

func TestFakeRuntimeConnectExercisesSyntheticSystemVPNTransaction(t *testing.T) {
	service, err := guiruntime.NewFakeService()
	if err != nil {
		t.Fatalf("NewFakeService: %v", err)
	}
	resources := guiruntime.RuntimeResourceSummary{Mode: guiruntime.ModeFake}
	app, err := newApp(service, resources, newTestLogSink(t), newDisabledSupervisor(resources))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}

	status, err := app.Connect("local-fast")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !status.Connected || status.TransportState != guiruntime.StateConnected || status.SystemVPNState != guiruntime.SystemVPNConnected {
		t.Fatalf("synthetic connected status = %+v", status)
	}
	refreshed, err := app.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !refreshed.Connected || refreshed.SystemVPNState != guiruntime.SystemVPNConnected {
		t.Fatalf("synthetic refreshed status = %+v", refreshed)
	}
}
