package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

type recoveryRuntimeService struct {
	stubRuntimeService
	statusError error
}

func (s *recoveryRuntimeService) Status(context.Context) (guiruntime.DesktopStatus, error) {
	return s.status, s.statusError
}

func TestAppRecoveryPreservesVPNThroughGapAndReplacesRecoveredSession(t *testing.T) {
	for _, gap := range []string{"status-error", "missing-profile", "not-ready-profile"} {
		t.Run(gap, func(t *testing.T) {
			service := &recoveryRuntimeService{}
			host := &stubSystemVPNHost{supported: true,
				statusObservation: connectedSystemVPNObservation("daemon-a", 7, "session-1", "hash-old"),
				stopObservation:   systemVPNObservation{State: guiruntime.SystemVPNDisconnected},
				startObservation:  connectedSystemVPNObservation("daemon-a", 8, "session-2", "hash-new"),
			}
			app, err := NewApp(service, newTestLogSink(t))
			if err != nil {
				t.Fatal(err)
			}
			app.systemVPN = host
			app.activeSystemVPNProfile = systemVPNProfileIdentity{DaemonInstanceID: "daemon-a", Revision: 7,
				SessionID: "session-1", SelectedNodeID: "node-1", ProfileHash: "hash-old", Ready: true,
				ProfileValidUntil: testSystemVPNProfileValidUntil}
			service.status = guiruntime.DesktopStatus{State: guiruntime.StateConnecting, TransportState: guiruntime.StateConnecting}
			if gap == "status-error" {
				service.statusError = errors.New("temporary API timeout")
			}
			if gap == "not-ready-profile" {
				service.status.SystemVPNProfile = json.RawMessage(`{"daemon_instance_id":"daemon-a","ready":false}`)
			}
			_, _ = app.GetStatus()
			if host.stopCalls != 0 || app.activeSystemVPNProfile.SessionID != "session-1" {
				t.Fatal("recovery gap stopped VPN or discarded user intent")
			}
			service.statusError = nil
			service.status = guiruntime.DesktopStatus{State: guiruntime.StateDegraded, TransportState: guiruntime.StateConnected,
				ActiveNodeID: "node-2", SystemVPNProfile: json.RawMessage(`{"schema_revision":"system-vpn-profile.v1","daemon_instance_id":"daemon-a","profile_revision":8,"profile_hash":"hash-new","session_id":"session-2","selected_node_id":"node-2","ready":true,"expires_at":"2099-01-01T00:00:00Z"}`)}
			status, err := app.GetStatus()
			if err != nil {
				t.Fatal(err)
			}
			if !status.Connected || host.stopCalls != 1 || host.startCalls != 1 || app.activeSystemVPNProfile.SessionID != "session-2" {
				t.Fatalf("recovery did not install new session route: status=%+v stops=%d starts=%d", status, host.stopCalls, host.startCalls)
			}
			if service.disconnectedCalls != 0 {
				t.Fatal("recovery canceled runtime desired connection")
			}
		})
	}
}

func TestAppRecoveryDisconnectClearsVPNIntent(t *testing.T) {
	service := &recoveryRuntimeService{}
	host := &stubSystemVPNHost{supported: true, stopObservation: systemVPNObservation{State: guiruntime.SystemVPNDisconnected},
		statusObservation: systemVPNObservation{State: guiruntime.SystemVPNDisconnected}}
	app, err := NewApp(service, newTestLogSink(t))
	if err != nil {
		t.Fatal(err)
	}
	app.systemVPN = host
	app.activeSystemVPNProfile = systemVPNProfileIdentity{DaemonInstanceID: "daemon-a", Ready: true}
	if _, err := app.Disconnect(); err != nil {
		t.Fatal(err)
	}
	service.status = guiruntime.DesktopStatus{TransportState: guiruntime.StateConnected, ActiveNodeID: "node-2",
		SystemVPNProfile: json.RawMessage(`{"schema_revision":"system-vpn-profile.v1","daemon_instance_id":"daemon-a","profile_revision":8,"profile_hash":"hash-new","session_id":"session-2","selected_node_id":"node-2","ready":true,"expires_at":"2099-01-01T00:00:00Z"}`)}
	if _, err := app.GetStatus(); err != nil {
		t.Fatal(err)
	}
	if host.startCalls != 0 || app.activeSystemVPNProfile.Ready {
		t.Fatal("Disconnect was undone by status refresh")
	}
}

func TestAppRecoveryRejectsOlderSessionRevision(t *testing.T) {
	active := systemVPNProfileIdentity{DaemonInstanceID: "daemon-a", SessionID: "session-1", SelectedNodeID: "node-1", Revision: 7, Ready: true}
	next := active
	next.SessionID, next.SelectedNodeID, next.ProfileHash = "session-2", "node-2", "new"
	if canReplaceActiveSystemVPNProfile(active, next) {
		t.Fatal("accepted session switch without newer revision")
	}
	next.Revision = 8
	next.DaemonInstanceID = "foreign-daemon"
	if canReplaceActiveSystemVPNProfile(active, next) {
		t.Fatal("accepted unexpected daemon instance")
	}
}

