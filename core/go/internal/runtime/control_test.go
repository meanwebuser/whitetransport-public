package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

func newTestControlPlane(cfg config.Config, bindings map[string]policy.CarrierBinding, carrierPolicy policy.CarrierPolicy, tunnel session.CarrierTunnel) (*ControlPlane, error) {
	cp, err := NewControlPlane(cfg, bindings, carrierPolicy, tunnel)
	if err != nil {
		return nil, err
	}
	cp.pollInterval = 25 * time.Millisecond
	cp.busyRetryAfter = 250 * time.Millisecond
	return cp, nil
}

func TestClassifyByRoleBulkAddsEgress(t *testing.T) {
	control := &ControlPlane{}
	ref := carrierRef{
		ID:         "vk.docs.1024:bulk",
		Descriptor: carriers.Descriptor{ID: carriers.CarrierVKDocs1024},
		Binding: policy.CarrierBinding{Endpoint: carriers.Endpoint{
			ID:      "vk.docs.1024:bulk",
			Carrier: carriers.CarrierVKDocs1024,
			Address: "memory://bulk-egress",
		}},
	}

	classifyByRole(control, ref, "bulk")

	if len(control.egress) != 1 || control.egress[0].ID != ref.ID {
		t.Fatalf("bulk role must remain an egress candidate, got %+v", control.egress)
	}
}

func TestControlPlaneHonorsExplicitBindingRolesWithoutFeatureFlag(t *testing.T) {
	t.Setenv("WT_CHANNEL_BINDINGS", "")
	controlCarrier := newMemoryCarrierWithDescriptor(t, "explicit-control", carriers.CarrierGitRepository)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "explicit-egress", carriers.CarrierGitRepository)
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "explicit-role-client", SocksListen: "127.0.0.1:0"},
		map[string]policy.CarrierBinding{
			"git.control": {
				Carrier:  controlCarrier,
				Endpoint: carriers.Endpoint{ID: "git.control", Carrier: carriers.CarrierGitRepository, Address: "control"},
				Role:     "discovery",
			},
			"git.primary": {
				Carrier:  egressCarrier,
				Endpoint: carriers.Endpoint{ID: "git.primary", Carrier: carriers.CarrierGitRepository, Address: "primary"},
				Role:     "egress",
			},
		},
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}
	if got := carrierRefIDs(control.bootstrap); len(got) != 1 || got[0] != "git.control" {
		t.Fatalf("bootstrap refs = %v, want only git.control", got)
	}
	if got := carrierRefIDs(control.control); len(got) != 1 || got[0] != "git.control" {
		t.Fatalf("control refs = %v, want only git.control", got)
	}
	if got := carrierRefIDs(control.egress); len(got) != 1 || got[0] != "git.primary" {
		t.Fatalf("egress refs = %v, want only git.primary", got)
	}
}

func TestControlPlanePacketSessionLifecycleAndAuthoritativeMetadata(t *testing.T) {
	tunnel := &packetLifecycleTestTunnel{}
	expiresAt := time.Now().Add(time.Minute)
	endpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-control"}
	control := &ControlPlane{
		cfg:    config.Config{Role: config.RoleClient, ClientID: "packet-client"},
		tunnel: tunnel,
		active: &activeSession{NodeID: "packet-node", SessionID: "real-session", EgressEndpoints: []carriers.Endpoint{endpoint}, ExpiresAt: expiresAt},
		state:  statusStateConnected,
		stopCh: make(chan struct{}),
	}
	_, _, err := control.OpenPacketEgress(context.Background(), session.PacketMetadata{FlowID: "flow", SessionID: "forged-session", PeerID: "attacker", ExpiresAt: time.Now().Add(24 * time.Hour)})
	if err != nil {
		t.Fatalf("OpenPacketEgress: %v", err)
	}
	metadata := tunnel.openedMetadata()
	if metadata.SessionID != "real-session" || metadata.PeerID != "packet-node" || !metadata.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("authoritative packet metadata not enforced: %+v", metadata)
	}
	control.Disconnect()
	if events := tunnel.snapshotEvents(); len(events) < 2 || events[0] != "close:real-session" || events[1] != "clear-cipher" {
		t.Fatalf("disconnect lifecycle order=%v, want packet close before cipher clear", events)
	}

	nodeTunnel := &packetLifecycleTestTunnel{}
	nodeControl := &ControlPlane{
		cfg:                 config.Config{Role: config.RoleNode, NodeID: "packet-node"},
		tunnel:              nodeTunnel,
		nodeBusy:            true,
		nodeSessionID:       "node-session",
		nodeSessionClientID: "packet-client",
		stopCh:              make(chan struct{}),
	}
	nodeControl.releaseNodeSession(context.Background(), []carriers.Endpoint{})
	if events := nodeTunnel.snapshotEvents(); len(events) < 2 || events[0] != "close:node-session" || events[1] != "clear-cipher" {
		t.Fatalf("node release lifecycle order=%v, want packet close before cipher clear", events)
	}
}

