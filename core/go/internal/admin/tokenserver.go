package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

type TokenServer struct {
	store *tokens.Store
}

func NewTokenServer(store *tokens.Store) *TokenServer {
	return &TokenServer{store: store}
}

func (s *TokenServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/tokens", s.handleTokens)
	mux.HandleFunc("/api/v1/tokens/refresh", s.handleRefresh)
	mux.HandleFunc("/api/v1/tokens/", s.handleTokenByID)
	mux.HandleFunc("/api/v1/bindings", s.handleBindings)
	return mux
}

// GET/POST /api/v1/tokens
func (s *TokenServer) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snap := s.store.Snapshot()
		writeJSON(w, http.StatusOK, snap.Tokens)
	case http.MethodPost:
		var req struct {
			ID                string            `json:"id"`
			Platform          string            `json:"platform"`
			Kind              string            `json:"kind"`
			Lifecycle         string            `json:"lifecycle"`
			Value             string            `json:"value"`
			Parts             map[string]string `json:"parts,omitempty"`
			CanCreateChannels bool              `json:"can_create_channels"`
			Tags              map[string]string `json:"tags,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode: %v", err)})
			return
		}
		if req.ID == "" || req.Platform == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and platform are required"})
			return
		}
		if _, exists := s.store.Get(req.ID); exists {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "token already exists"})
			return
		}
		tok := &tokens.Token{
			ID:                req.ID,
			Platform:          req.Platform,
			Lifecycle:         tokens.Lifecycle(req.Lifecycle),
			Status:            tokens.StatusActive,
			Value:             req.Value,
			Parts:             req.Parts,
			CanCreateChannels: req.CanCreateChannels,
			Tags:              req.Tags,
			CreatedAt:         time.Now(),
			LastUsed:          time.Now(),
		}
		switch tokens.TokenKind(req.Kind) {
		case tokens.KindAPIKey, tokens.KindJWT, tokens.KindCookies, tokens.KindLocalStorage,
			tokens.KindOAuthToken, tokens.KindSymmetricKey, tokens.KindComposite:
			tok.Kind = tokens.TokenKind(req.Kind)
		default:
			tok.Kind = tokens.KindAPIKey
		}
		s.store.Set(tok)
		writeJSON(w, http.StatusCreated, map[string]string{"id": req.ID, "status": "created"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// POST /api/v1/tokens/refresh
// Admin-push token refresh: resets health state and optionally updates the
// token value. Accepts a single token_id or a list of token_ids.
func (s *TokenServer) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		TokenID  string   `json:"token_id"`
		TokenIDs []string `json:"token_ids,omitempty"`
		Value    string   `json:"value,omitempty"`    // optional new credential value
		Parts    map[string]string `json:"parts,omitempty"` // optional new composite parts
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode: %v", err)})
		return
	}

	// Build the list of IDs to refresh.
	ids := req.TokenIDs
	if req.TokenID != "" {
		ids = append([]string{req.TokenID}, ids...)
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token_id or token_ids is required"})
		return
	}

	type result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(ids))
	for _, id := range ids {
		t, ok := s.store.Get(id)
		if !ok {
			results = append(results, result{ID: id, Status: "not_found", Error: "token not found"})
			continue
		}
		// Reset health state.
		t.Health.RateLimitHit = false
		t.Health.RateLimitReset = nil
		t.Health.QuotaExhausted = false
		t.Health.LastError = ""
		t.Health.ConsecutiveFails = 0
		// Optionally update credential value.
		if req.Value != "" {
			t.Value = req.Value
		}
		if req.Parts != nil {
			t.Parts = req.Parts
		}
		// Reactivate if it was limited or expired.
		if t.Status == tokens.StatusLimited || t.Status == tokens.StatusExpired {
			t.Status = tokens.StatusActive
		}
		s.store.Set(t)
		results = append(results, result{ID: id, Status: "refreshed"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// GET/PUT/DELETE /api/v1/tokens/{id}
// POST /api/v1/tokens/{id}/health
// POST /api/v1/tokens/{id}/status
func (s *TokenServer) handleTokenByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/")
	path = strings.TrimRight(path, "/")

	tokenID, subPath, _ := strings.Cut(path, "/")

	if tokenID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token id is required"})
		return
	}

	if subPath == "health" {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var event tokens.TokenHealthEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode: %v", err)})
			return
		}
		event.TokenID = tokenID
		s.store.ReportHealth(event)
		writeJSON(w, http.StatusOK, map[string]string{"status": "reported"})
		return
	}

	if subPath == "status" {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		t, ok := s.store.Get(tokenID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
			return
		}
		var req struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode: %v", err)})
			return
		}
		switch tokens.Status(req.Status) {
		case tokens.StatusActive, tokens.StatusLimited, tokens.StatusExpired, tokens.StatusRevoked:
			t.Status = tokens.Status(req.Status)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("invalid status %q: must be active, limited, expired, or revoked", req.Status),
			})
			return
		}
		s.store.Set(t)
		writeJSON(w, http.StatusOK, map[string]string{"id": tokenID, "status": string(t.Status)})
		return
	}

	switch r.Method {
	case http.MethodGet:
		t, ok := s.store.Get(tokenID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
			return
		}
		writeJSON(w, http.StatusOK, snapshotView(t))
	case http.MethodPut:
		t, ok := s.store.Get(tokenID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
			return
		}
		var req struct {
			Value             string            `json:"value"`
			Platform          string            `json:"platform"`
			Kind              string            `json:"kind"`
			Lifecycle         string            `json:"lifecycle"`
			Parts             map[string]string `json:"parts,omitempty"`
			CanCreateChannels *bool             `json:"can_create_channels,omitempty"`
			Tags              map[string]string `json:"tags,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode: %v", err)})
			return
		}
		if req.Value != "" {
			t.Value = req.Value
		}
		if req.Platform != "" {
			t.Platform = req.Platform
		}
		if req.Kind != "" {
			t.Kind = tokens.TokenKind(req.Kind)
		}
		if req.Lifecycle != "" {
			t.Lifecycle = tokens.Lifecycle(req.Lifecycle)
		}
		if req.Parts != nil {
			t.Parts = req.Parts
		}
		if req.CanCreateChannels != nil {
			t.CanCreateChannels = *req.CanCreateChannels
		}
		if req.Tags != nil {
			t.Tags = req.Tags
		}
		s.store.Set(t)
		writeJSON(w, http.StatusOK, map[string]string{"id": tokenID, "status": "updated"})
	case http.MethodDelete:
		// Soft-delete: mark as revoked before removing from the store.
		if t, ok := s.store.Get(tokenID); ok {
			t.Status = tokens.StatusRevoked
			s.store.Set(t)
		}
		s.store.Delete(tokenID)
		writeJSON(w, http.StatusOK, map[string]string{"id": tokenID, "status": "revoked"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// GET /api/v1/bindings
func (s *TokenServer) handleBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, s.store.Bindings())
}

func snapshotView(t *tokens.Token) tokens.TokenSnapshotView {
	return tokens.TokenSnapshotView{
		ID:                t.ID,
		Platform:          t.Platform,
		Kind:              t.Kind,
		Lifecycle:         t.Lifecycle,
		Status:            t.Status,
		MaskedValue:       t.MaskedValue(),
		CanCreateChannels: t.CanCreateChannels,
		ExpiresAt:         t.ExpiresAt,
		LastUsed:          t.LastUsed,
		Tags:              t.Tags,
		Health:            t.Health,
		Usage:             t.Usage,
	}
}
