package whitelist

import (
	"context"
	"errors"
	"testing"
	"time"

	"whitelist-bypass/relay/tunnel"
	"whitelist-bypass/relay/wbstream"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

// mockTunnel implements tunnel.DataTunnel for unit testing.
type mockTunnel struct {
	sent      [][]byte
	onDataFn  func([]byte)
	onCloseFn func()
}

func (m *mockTunnel) SendData(data []byte) {
	m.sent = append(m.sent, append([]byte(nil), data...))
}
func (m *mockTunnel) SetOnData(fn func([]byte))  { m.onDataFn = fn }
func (m *mockTunnel) SetOnClose(fn func())       { m.onCloseFn = fn }
func (m *mockTunnel) Reconfigure(fps, batch int) {}

func configuredProvider(t *testing.T, cfg provider.ProviderConfig) *Provider {
	t.Helper()
	a := &Provider{}
	if err := a.Configure(cfg); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return a
}

func emptyCfg() provider.ProviderConfig {
	return provider.ProviderConfig{
		Credentials: map[string]string{},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{},
	}
}

func TestAdapterIdentity(t *testing.T) {
	a := &Provider{}
	if got := a.ID(); got != "wbstream" {
		t.Errorf("ID() = %q, want wbstream", got)
	}
	if got := a.Type(); got != provider.TypeVideoCall {
		t.Errorf("Type() = %v, want TypeVideoCall", got)
	}
	if got := a.Category(); got != provider.CategoryVideo {
		t.Errorf("Category() = %v, want CategoryVideo", got)
	}
	if got := a.Version(); got != "1.0.0" {
		t.Errorf("Version() = %q, want 1.0.0", got)
	}
}

func TestConfigure(t *testing.T) {
	cfg := provider.ProviderConfig{
		Credentials: map[string]string{"access_token": "tok-123", "room_token": "room-tok"},
		Endpoints:   map[string]string{"server_url": "wss://livekit.example.com", "room_id": "room-42"},
		Settings:    map[string]any{"display_name": "TestNode", "tunnel_mode": "vp8"},
	}
	a := configuredProvider(t, cfg)
	if a.sessCfg.ServerURL != "wss://livekit.example.com" {
		t.Errorf("ServerURL = %q", a.sessCfg.ServerURL)
	}
	if a.sessCfg.AccessToken != "tok-123" {
		t.Errorf("AccessToken = %q", a.sessCfg.AccessToken)
	}
	if a.sessCfg.RoomToken != "room-tok" {
		t.Errorf("RoomToken = %q", a.sessCfg.RoomToken)
	}
	if a.sessCfg.RoomID != "room-42" {
		t.Errorf("RoomID = %q", a.sessCfg.RoomID)
	}
	if a.sessCfg.DisplayName != "TestNode" {
		t.Errorf("DisplayName = %q", a.sessCfg.DisplayName)
	}
	if a.sessCfg.TunnelMode != "vp8" {
		t.Errorf("TunnelMode = %q", a.sessCfg.TunnelMode)
	}
	if a.done == nil {
		t.Error("done channel not initialized")
	}
}

func TestConfigureDefaults(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if a.sessCfg.DisplayName != "WhiteTransport" {
		t.Errorf("default DisplayName = %q", a.sessCfg.DisplayName)
	}
	if a.sessCfg.TunnelMode != "dc" {
		t.Errorf("default TunnelMode = %q", a.sessCfg.TunnelMode)
	}
}

func TestSystemVPNNetworkOriginsIncludesWBStreamICEHost(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Endpoints: map[string]string{"server_url": "wss://livekit.example.com"},
	})

	origins := a.SystemVPNNetworkOrigins()
	if len(origins) != 2 {
		t.Fatalf("SystemVPNNetworkOrigins() = %v, want LiveKit and ICE/TURN origins", origins)
	}
	if origins[0] != "https://wb-stream-turn-1.wb.ru" && origins[1] != "https://wb-stream-turn-1.wb.ru" {
		t.Fatalf("SystemVPNNetworkOrigins() = %v, missing WBStream ICE/TURN host", origins)
	}
}

