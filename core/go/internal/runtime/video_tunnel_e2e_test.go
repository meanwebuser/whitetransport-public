package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	utunnel "whitelist-bypass/relay/tunnel"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/proxy"
	itunnel "github.com/meanwebuser/whitetransport/core/internal/tunnel"
)

// --- Fake video tunnel adapter ---

// fakeVideoProvider implements provider.Provider, VideoTunnelAdapter, and
// DataTunnelProvider. It uses cross-wired mock DataTunnels for in-process
// e2e testing without real video call providers.
type fakeVideoProvider struct {
	mu       sync.Mutex
	tun      utunnel.DataTunnel
	role     string // "creator" or "joiner"
	pair     *fakeVideoPair
	started  bool
	joinErr  error // if set, StartEgressAddr returns this error
}

// fakeVideoPair holds the cross-wired DT pair shared between creator and joiner.
type fakeVideoPair struct {
	creatorDT utunnel.DataTunnel
	joinerDT  utunnel.DataTunnel
}

// dataCallbackSlot synchronizes callback installation with cross-wired sends.
type dataCallbackSlot struct {
	mu sync.RWMutex
	fn func([]byte)
}

func (s *dataCallbackSlot) load() func([]byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fn
}

func (s *dataCallbackSlot) store(fn func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
}

func newFakeVideoPair() *fakeVideoPair {
	var creatorOnData, joinerOnData dataCallbackSlot
	creatorDT := &mockDTForE2E{
		sendFn: func(data []byte) {
			if onData := joinerOnData.load(); onData != nil {
				onData(data)
			}
		},
		setOnDataFn:  creatorOnData.store,
		setOnCloseFn: func(fn func()) {},
	}
	joinerDT := &mockDTForE2E{
		sendFn: func(data []byte) {
			if onData := creatorOnData.load(); onData != nil {
				onData(data)
			}
		},
		setOnDataFn:  joinerOnData.store,
		setOnCloseFn: func(fn func()) {},
	}
	return &fakeVideoPair{creatorDT: creatorDT, joinerDT: joinerDT}
}

type mockDTForE2E struct {
	sendFn       func([]byte)
	setOnDataFn  func(func([]byte))
	setOnCloseFn func(func())
}

func (m *mockDTForE2E) SendData(data []byte) {
	if m.sendFn != nil {
		m.sendFn(data)
	}
}
func (m *mockDTForE2E) SetOnData(fn func([]byte)) {
	if m.setOnDataFn != nil {
		m.setOnDataFn(fn)
	}
}
func (m *mockDTForE2E) SetOnClose(fn func()) {
	if m.setOnCloseFn != nil {
		m.setOnCloseFn(fn)
	}
}
func (m *mockDTForE2E) Reconfigure(fps, batch int) {}

// provider.Provider implementation
func (a *fakeVideoProvider) ID() string                                  { return "fake-video" }
func (a *fakeVideoProvider) Type() provider.Type                         { return provider.TypeVideoCall }
func (a *fakeVideoProvider) Category() provider.Category                 { return provider.CategoryVideo }
func (a *fakeVideoProvider) Version() string                             { return "1.0.0" }
func (a *fakeVideoProvider) Configure(cfg provider.ProviderConfig) error { return nil }
func (a *fakeVideoProvider) GetSchema() provider.Schema                  { return provider.Schema{} }
func (a *fakeVideoProvider) Health() provider.Health                     { return provider.Health{} }
func (a *fakeVideoProvider) GetLimits() provider.Limits {
	return provider.Limits{MaxPayloadBytes: 32768}
}
func (a *fakeVideoProvider) GetMetrics() provider.Metrics   { return provider.Metrics{} }
func (a *fakeVideoProvider) UpdateMetrics(provider.Metrics) {}
func (a *fakeVideoProvider) Load() error                    { return nil }
func (a *fakeVideoProvider) Unload() error                  { return nil }

func (a *fakeVideoProvider) Send(ctx context.Context, payload []byte) error {
	a.mu.Lock()
	t := a.tun
	a.mu.Unlock()
	if t == nil {
		return fmt.Errorf("fake-video: tunnel not connected")
	}
	t.SendData(payload)
	return nil
}

func (a *fakeVideoProvider) Receive(ctx context.Context) ([]byte, error) {
	return nil, fmt.Errorf("fake-video: receive not supported")
}

