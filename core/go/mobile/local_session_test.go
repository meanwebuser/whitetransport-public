package mobile

import (
	"path/filepath"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
)

func TestApplyMobileRuntimePathsUsesAppPrivateStateDirectory(t *testing.T) {
	cfg := config.Config{StateFile: "/data/user/0/bypass.whitelist/files/wt-runtime/state.json"}

	applyMobileRuntimePaths(&cfg)

	if cfg.SessionEgress.SingBox == nil {
		t.Fatal("mobile runtime did not configure session sing-box")
	}
	want := filepath.Join(filepath.Dir(cfg.StateFile), "sing-box")
	if got := cfg.SessionEgress.SingBox.ConfigDir; got != want {
		t.Fatalf("config dir = %q, want %q", got, want)
	}
}

func TestApplyMobileRuntimePathsPreservesExplicitConfigDirectory(t *testing.T) {
	cfg := config.Config{
		StateFile: "/data/user/0/bypass.whitelist/files/wt-runtime/state.json",
		SessionEgress: config.SessionEgressConfig{SingBox: &config.SessionSingBoxRuntimeConfig{
			ConfigDir: "/explicit/sing-box",
		}},
	}

	applyMobileRuntimePaths(&cfg)

	if got := cfg.SessionEgress.SingBox.ConfigDir; got != "/explicit/sing-box" {
		t.Fatalf("explicit config dir changed to %q", got)
	}
}

func TestApplyLocalSessionUsesMemoryOnlyAndEnablesClientRoom(t *testing.T) {
	cfg := config.Config{
		Role:       config.RoleClient,
		TokenStore: &config.TokenStoreConfig{Tokens: []config.TokenEntry{{ID: "bootstrap-only"}}},
		CarrierConfigs: []config.CarrierConfig{{
			ID:          "wbstream.vp8",
			CarrierType: "wbstream",
			WBStream:    &config.WBStreamConfig{},
		}},
	}

	err := applyLocalSession(&cfg, LocalSession{
		Platform:     "wbstream",
		AccessToken:  "local-access",
		CookieHeader: "local-cookie",
	})
	if err != nil {
		t.Fatalf("applyLocalSession: %v", err)
	}
	if !cfg.ClientRoomCreation {
		t.Fatal("local WBStream session must enable client room creation")
	}
	if got := cfg.CarrierConfigs[0].WBStream.AccessToken; got != "local-access" {
		t.Fatalf("access token = %q, want local in-memory value", got)
	}
	if got := cfg.CarrierConfigs[0].WBStream.CookieHeader; got != "local-cookie" {
		t.Fatalf("cookie header = %q, want local in-memory value", got)
	}
	if got := cfg.TokenStore.Tokens[0].ID; got != "bootstrap-only" {
		t.Fatalf("local session mutated TokenStore: %q", got)
	}
}

func TestApplyLocalSessionRejectsInvalidScope(t *testing.T) {
	base := config.Config{Role: config.RoleClient, CarrierConfigs: []config.CarrierConfig{{ID: "wbstream.vp8", WBStream: &config.WBStreamConfig{}}}}
	for _, session := range []LocalSession{
		{Platform: "dion", AccessToken: "local-access", CookieHeader: "local-cookie"},
		{Platform: "wbstream", AccessToken: "", CookieHeader: "local-cookie"},
	} {
		cfg := base
		if err := applyLocalSession(&cfg, session); err == nil {
			t.Fatalf("expected rejected session %+v", session)
		}
	}
}
