//go:build integration

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationTelemostCookiesExist(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}

	cookiesFile := filepath.Join(repoRoot(t), secretsDir, "yandex/yandex-cookies.json")
	requireFile(t, cookiesFile)

	data, err := os.ReadFile(cookiesFile)
	if err != nil {
		t.Fatalf("read cookies: %v", err)
	}
	var cookies []map[string]any
	if err := json.Unmarshal(data, &cookies); err != nil {
		t.Fatalf("parse cookies: %v", err)
	}
	t.Logf("Yandex cookies file: %d cookies loaded", len(cookies))
	if len(cookies) == 0 {
		t.Fatal("no cookies found")
	}

	hasSession := false
	for _, c := range cookies {
		name, _ := c["name"].(string)
		if strings.EqualFold(name, "Session_id") || strings.EqualFold(name, "yandexuid") {
			hasSession = true
			break
		}
	}
	if !hasSession {
		t.Log("warning: no Session_id or yandexuid cookie found - telemost joiner may still work")
	}
}