func TestExtractCandidateIPs(t *testing.T) {
	raw := "a=candidate:1 1 UDP 2122260223 212.192.31.128 64496 typ host\r\na=candidate:2 1 UDP 1686052607 2001:db8::10 5349 typ srflx"
	got := extractCandidateIPs(raw)
	want := []string{"212.192.31.128", "2001:db8::10"}
	if len(got) != len(want) {
		t.Fatalf("extractCandidateIPs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extractCandidateIPs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSystemVPNNetworkOriginsIncludesRemoteCandidateIPs(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Endpoints: map[string]string{"server_url": "wss://livekit.example.com"},
	})
	a.recordRemoteCandidate("a=candidate:1 1 UDP 2122260223 212.192.31.128 64496 typ host")

	origins := a.SystemVPNNetworkOrigins()
	if len(origins) != 3 || origins[2] != "https://212.192.31.128" {
		t.Fatalf("SystemVPNNetworkOrigins() = %v, want remote candidate bypass", origins)
	}
}

func TestGetSchema(t *testing.T) {
	schema := (&Provider{}).GetSchema()
	if schema.Name != "WBStream" {
		t.Errorf("schema.Name = %q", schema.Name)
	}
	if len(schema.Fields) != 6 {
		t.Errorf("schema.Fields count = %d, want 6", len(schema.Fields))
	}
	for _, f := range schema.Fields {
		if f.Name == "server_url" && !f.Required {
			t.Error("server_url should be required")
		}
	}
}

func TestSendWithoutTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	err := a.Send(context.Background(), []byte("hello"))
	if err == nil {
		t.Fatal("expected error when tunnel not connected")
	}
}

func TestReceiveNotSupported(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if _, err := a.Receive(context.Background()); err == nil {
		t.Fatal("expected error for Receive")
	}
}

func TestSendWithMockTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()

	if err := a.Send(context.Background(), []byte("ping")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(mt.sent) != 1 || string(mt.sent[0]) != "ping" {
		t.Errorf("mockTunnel.sent = %v", mt.sent)
	}
}

func TestStartClearsSessionAfterStartError(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Endpoints: map[string]string{"server_url": "wss://example.invalid"},
		Credentials: map[string]string{
			"room_token": "bad-token",
		},
		Settings: map[string]any{"tunnel_mode": "dc"},
	})

	startErr := errors.New("ws dial: websocket: bad handshake (status 401)")
	oldStart := startWBStreamSession
	startWBStreamSession = func(s *wbstream.Session) error { return startErr }
	defer func() { startWBStreamSession = oldStart }()

	a.SetOnData(func([]byte) {})
	a.SetOnClose(func() {})

	err := a.Start(context.Background())
	if !errors.Is(err, startErr) {
		t.Fatalf("Start error = %v, want %v", err, startErr)
	}
	if a.session != nil {
		t.Fatal("session should be cleared after Start failure")
	}
	if a.tunnel != nil {
		t.Fatal("tunnel should be cleared after Start failure")
	}
	if a.lastRoomID != "" {
		t.Fatalf("lastRoomID = %q, want empty", a.lastRoomID)
	}
	if a.onDataFn == nil || a.onCloseFn == nil {
		t.Fatal("callbacks should be preserved for a later retry")
	}
}