func TestClientPacketSessionExpirySurvivesConnectContextCancellation(t *testing.T) {
	tunnel := &packetLifecycleTestTunnel{}
	expiresAt := time.Now().Add(30 * time.Millisecond)
	control := &ControlPlane{
		cfg:    config.Config{Role: config.RoleClient, ClientID: "packet-client"},
		tunnel: tunnel,
		active: &activeSession{NodeID: "packet-node", SessionID: "expiry-session", ExpiresAt: expiresAt},
		state:  statusStateConnected,
		stopCh: make(chan struct{}),
	}
	connectCtx, cancel := context.WithCancel(context.Background())
	cancel()
	control.clientSessionTimeoutMonitor(connectCtx, session.Answer{SessionID: "expiry-session", NodeID: "packet-node", ExpiresAt: expiresAt})
	if control.Status().SessionActive {
		t.Fatal("client session survived its expiry because the Connect context was cancelled")
	}
	events := tunnel.snapshotEvents()
	if len(events) < 2 || events[0] != "close:expiry-session" || events[1] != "clear-cipher" {
		t.Fatalf("client expiry lifecycle order=%v, want packet close before cipher clear", events)
	}
}

func TestControlPlanePacketFailoverUsesDistinctFlowIDs(t *testing.T) {
	tunnel := &packetRouteRetryTestTunnel{}
	endpoints := []carriers.Endpoint{
		{ID: "packet-route-a", Carrier: carriers.CarrierVKDocs1024},
		{ID: "packet-route-b", Carrier: carriers.CarrierOKDocs256},
	}
	control := &ControlPlane{
		cfg:    config.Config{Role: config.RoleClient, ClientID: "packet-client"},
		tunnel: tunnel,
		active: &activeSession{NodeID: "packet-node", SessionID: "packet-session", EgressEndpoints: endpoints, ExpiresAt: time.Now().Add(time.Minute)},
		state:  statusStateConnected,
		stopCh: make(chan struct{}),
	}
	_, route, err := control.OpenPacketEgress(context.Background(), session.PacketMetadata{FlowID: "udp-flow"})
	if err != nil {
		t.Fatalf("OpenPacketEgress: %v", err)
	}
	if route != "packet-route-b" {
		t.Fatalf("route=%q, want second packet route", route)
	}
	flows := tunnel.snapshotFlows()
	if len(flows) != 2 || flows[0] != "udp-flow-route-0" || flows[1] != "udp-flow-route-1" {
		t.Fatalf("packet retry flow IDs=%v, want distinct route-scoped IDs", flows)
	}
}

type packetLifecycleTestTunnel struct {
	mu       sync.Mutex
	events   []string
	metadata session.PacketMetadata
}

type packetRouteRetryTestTunnel struct {
	mu    sync.Mutex
	flows []string
}

func (*packetRouteRetryTestTunnel) SupportsEndpoint(carriers.Endpoint) bool       { return true }
func (*packetRouteRetryTestTunnel) SupportsPacketEndpoint(carriers.Endpoint) bool { return true }
func (*packetRouteRetryTestTunnel) DialContext(context.Context, carriers.Endpoint, string) (net.Conn, error) {
	return nil, errors.New("unused")
}
func (tunnel *packetRouteRetryTestTunnel) OpenPacketConn(_ context.Context, _ carriers.Endpoint, metadata session.PacketMetadata) (net.PacketConn, error) {
	tunnel.mu.Lock()
	tunnel.flows = append(tunnel.flows, metadata.FlowID)
	attempt := len(tunnel.flows)
	tunnel.mu.Unlock()
	if attempt == 1 {
		return nil, errors.New("first packet route failed")
	}
	return nil, nil
}
func (tunnel *packetRouteRetryTestTunnel) snapshotFlows() []string {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	return append([]string(nil), tunnel.flows...)
}

func (tunnel *packetLifecycleTestTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }
func (tunnel *packetLifecycleTestTunnel) SupportsPacketEndpoint(carriers.Endpoint) bool {
	return true
}
func (tunnel *packetLifecycleTestTunnel) DialContext(context.Context, carriers.Endpoint, string) (net.Conn, error) {
	return nil, errors.New("unused")
}
func (tunnel *packetLifecycleTestTunnel) OpenPacketConn(_ context.Context, _ carriers.Endpoint, metadata session.PacketMetadata) (net.PacketConn, error) {
	tunnel.mu.Lock()
	tunnel.metadata = metadata
	tunnel.mu.Unlock()
	return nil, nil
}
func (tunnel *packetLifecycleTestTunnel) SetPacketSession(sessionID string, peerID string, _ time.Time) {
	tunnel.mu.Lock()
	tunnel.events = append(tunnel.events, "set:"+sessionID+":"+peerID)
	tunnel.mu.Unlock()
}
func (tunnel *packetLifecycleTestTunnel) ClosePacketSession(sessionID string) {
	tunnel.mu.Lock()
	tunnel.events = append(tunnel.events, "close:"+sessionID)
	tunnel.mu.Unlock()
}
func (tunnel *packetLifecycleTestTunnel) ClearCipher() {
	tunnel.mu.Lock()
	tunnel.events = append(tunnel.events, "clear-cipher")
	tunnel.mu.Unlock()
}
func (tunnel *packetLifecycleTestTunnel) openedMetadata() session.PacketMetadata {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	return tunnel.metadata
}
func (tunnel *packetLifecycleTestTunnel) snapshotEvents() []string {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	return append([]string(nil), tunnel.events...)
}

