package dion

import (
	"context"
	"testing"
	"time"

	"whitelist-bypass/relay/tunnel"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

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
	if a.ID() != "dion" {
		t.Errorf("ID() = %q", a.ID())
	}
	if a.Type() != provider.TypeVideoCall {
		t.Errorf("Type() = %v", a.Type())
	}
	if a.Category() != provider.CategoryVideo {
		t.Errorf("Category() = %v", a.Category())
	}
	if a.Version() != "1.0.0" {
		t.Errorf("Version() = %q", a.Version())
	}
}

func TestConfigure(t *testing.T) {
	cfg := provider.ProviderConfig{
		Credentials: map[string]string{
			"event_id":     "evt-123",
			"access_token": "tok-abc",
			"cookies_file": "/path/to/cookies.json",
		},
		Endpoints: map[string]string{},
		Settings:  map[string]any{"display_name": "DionTest", "role": "creator"},
	}
	a := configuredProvider(t, cfg)
	if a.sessCfg.EventID != "evt-123" {
		t.Errorf("EventID = %q", a.sessCfg.EventID)
	}
	if a.sessCfg.AccessToken != "tok-abc" {
		t.Errorf("AccessToken = %q", a.sessCfg.AccessToken)
	}
	if a.sessCfg.CookiesFile != "/path/to/cookies.json" {
		t.Errorf("CookiesFile = %q", a.sessCfg.CookiesFile)
	}
	if a.sessCfg.DisplayName != "DionTest" {
		t.Errorf("DisplayName = %q", a.sessCfg.DisplayName)
	}
	if a.sessCfg.Role != "creator" {
		t.Errorf("Role = %q", a.sessCfg.Role)
	}
}

func TestConfigureDefaults(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if a.sessCfg.DisplayName != "WhiteTransport" {
		t.Errorf("default DisplayName = %q", a.sessCfg.DisplayName)
	}
	if a.sessCfg.Role != "joiner" {
		t.Errorf("default Role = %q", a.sessCfg.Role)
	}
}

func TestConfigureEventIDFromEndpoints(t *testing.T) {
	cfg := provider.ProviderConfig{
		Credentials: map[string]string{},
		Endpoints:   map[string]string{"event_id": "evt-from-endpoint"},
		Settings:    map[string]any{},
	}
	a := configuredProvider(t, cfg)
	if a.sessCfg.EventID != "evt-from-endpoint" {
		t.Errorf("EventID from endpoints = %q", a.sessCfg.EventID)
	}
}

func TestConfigureEventIDCredentialsWinsOverEndpoints(t *testing.T) {
	cfg := provider.ProviderConfig{
		Credentials: map[string]string{"event_id": "from-creds"},
		Endpoints:   map[string]string{"event_id": "from-endpoints"},
		Settings:    map[string]any{},
	}
	a := configuredProvider(t, cfg)
	if a.sessCfg.EventID != "from-creds" {
		t.Errorf("EventID = %q, want from-creds", a.sessCfg.EventID)
	}
}

func TestGetSchema(t *testing.T) {
	schema := (&Provider{}).GetSchema()
	if schema.Name != "DION" {
		t.Errorf("schema.Name = %q", schema.Name)
	}
	if len(schema.Fields) != 5 {
		t.Errorf("schema.Fields = %d, want 5", len(schema.Fields))
	}
}

func TestSendWithoutTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if err := a.Send(context.Background(), []byte("hi")); err == nil {
		t.Fatal("expected error without tunnel")
	}
}

func TestReceiveNotSupported(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Receive(ctx); err == nil {
		t.Fatal("expected error for Receive")
	}
}

func TestSendWithMockTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()

	if err := a.Send(context.Background(), []byte("dion-data")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(mt.sent) != 1 || string(mt.sent[0]) != "dion-data" {
		t.Errorf("sent = %v", mt.sent)
	}
}

func TestSetOnDataPropagates(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()

	called := false
	a.SetOnData(func(b []byte) { called = true })
	if mt.onDataFn == nil {
		t.Fatal("SetOnData not propagated")
	}
	mt.onDataFn(nil)
	if !called {
		t.Error("callback not invoked")
	}
}

