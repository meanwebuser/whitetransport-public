package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

type backgroundRecoveryService struct {
	stubRuntimeService
	current       atomic.Pointer[guiruntime.DesktopStatus]
	statusCalls   atomic.Int32
	blockStatus   bool
	statusEntered chan struct{}
	enteredOnce   sync.Once
}

func (s *backgroundRecoveryService) Status(ctx context.Context) (guiruntime.DesktopStatus, error) {
	s.statusCalls.Add(1)
	if s.blockStatus {
		s.enteredOnce.Do(func() { close(s.statusEntered) })
		<-ctx.Done()
		return guiruntime.DesktopStatus{}, ctx.Err()
	}
	return *s.current.Load(), nil
}

type backgroundRecoveryHost struct {
	stubSystemVPNHost
	mu               sync.Mutex
	observation      systemVPNObservation
	events           chan string
	permissionDenied bool
}

func (h *backgroundRecoveryHost) Status(context.Context) (systemVPNObservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.observation, nil
}
func (h *backgroundRecoveryHost) Start(ctx context.Context, profile json.RawMessage) (systemVPNObservation, error) {
	return h.activate(ctx, profile, "start")
}
func (h *backgroundRecoveryHost) Refresh(ctx context.Context, profile json.RawMessage) (systemVPNObservation, error) {
	return h.activate(ctx, profile, "refresh")
}
func (h *backgroundRecoveryHost) activate(_ context.Context, profile json.RawMessage, event string) (systemVPNObservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	identity, err := decodeSystemVPNProfileIdentity(profile)
	if err != nil {
		return systemVPNObservation{}, err
	}
	h.events <- event
	if h.permissionDenied {
		return systemVPNObservation{State: guiruntime.SystemVPNPermissionRequired}, errors.New("permission_required: canceled")
	}
	h.observation = connectedSystemVPNObservationForIdentity(identity)
	return h.observation, nil
}
func (h *backgroundRecoveryHost) Stop(context.Context) (systemVPNObservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observation = systemVPNObservation{State: guiruntime.SystemVPNDisconnected}
	return h.observation, nil
}

func backgroundRecoveryStatus(revision uint64, session string) guiruntime.DesktopStatus {
	identity := systemVPNProfileIdentity{DaemonInstanceID: "daemon-a", Revision: revision, SessionID: session, SelectedNodeID: "node-1", ProfileHash: session, Ready: true, ProfileValidUntil: testSystemVPNProfileValidUntil}
	profile, _ := json.Marshal(map[string]any{"schema_revision": "system-vpn-profile.v1", "daemon_instance_id": identity.DaemonInstanceID, "profile_revision": revision, "profile_hash": session, "session_id": session, "selected_node_id": "node-1", "ready": true, "expires_at": identity.ProfileValidUntil})
	return guiruntime.DesktopStatus{TransportState: guiruntime.StateConnected, ActiveNodeID: "node-1", SystemVPNProfile: profile}
}

func TestBackgroundRecoveryActivatesAndRefreshesWithoutUIStatusPolling(t *testing.T) {
	for _, initialFailure := range []bool{true, false} {
		name := "session-refresh"
		if initialFailure {
			name = "initial-failure"
		}
		t.Run(name, func(t *testing.T) {
			initial := backgroundRecoveryStatus(7, "initial")
			service := &backgroundRecoveryService{stubRuntimeService: stubRuntimeService{connected: initial}}
			service.current.Store(&initial)
			if initialFailure {
				service.connectErr = errors.New("no_egress")
			}
			host := &backgroundRecoveryHost{stubSystemVPNHost: stubSystemVPNHost{supported: true}, observation: systemVPNObservation{State: guiruntime.SystemVPNDisconnected}, events: make(chan string, 10)}
			app, err := NewApp(service, newTestLogSink(t))
			if err != nil {
				t.Fatal(err)
			}
			app.systemVPN = host
			app.startup(context.Background())
			defer app.shutdown(context.Background())
			_, err = app.Connect("node-1")
			if (err != nil) != initialFailure {
				t.Fatalf("Connect=%v, initialFailure=%t", err, initialFailure)
			}
			if !initialFailure {
				<-host.events
			}
			recovered := backgroundRecoveryStatus(8, "recovered")
			service.current.Store(&recovered)
			want := "refresh"
			if initialFailure {
				want = "start"
			}
			select {
			case got := <-host.events:
				if got != want {
					t.Fatalf("event=%s want=%s", got, want)
				}
			case <-time.After(2500 * time.Millisecond):
				t.Fatal("recovery waited for a UI GetStatus call")
			}
		})
	}
}

