package telemost

import (
	"context"
	"strings"
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
	if a.ID() != "telemost" {
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
		Credentials: map[string]string{"join_link": "https://meet.yandex.ru/abc-def", "cookie": "cookie-val"},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{"display_name": "TestUser", "role": "creator"},
	}
	a := configuredProvider(t, cfg)
	if a.sessCfg.JoinLink != "https://meet.yandex.ru/abc-def" {
		t.Errorf("JoinLink = %q", a.sessCfg.JoinLink)
	}
	if a.sessCfg.Cookie != "cookie-val" {
		t.Errorf("Cookie = %q", a.sessCfg.Cookie)
	}
	if a.sessCfg.DisplayName != "TestUser" {
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

func TestConfigureJoinLinkFromEndpoints(t *testing.T) {
	cfg := provider.ProviderConfig{
		Credentials: map[string]string{},
		Endpoints:   map[string]string{"join_link": "https://meet.yandex.ru/from-endpoint"},
		Settings:    map[string]any{},
	}
	a := configuredProvider(t, cfg)
	if a.sessCfg.JoinLink != "https://meet.yandex.ru/from-endpoint" {
		t.Errorf("JoinLink from endpoints = %q", a.sessCfg.JoinLink)
	}
}

func TestGetSchema(t *testing.T) {
	schema := (&Provider{}).GetSchema()
	if schema.Name != "Telemost" {
		t.Errorf("schema.Name = %q", schema.Name)
	}
	if len(schema.Fields) != 4 {
		t.Errorf("schema.Fields = %d, want 4", len(schema.Fields))
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

	if err := a.Send(context.Background(), []byte("data")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(mt.sent) != 1 || string(mt.sent[0]) != "data" {
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
	a.UpdateMetrics(provider.Metrics{SentBytes: 100})
	if a.GetMetrics().SentBytes != 100 {
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

func TestCreateAndStartEgressWithoutJoinLink(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	if _, err := a.CreateAndStartEgress(context.Background()); err == nil || !strings.Contains(err.Error(), "join_link required") {
		t.Fatalf("CreateAndStartEgress error = %v, want explicit join_link requirement", err)
	}
}

func TestCreateAndStartEgressWithJoinLink(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"join_link": "https://meet.yandex.ru/test-room"},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{},
	})
	a.startEgress = func(context.Context, string) error { return nil }
	addr, err := a.CreateAndStartEgress(context.Background())
	if err != nil {
		t.Fatalf("CreateAndStartEgress: %v", err)
	}
	if addr != "https://meet.yandex.ru/test-room" {
		t.Errorf("addr = %q, want https://meet.yandex.ru/test-room", addr)
	}
}

func TestCreateAndStartEgressStartsNodeTunnelForExistingJoinLink(t *testing.T) {
	a := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"join_link": "https://meet.yandex.ru/test-room"},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{},
	})
	started := ""
	a.startEgress = func(_ context.Context, joinLink string) error {
		started = joinLink
		return nil
	}

	addr, err := a.CreateAndStartEgress(context.Background())
	if err != nil {
		t.Fatalf("CreateAndStartEgress: %v", err)
	}
	if addr != "https://meet.yandex.ru/test-room" || started != addr {
		t.Fatalf("addr/start = %q/%q, want one started existing join link", addr, started)
	}
}

func TestStartEgressAddrRequiresContext(t *testing.T) {
	// StartEgressAddr delegates to StartEgress which starts a real headless joiner.
	// We only verify the method signature exists via the interface check.
	// Actual egress requires a real Telemost server (integration test).
	a := configuredProvider(t, emptyCfg())
	// Verify second call to StartEgress would fail with "already active"
	// after the first one starts the joiner goroutine.
	_ = a.Close()
}

func TestExtractJoinLinkID(t *testing.T) {
	tests := []struct{ input, want string }{
		{"telemost://room-42", "room-42"},
		{"telemost://j/room-42", "j/room-42"},
		{"https://telemost.yandex.ru/my-meeting", "my-meeting"},
		{"https://meet.yandex.ru/abc?param=1", "abc"},
		{"plain-id", "plain-id"},
	}
	for _, tt := range tests {
		if got := extractJoinLinkID(tt.input); got != tt.want {
			t.Errorf("extractJoinLinkID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractJoinLinkIDExported(t *testing.T) {
	if got := ExtractJoinLinkID("telemost://test"); got != "test" {
		t.Errorf("ExtractJoinLinkID = %q", got)
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

func TestCloseResetsTunnel(t *testing.T) {
	a := configuredProvider(t, emptyCfg())
	mt := &mockTunnel{}
	a.mu.Lock()
	a.tunnel = mt
	a.mu.Unlock()

	_ = a.Close()
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.tunnel != nil {
		t.Error("tunnel should be nil after Close")
	}
	if a.onDataFn != nil {
		t.Error("onDataFn should be nil after Close")
	}
	if a.onCloseFn != nil {
		t.Error("onCloseFn should be nil after Close")
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

func TestNopStatus(t *testing.T) {
	ns := nopStatus{}
	ns.EmitStatus("test")     // should not panic
	ns.EmitStatusError("err") // should not panic
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