func TestAppRecoveryRetriesProviderActivationWithoutCancelingTransport(t *testing.T) {
	service := &recoveryRuntimeService{}
	host := &stubSystemVPNHost{supported: true,
		statusObservation: connectedSystemVPNObservation("daemon-a", 7, "session-1", "hash-old"),
		stopObservation:   systemVPNObservation{State: guiruntime.SystemVPNDisconnected},
		startObservation:  connectedSystemVPNObservation("daemon-a", 8, "session-2", "hash-new"),
		startErr:          errors.New("temporary provider start error"),
	}
	app, err := NewApp(service, newTestLogSink(t))
	if err != nil {
		t.Fatal(err)
	}
	app.systemVPN = host
	app.activeSystemVPNProfile = systemVPNProfileIdentity{DaemonInstanceID: "daemon-a", Revision: 7,
		SessionID: "session-1", SelectedNodeID: "node-1", ProfileHash: "hash-old", Ready: true,
		ProfileValidUntil: testSystemVPNProfileValidUntil}
	service.status = guiruntime.DesktopStatus{TransportState: guiruntime.StateConnected, ActiveNodeID: "node-2",
		SystemVPNProfile: json.RawMessage(`{"schema_revision":"system-vpn-profile.v1","daemon_instance_id":"daemon-a","profile_revision":8,"profile_hash":"hash-new","session_id":"session-2","selected_node_id":"node-2","ready":true,"expires_at":"2099-01-01T00:00:00Z"}`)}
	if _, err := app.GetStatus(); err == nil {
		t.Fatal("expected provider failure")
	}
	if service.disconnectedCalls != 0 || !app.activeSystemVPNProfile.Ready {
		t.Fatal("provider error canceled desired connection")
	}
	host.startErr = nil
	host.statusObservation = systemVPNObservation{State: guiruntime.SystemVPNDisconnected}
	status, err := app.GetStatus()
	if err != nil || !status.Connected || host.startCalls != 2 {
		t.Fatalf("provider retry failed: %+v %v", status, err)
	}
}

func TestAppRecoverySerializesWithUserStop(t *testing.T) {
	for _, stopVPNOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop-vpn-only-%t", stopVPNOnly), func(t *testing.T) {
			service := &recoveryRuntimeService{}
			host := &stubSystemVPNHost{supported: true,
				statusObservation: connectedSystemVPNObservation("daemon-a", 7, "session-1", "hash-old"),
				stopObservation:   systemVPNObservation{State: guiruntime.SystemVPNDisconnected},
				startObservation:  connectedSystemVPNObservation("daemon-a", 8, "session-2", "hash-new"),
				startEntered:      make(chan struct{}), releaseStart: make(chan struct{}),
			}
			app, err := NewApp(service, newTestLogSink(t))
			if err != nil {
				t.Fatal(err)
			}
			app.systemVPN = host
			app.activeSystemVPNProfile = systemVPNProfileIdentity{DaemonInstanceID: "daemon-a", Revision: 7,
				SessionID: "session-1", SelectedNodeID: "node-1", ProfileHash: "hash-old", Ready: true,
				ProfileValidUntil: testSystemVPNProfileValidUntil}
			service.status = guiruntime.DesktopStatus{TransportState: guiruntime.StateConnected, ActiveNodeID: "node-2",
				SystemVPNProfile: json.RawMessage(`{"schema_revision":"system-vpn-profile.v1","daemon_instance_id":"daemon-a","profile_revision":8,"profile_hash":"hash-new","session_id":"session-2","selected_node_id":"node-2","ready":true,"expires_at":"2099-01-01T00:00:00Z"}`)}
			recoveryDone := make(chan error, 1)
			go func() { _, err := app.GetStatus(); recoveryDone <- err }()
			<-host.startEntered
			stopDone := make(chan error, 1)
			go func() {
				var err error
				if stopVPNOnly {
					_, err = app.StopSystemVPN()
				} else {
					_, err = app.Disconnect()
				}
				stopDone <- err
			}()
			select {
			case err := <-stopDone:
				t.Fatalf("user stop bypassed in-flight recovery: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			close(host.releaseStart)
			if err := <-recoveryDone; err != nil {
				t.Fatal(err)
			}
			if err := <-stopDone; err != nil {
				t.Fatal(err)
			}
			host.statusObservation = systemVPNObservation{State: guiruntime.SystemVPNDisconnected}
			if _, err := app.GetStatus(); err != nil {
				t.Fatal(err)
			}
			if app.activeSystemVPNProfile.Ready || host.startCalls != 1 || host.stopCalls != 2 {
				t.Fatalf("recovery resurrected stopped VPN: starts=%d stops=%d", host.startCalls, host.stopCalls)
			}
		})
	}
}
