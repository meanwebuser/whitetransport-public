package runtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
	itunnel "github.com/meanwebuser/whitetransport/core/internal/tunnel"
)

// TestRoleReversalInstallsTwoXrayProfiles verifies the complete client-owned
// room boundary: a node seals two route-specific profiles, a client with no
// static VLESS settings opens them, and each route gets its own local sidecar.
func TestRoleReversalInstallsTwoXrayProfiles(t *testing.T) {
	pair := newFakeVideoPair()
	nodeAdapter := &fakeVideoProvider{pair: pair}
	clientAdapter := &fakeVideoProvider{pair: pair}
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://role-reversal-xray-control"}
	videoEndpoint := carriers.Endpoint{ID: "fake-video-egress", Carrier: carriers.CarrierVKDocs1024, Address: "fake-video://room-test"}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "role-reversal-xray-control", carriers.CarrierVKMessages)

	nodeVideoCarrier, err := carriers.NewProviderCarrier(nodeAdapter, videoEndpoint)
	if err != nil {
		t.Fatalf("wrap node video adapter: %v", err)
	}
	clientVideoCarrier, err := carriers.NewProviderCarrier(clientAdapter, videoEndpoint)
	if err != nil {
		t.Fatalf("wrap client video adapter: %v", err)
	}
	deCarrier := newTestProfileCarrier(t, "vless://11111111-1111-4111-8111-111111111111@de.example.invalid:443?type=httpupgrade&security=tls&sni=de.example.invalid#de")
	usCarrier := newTestProfileCarrier(t, "vless://22222222-2222-4222-8222-222222222222@us.example.invalid:443?type=httpupgrade&security=tls&sni=us.example.invalid#us")
	deEndpoint := carriers.Endpoint{ID: "xray-de-httpupgrade", Carrier: carriers.CarrierSingBoxVLESS, Address: "de.example.invalid:443"}
	usEndpoint := carriers.Endpoint{ID: "xray-us-httpupgrade", Carrier: carriers.CarrierSingBoxVLESS, Address: "us.example.invalid:443"}
	nodeBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: nodeVideoCarrier, Endpoint: videoEndpoint},
		deEndpoint.ID:              {Carrier: deCarrier, Endpoint: deEndpoint},
		usEndpoint.ID:              {Carrier: usCarrier, Endpoint: usEndpoint},
	}
	clientBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"fake-video":               {Carrier: clientVideoCarrier, Endpoint: videoEndpoint},
	}
	clientTunnel := itunnel.NewCompositeTunnel(
		itunnel.NewCarrierTunnel("role-reversal-xray-client", clientBindings),
		itunnel.NewSingBoxTunnel(clientBindings),
	)
	defer func() { _ = clientTunnel.Close() }()

	// This is deliberately an in-process sidecar: the assertion is profile
	// dispatch and per-route lifecycle, not reachability of a public VLESS host.
	runner := &profileSidecarRunner{}
	restoreRunner := itunnel.SetSingBoxRunnerForTest(runner)
	defer restoreRunner()

	bootstrapConfig := func(role config.Role) config.Config {
		return config.Config{
			Role:        role,
			NodeID:      "xray-node",
			ClientID:    "xray-client",
			SocksListen: "127.0.0.1:0",
			CarrierConfigs: []config.CarrierConfig{{
				ID:         carriers.CarrierVKMessages,
				Endpoint:   config.EndpointConfig{Address: controlEndpoint.Address},
				VKMessages: &config.VKMessagesConfig{},
			}},
			ClientRoomCreation: role == config.RoleClient,
		}
	}
	nodeControl, err := NewControlPlaneWithTokens(bootstrapConfig(config.RoleNode), nodeBindings, policy.DefaultAdaptivePolicy(), nil, newRoleReversalProfileTokenStore(controlEndpoint.Address))
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	clientControl, err := NewControlPlaneWithTokens(bootstrapConfig(config.RoleClient), clientBindings, policy.DefaultAdaptivePolicy(), clientTunnel, newRoleReversalProfileTokenStore(controlEndpoint.Address))
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}
	nodeControl.pollInterval = 25 * time.Millisecond
	nodeControl.busyRetryAfter = 250 * time.Millisecond
	clientControl.pollInterval = 25 * time.Millisecond
	clientControl.busyRetryAfter = 250 * time.Millisecond

	if clientTunnel.SupportsEndpoint(carriers.Endpoint{ID: deEndpoint.ID, Carrier: deEndpoint.ID, Address: deEndpoint.Address}) {
		t.Fatal("client unexpectedly supports an Xray route before receiving its encrypted profile")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)
	waitForNodeVisible(t, clientControl, "xray-node", true)

	status, err := clientControl.Connect(ctx, "xray-node")
	if err != nil {
		t.Fatalf("role-reversal connect: %v", err)
	}
	if status.State != statusStateConnected {
		t.Fatalf("state = %s, want connected", status.State)
	}
	profiles := map[string]carriers.Endpoint{}
	for _, endpoint := range status.EgressEndpoints {
		if endpoint.ID == deEndpoint.ID || endpoint.ID == usEndpoint.ID {
			profiles[endpoint.ID] = endpoint
		}
	}
	if len(profiles) != 2 {
		t.Fatalf("status missing two Xray profile endpoints: %+v", status.EgressEndpoints)
	}

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoListener.Close()
	go serveProfileEcho(echoListener)
	for id, endpoint := range profiles {
		if !clientTunnel.SupportsEndpoint(endpoint) {
			t.Fatalf("client did not install dynamic binding for %s: %+v", id, endpoint)
		}
		conn, dialErr := clientTunnel.DialContext(ctx, endpoint, echoListener.Addr().String())
		if dialErr != nil {
			t.Fatalf("dial %s through dynamic profile: %v", id, dialErr)
		}
		payload := []byte("profile-" + id)
		if _, writeErr := conn.Write(payload); writeErr != nil {
			_ = conn.Close()
			t.Fatalf("write %s: %v", id, writeErr)
		}
		got := make([]byte, len(payload))
		if _, readErr := io.ReadFull(conn, got); readErr != nil {
			_ = conn.Close()
			t.Fatalf("read %s: %v", id, readErr)
		}
		_ = conn.Close()
		if string(got) != string(payload) {
			t.Fatalf("payload for %s = %q, want %q", id, got, payload)
		}
	}
	if started := runner.startedEndpointIDs(); len(started) != 2 || !started[deEndpoint.ID] || !started[usEndpoint.ID] {
		t.Fatalf("sidecars = %+v, want one for each Xray endpoint", started)
	}

	clientControl.Disconnect()
	if clientTunnel.SupportsEndpoint(carriers.Endpoint{ID: carriers.CarrierSingBoxVLESS, Carrier: carriers.CarrierSingBoxVLESS}) {
		t.Fatal("disconnect left a synthetic canonical singbox.vless alias installed")
	}
	if closed := runner.closedEndpointIDs(); len(closed) != 2 || !closed[deEndpoint.ID] || !closed[usEndpoint.ID] {
		t.Fatalf("closed sidecars = %+v, want both dynamic profiles closed on disconnect", closed)
	}
}