// TestControlPlaneDerivesBootstrapCipherFromChannelBoundToken proves that a
// node and client can encrypt their session key when TokenStore credentials
// are scoped to a concrete discovery peer instead of the wildcard channel.
func TestControlPlaneDerivesBootstrapCipherFromChannelBoundToken(t *testing.T) {
	const discoveryPeerID = "2000000140"
	store := tokens.NewStore()
	store.Set(&tokens.Token{
		ID:        "vk-discovery",
		Platform:  "vk",
		Kind:      tokens.KindAPIKey,
		Lifecycle: tokens.LifecycleEmbedded,
		Status:    tokens.StatusActive,
		Value:     "test-channel-bound-bootstrap-token",
	})
	store.AddBinding(tokens.Binding{
		TokenID:        "vk-discovery",
		Platform:       "vk",
		ConnectionType: "messages",
		ChannelID:      discoveryPeerID,
		Role:           "discovery",
		Enabled:        true,
	})
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {
			Carrier:  newMemoryCarrierWithDescriptor(t, "channel-bound-bootstrap", carriers.CarrierVKMessages),
			Endpoint: carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: discoveryPeerID},
		},
	}
	control, err := NewControlPlaneWithTokens(
		config.Config{
			Role:        config.RoleClient,
			ClientID:    "channel-bound-client",
			SocksListen: "127.0.0.1:0",
			CarrierConfigs: []config.CarrierConfig{{
				ID:         carriers.CarrierVKMessages,
				Endpoint:   config.EndpointConfig{Address: discoveryPeerID},
				VKMessages: &config.VKMessagesConfig{},
			}},
		},
		bindings,
		policy.DefaultAdaptivePolicy(),
		nil,
		store,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}
	if control.bootstrapCipher == nil {
		t.Fatal("bootstrap cipher is nil for a concrete VK discovery binding")
	}
}

func TestControlPlaneDerivesBootstrapCipherFromLocalFileMailboxTokenStore(t *testing.T) {
	const channelID = "control"
	store := tokens.NewStore()
	store.Set(&tokens.Token{ID: "local-test-bootstrap", Platform: "local", Kind: tokens.KindAPIKey, Lifecycle: tokens.LifecycleEmbedded, Status: tokens.StatusActive, Value: "deterministic-local-session-key-not-a-secret"})
	store.AddBinding(tokens.Binding{TokenID: "local-test-bootstrap", Platform: "local", ConnectionType: "messages", ChannelID: channelID, Role: "discovery", Enabled: true})
	endpoint := carriers.Endpoint{ID: "local.control", Carrier: carriers.CarrierFileMailbox, Address: channelID}
	bindings := map[string]policy.CarrierBinding{
		"local.control": {Carrier: newMemoryCarrierWithDescriptor(t, "local-bootstrap", carriers.CarrierFileMailbox), Endpoint: endpoint},
	}
	control, err := NewControlPlaneWithTokens(
		config.Config{
			Role:     config.RoleClient,
			ClientID: "local-test-client",
			CarrierConfigs: []config.CarrierConfig{{
				ID:          "local.control",
				CarrierType: carriers.CarrierFileMailbox,
				Endpoint:    config.EndpointConfig{ID: endpoint.ID, Address: channelID},
				FileMailbox: &config.FileMailboxConfig{Dir: t.TempDir()},
			}},
		},
		bindings,
		policy.DefaultAdaptivePolicy(),
		nil,
		store,
	)
	if err != nil {
		t.Fatalf("new local control plane: %v", err)
	}
	if control.bootstrapCipher == nil {
		t.Fatal("local file mailbox TokenStore binding did not derive a bootstrap cipher")
	}
}