func TestSessionDoneClearsActiveSessionAndNotifiesClose(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"access_token": "tok-preserve"},
		Endpoints:   map[string]string{"server_url": "wss://livekit.example.com", "room_id": "room-1"},
		Settings:    map[string]any{"display_name": "TestNode"},
	})
	s := wbstream.NewSession(wbstream.SessionConfig{})
	mt := &mockTunnel{}
	closed := false
	a.SetOnClose(func() { closed = true })

	a.mu.Lock()
	a.session = s
	a.tunnel = mt
	a.lastRoomID = "room-1"
	a.wlHealth.VPNSessionActive = true
	a.mu.Unlock()

	a.handleSessionDone(s, "room-1")

	if !closed {
		t.Fatal("onClose callback should be invoked when wbstream session ends")
	}
	if a.session != nil {
		t.Fatal("session should be cleared after wbstream session ends")
	}
	if a.tunnel != nil {
		t.Fatal("tunnel should be cleared after wbstream session ends")
	}
	if a.lastRoomID != "" {
		t.Fatalf("lastRoomID = %q, want empty", a.lastRoomID)
	}
	if a.wlHealth.VPNSessionActive {
		t.Fatal("VPN session should be marked inactive")
	}
	// Per-session fields should be cleared.
	if a.sessCfg.RoomID != "" {
		t.Errorf("sessCfg.RoomID = %q, want empty after session done", a.sessCfg.RoomID)
	}
	if a.sessCfg.RoomToken != "" {
		t.Errorf("sessCfg.RoomToken = %q, want empty after session done", a.sessCfg.RoomToken)
	}
	if a.sessCfg.ServerURL != "" {
		t.Errorf("sessCfg.ServerURL = %q, want empty after session done", a.sessCfg.ServerURL)
	}
	// Adapter-level config must survive for reconnect.
	if a.sessCfg.AccessToken != "tok-preserve" {
		t.Errorf("sessCfg.AccessToken = %q, want tok-preserve (must survive session done)", a.sessCfg.AccessToken)
	}
	if a.sessCfg.DisplayName != "TestNode" {
		t.Errorf("sessCfg.DisplayName = %q, want TestNode (must survive session done)", a.sessCfg.DisplayName)
	}
}

func TestSetOnDataPropagatesToTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()

	called := false
	a.SetOnData(func(b []byte) { called = true })
	if mt.onDataFn == nil {
		t.Fatal("SetOnData did not propagate to tunnel")
	}
	mt.onDataFn([]byte("test"))
	if !called {
		t.Error("onData callback not invoked")
	}
}

func TestSetOnClosePropagatesToTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()

	called := false
	a.SetOnClose(func() { called = true })
	if mt.onCloseFn == nil {
		t.Fatal("SetOnClose did not propagate to tunnel")
	}
	mt.onCloseFn()
	if !called {
		t.Error("onClose callback not invoked")
	}
}

func TestSetOnDataWithoutTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	a.SetOnData(func(b []byte) {})
	a.SetOnClose(func() {})
	if a.onDataFn == nil {
		t.Error("onDataFn should be set")
	}
	if a.onCloseFn == nil {
		t.Error("onCloseFn should be set")
	}
}

func TestHealthAndMetrics(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	h := a.Health()
	if h.LastCheck.IsZero() {
		t.Error("Health.LastCheck should be set after Configure")
	}
	limits := a.GetLimits()
	if limits.MaxPayloadBytes != 32768 {
		t.Errorf("MaxPayloadBytes = %d", limits.MaxPayloadBytes)
	}
	m := a.GetMetrics()
	if m.SentBytes != 0 {
		t.Error("initial SentBytes should be 0")
	}
	a.UpdateMetrics(provider.Metrics{SentBytes: 500, Errors: 2})
	m = a.GetMetrics()
	if m.SentBytes != 500 || m.Errors != 2 {
		t.Errorf("updated metrics: %+v", m)
	}
}

func TestLoadUnload(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if err := a.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := a.Unload(); err != nil {
		t.Fatalf("Unload: %v", err)
	}
}

func TestCloseWithoutSession(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"access_token": "tok-abc"},
		Endpoints:   map[string]string{"server_url": "wss://livekit.example.com", "room_id": "room-old"},
		Settings:    map[string]any{"display_name": "MyNode"},
	})
	// Simulate stale per-session fields from a previous connection.
	a.mu.Lock()
	a.sessCfg.RoomID = "room-old"
	a.sessCfg.RoomToken = "old-rt"
	a.sessCfg.ServerURL = "wss://stale.example.com"
	a.mu.Unlock()

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Per-session fields should be cleared even when session was already nil.
	if a.sessCfg.RoomID != "" {
		t.Errorf("sessCfg.RoomID = %q, want empty after Close", a.sessCfg.RoomID)
	}
	if a.sessCfg.RoomToken != "" {
		t.Errorf("sessCfg.RoomToken = %q, want empty after Close", a.sessCfg.RoomToken)
	}
	// Adapter-level config must survive.
	if a.sessCfg.AccessToken != "tok-abc" {
		t.Errorf("sessCfg.AccessToken = %q, want tok-abc (must survive Close)", a.sessCfg.AccessToken)
	}
	if a.sessCfg.DisplayName != "MyNode" {
		t.Errorf("sessCfg.DisplayName = %q, want MyNode (must survive Close)", a.sessCfg.DisplayName)
	}
}