func newTestProfileCarrier(t *testing.T, uri string) *carriers.SingBoxVLESSCarrier {
	t.Helper()
	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{URI: uri, LocalListen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("new test profile carrier: %v", err)
	}
	return carrier
}

func newRoleReversalProfileTokenStore(channelID string) *tokens.Store {
	store := tokens.NewStore()
	store.Set(&tokens.Token{
		ID:        "role-reversal-vk",
		Platform:  "vk",
		Kind:      tokens.KindAPIKey,
		Lifecycle: tokens.LifecycleEmbedded,
		Status:    tokens.StatusActive,
		Value:     "role-reversal-profile-test-token",
	})
	store.AddBinding(tokens.Binding{
		TokenID:        "role-reversal-vk",
		Platform:       "vk",
		ConnectionType: "messages",
		ChannelID:      channelID,
		Role:           "discovery",
		Enabled:        true,
	})
	return store
}

type profileSidecarRunner struct {
	mu      sync.Mutex
	started map[string]bool
	closed  map[string]bool
}

func (r *profileSidecarRunner) Start(_ context.Context, _ *carriers.SingBoxVLESSCarrier, endpoint carriers.Endpoint, localListen string) (itunnel.SingBoxProcess, string, error) {
	listener, err := net.Listen("tcp", localListen)
	if err != nil {
		return nil, "", err
	}
	r.mu.Lock()
	if r.started == nil {
		r.started = make(map[string]bool)
	}
	r.started[endpoint.ID] = true
	r.mu.Unlock()
	process := &profileSidecarProcess{
		listener: listener,
		done:     make(chan struct{}),
		onClose: func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.closed == nil {
				r.closed = make(map[string]bool)
			}
			r.closed[endpoint.ID] = true
		},
	}
	go process.serve()
	return process, listener.Addr().String(), nil
}

func (r *profileSidecarRunner) startedEndpointIDs() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := make(map[string]bool, len(r.started))
	for id, started := range r.started {
		copy[id] = started
	}
	return copy
}

func (r *profileSidecarRunner) closedEndpointIDs() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := make(map[string]bool, len(r.closed))
	for id, closed := range r.closed {
		copy[id] = closed
	}
	return copy
}

type profileSidecarProcess struct {
	listener net.Listener
	done     chan struct{}
	onClose  func()
}

func (p *profileSidecarProcess) Close() error {
	err := p.listener.Close()
	<-p.done
	p.onClose()
	return err
}

func (p *profileSidecarProcess) serve() {
	defer close(p.done)
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go handleProfileSidecarSOCKS(conn)
	}
}

func handleProfileSidecarSOCKS(conn net.Conn) {
	defer conn.Close()
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return
	}
	if _, err := io.CopyN(io.Discard, conn, int64(greeting[1])); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil || request[1] != 0x01 || request[3] != 0x01 {
		return
	}
	var host [4]byte
	var port [2]byte
	if _, err := io.ReadFull(conn, host[:]); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(net.IP(host[:]).String(), fmt.Sprint(binary.BigEndian.Uint16(port[:]))))
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, target); done <- struct{}{} }()
	<-done
}

func serveProfileEcho(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}()
	}
}