func TestControlPlanesUseSharedBootstrapSecretAcrossDifferentProviderTokens(t *testing.T) {
	const endpointAddress = "2000000140"
	newStore := func(id, value string) *tokens.Store {
		store := tokens.NewStore()
		store.Set(&tokens.Token{ID: id, Platform: "vk", Kind: tokens.KindAPIKey, Lifecycle: tokens.LifecycleEmbedded, Status: tokens.StatusActive, Value: value})
		store.AddBinding(tokens.Binding{TokenID: id, Platform: "vk", ConnectionType: "messages", ChannelID: endpointAddress, Role: "discovery", Enabled: true})
		return store
	}
	newConfig := func(role config.Role, id string) config.Config {
		return config.Config{
			Role: role, ClientID: id, NodeID: id, BootstrapSecret: "shared-bootstrap-secret",
			CarrierConfigs: []config.CarrierConfig{{
				ID:         carriers.CarrierVKMessages,
				Endpoint:   config.EndpointConfig{Address: endpointAddress},
				VKMessages: &config.VKMessagesConfig{},
			}},
		}
	}
	newBindings := func(t *testing.T) map[string]policy.CarrierBinding {
		return map[string]policy.CarrierBinding{
			carriers.CarrierVKMessages: {
				Carrier:  newMemoryCarrierWithDescriptor(t, "shared-bootstrap", carriers.CarrierVKMessages),
				Endpoint: carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: endpointAddress},
			},
		}
	}

	node, err := NewControlPlaneWithTokens(newConfig(config.RoleNode, "node"), newBindings(t), policy.DefaultAdaptivePolicy(), nil, newStore("node-provider", "node-provider-token"))
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	client, err := NewControlPlaneWithTokens(newConfig(config.RoleClient, "client"), newBindings(t), policy.DefaultAdaptivePolicy(), nil, newStore("client-provider", "client-provider-token"))
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}
	if node.bootstrapSecretCipher == nil || client.bootstrapSecretCipher == nil {
		t.Fatal("dedicated bootstrap ciphers were not initialized")
	}
	sessionKey, err := fabric.GenerateSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := session.EncryptSessionKey(client.bootstrapSecretCipher, sessionKey[:])
	if err != nil {
		t.Fatal(err)
	}
	decrypted, encryptedDelivery, err := node.offerSessionKey(session.Offer{SessionKey: encrypted})
	if err != nil || !encryptedDelivery {
		t.Fatalf("shared bootstrap secret did not decrypt: encrypted=%v err=%v", encryptedDelivery, err)
	}
	if decrypted != sessionKey {
		t.Fatal("node decrypted a different session key")
	}
}

func TestBootstrapSecretCipherRequiresAdvertisedCapability(t *testing.T) {
	control := &ControlPlane{
		bootstrapSecretCipher: mustSessionCipher(t, "shared-bootstrap-secret"),
		legacyBootstrapCipher: mustSessionCipher(t, "legacy-provider-token"),
	}
	control.nodes = map[string]discoveredNode{
		"v2-node":     {Advertisement: session.NodeAdvertisement{NodeID: "v2-node", Capabilities: []string{"egress", BootstrapKeyCapability}}},
		"legacy-node": {Advertisement: session.NodeAdvertisement{NodeID: "legacy-node", Capabilities: []string{"egress"}}},
	}
	if got := control.bootstrapCipherForNode("v2-node"); got != control.bootstrapSecretCipher {
		t.Fatal("v2 node did not select dedicated bootstrap cipher")
	}
	if got := control.bootstrapCipherForNode("legacy-node"); got != control.legacyBootstrapCipher {
		t.Fatal("legacy node did not select provider-token bootstrap cipher")
	}
}

func TestControlPlaneDiscoversNodeAndConnectsSession(t *testing.T) {
	t.Helper()

	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://shared-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://shared-egress"}

	controlCarrier := newMemoryCarrierWithDescriptor(t, "control", carriers.CarrierVKMessages)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "egress", carriers.CarrierVKDocs1024)

	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "example-exit-node", DisplayName: "Example Exit Node", Country: "RU", Region: "Moscow", SocksListen: "127.0.0.1:1081"}, map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}

	clientControl, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "mac-client", SocksListen: "127.0.0.1:1083"}, map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	var nodes []NodeView
	for time.Now().Before(deadline) {
		nodes = clientControl.ListNodes()
		if len(nodes) == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected one discovered node, got %+v", nodes)
	}
	if nodes[0].NodeID != "example-exit-node" || !nodes[0].Available {
		t.Fatalf("unexpected discovered node view: %+v", nodes[0])
	}

	status, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("connect session: %v", err)
	}
	if status.State != statusStateConnected || status.ActiveNodeID != "example-exit-node" {
		t.Fatalf("unexpected connected status: %+v", status)
	}
	var foundVKDocs bool
	for _, ep := range status.EgressEndpoints {
		if ep.ID == carriers.CarrierVKDocs1024 {
			foundVKDocs = true
			break
		}
	}
	if !foundVKDocs {
		t.Fatalf("expected vk.docs.1024 in egress endpoints, got %+v", status.EgressEndpoints)
	}

	disconnected := clientControl.Disconnect()
	if disconnected.ActiveNodeID != "" || disconnected.SessionID != "" {
		t.Fatalf("expected cleared session after disconnect, got %+v", disconnected)
	}
}

func TestControlPlaneOfferCoversDelayedProviderWindow(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://slow-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://slow-egress"}
	controlCarrier := &offerCaptureCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "slow-control", carriers.CarrierVKMessages)}
	egressCarrier := newMemoryCarrierWithDescriptor(t, "slow-egress", carriers.CarrierVKDocs1024)
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "slow-exit-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	clientControl, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "slow-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)
	waitForNodeVisible(t, clientControl, "slow-exit-node", true)
	if _, err := clientControl.Connect(ctx, "slow-exit-node"); err != nil {
		t.Fatalf("connect slow provider session: %v", err)
	}

	offers := controlCarrier.offersSnapshot()
	if len(offers) != 1 {
		t.Fatalf("captured offers = %d, want one", len(offers))
	}
	if remaining := time.Until(offers[0].ExpiresAt); remaining < 5*time.Minute-5*time.Second {
		t.Fatalf("offer remaining TTL = %v, want at least five minutes", remaining)
	}
}

