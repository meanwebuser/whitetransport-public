package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/keys"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/providers"
)

type API struct {
	mu       sync.RWMutex
	providers *providers.Store
	keys     *keys.Store
	adapters map[string]provider.Provider
	server   *http.Server
}

type StatusResponse struct {
	Uptime    string            `json:"uptime"`
	Version   string            `json:"version"`
	Providers int               `json:"providers"`
	Keys      int               `json:"keys"`
	Adapters  []string          `json:"active_adapters"`
	Health    map[string]string `json:"health"`
}

func NewAPI(providerStore *providers.Store, keyStore *keys.Store) *API {
	return &API{
		providers: providerStore,
		keys:     keyStore,
		adapters: make(map[string]provider.Provider),
	}
}

func (a *API) RegisterAdapter(name string, adapter provider.Provider) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.adapters[name] = adapter
}

func (a *API) Handler() http.Handler {
	a.mu.RLock()
	defer a.mu.RUnlock()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", a.handleStatus)
	mux.HandleFunc("/api/v1/providers", a.handleProviders)
	mux.HandleFunc("/api/v1/keys", a.handleKeys)
	mux.HandleFunc("/api/v1/adapters", a.handleAdapters)
	return mux
}

func (a *API) Start(listenAddr string) error {
	a.server = &http.Server{
		Addr:    listenAddr,
		Handler: a.Handler(),
	}

	return a.server.ListenAndServe()
}

func (a *API) Stop(ctx context.Context) error {
	if a.server != nil {
		return a.server.Shutdown(ctx)
	}
	return nil
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	a.mu.RLock()
	adapterNames := make([]string, 0, len(a.adapters))
	health := make(map[string]string)
	for name, adapter := range a.adapters {
		adapterNames = append(adapterNames, name)
		h := adapter.Health()
		if h.ErrorCount > 0 {
			health[name] = "degraded"
		} else {
			health[name] = "healthy"
		}
	}
	providerCount := len(a.providers.List())
	keyCount := len(a.keys.List())
	a.mu.RUnlock()

	resp := StatusResponse{
		Uptime:    time.Now().UTC().Format(time.RFC3339),
		Version:   "1.0.0",
		Providers: providerCount,
		Keys:      keyCount,
		Adapters:  adapterNames,
		Health:    health,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		providers := a.providers.List()
		writeJSON(w, http.StatusOK, providers)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *API) handleKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		allKeys := a.keys.List()
		// Redact raw token values — never expose secrets to the admin UI.
		type keyResponse struct {
			*keys.Model
			Token        string `json:"-"` // hide raw value
			TokenPreview string `json:"token_preview"`
		}
		out := make([]keyResponse, len(allKeys))
		for i, k := range allKeys {
			out[i] = keyResponse{
				Model:        k,
				TokenPreview: redactToken(k.Token),
			}
		}
		writeJSON(w, http.StatusOK, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// redactToken returns a masked preview of a token string.
func redactToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

func (a *API) handleAdapters(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	switch r.Method {
	case http.MethodGet:
		adapters := make(map[string]provider.Schema)
		for name, adapter := range a.adapters {
			adapters[name] = adapter.GetSchema()
		}
		writeJSON(w, http.StatusOK, adapters)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
