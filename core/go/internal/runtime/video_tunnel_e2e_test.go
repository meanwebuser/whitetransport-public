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
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/proxy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
	itunnel "github.com/meanwebuser/whitetransport/core/internal/tunnel"
)

type roomPreparationProvider struct {
	*fakeVideoProvider
	id     string
	create func(context.Context) (string, error)
}

func (p *roomPreparationProvider) ID() string { return p.id }
func (p *roomPreparationProvider) CreateAndStartEgress(ctx context.Context) (string, error) {
	return p.create(ctx)
}

type roomPreparationScorer map[string]float64

func (s roomPreparationScorer) Score(desc carriers.Descriptor, _ fabric.TrafficClass) float64 {
	return s[desc.ID]
}

func TestPrepareClientEgressUsesEligiblePolicyOrderAndFallsBack(t *testing.T) {
	var attempted []string
	bindings := make(map[string]policy.CarrierBinding)
	for _, id := range []string{"excluded", "best", "fallback", "last"} {
		id := id
		p := &roomPreparationProvider{fakeVideoProvider: &fakeVideoProvider{}, id: id,
			create: func(ctx context.Context) (string, error) {
				attempted = append(attempted, id)
				if id == "best" {
					return "", fmt.Errorf("provider unavailable")
				}
				return id + "://room", nil
			},
		}
		ep := carriers.Endpoint{ID: id, Carrier: id}
		carrier, err := carriers.NewProviderCarrier(p, ep)
		if err != nil {
			t.Fatal(err)
		}
		bindings[id] = policy.CarrierBinding{Carrier: carrier, Endpoint: ep, Role: "egress"}
	}
	excluded := bindings["excluded"]
	excluded.Role = "client-node"
	bindings["excluded"] = excluded
	bindings[carriers.CarrierVKMessages] = policy.CarrierBinding{
		Carrier:  newMemoryCarrierWithDescriptor(t, "room-order-control", carriers.CarrierVKMessages),
		Endpoint: carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://room-order"},
	}
	p := policy.DefaultAdaptivePolicy()
	p.Scorer = roomPreparationScorer{"excluded": 1000, "best": 100, "fallback": 50, "last": 10, carriers.CarrierVKMessages: 1}
	c, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "room-order", SocksListen: "127.0.0.1:0"}, bindings, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	for range 20 {
		attempted = nil
		addr, id, ok := c.prepareClientEgress(context.Background())
		if !ok || id != "fallback" || addr != "fallback://room" {
			t.Fatalf("room = %q %q %v, attempts=%v", addr, id, ok, attempted)
		}
		if got := strings.Join(attempted, ","); got != "best,fallback" {
			t.Fatalf("attempts=%s, want best,fallback", got)
		}
	}
	// A policy refresh can make a previously eligible egress unavailable.
	p.Scorer.(roomPreparationScorer)["best"] = 0
	p.Scorer.(roomPreparationScorer)["fallback"] = -1
	p.Scorer.(roomPreparationScorer)["last"] = -1
	attempted = nil
	if _, _, ok := c.prepareClientEgress(context.Background()); ok || len(attempted) != 0 {
		t.Fatalf("ineligible providers attempted after policy refresh: %v", attempted)
	}
}