// TestControlPlaneConnectsWhenOneControlCarrierCannotWrite proves that a
// failed control carrier does not turn a healthy alternate carrier into a
// global outage. The node advertises over the healthy carrier, and the client
// must prioritize that contacted mailbox when it advertises reply endpoints.
func TestControlPlaneConnectsWhenOneControlCarrierCannotWrite(t *testing.T) {
	failedEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://failed-control"}
	healthyEndpoint := carriers.Endpoint{ID: carriers.CarrierOKMessages, Carrier: carriers.CarrierOKMessages, Address: "memory://healthy-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://egress"}

	failedCarrier := &writeFailingCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "failed-control", carriers.CarrierVKMessages)}
	healthyCarrier := newMemoryCarrierWithDescriptor(t, "healthy-control", carriers.CarrierOKMessages)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "egress", carriers.CarrierVKDocs1024)
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: failedCarrier, Endpoint: failedEndpoint},
		carriers.CarrierOKMessages: {Carrier: healthyCarrier, Endpoint: healthyEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "resilient-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	clientControl, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "resilient-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}
	// Map iteration must not decide the resilience contract: the contacted
	// healthy mailbox is advertised first, so the node must not touch the
	// unrelated failing fallback before delivering the answer.
	nodeControl.replyEndpoints = []carriers.Endpoint{failedEndpoint, healthyEndpoint}
	clientControl.replyEndpoints = []carriers.Endpoint{failedEndpoint, healthyEndpoint}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	waitForNodeVisible(t, clientControl, "resilient-node", true)
	status, err := clientControl.Connect(ctx, "resilient-node")
	if err != nil {
		t.Fatalf("connect with one failed carrier: %v", err)
	}
	if status.State != statusStateConnected || status.ActiveNodeID != "resilient-node" {
		t.Fatalf("unexpected connected status: %+v", status)
	}
	if failedCarrier.writeCount(session.PayloadSessionAnswer) != 0 {
		t.Fatalf("failed fallback session.answer writes = %d, want 0", failedCarrier.writeCount(session.PayloadSessionAnswer))
	}
	if got := countPayloads(t, healthyCarrier, healthyEndpoint, session.PayloadSessionAnswer); got != 1 {
		t.Fatalf("expected one session.answer through healthy carrier, got %d", got)
	}
}

func TestControlPlaneNodeBusyRejectsNewSession(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://busy-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://busy-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "busy-control", carriers.CarrierVKMessages)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "busy-egress", carriers.CarrierVKDocs1024)
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "example-exit-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	clientA, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "client-a", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "client-b", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientA.Stop()
	defer clientB.Stop()
	nodeControl.Start(ctx)
	clientA.Start(ctx)
	clientB.Start(ctx)

	waitForNodeVisible(t, clientA, "example-exit-node", true)
	waitForNodeVisible(t, clientB, "example-exit-node", true)

	if _, err := clientA.Connect(ctx, "example-exit-node"); err != nil {
		t.Fatalf("client A connect: %v", err)
	}

	waitForNodeVisible(t, clientB, "example-exit-node", false)
	connectStart := time.Now()
	_, err = clientB.Connect(ctx, "example-exit-node")
	elapsed := time.Since(connectStart)
	expectedMax := time.Duration(maxBusyRetries) * clientB.busyRetryAfter
	t.Logf("busy connect failed after %v (expected ~%v)", elapsed, expectedMax)
	if elapsed > expectedMax*3 {
		t.Fatalf("busy retry took %v, expected at most ~%v (capped backoff)", elapsed, expectedMax)
	}
	if err == nil || !strings.Contains(err.Error(), "busy after") {
		t.Fatalf("expected busy retry error, got %v", err)
	}
}

func TestControlPlaneReconnectAfterSessionRelease(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://reconnect-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://reconnect-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "reconnect-control", carriers.CarrierVKMessages)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "reconnect-egress", carriers.CarrierVKDocs1024)
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "example-exit-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	clientControl, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "client-1", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	waitForNodeVisible(t, clientControl, "example-exit-node", true)
	first, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if first.SessionID == "" {
		t.Fatal("expected non-empty session id")
	}

	nodeControl.releaseNodeSession(ctx, first.EgressEndpoints)
	waitForNodeVisible(t, clientControl, "example-exit-node", true)

	second, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if second.SessionID == first.SessionID {
		t.Fatalf("expected new session id after reconnect, got same %q", second.SessionID)
	}
}

