package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
)

func TestDefaultTokenStorePath(t *testing.T) {
	got := DefaultTokenStorePath("/repo")
	want := filepath.Join("/repo", "secrets", "token-store.json")
	if got != want {
		t.Errorf("DefaultTokenStorePath = %q, want %q", got, want)
	}
}

func TestTestOverridePath(t *testing.T) {
	got := TestOverridePath("/repo")
	want := filepath.Join("/repo", "secrets", "token-store.test.json")
	if got != want {
		t.Errorf("TestOverridePath = %q, want %q", got, want)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTokenStoreBasic(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0755); err != nil {
		t.Fatal(err)
	}

	store := map[string]any{
		"tokens": []map[string]any{
			{"id": "vk-1", "platform": "vk", "kind": "api_key", "lifecycle": "embedded", "value": "tok-abc"},
			{"id": "ok-1", "platform": "ok", "kind": "api_key", "lifecycle": "embedded", "value": "ok-tok"},
		},
		"bindings": []map[string]any{
			{"token_id": "vk-1", "platform": "vk", "connection_type": "messages", "channel_id": "123", "role": "discovery", "priority": 10, "enabled": true},
		},
	}
	writeJSON(t, filepath.Join(secretsDir, "token-store.json"), store)

	cfg, err := LoadTokenStore(dir)
	if err != nil {
		t.Fatalf("LoadTokenStore: %v", err)
	}
	if len(cfg.Tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(cfg.Tokens))
	}
	if cfg.Tokens[0].ID != "vk-1" {
		t.Errorf("token[0].ID = %q, want vk-1", cfg.Tokens[0].ID)
	}
	if len(cfg.Bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(cfg.Bindings))
	}
}

func TestLoadTokenStoreMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadTokenStore(dir)
	if err == nil {
		t.Fatal("expected error for missing token-store.json")
	}
}

func TestLoadTokenStoreInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "token-store.json"), []byte("{invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadTokenStore(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestMergeOverrides(t *testing.T) {
	base := &config.TokenStoreConfig{
		Tokens: []config.TokenEntry{
			{ID: "vk-1", Value: "old-token"},
			{ID: "ok-1", Value: "ok-token"},
		},
		Bindings: []config.BindingEntry{
			{TokenID: "vk-1", Platform: "vk", ConnectionType: "messages", Role: "discovery", Priority: 10},
		},
	}
	override := &config.TokenStoreConfig{
		Tokens: []config.TokenEntry{
			{ID: "vk-1", Value: "new-token"},       // override existing
			{ID: "new-1", Value: "brand-new-token"}, // add new
		},
		Bindings: []config.BindingEntry{
			{TokenID: "vk-1", Platform: "vk", ConnectionType: "messages", Role: "discovery", Priority: 20}, // override
			{TokenID: "new-1", Platform: "new", ConnectionType: "api", Role: "egress", Priority: 5},        // add new
		},
	}

	mergeOverrides(base, override)

	if len(base.Tokens) != 3 {
		t.Fatalf("expected 3 tokens after merge, got %d", len(base.Tokens))
	}
	if base.Tokens[0].Value != "new-token" {
		t.Errorf("vk-1 token not overridden: got %q", base.Tokens[0].Value)
	}
	if base.Tokens[2].ID != "new-1" {
		t.Errorf("new token not appended: got %q", base.Tokens[2].ID)
	}

	if len(base.Bindings) != 2 {
		t.Fatalf("expected 2 bindings after merge, got %d", len(base.Bindings))
	}
	if base.Bindings[0].Priority != 20 {
		t.Errorf("binding not overridden: priority = %d, want 20", base.Bindings[0].Priority)
	}
}

func TestLoadTokenStoreWithOverride(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0755); err != nil {
		t.Fatal(err)
	}

	base := map[string]any{
		"tokens":   []map[string]any{{"id": "vk-1", "platform": "vk", "kind": "api_key", "value": "base-tok"}},
		"bindings": []map[string]any{},
	}
	writeJSON(t, filepath.Join(secretsDir, "token-store.json"), base)

	override := map[string]any{
		"tokens":   []map[string]any{{"id": "vk-1", "platform": "vk", "kind": "api_key", "value": "override-tok"}},
		"bindings": []map[string]any{},
	}
	writeJSON(t, filepath.Join(secretsDir, "token-store.test.json"), override)

	cfg, err := LoadTokenStore(dir)
	if err != nil {
		t.Fatalf("LoadTokenStore: %v", err)
	}
	if cfg.Tokens[0].Value != "override-tok" {
		t.Errorf("override not applied: got %q, want override-tok", cfg.Tokens[0].Value)
	}
}

func TestRepoRoot(t *testing.T) {
	root := RepoRoot()
	if root == "" {
		t.Skip("RepoRoot returned empty (not running from within monorepo)")
	}
	// Verify it found the actual monorepo root
	if _, err := os.Stat(filepath.Join(root, "core", "go", "go.mod")); err != nil {
		t.Errorf("RepoRoot %q does not contain core/go/go.mod", root)
	}
}