func TestPrepareClientEgressQualifiesHTTPRoomAddress(t *testing.T) {
	const telemostCarrier = "telemost"
	provider := &roomPreparationProvider{
		fakeVideoProvider: &fakeVideoProvider{},
		id:                telemostCarrier,
		create: func(context.Context) (string, error) {
			return "https://telemost.yandex.ru/j/room-42", nil
		},
	}
	ep := carriers.Endpoint{ID: telemostCarrier, Carrier: telemostCarrier}
	carrier, err := carriers.NewProviderCarrier(provider, ep)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "telemost-room-envelope", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{
		telemostCarrier: {Carrier: carrier, Endpoint: ep, Role: "egress"},
		carriers.CarrierVKMessages: {
			Carrier:  newMemoryCarrierWithDescriptor(t, "telemost-room-control", carriers.CarrierVKMessages),
			Endpoint: carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://telemost-room-control"},
		},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	addr, id, ok := c.prepareClientEgress(context.Background())
	if !ok {
		t.Fatal("Telemost room creation failed")
	}
	if id != telemostCarrier {
		t.Fatalf("carrier = %q, want %q", id, telemostCarrier)
	}
	if want := "telemost://https://telemost.yandex.ru/j/room-42"; addr != want {
		t.Fatalf("room endpoint = %q, want %q", addr, want)
	}
}

func TestPrepareClientEgressBoundsUnavailableProvider(t *testing.T) {
	c := &ControlPlane{policy: policy.DefaultAdaptivePolicy()}
	c.policy.Scorer = roomPreparationScorer{"stalled": 100, "fallback": 50}
	for _, id := range []string{"stalled", "fallback"} {
		id := id
		p := &roomPreparationProvider{fakeVideoProvider: &fakeVideoProvider{}, id: id,
			create: func(ctx context.Context) (string, error) {
				if id == "stalled" {
					if _, ok := ctx.Deadline(); !ok {
						return "", fmt.Errorf("missing setup deadline")
					}
					<-ctx.Done()
					return "", ctx.Err()
				}
				return "fallback://room", nil
			},
		}
		ep := carriers.Endpoint{ID: id, Carrier: id}
		carrier, err := carriers.NewProviderCarrier(p, ep)
		if err != nil {
			t.Fatal(err)
		}
		c.egress = append(c.egress, carrierRef{ID: id, Descriptor: carrier.Descriptor(), Binding: policy.CarrierBinding{Carrier: carrier, Endpoint: ep}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	started := time.Now()
	_, id, ok := c.prepareClientEgress(ctx)
	if !ok || id != "fallback" {
		t.Fatalf("room carrier=%q ok=%v: slow provider prevented fallback", id, ok)
	}
	if elapsed := time.Since(started); elapsed < 7*time.Second || elapsed > 10*time.Second {
		t.Fatalf("slow setup elapsed=%s, want provider deadline before overall connection cancellation", elapsed)
	}
}

// --- Fake video tunnel adapter ---

// fakeVideoProvider implements provider.Provider, VideoTunnelAdapter, and
// DataTunnelProvider. It uses cross-wired mock DataTunnels for in-process
// e2e testing without real video call providers.
type fakeVideoProvider struct {
	mu      sync.Mutex
	tun     utunnel.DataTunnel
	role    string // "creator" or "joiner"
	pair    *fakeVideoPair
	started bool
	joinErr error // if set, StartEgressAddr returns this error
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

type clonableVideoProvider struct {
	*fakeVideoProvider
	guest *fakeVideoProvider
}

func (p *clonableVideoProvider) CloneForSession() (provider.Provider, error) {
	return p.guest, nil
}

func TestJoinedClientRoomRegistersNodeEgressBinding(t *testing.T) {
	adapter := &clonableVideoProvider{
		fakeVideoProvider: &fakeVideoProvider{},
		guest:             &fakeVideoProvider{pair: newFakeVideoPair()},
	}
	configured := carriers.Endpoint{ID: "configured", Address: "configured-room"}
	carrier, err := carriers.NewProviderCarrier(adapter, configured)
	if err != nil {
		t.Fatal(err)
	}
	tunnel := itunnel.NewCarrierTunnel("node", nil)
	node := &ControlPlane{
		cfg:    config.Config{Role: config.RoleNode},
		tunnel: tunnel,
		egress: []carrierRef{{
			ID:         adapter.ID(),
			Descriptor: carrier.Descriptor(),
			Binding:    policy.CarrierBinding{Carrier: carrier, Endpoint: configured},
		}},
	}
	const sessionID = "joined-room-session"
	node.admitNodeSession(sessionID, "client")
	ctx := context.Background()
	joined, err := node.joinClientRoomForSession(ctx, "fake-video://client-room", sessionID)
	if err != nil || !joined {
		t.Fatalf("join client room = %t, %v", joined, err)
	}
	endpoint := carriers.Endpoint{ID: adapter.ID() + ":" + sessionID}
	if !tunnel.SupportsEndpoint(endpoint) {
		t.Fatal("node tunnel cannot read from the joined room's provider clone")
	}
	node.releaseNodeSessionFor(ctx, sessionID, nil)
	if tunnel.SupportsEndpoint(endpoint) || adapter.guest.started {
		t.Fatal("release left the joined room registered or its provider running")
	}
}

type releaseProbeTunnel struct {
	*itunnel.CarrierTunnel
	clearedSession string
}

func (t *releaseProbeTunnel) ClearSessionCipher(sessionID string) {
	t.CarrierTunnel.ClearSessionCipher(sessionID)
	t.clearedSession = sessionID
}

type videoCloseProbe struct {
	*fakeVideoProvider
	onClose func()
}

func (p *videoCloseProbe) Close() error {
	p.onClose()
	return p.fakeVideoProvider.Close()
}

func TestNodeReleaseCancelsPendingCarrierDialsBeforeClosingVideoRoom(t *testing.T) {
	const sessionID = "released-session"
	egress := &releaseProbeTunnel{CarrierTunnel: itunnel.NewCarrierTunnel("node", nil)}
	var clearedAtVideoClose string
	adapter := &videoCloseProbe{
		fakeVideoProvider: &fakeVideoProvider{},
		onClose: func() {
			clearedAtVideoClose = egress.clearedSession
		},
	}
	endpoint := carriers.Endpoint{ID: "room", Carrier: adapter.ID(), Address: "room"}
	carrier, err := carriers.NewProviderCarrier(adapter, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	node := &ControlPlane{cfg: config.Config{Role: config.RoleNode, NodeID: "node"}, tunnel: egress}
	node.admitNodeSession(sessionID, "client")
	node.nodeSessions[sessionID].Video = map[string]policy.CarrierBinding{
		adapter.ID(): {Carrier: carrier, Endpoint: endpoint},
	}

	node.handleRelease(t.Context(), session.Release{SessionID: sessionID, ClientID: "client", NodeID: "node"})

	if clearedAtVideoClose != sessionID {
		t.Fatalf("cleared session when provider teardown started = %q, want %q", clearedAtVideoClose, sessionID)
	}
}

func TestFailedGuestJoinReleasesLegacyFallback(t *testing.T) {
	pair := newFakeVideoPair()
	adapter := &clonableVideoProvider{
		fakeVideoProvider: &fakeVideoProvider{pair: pair},
		guest:             &fakeVideoProvider{joinErr: fmt.Errorf("guest join rejected")},
	}
	endpoint := carriers.Endpoint{ID: adapter.ID(), Carrier: adapter.ID(), Address: "configured-room"}
	carrier, err := carriers.NewProviderCarrier(adapter, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}
	node := &ControlPlane{
		cfg:                   config.Config{Role: config.RoleNode},
		bindings:              map[string]policy.CarrierBinding{adapter.ID(): binding},
		egress:                []carrierRef{{ID: adapter.ID(), Descriptor: carrier.Descriptor(), Binding: binding}},
		videoTunnelCarrierIDs: map[string]bool{adapter.ID(): true},
	}
	const sessionID = "fallback-session"
	node.admitNodeSession(sessionID, "client")
	ctx := context.Background()
	if joined, err := node.joinClientRoomForSession(ctx, "fake-video://client-room", sessionID); joined || err == nil {
		t.Fatalf("join client room = %t, %v; want guest join error", joined, err)
	}
	egress := node.buildEgressEndpoints(ctx)
	if len(egress) != 1 || !adapter.started {
		t.Fatal("legacy fallback did not start the shared provider")
	}
	node.releaseNodeSessionFor(ctx, sessionID, egress)
	if adapter.started {
		t.Fatal("failed guest clone suppressed cleanup of the shared fallback")
	}
}

// --- Tests ---

func TestUnifiedTunnelDialsWrappedVideoProviderDataTunnel(t *testing.T) {
	pair := newFakeVideoPair()
	nodeAdapter := &fakeVideoProvider{pair: pair}
	clientAdapter := &fakeVideoProvider{pair: pair}
	endpoint := carriers.Endpoint{ID: "wrapped-video", Carrier: "fake-video", Address: "fake-video://room-test"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := nodeAdapter.CreateAndStartEgress(ctx); err != nil {
		t.Fatalf("start node video egress: %v", err)
	}
	if err := clientAdapter.StartEgressAddr(ctx, endpoint.Address); err != nil {
		t.Fatalf("join client video egress: %v", err)
	}
	nodeEgress := itunnel.NewDataTunnelEgress("wrapped-video-node", endpoint.Carrier, endpoint.ID, nodeAdapter)
	go func() { _ = nodeEgress.ServeEgress(ctx, nil) }()
	time.Sleep(50 * time.Millisecond)

	clientCarrier, err := carriers.NewProviderCarrier(clientAdapter, endpoint)
	if err != nil {
		t.Fatalf("wrap client provider: %v", err)
	}
	clientTunnel := itunnel.NewUnifiedCarrierTunnel("wrapped-video-client", map[string]policy.CarrierBinding{
		endpoint.Carrier: {Carrier: clientCarrier, Endpoint: endpoint},
	})
	defer clientTunnel.Close()

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoListener.Close()
	go func() {
		for {
			conn, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				return
			}
			go io.Copy(conn, conn)
		}
	}()

	conn, err := clientTunnel.DialContext(ctx, endpoint, echoListener.Addr().String())
	if err != nil {
		t.Fatalf("dial wrapped video provider: %v", err)
	}
	defer conn.Close()
	payload := []byte("wrapped-provider-video-nonce")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write wrapped video payload: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
		t.Fatalf("wrapped video payload got=%q err=%v", got, err)
	}
}

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
func TestRoleReversalClientCreatesRoomWithVLESSConfiguredNodeJoinsAsGuest(t *testing.T) {
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
	// A configured VLESS carrier says nothing about its reachability and must
	// not prevent the client from offering a room made with its own credentials.
	clientBindings[carriers.CarrierSingBoxVLESS] = policy.CarrierBinding{
		Carrier:  newMemoryCarrierWithDescriptor(t, "rr-vless", carriers.CarrierSingBoxVLESS),
		Endpoint: carriers.Endpoint{ID: carriers.CarrierSingBoxVLESS, Carrier: carriers.CarrierSingBoxVLESS, Address: "memory://rr-vless"},
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

	ep := carriers.Endpoint{ID: "fake-video-ep", Carrier: "fake-video", Address: "fake-video://room-test"}

	// Exercise the same wrapped-provider path used by production bindings:
	// SOCKS -> active session -> UnifiedCarrierTunnel -> DataTunnelEgress.
	nodeAdapter := &fakeVideoProvider{pair: pair}
	clientAdapter := &fakeVideoProvider{pair: pair}
	if _, err := nodeAdapter.CreateAndStartEgress(ctx); err != nil {
		t.Fatalf("start node video egress: %v", err)
	}
	if err := clientAdapter.StartEgressAddr(ctx, ep.Address); err != nil {
		t.Fatalf("join client video egress: %v", err)
	}
	nodeEgress := itunnel.NewDataTunnelEgress("test-node", ep.Carrier, ep.ID, nodeAdapter)
	go func() { _ = nodeEgress.ServeEgress(ctx, nil) }()
	time.Sleep(50 * time.Millisecond)

	clientCarrier, err := carriers.NewProviderCarrier(clientAdapter, ep)
	if err != nil {
		t.Fatalf("wrap client video provider: %v", err)
	}
	clientTunnel := itunnel.NewUnifiedCarrierTunnel("test-client", map[string]policy.CarrierBinding{
		ep.Carrier: {Carrier: clientCarrier, Endpoint: ep},
	})
	defer clientTunnel.Close()
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:          "test-node",
			SessionID:       "fake-video-session",
			EgressEndpoints: []carriers.Endpoint{ep},
		},
		policy: policy.DefaultAdaptivePolicy(),
		tunnel: clientTunnel,
	}

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
			return control.DialEgress(ctx, targetAddr)
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