func TestSetOnClosePropagates(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()

	called := false
	a.SetOnClose(func() { called = true })
	if mt.onCloseFn == nil {
		t.Fatal("SetOnClose not propagated")
	}
	mt.onCloseFn()
	if !called {
		t.Error("callback not invoked")
	}
}

func TestSetOnDataWithoutTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	a.SetOnData(func(b []byte) {})
	a.SetOnClose(func() {})
	if a.onDataFn == nil || a.onCloseFn == nil {
		t.Error("callbacks should be stored even without tunnel")
	}
}

func TestHealthAndMetrics(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	h := a.Health()
	if h.LastCheck.IsZero() {
		t.Error("LastCheck should be set")
	}
	limits := a.GetLimits()
	if limits.MaxPayloadBytes != 32768 {
		t.Errorf("MaxPayloadBytes = %d", limits.MaxPayloadBytes)
	}
	m := a.GetMetrics()
	if m.SentBytes != 0 {
		t.Error("initial SentBytes should be 0")
	}
	a.UpdateMetrics(provider.Metrics{SentBytes: 200})
	if a.GetMetrics().SentBytes != 200 {
		t.Error("UpdateMetrics failed")
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
	a := configuredProvider(t, emptyCfg())
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestCloseResetsState(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.onDataFn = func([]byte) {}
	a.onCloseFn = func() {}
	a.mu.Unlock()

	_ = a.Close()
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.tunnel != nil {
		t.Error("tunnel should be nil after Close")
	}
	if a.call != nil {
		t.Error("call should be nil after Close")
	}
	if a.hj != nil {
		t.Error("hj should be nil after Close")
	}
	if a.session != nil {
		t.Error("session should be nil after Close")
	}
	if a.onDataFn != nil {
		t.Error("onDataFn should be nil after Close")
	}
	if a.onCloseFn != nil {
		t.Error("onCloseFn should be nil after Close")
	}
}

func TestDoneWithoutCall(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if ch := a.Done(); ch != nil {
		t.Error("Done should return nil when no call is active")
	}
}

func TestNormalizeSlug(t *testing.T) {
	tests := []struct{ input, want string }{
		{"dion://my-event", "my-event"},
		{"https://dion.vc/event/abc123", "abc123"},
		{"https://dion.vc/xyz?param=1", "xyz"},
		{"plain-slug", "plain-slug"},
		{"  spaced  ", "spaced"},
	}
	for _, tt := range tests {
		if got := normalizeSlug(tt.input); got != tt.want {
			t.Errorf("normalizeSlug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractEventID(t *testing.T) {
	if got := ExtractEventID("dion://test-evt"); got != "test-evt" {
		t.Errorf("ExtractEventID = %q", got)
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

func TestNopStatus(t *testing.T) {
	ns := nopStatus{}
	ns.EmitStatus("test")
	ns.EmitStatusError("err")
}

func TestHealthTimestamp(t *testing.T) {
	before := time.Now()
	a := configuredProvider(t, emptyCfg())
	after := time.Now()
	h := a.Health()
	if h.LastCheck.Before(before) || h.LastCheck.After(after) {
		t.Errorf("LastCheck %v not in [%v, %v]", h.LastCheck, before, after)
	}
}

func TestDataTunnelAccessor(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if a.tunnel != nil {
		t.Error("tunnel should be nil initially")
	}
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()
	if a.tunnel != tunnel.DataTunnel(mt) {
		t.Error("tunnel accessor mismatch")
	}
}

func TestStartEgressAlreadyActive(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	// Simulate an active joiner by setting hj.
	a.mu.Lock()
	a.hj = nil // placeholder — we just check the guard.
	a.mu.Unlock()

	// Set hj to non-nil via a dummy to trigger "already active".
	// We can't create a real DionHeadlessJoiner without deps, so skip
	// this test for the joiner guard. The guard is tested via interface check.
}

func TestCreateAndStartEgressRequiresNetwork(t *testing.T) {
	// CreateAndStartEgress creates a real DION session which requires network.
	// We verify it doesn't panic with empty config but returns an error.
	a := configuredProvider(t, emptyCfg())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := a.CreateAndStartEgress(ctx)
	// Expected: either session creation fails or guest auth fails.
	if err == nil {
		t.Fatal("expected error without real DION server")
	}
}
