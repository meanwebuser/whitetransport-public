package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const secretsDir = "secrets/production"

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("skip: %s not set", key)
	}
	return v
}

func requireFile(t *testing.T, path string) string {
	t.Helper()
	abs := path
	if !filepath.IsAbs(path) {
		abs = filepath.Join(repoRoot(t), path)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("skip: file not found: %s", abs)
	}
	return abs
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	testsDir := filepath.Dir(file)
	coreGo := filepath.Dir(testsDir)
	coreDir := filepath.Dir(coreGo)
	return filepath.Dir(coreDir)
}

func loadVKToken(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("WT_VK_TOKEN"); v != "" {
		return v
	}
	path := filepath.Join(repoRoot(t), secretsDir, "vk-tokens.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("skip: cannot read vk-tokens.json: %v", err)
	}
	var doc struct {
		Accounts []struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Skipf("skip: cannot parse vk-tokens.json: %v", err)
	}
	accountID := os.Getenv("WT_VK_ACCOUNT_ID")
	for _, a := range doc.Accounts {
		if a.Token != "" && (accountID == "" || a.ID == accountID) {
			return a.Token
		}
	}
	t.Skip("skip: no matching VK account in vk-tokens.json")
	return ""
}

func loadOKToken(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("WT_OK_TOKEN"); v != "" {
		return v
	}
	path := filepath.Join(repoRoot(t), secretsDir, "ok-tokens.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("skip: cannot read ok-tokens.json: %v", err)
	}
	var doc struct {
		OKMessages struct {
			Token string `json:"token"`
		} `json:"ok_messages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Skipf("skip: cannot parse ok-tokens.json: %v", err)
	}
	if doc.OKMessages.Token == "" {
		t.Skip("skip: empty OK token")
	}
	return doc.OKMessages.Token
}

func loadDIONAccessToken(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("WT_DION_ACCESS_TOKEN"); v != "" {
		return v
	}
	path := filepath.Join(repoRoot(t), secretsDir, "dion-tokens.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("skip: cannot read dion-tokens.json: %v", err)
	}
	var doc struct {
		Dion struct {
			AccessToken string `json:"access_token"`
		} `json:"dion"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Skipf("skip: cannot parse dion-tokens.json: %v", err)
	}
	if doc.Dion.AccessToken == "" {
		t.Skip("skip: empty DION access token")
	}
	return doc.Dion.AccessToken
}
