package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWBStreamBrowserExport(t *testing.T) {
	// Create test export file matching the format you provided
	exportData := map[string]interface{}{
		"version":    1,
		"exportedAt": "2026-06-29T07:14:21.538Z",
		"source": map[string]string{
			"url":  "https://stream.wb.ru/room/019f123a-97bb-7aab-8640-adfa937b050e",
			"host": "stream.wb.ru",
		},
		"selectedTypes": []string{"cookies", "localStorage", "sessionStorage"},
		"cookies": []map[string]interface{}{
			{
				"name":           "_wbauid",
				"value":          "synthetic-device-id",
				"domain":         ".stream.wb.ru",
				"path":           "/",
				"secure":         false,
				"httpOnly":       false,
				"expirationDate": float64(1812310136),
				"storeId":        "0",
			},
			{
				"name":           "x_wbaas_token",
				"value":          "synthetic-cookie-token",
				"domain":         "stream.wb.ru",
				"path":           "/",
				"secure":         true,
				"httpOnly":       false,
				"sameSite":       "no_restriction",
				"expirationDate": float64(1783926759.060085),
				"storeId":        "0",
			},
			{
				"name":           "wbx-validation-key",
				"value":          "synthetic-validation-key",
				"domain":         ".wb.ru",
				"path":           "/",
				"secure":         true,
				"httpOnly":       true,
				"sameSite":       "lax",
				"expirationDate": float64(1785309235.921142),
				"storeId":        "0",
			},
		},
		"localStorage": []map[string]string{
			{
				"key":   "wb_auth_auth_slice",
				"value": `{"authType":"wb","accessToken":"synthetic-access-token","phoneToken":"synthetic-phone-token","phone":"+70000000000"}`,
			},
		},
		"sessionStorage": []map[string]string{},
	}

	// Write to temp file
	tmpDir := t.TempDir()
	exportFile := filepath.Join(tmpDir, "test-wb-export.json")

	data, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	if err := os.WriteFile(exportFile, data, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Parse the export
	creds, err := ParseBrowserExport(exportFile)
	if err != nil {
		t.Fatalf("ParseBrowserExport failed: %v", err)
	}

	// Verify results
	if len(creds) != 1 {
		t.Fatalf("Expected 1 credential, got %d", len(creds))
	}

	cred := creds[0]
	if cred.Platform != "wbstream" {
		t.Errorf("Expected platform wbstream, got %s", cred.Platform)
	}

	if cred.Token == "" {
		t.Error("Expected access token to be extracted")
	}

	if cred.Cookie == "" {
		t.Error("Expected cookie header to be extracted")
	}

	// Verify cookie header contains the token
	if cred.Cookie != "_wbauid=synthetic-device-id; x_wbaas_token=synthetic-cookie-token; wbx-validation-key=synthetic-validation-key" {
		t.Errorf("Unexpected cookie header: %s", cred.Cookie)
	}

	t.Logf("Successfully parsed WBStream export for platform %s", cred.Platform)
}

func TestParseDionBrowserExport(t *testing.T) {
	exportData := map[string]interface{}{
		"version":    1,
		"exportedAt": "2026-06-29T07:14:21.538Z",
		"source": map[string]string{
			"url":  "https://dion.ru/video",
			"host": "dion.ru",
		},
		"selectedTypes": []string{"cookies", "localStorage"},
		"cookies": []map[string]interface{}{
			{
				"name":   "auth_token",
				"value":  "dion-test-token-123",
				"domain": ".dion.ru",
				"path":   "/",
			},
		},
		"localStorage": []map[string]string{
			{
				"key":   "dion_auth",
				"value": `{"access_token":"synthetic-dion-access","refresh_token":"synthetic-dion-refresh"}`,
			},
		},
	}

	tmpDir := t.TempDir()
	exportFile := filepath.Join(tmpDir, "test-dion-export.json")

	data, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	if err := os.WriteFile(exportFile, data, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	creds, err := ParseBrowserExport(exportFile)
	if err != nil {
		t.Fatalf("ParseBrowserExport failed: %v", err)
	}

	if len(creds) != 1 {
		t.Fatalf("Expected 1 credential, got %d", len(creds))
	}

	cred := creds[0]
	if cred.Platform != "dion" {
		t.Errorf("Expected platform dion, got %s", cred.Platform)
	}

	if cred.Token != "dion-test-token-123" {
		t.Errorf("Expected access token from cookie (priority over localStorage for DION), got %s", cred.Token)
	}

	t.Logf("✓ Successfully parsed DION export")
}

func TestParseDionBrowserExportReadsDionAuthStorageKey(t *testing.T) {
	exportData := map[string]interface{}{
		"version": 1,
		"source": map[string]string{"url": "https://dion.gov.ru/", "host": "dion.gov.ru"},
		"localStorage": []map[string]string{{
			"key": "dion_auth",
			"value": `{"access_token":"synthetic-dion-access","refresh_token":"synthetic-dion-refresh"}`,
		}},
	}
	path := filepath.Join(t.TempDir(), "dion-auth.json")
	raw, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	creds, err := ParseBrowserExport(path)
	if err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(creds) != 1 || creds[0].Token != "synthetic-dion-access" || !strings.Contains(creds[0].Extra, "synthetic-dion-refresh") {
		t.Fatalf("unexpected DION credential shape: count=%d has_token=%t has_refresh=%t", len(creds), len(creds) == 1 && creds[0].Token != "", len(creds) == 1 && creds[0].Extra != "")
	}
}

func TestParseDionBrowserExportPrefersAccessCookieAndRetainsRefreshCookie(t *testing.T) {
	exportData := map[string]interface{}{
		"version": 1,
		"source":  map[string]string{"url": "https://dion.gov.ru/", "host": "dion.gov.ru"},
		"cookies": []map[string]string{
			{"name": "vc-refresh-token", "value": "synthetic-refresh", "domain": ".dion.gov.ru", "path": "/"},
			{"name": "vc-access-token", "value": "synthetic-access", "domain": ".dion.gov.ru", "path": "/"},
		},
	}
	path := filepath.Join(t.TempDir(), "dion-cookies.json")
	raw, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	creds, err := ParseBrowserExport(path)
	if err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(creds) != 1 || creds[0].Token != "synthetic-access" || !strings.Contains(creds[0].Extra, "synthetic-refresh") {
		t.Fatalf("unexpected DION cookie credential: count=%d token=%q extra_has_refresh=%t", len(creds), func() string { if len(creds) == 1 { return creds[0].Token }; return "" }(), len(creds) == 1 && strings.Contains(creds[0].Extra, "synthetic-refresh"))
	}
}

func TestParseTelemostBrowserExportRetainsMeetingLink(t *testing.T) {
	exportData := map[string]interface{}{
		"version": 1,
		"source": map[string]string{
			"url":  "https://telemost.yandex.ru/j/synthetic-room",
			"host": "telemost.yandex.ru",
		},
		"cookies": []map[string]interface{}{{
			"name": "Session_id", "value": "synthetic-session", "domain": ".yandex.ru", "path": "/",
		}},
	}
	path := filepath.Join(t.TempDir(), "telemost.json")
	raw, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	creds, err := ParseBrowserExport(path)
	if err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("credential count = %d, want 1", len(creds))
	}
	if got, want := creds[0].Extra, "https://telemost.yandex.ru/j/synthetic-room"; got != want {
		t.Fatalf("join link = %q, want %q", got, want)
	}
}

func TestParseUnsupportedHost(t *testing.T) {
	exportData := map[string]interface{}{
		"version":    1,
		"exportedAt": "2026-06-29T07:14:21.538Z",
		"source": map[string]string{
			"url":  "https://example.com/page",
			"host": "example.com",
		},
		"selectedTypes":  []string{"cookies"},
		"cookies":        []map[string]interface{}{},
		"localStorage":   []map[string]string{},
		"sessionStorage": []map[string]string{},
	}

	tmpDir := t.TempDir()
	exportFile := filepath.Join(tmpDir, "test-unsupported.json")

	data, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	if err := os.WriteFile(exportFile, data, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err = ParseBrowserExport(exportFile)
	if err == nil {
		t.Fatal("Expected error for unsupported host, got nil")
	}

	t.Logf("✓ Correctly rejected unsupported host: %v", err)
}