// TestClosePreservesAccessTokenForReconnect is the regression test for the
// WBStream 401-on-reconnect bug. It verifies that Close() does NOT destroy
// the AccessToken and DisplayName set during Configure(), so the adapter can
// start a new WBStream session (StartEgressAddr) without reconfiguration.
func TestClosePreservesAccessTokenForReconnect(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"access_token": "wb-node-token-xyz"},
		Endpoints:   map[string]string{"server_url": "wss://livekit.vc.wb.ru"},
		Settings:    map[string]any{"display_name": "ReconnectTest"},
	})

	// Simulate a full session lifecycle: session starts, runs, then closes.
	s := wbstream.NewSession(wbstream.SessionConfig{})
	mt := &mockTunnel{}
	a.mu.Lock()
	a.session = s
	a.tunnel = mt
	a.sessCfg.RoomID = "room-1"
	a.sessCfg.RoomToken = "rt-1"
	a.sessCfg.ServerURL = "wss://livekit.vc.wb.ru"
	a.lastRoomID = "room-1"
	a.wlHealth.VPNSessionActive = true
	a.mu.Unlock()

	// Close the adapter (as Disconnect would do).
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify session state is fully cleared.
	if a.session != nil {
		t.Fatal("session should be nil after Close")
	}
	if a.tunnel != nil {
		t.Fatal("tunnel should be nil after Close")
	}
	if a.lastRoomID != "" {
		t.Fatalf("lastRoomID = %q, want empty", a.lastRoomID)
	}

	// Critical: adapter-level config must survive for the next connection.
	if a.sessCfg.AccessToken != "wb-node-token-xyz" {
		t.Errorf("AccessToken = %q after Close, want wb-node-token-xyz (root cause of 401 on reconnect)", a.sessCfg.AccessToken)
	}
	if a.sessCfg.DisplayName != "ReconnectTest" {
		t.Errorf("DisplayName = %q after Close, want ReconnectTest", a.sessCfg.DisplayName)
	}
	if a.sessCfg.TunnelMode != "dc" {
		t.Errorf("TunnelMode = %q after Close, want dc", a.sessCfg.TunnelMode)
	}

	// Per-session fields should be cleared.
	if a.sessCfg.RoomID != "" {
		t.Errorf("sessCfg.RoomID = %q, want empty after Close", a.sessCfg.RoomID)
	}
	if a.sessCfg.RoomToken != "" {
		t.Errorf("sessCfg.RoomToken = %q, want empty after Close", a.sessCfg.RoomToken)
	}
	if a.sessCfg.ServerURL != "" {
		t.Errorf("sessCfg.ServerURL = %q, want empty after Close", a.sessCfg.ServerURL)
	}
}

// TestRoomChangePreservesCallbacks verifies that onDataFn/onCloseFn survive
// room transitions in startLocked. The tunnel layer sets these via
// SetOnData/SetOnClose and expects them to persist across reconnections.
func TestRoomChangePreservesCallbacks(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"access_token": "tok-1"},
		Endpoints:   map[string]string{"server_url": "wss://livekit.example.com"},
		Settings:    map[string]any{},
	})

	dataCalled := false
	closeCalled := false
	a.SetOnData(func([]byte) { dataCalled = true })
	a.SetOnClose(func() { closeCalled = true })

	// Simulate an active session for room-1.
	oldSession := wbstream.NewSession(wbstream.SessionConfig{})
	oldTunnel := &mockTunnel{}
	a.mu.Lock()
	a.session = oldSession
	a.tunnel = oldTunnel
	a.sessCfg.RoomID = "room-2"
	a.sessCfg.RoomToken = "rt-2"
	a.sessCfg.ServerURL = "wss://livekit.example.com"
	a.lastRoomID = "room-1"
	a.mu.Unlock()

	// Mock startWBStreamSession to succeed with a new session.
	oldStart := startWBStreamSession
	defer func() { startWBStreamSession = oldStart }()
	startWBStreamSession = func(s *wbstream.Session) error { return nil }

	// Start with the new room config — should trigger room-change reset.
	err := a.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Callbacks must survive the room transition.
	if a.onDataFn == nil {
		t.Fatal("onDataFn should be preserved across room transitions")
	}
	if a.onCloseFn == nil {
		t.Fatal("onCloseFn should be preserved across room transitions")
	}

	// Verify the callbacks are still functional.
	a.onDataFn([]byte("test"))
	if !dataCalled {
		t.Error("onDataFn callback should still work after room change")
	}
	a.onCloseFn()
	if !closeCalled {
		t.Error("onCloseFn callback should still work after room change")
	}

	// Clean up.
	a.Close()
}

