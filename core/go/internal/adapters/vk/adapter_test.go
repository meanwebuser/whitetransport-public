package vk

import (
	"context"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

func TestAdapterIdentity(t *testing.T) {
	a := &Provider{}
	if got := a.ID(); got != "vk" {
		t.Errorf("ID() = %q, want vk", got)
	}
	if got := a.Type(); got != provider.TypeMessaging {
		t.Errorf("Type() = %v, want TypeMessaging", got)
	}
	if got := a.Category(); got != provider.CategorySocial {
		t.Errorf("Category() = %v, want CategorySocial", got)
	}
	if got := a.Version(); got != "1.0.0" {
		t.Errorf("Version() = %q, want 1.0.0", got)
	}
}

func TestGetSchema(t *testing.T) {
	schema := (&Provider{}).GetSchema()
	if schema.Name != "VK Messages" {
		t.Errorf("schema.Name = %q", schema.Name)
	}
	if len(schema.Fields) != 3 {
		t.Errorf("schema.Fields count = %d, want 3", len(schema.Fields))
	}
	for _, f := range schema.Fields {
		if f.Name == "token" && !f.Required {
			t.Error("token should be required")
		}
	}
}

func TestConfigureMissingToken(t *testing.T) {
	a := &Provider{}
	err := a.Configure(provider.ProviderConfig{
		Credentials: map[string]string{},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestConfigureEmptyToken(t *testing.T) {
	a := &Provider{}
	err := a.Configure(provider.ProviderConfig{
		Credentials: map[string]string{"token": ""},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestConfigureValid(t *testing.T) {
	a := &Provider{}
	err := a.Configure(provider.ProviderConfig{
		Credentials: map[string]string{"token": "test-vk-token-123"},
		Endpoints:   map[string]string{"api_url": "https://api.vk.com/method", "peer_id": "12345"},
		Settings:    map[string]any{"api_version": "5.199"},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if a.messaging == nil {
		t.Error("messaging carrier should be initialized")
	}
	if a.health.LastCheck.IsZero() {
		t.Error("health.LastCheck should be set")
	}
}

func TestSendWithoutConfigure(t *testing.T) {
	a := &Provider{}
	err := a.Send(context.Background(), []byte("hello"))
	if err == nil {
		t.Fatal("expected error when not configured")
	}
}

func TestReceiveWithoutConfigure(t *testing.T) {
	a := &Provider{}
	_, err := a.Receive(context.Background())
	if err == nil {
		t.Fatal("expected error when not configured")
	}
}

func TestHealthAndMetrics(t *testing.T) {
	a := &Provider{}
	if err := a.Configure(provider.ProviderConfig{
		Credentials: map[string]string{"token": "tok"},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{},
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	h := a.Health()
	if h.LastCheck.IsZero() {
		t.Error("Health.LastCheck should be set after Configure")
	}

	limits := a.GetLimits()
	if limits.MaxPayloadBytes != 4096 {
		t.Errorf("MaxPayloadBytes = %d, want 4096", limits.MaxPayloadBytes)
	}
	if limits.MaxRatePerMinute != 120 {
		t.Errorf("MaxRatePerMinute = %d, want 120", limits.MaxRatePerMinute)
	}
	if limits.MaxDailyBytes != 1_000_000_000 {
		t.Errorf("MaxDailyBytes = %d", limits.MaxDailyBytes)
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
	a := &Provider{}
	if err := a.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := a.Unload(); err != nil {
		t.Fatalf("Unload: %v", err)
	}
}

func TestHealthTimestamp(t *testing.T) {
	before := time.Now()
	a := &Provider{}
	if err := a.Configure(provider.ProviderConfig{
		Credentials: map[string]string{"token": "tok"},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	h := a.Health()
	if h.LastCheck.Before(before) || h.LastCheck.After(after) {
		t.Errorf("LastCheck %v not between %v and %v", h.LastCheck, before, after)
	}
}

func TestProviderInterface(t *testing.T) {
	var _ provider.Provider = (*Provider)(nil)
}