func TestControlPlaneDisconnectReleasesNodeSession(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://disconnect-release-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://disconnect-release-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "disconnect-release-control", carriers.CarrierVKMessages)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "disconnect-release-egress", carriers.CarrierVKDocs1024)
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "example-exit-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	clientControl, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "client-1", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	waitForNodeVisible(t, clientControl, "example-exit-node", true)
	first, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if first.SessionID == "" {
		t.Fatal("expected non-empty first session id")
	}

	clientControl.Disconnect()
	waitForNodeVisible(t, clientControl, "example-exit-node", true)

	second, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("second connect after disconnect release: %v", err)
	}
	if second.SessionID == first.SessionID {
		t.Fatalf("expected new session id after disconnect, got same %q", second.SessionID)
	}
	if got := countPayloads(t, controlCarrier, controlEndpoint, session.PayloadSessionRelease); got != 1 {
		t.Fatalf("expected one session.release on control mailbox, got %d", got)
	}
}

func TestControlPlaneIgnoresStaleSessionRelease(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://stale-release-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://stale-release-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "stale-release-control", carriers.CarrierVKMessages)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "stale-release-egress", carriers.CarrierVKDocs1024)
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}
	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "example-exit-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	nodeControl.nodeBusy = true
	nodeControl.nodeSessionID = "active-session"
	nodeControl.nodeSessionClientID = "client-a"
	nodeControl.nodeSessionEndpoints = []carriers.Endpoint{egressEndpoint}

	nodeControl.handleRelease(context.Background(), session.Release{
		SessionID: "old-session",
		ClientID:  "client-a",
		NodeID:    "example-exit-node",
	})

	if !nodeControl.nodeBusy {
		t.Fatal("stale release cleared the active node session")
	}
	if nodeControl.nodeSessionID != "active-session" {
		t.Fatalf("nodeSessionID = %q, want active-session", nodeControl.nodeSessionID)
	}
}

func TestControlPlaneTunnelCloseReleasesBusyNode(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://closed-control"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://closed-egress"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "closed-control", carriers.CarrierVKMessages)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "closed-egress", carriers.CarrierVKDocs1024)
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}
	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "example-exit-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nodeControl.nodeBusy = true
	nodeControl.nodeSessionEndpoints = []carriers.Endpoint{egressEndpoint}

	nodeControl.onTunnelClosed(ctx)

	if nodeControl.nodeBusy {
		t.Fatal("node stayed busy after tunnel close")
	}
	if len(nodeControl.nodeSessionEndpoints) != 0 {
		t.Fatalf("nodeSessionEndpoints = %+v, want cleared", nodeControl.nodeSessionEndpoints)
	}
	if !nodeControl.advertised {
		t.Fatal("node did not re-advertise after tunnel close release")
	}
}

func TestControlPlaneConnectTimeoutWithoutAnswer(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://timeout-control"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "timeout-control", carriers.CarrierVKMessages)
	clientControl, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "client-timeout", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer clientControl.Stop()
	clientControl.Start(ctx)

	clientControl.storeAdvertisement(session.NodeAdvertisement{
		NodeID:   "silent-node",
		Role:     session.RoleNode,
		Carriers: []carriers.Endpoint{controlEndpoint},
	})

	shortCtx, shortCancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer shortCancel()
	_, err = clientControl.Connect(shortCtx, "silent-node")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if clientControl.Status().State != statusStateDisconnected {
		t.Fatalf("expected disconnected state after timeout, got %+v", clientControl.Status())
	}
}