// DataTunnelProvider — used by DataTunnelEgress.
func (a *fakeVideoProvider) DataTunnel() utunnel.DataTunnel {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tun
}

// VideoTunnelAdapter implementation
func (a *fakeVideoProvider) CreateAndStartEgress(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tun = a.pair.creatorDT
	a.role = "creator"
	a.started = true
	return "fake-video://room-test", nil
}

func (a *fakeVideoProvider) StartEgressAddr(ctx context.Context, addr string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.joinErr != nil {
		return a.joinErr
	}
	a.tun = a.pair.joinerDT
	a.role = "joiner"
	a.started = true
	return nil
}

func (a *fakeVideoProvider) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tun = nil
	a.started = false
	return nil
}

// Compile-time interface checks.
var _ provider.Provider = (*fakeVideoProvider)(nil)
var _ VideoTunnelAdapter = (*fakeVideoProvider)(nil)

// --- Tests ---

// TestVideoTunnelAdapterDiscoveryConnectDisconnect verifies the full control
// plane flow with a fake video tunnel adapter:
// 1. Node advertises via memory carrier
// 2. Client discovers node
// 3. Client connects — node calls CreateAndStartEgress, client calls StartEgressAddr
// 4. Egress endpoints carry the fake-video address
// 5. Client disconnects
func TestVideoTunnelAdapterDiscoveryConnectDisconnect(t *testing.T) {
	pair := newFakeVideoPair()
	nodeAdapter := &fakeVideoProvider{pair: pair}
	clientAdapter := &fakeVideoProvider{pair: pair}

	// Memory carriers for bootstrap/control.
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://vt-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://vt-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "vt-control", carriers.CarrierVKMessages)

	// Wrap the fake adapters as ProviderCarrier.
	nodeVideoCarrier, err := carriers.NewProviderCarrier(nodeAdapter, egressEndpoint)
	if err != nil {
		t.Fatalf("wrap node adapter: %v", err)
	}
	clientVideoCarrier, err := carriers.NewProviderCarrier(clientAdapter, egressEndpoint)
	if err != nil {
		t.Fatalf("wrap client adapter: %v", err)
	}

	nodeBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: nodeVideoCarrier, Endpoint: egressEndpoint},
	}
	clientBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: clientVideoCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(config.Config{
		Role: config.RoleNode, NodeID: "example-exit-node", DisplayName: "Example Exit Node",
		SocksListen: "127.0.0.1:0",
	}, nodeBindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("node control plane: %v", err)
	}
	clientControl, err := newTestControlPlane(config.Config{
		Role: config.RoleClient, ClientID: "e2e-client",
		SocksListen: "127.0.0.1:0",
	}, clientBindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("client control plane: %v", err)
	}

	// Verify fake-video is registered as a video tunnel carrier.
	if !nodeControl.videoTunnelCarrierIDs["fake-video"] {
		t.Error("node: fake-video should be in videoTunnelCarrierIDs")
	}
	if !clientControl.videoTunnelCarrierIDs["fake-video"] {
		t.Error("client: fake-video should be in videoTunnelCarrierIDs")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	// Wait for node discovery.
	waitForNodeVisible(t, clientControl, "example-exit-node", true)

	// Connect.
	status, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if status.State != statusStateConnected {
		t.Fatalf("state = %s, want connected", status.State)
	}
	if status.ActiveNodeID != "example-exit-node" {
		t.Fatalf("ActiveNodeID = %s, want example-exit-node", status.ActiveNodeID)
	}

	// Verify egress endpoints include the fake-video carrier.
	foundVideoEgress := false
	for _, ep := range status.EgressEndpoints {
		if ep.Carrier == "fake-video" {
			foundVideoEgress = true
			if !strings.HasPrefix(ep.Address, "fake-video://") {
				t.Errorf("egress address = %q, want fake-video:// prefix", ep.Address)
			}
		}
	}
	if !foundVideoEgress {
		t.Fatalf("no fake-video egress endpoint in %+v", status.EgressEndpoints)
	}

	// Verify node adapter created egress.
	nodeAdapter.mu.Lock()
	nodeStarted := nodeAdapter.started
	nodeRole := nodeAdapter.role
	nodeAdapter.mu.Unlock()
	if !nodeStarted {
		t.Error("node adapter CreateAndStartEgress was not called")
	}
	if nodeRole != "creator" {
		t.Errorf("node adapter role = %q, want creator", nodeRole)
	}

	// Verify client adapter joined.
	clientAdapter.mu.Lock()
	clientStarted := clientAdapter.started
	clientRole := clientAdapter.role
	clientAdapter.mu.Unlock()
	if !clientStarted {
		t.Error("client adapter StartEgressAddr was not called")
	}
	if clientRole != "joiner" {
		t.Errorf("client adapter role = %q, want joiner", clientRole)
	}

	// Disconnect.
	disc := clientControl.Disconnect()
	if disc.State != statusStateDisconnected {
		t.Errorf("disconnect state = %s", disc.State)
	}
}