func TestBackgroundRecoveryDoesNotActivateWithoutConnect(t *testing.T) {
	status := backgroundRecoveryStatus(7, "ready")
	service := &backgroundRecoveryService{}
	service.current.Store(&status)
	host := &backgroundRecoveryHost{stubSystemVPNHost: stubSystemVPNHost{supported: true}, events: make(chan string, 10)}
	app, err := NewApp(service, newTestLogSink(t))
	if err != nil {
		t.Fatal(err)
	}
	app.systemVPN = host
	app.startup(context.Background())
	done := app.backgroundDone
	app.startup(context.Background())
	if app.backgroundDone != done {
		t.Fatal("repeated startup created another reconciliation loop")
	}
	defer app.shutdown(context.Background())
	select {
	case event := <-host.events:
		t.Fatalf("activation without Connect: %s", event)
	case <-time.After(1200 * time.Millisecond):
	}
	if service.statusCalls.Load() != 0 {
		t.Fatal("idle background reconciliation should not inspect or mutate unrequested transport")
	}
}

func TestBackgroundRecoveryPermissionFailureDoesNotRepeatAuthorization(t *testing.T) {
	initial := backgroundRecoveryStatus(7, "initial")
	service := &backgroundRecoveryService{stubRuntimeService: stubRuntimeService{connected: initial}}
	service.current.Store(&initial)
	host := &backgroundRecoveryHost{stubSystemVPNHost: stubSystemVPNHost{supported: true}, observation: systemVPNObservation{State: guiruntime.SystemVPNDisconnected}, events: make(chan string, 10)}
	app, err := NewApp(service, newTestLogSink(t))
	if err != nil {
		t.Fatal(err)
	}
	app.systemVPN = host
	app.startup(context.Background())
	defer app.shutdown(context.Background())
	if _, err := app.Connect("node-1"); err != nil {
		t.Fatal(err)
	}
	<-host.events
	host.mu.Lock()
	host.permissionDenied = true
	host.mu.Unlock()
	recovered := backgroundRecoveryStatus(8, "recovered")
	service.current.Store(&recovered)
	select {
	case event := <-host.events:
		if event != "refresh" {
			t.Fatalf("unexpected event=%s", event)
		}
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("no background refresh attempt")
	}
	select {
	case event := <-host.events:
		t.Fatalf("authorization repeated during background polling: %s", event)
	case <-time.After(1200 * time.Millisecond):
	}
}

func TestBackgroundRecoveryShutdownCancelsInFlightStatusBeforeTakingLifecycleLock(t *testing.T) {
	service := &backgroundRecoveryService{stubRuntimeService: stubRuntimeService{connectErr: errors.New("no_egress")}, blockStatus: true, statusEntered: make(chan struct{})}
	host := &backgroundRecoveryHost{stubSystemVPNHost: stubSystemVPNHost{supported: true}, events: make(chan string, 10)}
	app, err := NewApp(service, newTestLogSink(t))
	if err != nil {
		t.Fatal(err)
	}
	app.systemVPN = host
	app.startup(context.Background())
	defer app.shutdown(context.Background())
	_, _ = app.Connect("node-1")
	select {
	case <-service.statusEntered:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("background status did not begin")
	}
	shutdownDone := make(chan struct{})
	go func() { app.shutdown(context.Background()); close(shutdownDone) }()
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited on lifecycle lock without cancelling in-flight status")
	}
	select {
	case <-app.backgroundDone:
	default:
		t.Fatal("shutdown leaked the reconciliation goroutine")
	}
	if service.statusCalls.Load() != 1 {
		t.Fatal("shutdown started an extra recovery request")
	}
}
