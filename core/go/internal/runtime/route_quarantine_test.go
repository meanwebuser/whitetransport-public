package runtime

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestRouteStreamRegistryRejectsPreQuarantineDialRegistration(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	lease, ok := registry.beginDial("primary")
	if !ok {
		t.Fatal("primary unexpectedly ineligible")
	}
	registry.quarantine("primary", errors.New("forced failure"))
	left, right := net.Pipe()
	defer right.Close()
	if conn, registered := registry.register(lease, left, nil); registered || conn != nil {
		t.Fatal("stale generation registered after quarantine")
	}
	if registry.canDial("primary") {
		t.Fatal("quarantined route remained eligible")
	}
}

func TestRouteStreamRegistryGenerationBarrierRejectsConcurrentStaleDials(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	const attempts = 64
	leases := make([]routeDialLease, attempts)
	for index := range leases {
		lease, ok := registry.beginDial("primary")
		if !ok {
			t.Fatal("primary became ineligible before barrier")
		}
		leases[index] = lease
	}
	registry.quarantine("primary", errors.New("barrier"))

	var registered atomic.Int64
	var wait sync.WaitGroup
	for _, lease := range leases {
		wait.Add(1)
		go func(lease routeDialLease) {
			defer wait.Done()
			left, right := net.Pipe()
			defer right.Close()
			if conn, ok := registry.register(lease, left, nil); ok {
				registered.Add(1)
				_ = conn.Close()
			}
		}(lease)
	}
	wait.Wait()
	if registered.Load() != 0 || registry.activeCount("primary") != 0 {
		t.Fatalf("post-barrier registrations=%d active=%d", registered.Load(), registry.activeCount("primary"))
	}
}

func TestRouteStreamRegistryInterruptsOpenStreamWithinBound(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	lease, _ := registry.beginDial("primary")
	left, right := net.Pipe()
	defer right.Close()
	conn, ok := registry.register(lease, left, nil)
	if !ok {
		t.Fatal("could not register open stream")
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		readErr <- err
	}()
	started := time.Now()
	registry.quarantine("primary", errors.New("forced failure"))
	select {
	case err := <-readErr:
		if err == nil || time.Since(started) > 600*time.Millisecond {
			t.Fatalf("open stream interruption err=%v elapsed=%s", err, time.Since(started))
		}
	case <-time.After(time.Second):
		t.Fatal("open stream survived quarantine bound")
	}
	for deadline := time.Now().Add(time.Second); registry.activeCount("primary") != 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if registry.activeCount("primary") != 0 {
		t.Fatal("closed generation remained registered")
	}
}

func TestTrackedRouteConnIOFailureQuarantinesOnce(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	lease, _ := registry.beginDial("primary")
	left, right := net.Pipe()
	defer right.Close()
	var notifications atomic.Int64
	conn, ok := registry.register(lease, failOnReadConn{Conn: left}, func(error) { notifications.Add(1) })
	if !ok {
		t.Fatal("could not register stream")
	}
	_, _ = conn.Read(make([]byte, 1))
	_, _ = conn.Read(make([]byte, 1))
	if registry.canDial("primary") || notifications.Load() != 1 {
		t.Fatalf("eligible=%v notifications=%d", registry.canDial("primary"), notifications.Load())
	}
}

func TestTrackedRouteConnGracefulEOFDoesNotQuarantine(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	lease, _ := registry.beginDial("primary")
	left, right := net.Pipe()
	conn, ok := registry.register(lease, left, nil)
	if !ok {
		t.Fatal("could not register stream")
	}
	_ = right.Close()
	_, _ = conn.Read(make([]byte, 1))
	if !registry.canDial("primary") {
		t.Fatal("graceful EOF quarantined healthy route")
	}
	_ = conn.Close()
}

func TestTrackedRouteConnGracefulEOFIgnoresUntypedStaleFailureCause(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	lease, _ := registry.beginDial("primary")
	conn, ok := registry.register(lease, staleFailureEOFConn{cause: errors.New("late provider poll error")}, nil)
	if !ok {
		t.Fatal("could not register stream")
	}
	_, _ = conn.Read(make([]byte, 1))
	if !registry.canDial("primary") {
		t.Fatal("graceful EOF inherited an untyped stale failure cause")
	}
	_ = conn.Close()
}

func TestTrackedRouteConnClosedPipeDoesNotQuarantine(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	lease, _ := registry.beginDial("primary")
	conn, ok := registry.register(lease, staticReadErrorConn{err: io.ErrClosedPipe}, nil)
	if !ok {
		t.Fatal("could not register stream")
	}
	_, _ = conn.Read(make([]byte, 1))
	if !registry.canDial("primary") {
		t.Fatal("normal net.Pipe stream closure quarantined healthy route")
	}
	_ = conn.Close()
}

func TestTrackedRouteConnEOFWithCausalCarrierFailureQuarantines(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	lease, _ := registry.beginDial("primary")
	conn, ok := registry.register(lease, staleFailureEOFConn{cause: session.NewCarrierFailure(errors.New("provider read failed"))}, nil)
	if !ok {
		t.Fatal("could not register stream")
	}
	_, _ = conn.Read(make([]byte, 1))
	if registry.canDial("primary") {
		t.Fatal("causal typed carrier failure did not quarantine route")
	}
	_ = conn.Close()
}

