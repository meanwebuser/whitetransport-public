package provider

import (
	"testing"
	"time"
)

func TestTypeConstants(t *testing.T) {
	types := []Type{TypeMessaging, TypeFileTransfer, TypeVideoCall, TypeAudioCall, TypeCloudStorage, TypeSocial}
	seen := make(map[Type]bool, len(types))
	for _, tp := range types {
		if tp == "" {
			t.Error("empty Type constant")
		}
		if seen[tp] {
			t.Errorf("duplicate Type: %q", tp)
		}
		seen[tp] = true
	}
}

func TestCategoryConstants(t *testing.T) {
	cats := []Category{CategorySocial, CategoryCloud, CategoryVideo, CategoryAudio, CategoryOther}
	seen := make(map[Category]bool, len(cats))
	for _, c := range cats {
		if c == "" {
			t.Error("empty Category constant")
		}
		if seen[c] {
			t.Errorf("duplicate Category: %q", c)
		}
		seen[c] = true
	}
}

func TestKeyTypeConstants(t *testing.T) {
	keys := []KeyType{KeyPermanent, KeyTemporary, KeyAnonymous}
	for _, k := range keys {
		if k == "" {
			t.Error("empty KeyType constant")
		}
	}
}

func TestKeyStatusConstants(t *testing.T) {
	statuses := []KeyStatus{KeyActive, KeyExpired, KeyRevoked, KeyLimited}
	for _, s := range statuses {
		if s == "" {
			t.Error("empty KeyStatus constant")
		}
	}
}

func TestHealthZeroValue(t *testing.T) {
	h := Health{}
	if h.SuccessRate != 0 {
		t.Error("zero SuccessRate expected")
	}
	if h.AvgLatency != 0 {
		t.Error("zero AvgLatency expected")
	}
	if h.ErrorCount != 0 {
		t.Error("zero ErrorCount expected")
	}
	if !h.LastCheck.IsZero() {
		t.Error("zero LastCheck expected")
	}
}

func TestLimitsDefaults(t *testing.T) {
	l := Limits{MaxPayloadBytes: 4096, MaxRatePerMinute: 120, MaxDailyBytes: 1_000_000_000}
	if l.MaxPayloadBytes != 4096 {
		t.Errorf("MaxPayloadBytes = %d", l.MaxPayloadBytes)
	}
	if l.MaxDailyBytes != 1_000_000_000 {
		t.Errorf("MaxDailyBytes = %d", l.MaxDailyBytes)
	}
}

func TestMetricsAccumulation(t *testing.T) {
	m := Metrics{
		SentBytes:     100,
		ReceivedBytes: 200,
		MessagesSent:  5,
		MessagesRecv:  10,
		Errors:        1,
		AvgLatency:    50 * time.Millisecond,
		Uptime:        10 * time.Second,
	}
	if m.SentBytes != 100 {
		t.Errorf("SentBytes = %d", m.SentBytes)
	}
	if m.AvgLatency != 50*time.Millisecond {
		t.Errorf("AvgLatency = %v", m.AvgLatency)
	}
}

func TestChannelStruct(t *testing.T) {
	ch := Channel{ID: "ch-1", Type: "messaging", Limits: Limits{MaxPayloadBytes: 2048}, Enabled: true}
	if ch.ID != "ch-1" {
		t.Error("Channel ID mismatch")
	}
	if !ch.Enabled {
		t.Error("Channel should be enabled")
	}
}

func TestSchemaFields(t *testing.T) {
	s := Schema{
		Name:    "Test Provider",
		Version: "1.0.0",
		Fields: []Field{
			{Name: "token", Type: "string", Required: true},
			{Name: "optional", Type: "string", Required: false, Default: "def"},
		},
	}
	if len(s.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(s.Fields))
	}
	if !s.Fields[0].Required {
		t.Error("first field should be required")
	}
	if s.Fields[1].Default != "def" {
		t.Errorf("default = %v, want def", s.Fields[1].Default)
	}
}

func TestProviderConfigMaps(t *testing.T) {
	cfg := ProviderConfig{
		Type:     TypeMessaging,
		Category: CategorySocial,
		Endpoints: map[string]string{
			"api_url": "https://api.example.com",
		},
		Credentials: map[string]string{
			"token": "secret",
		},
		Settings: map[string]any{
			"timeout": 30,
		},
	}
	if cfg.Endpoints["api_url"] != "https://api.example.com" {
		t.Error("endpoint mismatch")
	}
	if cfg.Credentials["token"] != "secret" {
		t.Error("credential mismatch")
	}
	if cfg.Settings["timeout"] != 30 {
		t.Error("setting mismatch")
	}
}

// TestProviderInterface ensures the Provider interface is well-formed
// by verifying it can be used as a type constraint.
func TestProviderInterface(t *testing.T) {
	var _ Provider = (Provider)(nil) // compile-time check — only validates syntax
}