func TestControlPlaneSessionErrorUnblocksOnlyMatchingPendingConnect(t *testing.T) {
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://session-error-control"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "session-error-control", carriers.CarrierVKMessages)
	clientControl, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "client-session-error", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer clientControl.Stop()
	clientControl.Start(ctx)
	clientControl.storeAdvertisement(session.NodeAdvertisement{
		NodeID:   "no-egress-node",
		Role:     session.RoleNode,
		Carriers: []carriers.Endpoint{controlEndpoint},
	})

	type connectResult struct {
		err error
	}
	resultCh := make(chan connectResult, 1)
	go func() {
		_, connectErr := clientControl.Connect(ctx, "no-egress-node")
		resultCh <- connectResult{err: connectErr}
	}()

	var sessionID string
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		clientControl.mu.RLock()
		for id := range clientControl.pending {
			sessionID = id
			break
		}
		clientControl.mu.RUnlock()
		if sessionID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sessionID == "" {
		t.Fatal("Connect did not register a pending session")
	}

	nodeEngine := session.NewEngine("no-egress-node")
	if err := nodeEngine.SendSessionError(ctx, controlCarrier, controlEndpoint, session.SessionError{
		SessionID: "another-pending-session",
		SenderID:  "no-egress-node",
		Error:     "unrelated failure",
		Code:      "no_egress",
	}); err != nil {
		t.Fatalf("send unrelated session error: %v", err)
	}
	select {
	case result := <-resultCh:
		t.Fatalf("unrelated session error unblocked Connect: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := nodeEngine.SendSessionError(ctx, controlCarrier, controlEndpoint, session.SessionError{
		SessionID: sessionID,
		SenderID:  "no-egress-node",
		Error:     "no compatible egress endpoint",
		Code:      "no_egress",
	}); err != nil {
		t.Fatalf("send matching session error: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.err == nil {
			t.Fatal("Connect succeeded after session.error")
		}
		if !strings.Contains(result.err.Error(), "no_egress") || !strings.Contains(result.err.Error(), "no compatible egress endpoint") {
			t.Fatalf("Connect error = %v, want matching session.error details", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("matching session.error did not unblock Connect promptly")
	}
}

func TestControlPlaneConnectsAcrossSplitDiscoveryControlAndReplyMailboxes(t *testing.T) {
	discoveryEndpoint := carriers.Endpoint{ID: "discovery-mailbox", Carrier: "test.discovery", Address: "memory://discovery"}
	nodeControlEndpoint := carriers.Endpoint{ID: "node-control-mailbox", Carrier: "test.node-control", Address: "memory://node-control"}
	clientReplyEndpoint := carriers.Endpoint{ID: "client-reply-mailbox", Carrier: "test.client-reply", Address: "memory://client-reply"}
	egressEndpoint := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "memory://split-egress"}

	discoveryCarrier := newMemoryCarrierWithCustomDescriptor("test.discovery", fabric.TrafficBootstrap)
	nodeControlCarrier := newMemoryCarrierWithCustomDescriptor("test.node-control", fabric.TrafficControl)
	clientReplyCarrier := newMemoryCarrierWithCustomDescriptor("test.client-reply", fabric.TrafficControl)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "split-egress", carriers.CarrierVKDocs1024)

	nodeControl, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "example-exit-node", DisplayName: "Example Exit Node", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{
		"test.discovery":           {Carrier: discoveryCarrier, Endpoint: discoveryEndpoint},
		"test.node-control":        {Carrier: nodeControlCarrier, Endpoint: nodeControlEndpoint},
		"test.client-reply":        {Carrier: clientReplyCarrier, Endpoint: carriers.Endpoint{ID: "node-client-reply-unused", Carrier: "test.client-reply", Address: "memory://node-client-reply-unused"}},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	nodeControl.replyEndpoints = []carriers.Endpoint{nodeControlEndpoint}
	nodeControl.productVersion = "0.1.123"

	clientControl, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "mac-client", SocksListen: "127.0.0.1:0"}, map[string]policy.CarrierBinding{
		"test.discovery":           {Carrier: discoveryCarrier, Endpoint: discoveryEndpoint},
		"test.node-control":        {Carrier: nodeControlCarrier, Endpoint: carriers.Endpoint{ID: "client-node-control-unused", Carrier: "test.node-control", Address: "memory://client-node-control-unused"}},
		"test.client-reply":        {Carrier: clientReplyCarrier, Endpoint: clientReplyEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}, policy.DefaultAdaptivePolicy(), nil)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}
	clientControl.replyEndpoints = []carriers.Endpoint{clientReplyEndpoint}
	clientControl.productVersion = "0.1.1"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	waitForNodeVisible(t, clientControl, "example-exit-node", true)
	status, err := clientControl.Connect(ctx, "example-exit-node")
	if err != nil {
		t.Fatalf("connect split-mailbox session: %v", err)
	}
	if status.State != statusStateConnected || status.ActiveNodeID != "example-exit-node" {
		t.Fatalf("unexpected connected status: %+v", status)
	}

	if got := countPayloads(t, nodeControlCarrier, nodeControlEndpoint, session.PayloadSessionOffer); got != 1 {
		t.Fatalf("expected one session.offer on node control mailbox, got %d", got)
	}
	if got := countPayloads(t, discoveryCarrier, discoveryEndpoint, session.PayloadSessionOffer); got != 0 {
		t.Fatalf("expected no session.offer on discovery mailbox, got %d", got)
	}
	if got := countPayloads(t, clientReplyCarrier, clientReplyEndpoint, session.PayloadSessionAnswer); got != 1 {
		t.Fatalf("expected one session.answer on client reply mailbox, got %d", got)
	}
}

type memoryCarrier struct {
	descriptor carriers.Descriptor
	memory     *carriers.MemoryCarrier
}

type offerCaptureCarrier struct {
	*memoryCarrier
	mu     sync.Mutex
	offers []session.Offer
}

func (c *offerCaptureCarrier) Write(ctx context.Context, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	if envelope.PayloadType == session.PayloadSessionOffer {
		offer, err := session.DecodePayload[session.Offer](envelope.Payload)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.offers = append(c.offers, offer)
		c.mu.Unlock()
	}
	return c.memoryCarrier.Write(ctx, endpoint, envelope)
}

func (c *offerCaptureCarrier) offersSnapshot() []session.Offer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]session.Offer(nil), c.offers...)
}

// writeFailingCarrier models an unavailable provider while retaining a real
// mailbox read path. It lets the integration test verify send failover rather
// than merely unit-test a selector.
type writeFailingCarrier struct {
	*memoryCarrier
	mu          sync.Mutex
	writeCounts map[string]int
}

