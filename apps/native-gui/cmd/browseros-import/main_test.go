package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerRejectsWrongNonceWithoutCallingImporter(t *testing.T) {
	called := false
	handler := newHandler("expected", func([]byte) (importResult, error) {
		called = true
		return importResult{}, nil
	}, make(chan error, 1))
	req := httptest.NewRequest(http.MethodPost, capturePath, bytes.NewBufferString(`{}`))
	req.Header.Set("X-WT-Capture-Nonce", "wrong")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusForbidden)
	}
	if called {
		t.Fatal("importer was called for a bad nonce")
	}
}

func TestNormalizeScheduledExportAddsDionSource(t *testing.T) {
	body, err := normalizeScheduledExport([]byte(`{"cookies":{"vc-access-token":"fixture"}}`), "dion.vc")
	if err != nil {
		t.Fatalf("normalizeScheduledExport: %v", err)
	}
	var export map[string]any
	if err := json.Unmarshal(body, &export); err != nil {
		t.Fatalf("decode normalized export: %v", err)
	}
	source, ok := export["source"].(map[string]any)
	if !ok || source["host"] != "dion.vc" || source["url"] != "https://dion.vc/" {
		t.Fatalf("source = %#v, want DION BrowserOS source", export["source"])
	}
	if export["version"] != float64(1) {
		t.Fatalf("version = %#v, want 1", export["version"])
	}
	cookies, ok := export["cookies"].([]any)
	if !ok || len(cookies) != 1 {
		t.Fatalf("cookies = %#v, want one normalized BrowserOS cookie", export["cookies"])
	}
}

func TestNormalizeScheduledExportConvertsWBStreamLocalStorageMap(t *testing.T) {
	body, err := normalizeScheduledExport([]byte(`{"cookies":{"x_wbaas_token":"fixture-cookie"},"localStorage":{"wb_auth_auth_slice":"{\"accessToken\":\"fixture-token\"}"}}`), "stream.wb.ru")
	if err != nil {
		t.Fatalf("normalizeScheduledExport: %v", err)
	}
	var export map[string]any
	if err := json.Unmarshal(body, &export); err != nil {
		t.Fatalf("decode normalized export: %v", err)
	}
	storage, ok := export["localStorage"].([]any)
	if !ok || len(storage) != 1 {
		t.Fatalf("localStorage = %#v, want one normalized BrowserOS entry", export["localStorage"])
	}
	entry, ok := storage[0].(map[string]any)
	if !ok || entry["key"] != "wb_auth_auth_slice" || entry["value"] != `{"accessToken":"fixture-token"}` {
		t.Fatalf("localStorage entry = %#v, want WBStream auth slice", storage[0])
	}
}

func TestHandlerReturnsOnlySafeImportSummary(t *testing.T) {
	handler := newHandler("expected", func(got []byte) (importResult, error) {
		if string(got) != `{"cookies":[]}` {
			t.Fatalf("body = %s", got)
		}
		return importResult{Platforms: []string{"telemost", "vk"}, Count: 2}, nil
	}, make(chan error, 1))
	req := httptest.NewRequest(http.MethodPost, capturePath, bytes.NewBufferString(`{"cookies":[]}`))
	req.Header.Set("X-WT-Capture-Nonce", "expected")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if body := res.Body.String(); body != "{\"platforms\":[\"telemost\",\"vk\"],\"count\":2}\n" {
		t.Fatalf("unexpected response: %q", body)
	}
}