func TestRouteStreamRegistryShutdownInterruptsAndRejectsStaleSessionDials(t *testing.T) {
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	routeKey := "session-a\x00yandex.primary"
	lease, _ := registry.beginDial(routeKey)
	staleLease, _ := registry.beginDial(routeKey)
	left, right := net.Pipe()
	defer right.Close()
	conn, ok := registry.register(lease, left, nil)
	if !ok {
		t.Fatal("could not register stream")
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		readErr <- err
	}()

	registry.shutdownSession("session-a")
	select {
	case <-readErr:
	case <-time.After(time.Second):
		t.Fatal("session shutdown did not interrupt open stream")
	}
	if registry.activeCount(routeKey) != 0 || registry.routeCount() != 0 {
		t.Fatalf("session routes retained: active=%d routes=%d", registry.activeCount(routeKey), registry.routeCount())
	}
	late, peer := net.Pipe()
	defer peer.Close()
	if registered, ok := registry.register(staleLease, late, nil); ok || registered != nil {
		t.Fatal("stale pre-shutdown dial registered")
	}
	if _, ok := registry.beginDial(routeKey); ok {
		t.Fatal("shutdown registry accepted a new dial")
	}
}

func TestDisconnectShutsDownActiveSessionRouteRegistry(t *testing.T) {
	control, registry, routeKey, peer := controlWithTrackedSessionStream(t, "disconnect-session")
	defer peer.Close()
	control.Disconnect()
	if control.egressRouteStreams != nil {
		t.Fatal("disconnect retained active route registry")
	}
	if registry.routeCount() != 0 || registry.canDial(routeKey) {
		t.Fatalf("disconnect left route registry usable: routes=%d eligible=%v", registry.routeCount(), registry.canDial(routeKey))
	}
}

func TestStopShutsDownActiveSessionRouteRegistry(t *testing.T) {
	control, registry, routeKey, peer := controlWithTrackedSessionStream(t, "stop-session")
	defer peer.Close()
	control.stopCh = make(chan struct{})
	control.Stop()
	if control.egressRouteStreams != nil {
		t.Fatal("stop retained active route registry")
	}
	if registry.routeCount() != 0 || registry.canDial(routeKey) {
		t.Fatalf("stop left route registry usable: routes=%d eligible=%v", registry.routeCount(), registry.canDial(routeKey))
	}
}

func TestClientSessionExpiryShutsDownRouteRegistry(t *testing.T) {
	control, registry, routeKey, peer := controlWithTrackedSessionStream(t, "expiry-session")
	defer peer.Close()
	control.stopCh = make(chan struct{})
	answer := session.Answer{SessionID: "expiry-session", ExpiresAt: time.Now().Add(20 * time.Millisecond)}
	done := make(chan struct{})
	go func() {
		control.clientSessionTimeoutMonitor(context.Background(), answer)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session expiry monitor did not complete")
	}
	if control.egressRouteStreams != nil {
		t.Fatal("session expiry retained active route registry")
	}
	if registry.routeCount() != 0 || registry.canDial(routeKey) {
		t.Fatalf("session expiry left route registry usable: routes=%d eligible=%v", registry.routeCount(), registry.canDial(routeKey))
	}
}

func controlWithTrackedSessionStream(t *testing.T, sessionID string) (*ControlPlane, *routeStreamRegistry, string, net.Conn) {
	t.Helper()
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	routeKey := sessionID + "\x00yandex.primary"
	lease, _ := registry.beginDial(routeKey)
	left, right := net.Pipe()
	if _, ok := registry.register(lease, left, nil); !ok {
		t.Fatal("could not register active session stream")
	}
	control := &ControlPlane{
		active:             &activeSession{SessionID: sessionID},
		egressRouteStreams: registry,
		state:              statusStateConnected,
	}
	return control, registry, routeKey, right
}

func TestDialBatchGenerationBarrierRejectsStalePrimaryAndUsesBackup(t *testing.T) {
	primary := carriers.Endpoint{ID: "primary", Carrier: carriers.CarrierYandexDisk}
	backup := carriers.Endpoint{ID: "backup", Carrier: carriers.CarrierOKDocs256}
	tunnel := &quarantineRaceTunnel{primaryID: primary.ID, started: make(chan struct{}), release: make(chan struct{}), peers: make(chan net.Conn, 2)}
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	type result struct {
		conn  net.Conn
		route string
		err   error
	}
	first := make(chan result, 1)
	go func() {
		conn, route, err := dialBatchWithRegistry(context.Background(), tunnel, []carriers.Endpoint{primary}, "target:443", time.Second, "session", registry, nil, nil)
		first <- result{conn: conn, route: route, err: err}
	}()
	<-tunnel.started
	registry.quarantine(sessionRouteKey("session", primary), errors.New("forced primary failure"))
	close(tunnel.release)
	stale := <-first
	if stale.conn != nil || !errors.Is(stale.err, ErrStaleEgressGeneration) {
		t.Fatalf("stale primary result conn=%v route=%q err=%v", stale.conn, stale.route, stale.err)
	}

	conn, route, err := dialBatchWithRegistry(context.Background(), tunnel, []carriers.Endpoint{primary, backup}, "target:443", time.Second, "session", registry, nil, nil)
	if err != nil {
		t.Fatalf("backup dial: %v", err)
	}
	defer conn.Close()
	if route != backup.ID {
		t.Fatalf("route=%q, want backup", route)
	}
	for len(tunnel.peers) > 0 {
		_ = (<-tunnel.peers).Close()
	}
}

