package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
)

func TestConfigHandler_ListEmpty(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	cs := NewConfigServer(ps, ks)

	handler := cs.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/config")
	if err != nil {
		t.Fatalf("GET /api/v1/config: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty config map, got %v", result)
	}
}

func TestConfigHandler_SetAndGet(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	cs := NewConfigServer(ps, ks)

	cfg := map[string]any{
		"id":      "test.carrier",
		"enabled": true,
		"token":   "secret",
	}
	if err := cs.SetProviderConfig("test.carrier", cfg); err != nil {
		t.Fatalf("SetProviderConfig: %v", err)
	}

	handler := cs.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	t.Run("list includes test.carrier", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/v1/config")
		if err != nil {
			t.Fatalf("GET /api/v1/config: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if _, ok := result["test.carrier"]; !ok {
			t.Fatalf("expected test.carrier in config list, got %v", result)
		}
	})

	t.Run("get by id returns config", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/v1/config/test.carrier")
		if err != nil {
			t.Fatalf("GET /api/v1/config/test.carrier: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result["id"] != "test.carrier" {
			t.Fatalf("expected id test.carrier, got %v", result["id"])
		}
		if result["token"] != "secret" {
			t.Fatalf("expected token secret, got %v", result["token"])
		}
	})

	t.Run("get unknown id returns 404", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/v1/config/unknown")
		if err != nil {
			t.Fatalf("GET /api/v1/config/unknown: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", resp.StatusCode)
		}
	})
}

func TestConfigHandler_PutNewConfig(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	cs := NewConfigServer(ps, ks)

	handler := cs.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := map[string]any{
		"id":    "new.carrier",
		"token": "new-token",
	}
	data, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/config/new.carrier", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/v1/config/new.carrier: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["status"] != "updated" {
		t.Fatalf("expected status updated, got %v", result["status"])
	}

	got, err := cs.GetProviderConfig("new.carrier")
	if err != nil {
		t.Fatalf("GetProviderConfig: %v", err)
	}
	gotMap, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if gotMap["token"] != "new-token" {
		t.Fatalf("expected token new-token, got %v", gotMap["token"])
	}
}

func TestConfigHandler_MethodNotAllowed(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	cs := NewConfigServer(ps, ks)

	handler := cs.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	t.Run("POST on list", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/api/v1/config", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /api/v1/config: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", resp.StatusCode)
		}
	})

	t.Run("DELETE on by-id", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/config/test", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /api/v1/config/test: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", resp.StatusCode)
		}
	})
}

func TestConfigHandler_EmptyIDReturns400(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	cs := NewConfigServer(ps, ks)

	handler := cs.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/config/")
	if err != nil {
		t.Fatalf("GET /api/v1/config/: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