func (c *writeFailingCarrier) Write(_ context.Context, _ carriers.Endpoint, envelope fabric.Envelope) error {
	c.mu.Lock()
	if c.writeCounts == nil {
		c.writeCounts = make(map[string]int)
	}
	c.writeCounts[envelope.PayloadType]++
	c.mu.Unlock()
	return errors.New("injected carrier write failure")
}

func (c *writeFailingCarrier) writeCount(payloadType string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeCounts[payloadType]
}

func newMemoryCarrierWithDescriptor(t *testing.T, id string, descriptorID string) *memoryCarrier {
	t.Helper()
	descriptor, err := carriers.FindStandardDescriptor(descriptorID)
	if err != nil {
		t.Fatalf("descriptor %s: %v", descriptorID, err)
	}
	descriptor.ID = descriptorID
	return &memoryCarrier{descriptor: descriptor, memory: carriers.NewMemoryCarrier(id)}
}

func newMemoryCarrierWithCustomDescriptor(id string, traffic ...fabric.TrafficClass) *memoryCarrier {
	desc := carriers.Descriptor{
		ID:             id,
		Provider:       "memory",
		Mode:           carriers.DeliveryMailbox,
		TrafficClasses: traffic,
		Capabilities:   []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox, carriers.CapRetained},
		Limits:         carriers.Limits{MaxPayloadBytes: 1 << 20, SendsPerMinute: 600, PollsPerMinute: 600},
		Metrics: carriers.Metrics{
			Healthy:        true,
			QuotaRemaining: -1, // quota-aware scorer: treat test carriers as unlimited
		},
	}
	return &memoryCarrier{descriptor: desc, memory: carriers.NewMemoryCarrierWithDescriptor(desc)}
}

func countPayloads(t *testing.T, carrier *memoryCarrier, endpoint carriers.Endpoint, payloadType string) int {
	t.Helper()
	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("read %s: %v", endpoint.ID, err)
	}
	count := 0
	for _, envelope := range read.Envelopes {
		if envelope.PayloadType == payloadType {
			count++
		}
	}
	return count
}

func (c *memoryCarrier) Descriptor() carriers.Descriptor { return c.descriptor }
func (c *memoryCarrier) Write(ctx context.Context, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	return c.memory.Write(ctx, endpoint, envelope)
}
func (c *memoryCarrier) Read(ctx context.Context, endpoint carriers.Endpoint, cursor carriers.Cursor) (carriers.ReadResult, error) {
	return c.memory.Read(ctx, endpoint, cursor)
}
func (c *memoryCarrier) Probe(ctx context.Context, endpoint carriers.Endpoint) (carriers.Metrics, error) {
	return c.memory.Probe(ctx, endpoint)
}

func (c *memoryCarrier) DeleteMessage(ctx context.Context, endpoint carriers.Endpoint, messageID string) error {
	// Memory carrier doesn't support message deletion
	return fmt.Errorf("delete message not implemented for memory carrier")
}

func TestControlPlaneDialEgressRequiresImplementedTunnel(t *testing.T) {
	control := &ControlPlane{cfg: config.Config{Role: config.RoleClient}, active: &activeSession{NodeID: "example-exit-node", EgressEndpoints: []carriers.Endpoint{{ID: carriers.CarrierVKDocs1024}}}}
	_, route, err := control.DialEgress(context.Background(), "example.com:443")
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("expected carrier tunnel not implemented error, got route=%q err=%v", route, err)
	}
}

func waitForNodeVisible(t *testing.T, control *ControlPlane, nodeID string, available bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, node := range control.ListNodes() {
			if node.NodeID == nodeID && node.Available == available {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("node %s did not reach available=%v; nodes=%+v", nodeID, available, control.ListNodes())
}

func TestIsAuthErrorRecognizesLiveKitWebsocketStatus(t *testing.T) {
	if !isAuthError("ws dial: websocket: bad handshake (status 401)") {
		t.Fatal("expected LiveKit websocket status 401 to be treated as auth error")
	}
	if !isAuthError("ws dial: websocket: bad handshake (status 403)") {
		t.Fatal("expected LiveKit websocket status 403 to be treated as auth error")
	}
	if isAuthError("ws dial: network is unreachable") {
		t.Fatal("network failure should not be treated as auth error")
	}
}

func TestClassifyByRoleRoutesSessionEndpointRolesToEgress(t *testing.T) {
	for _, role := range []string{"node", "client", "egress", "flex"} {
		t.Run(role, func(t *testing.T) {
			control := &ControlPlane{}
			ref := carrierRef{
				ID: "wbstream." + role,
				Binding: policy.CarrierBinding{
					Endpoint: carriers.Endpoint{ID: "wbstream-" + role, Carrier: "wbstream"},
				},
			}
			classifyByRole(control, ref, role)
			if len(control.egress) != 1 || control.egress[0].ID != ref.ID {
				t.Fatalf("role %q egress = %+v, want %q", role, control.egress, ref.ID)
			}
		})
	}
}
