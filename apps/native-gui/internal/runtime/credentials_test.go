package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientCredentialsCRUD(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	creds, err := LoadClientCredentials()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("expected 0 creds, got %d", len(creds))
	}

	cred := ClientCredential{
		Platform: "wbstream",
		Label:    "test",
		Token:    "vk1.a.test-token",
	}
	creds, err = AddClientCredential(cred)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 cred, got %d", len(creds))
	}
	if creds[0].ID == "" {
		t.Fatal("expected non-empty ID")
	}

	loaded, err := LoadClientCredentials()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Token != "vk1.a.test-token" {
		t.Fatalf("unexpected loaded creds: %+v", loaded)
	}

	id := creds[0].ID
	creds, err = RemoveClientCredential(id)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("expected 0 after remove, got %d", len(creds))
	}
}

func TestClientCredentialsUnsupportedPlatform(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, err := AddClientCredential(ClientCredential{Platform: "unknown"})
	if err == nil {
		t.Fatal("expected error for unsupported platform")
	}
}

func TestReplaceClientCredentialsForPlatformsRemovesStaleRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, credential := range []ClientCredential{
		{ID: "wb-old", Platform: "wbstream", Token: "old-wb-token"},
		{ID: "vk-keep", Platform: "vk", Token: "vk-test-token"},
	} {
		if _, err := AddClientCredential(credential); err != nil {
			t.Fatalf("seed credential %q: %v", credential.ID, err)
		}
	}

	updated, err := ReplaceClientCredentialsForPlatforms([]ClientCredential{
		{ID: "wb-new", Platform: "wbstream", Token: "new-wb-token", Cookie: "session=test"},
		{ID: "dion-new", Platform: "dion", Token: "dion-test-token"},
	})
	if err != nil {
		t.Fatalf("replace imported credentials: %v", err)
	}
	if len(updated) != 3 {
		t.Fatalf("credential count = %d, want 3", len(updated))
	}

	byID := make(map[string]ClientCredential, len(updated))
	for _, credential := range updated {
		byID[credential.ID] = credential
	}
	if _, stale := byID["wb-old"]; stale {
		t.Fatal("stale WBStream credential remained after import replacement")
	}
	if got := byID["wb-new"].Token; got != "new-wb-token" {
		t.Fatalf("replacement WBStream token = %q, want new value", got)
	}
	if _, kept := byID["vk-keep"]; !kept {
		t.Fatal("unrelated platform credential was removed")
	}
	if _, imported := byID["dion-new"]; !imported {
		t.Fatal("second imported platform credential was not stored")
	}
}

func TestHasClientRoomCredentials(t *testing.T) {
	creds := []ClientCredential{
		{Platform: "vk", Token: "some-token"},
	}
	if HasClientRoomCredentials(creds) {
		t.Fatal("vk should not enable room creation")
	}

	creds = append(creds, ClientCredential{Platform: "wbstream", Token: "wb-token"})
	if !HasClientRoomCredentials(creds) {
		t.Fatal("wbstream with token should enable room creation")
	}

	creds = []ClientCredential{
		{Platform: "dion", Cookie: "some-cookie"},
	}
	if !HasClientRoomCredentials(creds) {
		t.Fatal("dion with cookie should enable room creation")
	}
}

func TestClientCredentialsFilePermissions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, err := AddClientCredential(ClientCredential{Platform: "telemost", Token: "test"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	path, err := ClientTokensPath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 permissions, got %v", info.Mode().Perm())
	}

	dir := filepath.Dir(path)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("expected 0700 dir permissions, got %v", dirInfo.Mode().Perm())
	}
}

func TestSummarizeMasksSecrets(t *testing.T) {
	creds := []ClientCredential{
		{
			ID:        "wb-1",
			Platform:  "wbstream",
			Label:     "main",
			Token:     "secret-token",
			Cookie:    "secret-cookie",
			CreatedAt: time.Now().UTC(),
		},
	}
	summaries := SummarizeClientCredentials(creds)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	s := summaries[0]
	if !s.HasToken {
		t.Error("HasToken should be true")
	}
	if !s.HasCookie {
		t.Error("HasCookie should be true")
	}
}

func TestApplyClientCredentialOverrides(t *testing.T) {
	baseWBConfig := map[string]any{
		"id":       "wbstream.vp8",
		"endpoint": map[string]any{"id": "wb-egress", "address": "*"},
		"wbstream_legacy": map[string]any{
			"access_token":  "old-node-token",
			"cookie_header": "old-cookie",
		},
	}
	rawBase, _ := json.Marshal(baseWBConfig)
	carrierIDs := []string{"wbstream.vp8"}
	configs := []json.RawMessage{rawBase}

	creds := []ClientCredential{
		{Platform: "wbstream", Token: "client-personal-token", Cookie: "client-personal-cookie"},
	}

	outIDs, outConfigs := applyClientCredentialOverrides(carrierIDs, configs, creds)
	if len(outIDs) != 1 || len(outConfigs) != 1 {
		t.Fatalf("expected 1 carrier, got %d", len(outConfigs))
	}

	var patched map[string]any
	if err := json.Unmarshal(outConfigs[0], &patched); err != nil {
		t.Fatalf("unmarshal patched: %v", err)
	}
	wbl, _ := patched["wbstream_legacy"].(map[string]any)
	if wbl == nil {
		t.Fatal("wbstream_legacy missing")
	}
	if wbl["access_token"] != "client-personal-token" {
		t.Errorf("access_token = %v, want client-personal-token", wbl["access_token"])
	}
	if wbl["cookie_header"] != "client-personal-cookie" {
		t.Errorf("cookie_header = %v, want client-personal-cookie", wbl["cookie_header"])
	}
}

func TestOverrideCarrierConfigPreservesOtherFields(t *testing.T) {
	base := map[string]any{
		"id":        "wbstream.vp8",
		"token_ref": "wb-ref-1",
		"wbstream_legacy": map[string]any{
			"access_token":       "old",
			"local_storage_file": "/path/to/file",
		},
		"endpoint": map[string]any{"id": "ep1", "address": "*"},
	}
	raw, _ := json.Marshal(base)

	patched := overrideCarrierConfig(raw, map[string]any{
		"wbstream_legacy": map[string]string{
			"access_token": "new-token",
		},
	})

	var result map[string]any
	if err := json.Unmarshal(patched, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["id"] != "wbstream.vp8" {
		t.Error("id field should be preserved")
	}
	if result["token_ref"] != "wb-ref-1" {
		t.Error("token_ref should be preserved")
	}
	wbl, _ := result["wbstream_legacy"].(map[string]any)
	if wbl["local_storage_file"] != "/path/to/file" {
		t.Error("local_storage_file should be preserved")
	}
	if wbl["access_token"] != "new-token" {
		t.Error("access_token should be overridden")
	}
}
