package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
)

func TestAPIHandler_Status(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	api := NewAPI(ps, ks)

	ps.Set(&providers.Model{ID: "test.provider", Name: "test", Version: "1.0.0"})
	ks.Set(&keys.Model{ID: "test-key", ProviderID: "test.provider"})

	handler := api.Handler()

	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/status")
	if err != nil {
		t.Fatalf("GET /api/v1/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
}

func TestAPIHandler_StatusEmpty(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	api := NewAPI(ps, ks)

	handler := api.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/status")
	if err != nil {
		t.Fatalf("GET /api/v1/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIHandler_Providers(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	api := NewAPI(ps, ks)

	ps.Set(&providers.Model{ID: "vk.messages", Name: "VK Messages", Version: "1.0.0"})
	ps.Set(&providers.Model{ID: "ok.messages", Name: "OK Messages", Version: "1.0.0"})

	handler := api.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/providers")
	if err != nil {
		t.Fatalf("GET /api/v1/providers: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIHandler_ProvidersMethodNotAllowed(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	api := NewAPI(ps, ks)

	handler := api.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/providers", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/v1/providers: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestAPIHandler_Keys(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	api := NewAPI(ps, ks)

	ks.Set(&keys.Model{ID: "vk-token", ProviderID: "vk", Type: provider.KeyPermanent})

	handler := api.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/keys")
	if err != nil {
		t.Fatalf("GET /api/v1/keys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIHandler_Adapters(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	api := NewAPI(ps, ks)

	handler := api.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/adapters")
	if err != nil {
		t.Fatalf("GET /api/v1/adapters: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIHandler_StatusWithAdapters(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	api := NewAPI(ps, ks)

	api.RegisterAdapter("memory", &testAdapter{name: "memory"})

	handler := api.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/status")
	if err != nil {
		t.Fatalf("GET /api/v1/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPIHandler_NotFound(t *testing.T) {
	ps := providers.NewStore()
	ks := keys.NewStore()
	api := NewAPI(ps, ks)

	handler := api.Handler()
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/nonexistent")
	if err != nil {
		t.Fatalf("GET /api/v1/nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

type testAdapter struct {
	name string
}

func (a *testAdapter) ID() string                             { return a.name }
func (a *testAdapter) Type() provider.Type                    { return provider.TypeMessaging }
func (a *testAdapter) Category() provider.Category            { return provider.CategoryOther }
func (a *testAdapter) Version() string                        { return "1.0.0" }
func (a *testAdapter) Configure(provider.ProviderConfig) error { return nil }
func (a *testAdapter) GetSchema() provider.Schema             { return provider.Schema{} }
func (a *testAdapter) Send(_ context.Context, _ []byte) error  { return nil }
func (a *testAdapter) Receive(_ context.Context) ([]byte, error) { return nil, nil }
func (a *testAdapter) Health() provider.Health                { return provider.Health{} }
func (a *testAdapter) GetLimits() provider.Limits              { return provider.Limits{} }
func (a *testAdapter) GetMetrics() provider.Metrics            { return provider.Metrics{} }
func (a *testAdapter) UpdateMetrics(provider.Metrics)           {}
func (a *testAdapter) Load() error                              { return nil }
func (a *testAdapter) Unload() error                            { return nil }