func TestStartEgressAddrWithoutServerURL(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if err := a.StartEgressAddr(context.Background(), "wbstream://room-123"); err == nil {
		t.Fatal("expected error without server_url")
	}
}

func TestCreateAndStartEgressWithoutServerURL(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if _, err := a.CreateAndStartEgress(context.Background()); err == nil {
		t.Fatal("expected error without server_url")
	}
}

func TestCreateAndStartEgressWithoutAccessToken(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{},
		Endpoints:   map[string]string{"server_url": "wss://example.com"},
		Settings:    map[string]any{},
	})
	if _, err := a.CreateAndStartEgress(context.Background()); err == nil {
		t.Fatal("expected error without access_token")
	}
}

func TestSystemVPNNetworkOriginsExposeActiveServerAndICEHosts(t *testing.T) {
	adapter := &Provider{}
	if err := adapter.Configure(provider.ProviderConfig{
		Type:      provider.TypeVideoCall,
		Endpoints: map[string]string{"server_url": "wss://livekit.example.com"},
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	origins := adapter.SystemVPNNetworkOrigins()
	if len(origins) != 2 || origins[0] != "wss://livekit.example.com" || origins[1] != "https://wb-stream-turn-1.wb.ru" {
		t.Fatalf("SystemVPNNetworkOrigins = %#v", origins)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if origins := adapter.SystemVPNNetworkOrigins(); len(origins) != 0 {
		t.Fatalf("closed SystemVPNNetworkOrigins = %#v, want empty", origins)
	}
}

func TestExtractRoomID(t *testing.T) {
	tests := []struct{ input, want string }{
		{"wbstream://room-42", "room-42"},
		{"room-plain", "room-plain"},
		{"https://livekit.example.com/room-99", "room-99"},
		{"  wbstream://trimmed  ", "trimmed"},
	}
	for _, tt := range tests {
		if got := ExtractRoomID(tt.input); got != tt.want {
			t.Errorf("ExtractRoomID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestVideoTunnelAdapterInterface(t *testing.T) {
	type vta interface {
		CreateAndStartEgress(ctx context.Context) (string, error)
		StartEgressAddr(ctx context.Context, addr string) error
		Close() error
	}
	var _ vta = (*Provider)(nil)
}

func TestDataTunnelAccessor(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if a.DataTunnel() != nil {
		t.Error("DataTunnel should be nil before connection")
	}
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()
	if a.DataTunnel() != tunnel.DataTunnel(mt) {
		t.Error("DataTunnel should return the mock")
	}
}

func TestSendDataRaw(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()
	a.SendData([]byte("raw-data"))
	if len(mt.sent) != 1 || string(mt.sent[0]) != "raw-data" {
		t.Errorf("SendData: sent = %v", mt.sent)
	}
}

func TestSendDataNilTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	a.SendData([]byte("ignored")) // should not panic
}

func TestWhitelistHealthDefault(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	wh := a.WhitelistHealth()
	if wh.VPNSessionActive {
		t.Error("should not have active session initially")
	}
}

func TestHealthTimestamp(t *testing.T) {
	before := time.Now()
	a := configuredProvider(t, emptyCfg())
	after := time.Now()
	h := a.Health()
	if h.LastCheck.Before(before) || h.LastCheck.After(after) {
		t.Errorf("LastCheck %v not between %v and %v", h.LastCheck, before, after)
	}
}
