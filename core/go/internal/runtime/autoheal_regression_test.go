package runtime

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

// A failed destination (or a closed browser request) must not disable a
// healthy transport for every unrelated destination until background probes.
func TestAutohealDoesNotQuarantineRequestFailures(t *testing.T) {
	for _, failure := range []error{errors.New("target connection refused"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			endpoint := carriers.Endpoint{ID: "primary", Carrier: carriers.CarrierSSHTCP}
			routes := &requestFailureTunnel{err: failure}
			control := &ControlPlane{
				cfg:    config.Config{Role: config.RoleClient},
				active: &activeSession{SessionID: "request-failure", EgressEndpoints: []carriers.Endpoint{endpoint}},
				policy: policy.DefaultAdaptivePolicy(), tunnel: routes,
				egressRecovery: policy.NewEgressRecoveryTracker(policy.EgressRecoveryConfig{FailureThreshold: 1}, nil),
			}
			if conn, _, err := control.DialEgress(context.Background(), "unavailable.test:443"); err == nil {
				conn.Close()
				t.Fatal("failed request unexpectedly succeeded")
			}
			if !control.egressRecovery.CanDial(policy.EgressEndpointKey(endpoint)) {
				t.Fatal("request failure quarantined a healthy route")
			}
			routes.err = nil
			conn, route, err := control.DialEgress(context.Background(), "available.test:443")
			if err != nil {
				t.Fatalf("next destination failed: %v", err)
			}
			defer conn.Close()
			if route != endpoint.ID {
				t.Fatalf("route = %s, want primary", route)
			}
		})
	}
}

type requestFailureTunnel struct{ err error }

func (*requestFailureTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }
func (t *requestFailureTunnel) DialContext(context.Context, carriers.Endpoint, string) (net.Conn, error) {
	if t.err != nil {
		return nil, t.err
	}
	client, peer := net.Pipe()
	peer.Close()
	return client, nil
}

// Initial selection and failback must skip routes the installed tunnel cannot
// use. Otherwise an unsupported high-score route pins the recovery scan above
// the real degraded primary, so that primary is never probed or restored.
func TestAutohealRecoversUsablePrimaryBelowUnsupportedRoute(t *testing.T) {
	now := time.Now()
	unsupported := carriers.Endpoint{ID: "unsupported", Carrier: carriers.CarrierSSHTCP}
	primary := carriers.Endpoint{ID: "primary", Carrier: carriers.CarrierVKDocs1024}
	backup := carriers.Endpoint{ID: "backup", Carrier: carriers.CarrierFileMailbox}
	probe := &recoveryProbeCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "autoheal-primary", carriers.CarrierVKDocs1024), results: []bool{true}}
	recovery := policy.NewEgressRecoveryTracker(policy.EgressRecoveryConfig{
		FailureThreshold: 1, InitialProbeDelay: time.Second, ProbeSuccesses: 1,
	}, func() time.Time { return now })
	routes := &usableRecoveryTunnel{recoveryRouteTunnel: recoveryRouteTunnel{primaryID: primary.ID, backupID: backup.ID}, unsupportedID: unsupported.ID}
	control := &ControlPlane{
		cfg:    config.Config{Role: config.RoleClient},
		active: &activeSession{SessionID: "usable-recovery", EgressEndpoints: []carriers.Endpoint{unsupported, primary, backup}},
		policy: policy.DefaultAdaptivePolicy(), tunnel: routes, egressRecovery: recovery,
		bindings: map[string]policy.CarrierBinding{primary.Carrier: {Carrier: probe, Endpoint: primary}},
	}
	recovery.Quarantine(policy.EgressEndpointKey(primary))
	conn, route, err := control.DialEgress(context.Background(), "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if route != backup.ID {
		t.Fatalf("route = %s, want backup", route)
	}
	fallbackConn := conn
	if got := control.Status().AutomaticEgressEndpointID; got != backup.ID {
		t.Fatalf("automatic preference = %s, want usable backup", got)
	}
	now = now.Add(time.Second)
	if !control.recoverOneEgressEndpoint(context.Background()) {
		t.Fatal("usable primary was never probed")
	}
	routes.mu.Lock()
	routes.allowPrimary = true
	routes.mu.Unlock()
	conn, route, err = control.DialEgress(context.Background(), "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if route != primary.ID {
		t.Fatalf("recovered route = %s, want primary", route)
	}
	for _, stream := range []net.Conn{fallbackConn, conn} {
		if err := stream.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Write([]byte("payload")); err != nil {
			t.Fatalf("write after failback: %v", err)
		}
		payload := make([]byte, 7)
		if _, err := io.ReadFull(stream, payload); err != nil || string(payload) != "payload" {
			t.Fatalf("payload after failback = %q, err = %v", payload, err)
		}
	}
}

type usableRecoveryTunnel struct {
	recoveryRouteTunnel
	unsupportedID string
}

func TestAutohealUnsupportedRouteCannotHideCarrierExhaustion(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := "foreground"
		if background {
			name = "background"
		}
		t.Run(name, func(t *testing.T) {
			unsupported := carriers.Endpoint{ID: "unsupported", Carrier: carriers.CarrierSSHTCP}
			primary := carriers.Endpoint{ID: "primary", Carrier: carriers.CarrierVKDocs1024}
			control, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "exhausted-client", SocksListen: "127.0.0.1:0"}, nil, policy.DefaultAdaptivePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer control.Stop()
			control.active = &activeSession{NodeID: "failed-node", SessionID: "exhausted-session", EgressEndpoints: []carriers.Endpoint{unsupported, primary}, AutomaticEgressEndpointID: primary.ID}
			control.tunnel = &usableRecoveryTunnel{unsupportedID: unsupported.ID}
			control.clientIntentCtx, control.clientIntentCancel = context.WithCancel(context.Background())
			control.egressRecovery = policy.NewEgressRecoveryTracker(policy.DefaultEgressRecoveryConfig(), nil)
			control.egressRouteStreams = newRouteStreamRegistry(defaultRouteInterruptAfter, nil)
			failure := session.NewCarrierFailure(errors.New("stream lost"))
			control.egressRouteStreams.quarantine(sessionRouteKey(control.active.SessionID, primary), failure)
			control.recordQuarantinedEgressStreamFailure(control.active.SessionID, primary, failure)
			if background {
				control.recoverConnectionIntent()
			} else {
				_, _, err = control.DialEgress(context.Background(), "example.test:443")
				if err == nil {
					t.Fatal("all usable routes failed, but dial succeeded")
				}
			}
			// No replacement is advertised: the dead session must be abandoned
			// and persistent intent retained for a later node advertisement.
			if control.active != nil {
				t.Fatal("unsupported route suppressed node reselection")
			}
			if control.clientIntentCtx == nil || control.clientIntentCtx.Err() != nil {
				t.Fatal("carrier exhaustion cancelled Connect intent")
			}
		})
	}
}

func (t *usableRecoveryTunnel) SupportsEndpoint(endpoint carriers.Endpoint) bool {
	return endpoint.ID != t.unsupportedID
}