// TestVideoTunnelStartFailureDoesNotClaimConnected protects the product status
// contract: receiving a session answer is not sufficient when the local video
// adapter cannot join the advertised egress endpoint.
func TestVideoTunnelStartFailureDoesNotClaimConnected(t *testing.T) {
	pair := newFakeVideoPair()
	nodeAdapter := &fakeVideoProvider{pair: pair}
	clientAdapter := &fakeVideoProvider{pair: pair, joinErr: fmt.Errorf("simulated client join failure")}

	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://failed-join-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://failed-join-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "failed-join-control", carriers.CarrierVKMessages)
	nodeVideoCarrier, err := carriers.NewProviderCarrier(nodeAdapter, egressEndpoint)
	if err != nil {
		t.Fatalf("wrap node adapter: %v", err)
	}
	clientVideoCarrier, err := carriers.NewProviderCarrier(clientAdapter, egressEndpoint)
	if err != nil {
		t.Fatalf("wrap client adapter: %v", err)
	}

	nodeControl, err := newTestControlPlane(config.Config{
		Role: config.RoleNode, NodeID: "failed-join-node", SocksListen: "127.0.0.1:0",
	}, map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: nodeVideoCarrier, Endpoint: egressEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("node control plane: %v", err)
	}
	clientControl, err := newTestControlPlane(config.Config{
		Role: config.RoleClient, ClientID: "failed-join-client", SocksListen: "127.0.0.1:0",
	}, map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: clientVideoCarrier, Endpoint: egressEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("client control plane: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)
	waitForNodeVisible(t, clientControl, "failed-join-node", true)

	status, err := clientControl.Connect(ctx, "failed-join-node")
	if err != nil {
		t.Fatalf("connect negotiation: %v", err)
	}
	if status.State != statusStateDegraded {
		t.Fatalf("state after local StartEgressAddr failure = %q, want %q", status.State, statusStateDegraded)
	}
	if !strings.Contains(status.LastError, "simulated client join failure") {
		t.Fatalf("last error after local StartEgressAddr failure = %q, want join failure", status.LastError)
	}
	clientControl.Disconnect()
}

// TestRoleReversalClientCreatesRoomNodeJoinsAsGuest verifies the role-reversal
// flow: client creates the egress room locally (ClientRoomCreation config),
// node joins as guest via StartEgressAddr.
func TestRoleReversalClientCreatesRoomNodeJoinsAsGuest(t *testing.T) {
	pair := newFakeVideoPair()
	nodeAdapter := &fakeVideoProvider{pair: pair}
	clientAdapter := &fakeVideoProvider{pair: pair}

	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://rr-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://rr-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "rr-control", carriers.CarrierVKMessages)

	nodeVideoCarrier, err := carriers.NewProviderCarrier(nodeAdapter, egressEndpoint)
	if err != nil {
		t.Fatalf("wrap node adapter: %v", err)
	}
	clientVideoCarrier, err := carriers.NewProviderCarrier(clientAdapter, egressEndpoint)
	if err != nil {
		t.Fatalf("wrap client adapter: %v", err)
	}

	nodeBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: nodeVideoCarrier, Endpoint: egressEndpoint},
	}
	clientBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: clientVideoCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(config.Config{
		Role: config.RoleNode, NodeID: "example-exit-node", DisplayName: "Example Exit Node",
		SocksListen: "127.0.0.1:0",
	}, nodeBindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("node control plane: %v", err)
	}
	clientControl, err := newTestControlPlane(config.Config{
		Role: config.RoleClient, ClientID: "rr-client",
		SocksListen:         "127.0.0.1:0",
		ClientRoomCreation:  true,
	}, clientBindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("client control plane: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	waitForNodeVisible(t, clientControl, "example-exit-node", true)

	status, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if status.State != statusStateConnected {
		t.Fatalf("state = %s, want connected", status.State)
	}

	clientAdapter.mu.Lock()
	clientRole := clientAdapter.role
	clientStarted := clientAdapter.started
	clientAdapter.mu.Unlock()
	if !clientStarted {
		t.Error("client adapter CreateAndStartEgress was not called")
	}
	if clientRole != "creator" {
		t.Errorf("client adapter role = %q, want creator (role reversal)", clientRole)
	}

	nodeAdapter.mu.Lock()
	nodeRole := nodeAdapter.role
	nodeStarted := nodeAdapter.started
	nodeAdapter.mu.Unlock()
	if !nodeStarted {
		t.Error("node adapter StartEgressAddr was not called")
	}
	if nodeRole != "joiner" {
		t.Errorf("node adapter role = %q, want joiner (role reversal)", nodeRole)
	}

	if len(status.EgressEndpoints) == 0 {
		t.Error("expected at least one egress endpoint in status")
	}

	disc := clientControl.Disconnect()
	if disc.State != statusStateDisconnected {
		t.Errorf("disconnect state = %s", disc.State)
	}
}

// TestRoleReversalFallbackWhenGuestJoinFails verifies that when the node cannot
// join the client room (e.g. adapter mismatch), it falls back to creating its
// own room (legacy behavior).
func TestRoleReversalFallbackWhenGuestJoinFails(t *testing.T) {
	pair := newFakeVideoPair()
	nodeAdapter := &fakeVideoProvider{pair: pair, joinErr: fmt.Errorf("simulated guest join failure")}
	clientAdapter := &fakeVideoProvider{pair: pair}

	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://fb-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://fb-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "fb-control", carriers.CarrierVKMessages)

	nodeVideoCarrier, err := carriers.NewProviderCarrier(nodeAdapter, egressEndpoint)
	if err != nil {
		t.Fatalf("wrap node adapter: %v", err)
	}
	clientVideoCarrier, err := carriers.NewProviderCarrier(clientAdapter, egressEndpoint)
	if err != nil {
		t.Fatalf("wrap client adapter: %v", err)
	}

	nodeBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: nodeVideoCarrier, Endpoint: egressEndpoint},
	}
	clientBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: clientVideoCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(config.Config{
		Role: config.RoleNode, NodeID: "example-exit-node", DisplayName: "Example Exit Node",
		SocksListen: "127.0.0.1:0",
	}, nodeBindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("node control plane: %v", err)
	}
	clientControl, err := newTestControlPlane(config.Config{
		Role: config.RoleClient, ClientID: "fb-client",
		SocksListen:        "127.0.0.1:0",
		ClientRoomCreation: true,
	}, clientBindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("client control plane: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	waitForNodeVisible(t, clientControl, "example-exit-node", true)

	status, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("connect: %v (fallback should succeed)", err)
	}
	if status.State != statusStateConnected {
		t.Fatalf("state = %s, want connected (fallback)", status.State)
	}

	// Node fell back to creating its own room.
	nodeAdapter.mu.Lock()
	nodeRole := nodeAdapter.role
	nodeAdapter.mu.Unlock()
	if nodeRole != "creator" {
		t.Errorf("node role = %q, want creator (fallback to legacy)", nodeRole)
	}

	clientControl.Disconnect()
}
// through a fake video tunnel using the DataTunnelEgress binary protocol.
// This simulates what happens after the control plane establishes the session:
// node-side ServeEgress handles incoming connections, client-side DialContext
// opens a connection through the tunnel to a real TCP echo server.
func TestDataFlowThroughVideoTunnel(t *testing.T) {
	pair := newFakeVideoPair()

	// Start a real TCP echo server.
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoListener.Close()
	echoAddr := echoListener.Addr().String()
	go func() {
		for {
			conn, err := echoListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ep := carriers.Endpoint{ID: "fake-video-ep", Carrier: "fake-video"}

	// Node side: wrap the creator DT in DataTunnelEgress and serve.
	nodeProvider := &dtProvider{dt: pair.creatorDT}
	nodeEgress := itunnel.NewDataTunnelEgress("test-node", "fake-video", "fake-video-ep", nodeProvider)
	go func() { _ = nodeEgress.ServeEgress(ctx, nil) }()

	time.Sleep(50 * time.Millisecond) // let node start serving

	// Client side: wrap the joiner DT in DataTunnelEgress and dial.
	clientProvider := &dtProvider{dt: pair.joinerDT}
	clientEgress := itunnel.NewDataTunnelEgress("test-client", "fake-video", "fake-video-ep", clientProvider)

	conn, err := clientEgress.DialContext(ctx, ep, echoAddr)
	if err != nil {
		t.Fatalf("DialContext through video tunnel: %v", err)
	}
	defer conn.Close()

	// Write data and read echo.
	payload := "hello through video tunnel"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 128)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])
	if got != payload {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
}

// TestSOCKSProxyThroughVideoTunnel verifies the full SOCKS5 proxy flow
// through a fake video tunnel: SOCKS client → SOCKS server → EgressDialer →
// DataTunnelEgress → fake video tunnel → echo server → back.
func TestSOCKSProxyThroughVideoTunnel(t *testing.T) {
	pair := newFakeVideoPair()

	// Start a real HTTP server as the "internet target".
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proxied via video tunnel path=%s", r.URL.Path)
	}))
	defer target.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ep := carriers.Endpoint{ID: "fake-video-ep", Carrier: "fake-video"}

	// Node side: serve egress through the creator DT.
	nodeProvider := &dtProvider{dt: pair.creatorDT}
	nodeEgress := itunnel.NewDataTunnelEgress("test-node", "fake-video", "fake-video-ep", nodeProvider)
	go func() { _ = nodeEgress.ServeEgress(ctx, nil) }()
	time.Sleep(50 * time.Millisecond)

	// Client side: create a DataTunnelEgress for dialing.
	clientProvider := &dtProvider{dt: pair.joinerDT}
	clientEgress := itunnel.NewDataTunnelEgress("test-client", "fake-video", "fake-video-ep", clientProvider)

	// SOCKS5 server that dials through the video tunnel.
	socksListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks listen: %v", err)
	}
	socksAddr := socksListener.Addr().String()
	socksListener.Close()

	socksServer := proxy.Server{
		ListenAddr: socksAddr,
		EgressDialer: func(ctx context.Context, targetAddr string) (net.Conn, string, error) {
			conn, err := clientEgress.DialContext(ctx, ep, targetAddr)
			return conn, "fake-video", err
		},
	}
	errCh := make(chan error, 1)
	go func() { errCh <- socksServer.ListenAndServe(ctx) }()

	// Wait for SOCKS server to be ready.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", socksAddr, 100*time.Millisecond); err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Perform SOCKS5 handshake through the proxy.
	targetURL, _ := url.Parse(target.URL)
	port, _ := net.LookupPort("tcp", targetURL.Port())

	conn, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer conn.Close()

	// SOCKS5 greeting.
	conn.Write([]byte{0x05, 1, 0x00})
	reply := make([]byte, 2)
	io.ReadFull(conn, reply)
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("socks greeting reply: %v", reply)
	}

	// SOCKS5 CONNECT to the HTTP target.
	connectReq := []byte{0x05, 0x01, 0x00, 0x01}  // CONNECT, IPv4
	connectReq = append(connectReq, 127, 0, 0, 1) // 127.0.0.1
	connectReq = append(connectReq, byte(port>>8), byte(port))
	conn.Write(connectReq)

	connectReply := make([]byte, 4)
	io.ReadFull(conn, connectReply)
	if connectReply[1] != 0x00 {
		t.Fatalf("socks connect reply code = %d, want 0", connectReply[1])
	}
	// Discard bound address (IPv4 = 4 bytes + 2 port).
	discard := make([]byte, 4+2)
	io.ReadFull(conn, discard)

	// Send HTTP request through the SOCKS tunnel.
	fmt.Fprintf(conn, "GET /test-path HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", targetURL.Host)
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		if err := cw.CloseWrite(); err != nil {
			t.Fatalf("close write: %v", err)
		}
	}

	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	response := string(body)
	if !strings.Contains(response, "proxied via video tunnel path=/test-path") {
		t.Fatalf("unexpected response: %s", response)
	}

	cancel()
}

// dtProvider wraps a DataTunnel as a DataTunnelProvider.
type dtProvider struct {
	dt utunnel.DataTunnel
}

func (p *dtProvider) DataTunnel() utunnel.DataTunnel { return p.dt }
