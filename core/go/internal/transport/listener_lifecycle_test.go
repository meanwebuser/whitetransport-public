package transport

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

func TestStartListenerCarriersCompletesBeforeControlStartBoundary(t *testing.T) {
	events := make([]string, 0, 2)
	listener := &fakeListenerCarrier{name: "ssh", events: &events}
	bindings := map[string]policy.CarrierBinding{
		"ssh.fabric": {Carrier: listener, Endpoint: carriers.Endpoint{ID: "ssh-listener", Carrier: carriers.CarrierSSHFabric}},
	}

	started, err := startListenerCarriers(context.Background(), bindings)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	events = append(events, "control:start")
	t.Cleanup(func() { _ = stopListenerCarriers(context.Background(), started) })

	want := []string{"listener:ssh:start", "control:start"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestStartListenerCarriersUnwindsEarlierListenerOnFailure(t *testing.T) {
	events := make([]string, 0, 3)
	startFailure := errors.New("listener bind failed")
	first := &fakeListenerCarrier{name: "first", events: &events}
	second := &fakeListenerCarrier{name: "second", events: &events, startErr: startFailure}
	bindings := map[string]policy.CarrierBinding{
		"a-first":  {Carrier: first, Endpoint: carriers.Endpoint{ID: "first", Carrier: carriers.CarrierSSHFabric}},
		"b-second": {Carrier: second, Endpoint: carriers.Endpoint{ID: "second", Carrier: carriers.CarrierSSHFabric}},
	}

	started, err := startListenerCarriers(context.Background(), bindings)
	if !errors.Is(err, startFailure) {
		t.Fatalf("start error = %v, want %v", err, startFailure)
	}
	if len(started) != 0 {
		t.Fatalf("started listeners = %d, want none after unwind", len(started))
	}
	want := []string{"listener:first:start", "listener:second:start", "listener:first:stop"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestStopListenerCarriersStopsListenerWithoutActiveSession(t *testing.T) {
	events := make([]string, 0, 1)
	listener := &fakeListenerCarrier{name: "idle", events: &events}

	if err := stopListenerCarriers(context.Background(), []carriers.ListenerCarrier{listener}); err != nil {
		t.Fatalf("stop idle listener: %v", err)
	}
	if listener.stopCalls != 1 {
		t.Fatalf("StopListener calls = %d, want 1", listener.stopCalls)
	}
	want := []string{"listener:idle:stop"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestStartListenerCarriersStartsSharedAliasInstanceOnce(t *testing.T) {
	events := make([]string, 0, 1)
	shared := &fakeListenerCarrier{name: "shared", events: &events}
	bindings := map[string]policy.CarrierBinding{
		"ssh-control": {Carrier: shared, Endpoint: carriers.Endpoint{ID: "control", Carrier: carriers.CarrierSSHFabric}},
		"ssh-egress":  {Carrier: shared, Endpoint: carriers.Endpoint{ID: "egress", Carrier: carriers.CarrierSSHFabric}},
	}

	started, err := startListenerCarriers(context.Background(), bindings)
	if err != nil {
		t.Fatalf("start listeners: %v", err)
	}
	t.Cleanup(func() { _ = stopListenerCarriers(context.Background(), started) })

	if shared.startCalls != 1 {
		t.Fatalf("StartListener calls = %d, want 1 for aliases sharing one instance", shared.startCalls)
	}
	if len(started) != 1 {
		t.Fatalf("started listeners = %d, want 1", len(started))
	}
}

type fakeListenerCarrier struct {
	carriers.BaseCarrier
	name       string
	events     *[]string
	startErr   error
	startCalls int
	stopCalls  int
}

func (f *fakeListenerCarrier) Descriptor() carriers.Descriptor {
	return carriers.Descriptor{ID: carriers.CarrierSSHFabric}
}

func (f *fakeListenerCarrier) Write(context.Context, carriers.Endpoint, fabric.Envelope) error {
	return nil
}

func (f *fakeListenerCarrier) Read(context.Context, carriers.Endpoint, carriers.Cursor) (carriers.ReadResult, error) {
	return carriers.ReadResult{}, nil
}

func (f *fakeListenerCarrier) Probe(context.Context, carriers.Endpoint) (carriers.Metrics, error) {
	return carriers.Metrics{}, nil
}

func (f *fakeListenerCarrier) StartListener(_ context.Context, _ carriers.Endpoint) error {
	f.startCalls++
	*f.events = append(*f.events, "listener:"+f.name+":start")
	return f.startErr
}

func (f *fakeListenerCarrier) StopListener(context.Context) error {
	f.stopCalls++
	*f.events = append(*f.events, "listener:"+f.name+":stop")
	return nil
}

func (f *fakeListenerCarrier) ListenerHealth() carriers.HealthStatus {
	return carriers.HealthStatus{Healthy: true, Ready: f.startCalls > f.stopCalls}
}

var _ carriers.ListenerCarrier = (*fakeListenerCarrier)(nil)
