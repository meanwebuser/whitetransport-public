package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHandlerRejectsWrongNonceWithoutCallingImporter(t *testing.T) {
	called := false
	handler := newHandler("expected", func([]byte) (importResult, error) {
		called = true
		return importResult{}, nil
	}, make(chan error, 1))
	req := httptest.NewRequest(http.MethodPost, capturePath, bytes.NewBufferString(`{}`))
	req.Header.Set("X-WT-Bridge-Nonce", "wrong")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusForbidden)
	}
	if called {
		t.Fatal("importer was called for a bad nonce")
	}
}

func TestHandlerReturnsOnlySafeImportSummary(t *testing.T) {
	handler := newHandler("expected", func(got []byte) (importResult, error) {
		if string(got) != `{"cookies":[],"secret":"fixture-secret"}` {
			t.Fatalf("body = %s", got)
		}
		return importResult{Platforms: []string{"wbstream"}, Count: 1}, nil
	}, make(chan error, 1))
	req := httptest.NewRequest(http.MethodPost, capturePath, bytes.NewBufferString(`{"cookies":[],"secret":"fixture-secret"}`))
	req.Header.Set("X-WT-Bridge-Nonce", "expected")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if body := res.Body.String(); body != "{\"platforms\":[\"wbstream\"],\"count\":1}\n" {
		t.Fatalf("unexpected response: %q", body)
	}
}

func TestHandlerAllowsExtensionPreflightWithoutImport(t *testing.T) {
	called := false
	handler := newHandler("expected", func([]byte) (importResult, error) {
		called = true
		return importResult{}, nil
	}, make(chan error, 1))
	req := httptest.NewRequest(http.MethodOptions, capturePath, nil)
	req.Header.Set("Origin", "chrome-extension://abcdefghijklmnopabcdefghijklmnop")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,x-wt-bridge-nonce")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNoContent)
	}
	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "chrome-extension://abcdefghijklmnopabcdefghijklmnop" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := res.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, OPTIONS" {
		t.Fatalf("allow methods = %q", got)
	}
	if called {
		t.Fatal("importer was called for a preflight")
	}
}

func TestHandlerRejectsOrdinaryWebOrigin(t *testing.T) {
	called := false
	handler := newHandler("expected", func([]byte) (importResult, error) {
		called = true
		return importResult{}, nil
	}, make(chan error, 1))
	req := httptest.NewRequest(http.MethodPost, capturePath, bytes.NewBufferString("{\"cookies\":[]}"))
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("X-WT-Bridge-Nonce", "expected")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusForbidden)
	}
	if called {
		t.Fatal("importer was called for an ordinary web origin")
	}
}

func TestHandlerRejectsMalformedExtensionOrigin(t *testing.T) {
	handler := newHandler("expected", func([]byte) (importResult, error) {
		t.Fatal("importer was called for a malformed extension origin")
		return importResult{}, nil
	}, make(chan error, 1))
	req := httptest.NewRequest(http.MethodPost, capturePath, bytes.NewBufferString("{}"))
	req.Header.Set("Origin", "chrome-extension://not-a-real-extension-id")
	req.Header.Set("X-WT-Bridge-Nonce", "expected")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusForbidden)
	}
}

func TestDevelopmentDefaultsRemainEphemeral(t *testing.T) {
	if defaultBridgePort != 0 {
		t.Fatalf("default bridge port = %d, want ephemeral port 0", defaultBridgePort)
	}
	if defaultBridgeNonce != "" {
		t.Fatalf("default bridge nonce = %q, want generated nonce", defaultBridgeNonce)
	}
}

func TestCommandHandlerReturnsAndConsumesUpdateRequest(t *testing.T) {
	commandFile := filepath.Join(t.TempDir(), "browser-refresh.command")
	if err := os.WriteFile(commandFile, []byte("{\"command\":\"update\",\"url\":\"https://vk.com/feed?drop=1\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithOptions("expected", func([]byte) (importResult, error) {
		return importResult{}, nil
	}, make(chan error, 1), true, commandFile)

	request := httptest.NewRequest(http.MethodGet, commandPath, nil)
	request.Header.Set("X-WT-Bridge-Nonce", "expected")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "{\"command\":\"update\",\"url\":\"https://vk.com/feed?drop=1\"}\n" {
		t.Fatalf("command response = %d %q", response.Code, response.Body.String())
	}
	if _, err := os.Stat(commandFile); !os.IsNotExist(err) {
		t.Fatalf("command file still exists: %v", err)
	}

	second := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodGet, commandPath, nil)
	secondRequest.Header.Set("X-WT-Bridge-Nonce", "expected")
	handler.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusNoContent {
		t.Fatalf("empty command status = %d, want %d", second.Code, http.StatusNoContent)
	}
}

func TestReadBrowserCommandClaimsConcurrentPollOnce(t *testing.T) {
	commandFile := filepath.Join(t.TempDir(), "browser-refresh.command")
	if err := os.WriteFile(commandFile, []byte("update"), 0o600); err != nil {
		t.Fatal(err)
	}

	const workers = 32
	start := make(chan struct{})
	results := make(chan map[string]string, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			<-start
			command, err := readBrowserCommand(commandFile)
			if err != nil {
				errors <- err
				return
			}
			results <- command
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errors)

	claimed := 0
	for command := range results {
		if command != nil && command["command"] == "update" {
			claimed++
		}
	}
	for err := range errors {
		t.Fatalf("concurrent command read failed: %v", err)
	}
	if claimed != 1 {
		t.Fatalf("claimed commands = %d, want exactly one", claimed)
	}
}