func TestSelectEgressEndpointReturnsFromManualPinToAutomatic(t *testing.T) {
	control := &ControlPlane{active: &activeSession{
		SessionID:                 "session",
		SelectedEgressEndpointID:  "primary",
		AutomaticEgressEndpointID: "backup",
		EgressEndpoints:           []carriers.Endpoint{{ID: "primary", Carrier: carriers.CarrierYandexDisk}},
	}}
	status, err := control.SelectEgressEndpoint("auto")
	if err != nil {
		t.Fatalf("return to automatic: %v", err)
	}
	if status.SelectedEgressEndpointID != "" || status.AutomaticEgressEndpointID != "primary" {
		t.Fatalf("unexpected automatic status: %+v", status)
	}
}

func TestDialRequestErrorsDoNotQuarantineHealthyRoute(t *testing.T) {
	endpoint := carriers.Endpoint{ID: "primary", Carrier: carriers.CarrierYandexDisk}
	for _, requestErr := range []error{context.Canceled, context.DeadlineExceeded, errors.New("target connection refused")} {
		registry := newRouteStreamRegistry(250*time.Millisecond, nil)
		_, _, _ = dialBatchWithRegistry(context.Background(), staticErrorTunnel{err: requestErr}, []carriers.Endpoint{endpoint}, "target:443", time.Second, "session", registry, nil, nil)
		if !registry.canDial(sessionRouteKey("session", endpoint)) {
			t.Fatalf("request error %v quarantined healthy carrier", requestErr)
		}
	}
}

func TestDialTypedCarrierFailureQuarantinesRoute(t *testing.T) {
	endpoint := carriers.Endpoint{ID: "primary", Carrier: carriers.CarrierYandexDisk}
	registry := newRouteStreamRegistry(250*time.Millisecond, nil)
	_, _, _ = dialBatchWithRegistry(context.Background(), staticErrorTunnel{err: session.NewCarrierFailure(errors.New("provider unavailable"))}, []carriers.Endpoint{endpoint}, "target:443", time.Second, "session", registry, nil, nil)
	if registry.canDial(sessionRouteKey("session", endpoint)) {
		t.Fatal("typed carrier failure left route eligible")
	}
}

type quarantineRaceTunnel struct {
	primaryID string
	started   chan struct{}
	release   chan struct{}
	peers     chan net.Conn
	once      sync.Once
}

type failOnReadConn struct{ net.Conn }

func (c failOnReadConn) Read([]byte) (int, error) { return 0, errors.New("provider read failure") }

type staleFailureEOFConn struct{ cause error }

func (c staleFailureEOFConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c staleFailureEOFConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c staleFailureEOFConn) Close() error                     { return nil }
func (c staleFailureEOFConn) LocalAddr() net.Addr              { return nil }
func (c staleFailureEOFConn) RemoteAddr() net.Addr             { return nil }
func (c staleFailureEOFConn) SetDeadline(time.Time) error      { return nil }
func (c staleFailureEOFConn) SetReadDeadline(time.Time) error  { return nil }
func (c staleFailureEOFConn) SetWriteDeadline(time.Time) error { return nil }
func (c staleFailureEOFConn) FailureCause() error              { return c.cause }

type staticReadErrorConn struct{ err error }

func (c staticReadErrorConn) Read([]byte) (int, error)         { return 0, c.err }
func (c staticReadErrorConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c staticReadErrorConn) Close() error                     { return nil }
func (c staticReadErrorConn) LocalAddr() net.Addr              { return nil }
func (c staticReadErrorConn) RemoteAddr() net.Addr             { return nil }
func (c staticReadErrorConn) SetDeadline(time.Time) error      { return nil }
func (c staticReadErrorConn) SetReadDeadline(time.Time) error  { return nil }
func (c staticReadErrorConn) SetWriteDeadline(time.Time) error { return nil }

type staticErrorTunnel struct{ err error }

func (t staticErrorTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }
func (t staticErrorTunnel) DialContext(context.Context, carriers.Endpoint, string) (net.Conn, error) {
	return nil, t.err
}

func (t *quarantineRaceTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }

func (t *quarantineRaceTunnel) DialContext(_ context.Context, endpoint carriers.Endpoint, _ string) (net.Conn, error) {
	if endpoint.ID == t.primaryID {
		t.once.Do(func() { close(t.started) })
		<-t.release
	}
	left, right := net.Pipe()
	t.peers <- right
	return left, nil
}
