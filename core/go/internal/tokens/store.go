package tokens

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Store is the centralized in-memory token registry with usage monitoring
// and health tracking. It resolves tokens by (Platform, ConnectionType,
// ChannelID) triples with wildcard and fallback support.
type Store struct {
	mu       sync.RWMutex
	tokens   map[string]*Token      // tokenID -> Token
	bindings []Binding              // ordered by priority at resolution time
	usageLog map[string]*TokenUsage // composite key -> usage
	watchers []func(TokenHealthEvent)
}

// NewStore creates an empty token store.
func NewStore() *Store {
	return &Store{
		tokens:   make(map[string]*Token),
		usageLog: make(map[string]*TokenUsage),
	}
}

// ── Token CRUD ──────────────────────────────────────────────────────────

// Set registers or updates a token in the store.
func (s *Store) Set(t *Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[t.ID] = t
}

// Get returns a token by ID.
func (s *Store) Get(id string) (*Token, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tokens[id]
	return t, ok
}

// Delete removes a token by ID.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, id)
}

// List returns all tokens.
func (s *Store) List() []*Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Token, 0, len(s.tokens))
	for _, t := range s.tokens {
		out = append(out, t)
	}
	return out
}

// ListByPlatform returns tokens filtered by platform.
func (s *Store) ListByPlatform(platform string) []*Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Token
	for _, t := range s.tokens {
		if t.Platform == platform {
			out = append(out, t)
		}
	}
	return out
}

// ── Binding management ──────────────────────────────────────────────────

// AddBinding registers a token-to-carrier binding.
func (s *Store) AddBinding(b Binding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = append(s.bindings, b)
}

// SetBindings replaces all bindings.
func (s *Store) SetBindings(bindings []Binding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = make([]Binding, len(bindings))
	copy(s.bindings, bindings)
}

// Bindings returns all registered bindings.
func (s *Store) Bindings() []Binding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Binding, len(s.bindings))
	copy(out, s.bindings)
	return out
}

// ── Resolution ──────────────────────────────────────────────────────────

// Resolve finds the best token(s) for a (platform, connectionType, channelID)
// triple. Returns an ordered list: primary first, fallbacks after.
//
// Algorithm:
//  1. Filter bindings: platform match, connectionType match (or "*"), channelID match (or "*")
//  2. Filter: binding.Enabled, token active (not revoked/expired)
//  3. Filter: token not rate-limited (unless all are rate-limited)
//  4. Sort: priority ASC, successRate DESC, lastUsed ASC
func (s *Store) Resolve(platform, connectionType, channelID string) ([]*Token, error) {
	return s.resolve(platform, connectionType, channelID, "")
}

// ResolveOneForRole returns the best active token bound to an exact role.
// Unlike Resolve, role matching is strict so a client cannot silently select a
// node credential when both principals share the same carrier and channel.
func (s *Store) ResolveOneForRole(platform, connectionType, channelID, role string) (*Token, error) {
	resolved, err := s.resolve(platform, connectionType, channelID, role)
	if err != nil {
		return nil, err
	}
	return resolved[0], nil
}

func (s *Store) resolve(platform, connectionType, channelID, role string) ([]*Token, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type candidate struct {
		token    *Token
		priority int
	}

	var candidates []candidate
	for _, b := range s.bindings {
		if !b.Enabled {
			continue
		}
		if b.Platform != platform {
			continue
		}
		if b.ConnectionType != connectionType && b.ConnectionType != "*" {
			continue
		}
		if b.ChannelID != channelID && b.ChannelID != "*" {
			continue
		}
		if role != "" && b.Role != role {
			continue
		}
		t, ok := s.tokens[b.TokenID]
		if !ok {
			continue
		}
		if !t.IsActive() {
			continue
		}
		candidates = append(candidates, candidate{token: t, priority: b.Priority})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no active token for %s/%s/%s", platform, connectionType, channelID)
	}

	// Filter out rate-limited tokens if at least one healthy one exists.
	hasHealthy := false
	for _, c := range candidates {
		if !c.token.IsRateLimited() {
			hasHealthy = true
			break
		}
	}

	var filtered []candidate
	for _, c := range candidates {
		if hasHealthy && c.token.IsRateLimited() {
			continue
		}
		filtered = append(filtered, c)
	}

	// Sort: priority ASC, successRate DESC, lastUsed ASC.
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].priority != filtered[j].priority {
			return filtered[i].priority < filtered[j].priority
		}
		if filtered[i].token.Health.SuccessRate != filtered[j].token.Health.SuccessRate {
			return filtered[i].token.Health.SuccessRate > filtered[j].token.Health.SuccessRate
		}
		return filtered[i].token.LastUsed.Before(filtered[j].token.LastUsed)
	})

	out := make([]*Token, len(filtered))
	for i, c := range filtered {
		out[i] = c.token
	}
	return out, nil
}

// ResolveOne returns the single best token for a triple, or an error.
func (s *Store) ResolveOne(platform, connectionType, channelID string) (*Token, error) {
	tokens, err := s.Resolve(platform, connectionType, channelID)
	if err != nil {
		return nil, err
	}
	return tokens[0], nil
}

// ── Usage tracking ──────────────────────────────────────────────────────

// RecordUsage increments usage counters for a token+channel combination.
// sent/recv are message counts. requestErr should be non-nil if the API call failed.
func (s *Store) RecordUsage(tokenID, connectionType, channelID string, sent, recv int64, requestErr error) {
	hadError := requestErr != nil
	s.mu.Lock()
	defer s.mu.Unlock()

	key := usageKey(tokenID, connectionType, channelID)
	u, ok := s.usageLog[key]
	if !ok {
		u = &TokenUsage{}
		s.usageLog[key] = u
	}
	u.RequestsTotal++
	u.MessagesSent += sent
	u.MessagesRecv += recv
	if hadError {
		u.Errors++
	}

	// Also update the token's aggregate usage and last-used timestamp.
	if t, ok := s.tokens[tokenID]; ok {
		t.LastUsed = time.Now()
		t.Usage.RequestsTotal++
		t.Usage.MessagesSent += sent
		t.Usage.MessagesRecv += recv
		if hadError {
			t.Usage.Errors++
		}
	}
}

// ── Health reporting ────────────────────────────────────────────────────

// ReportHealth accepts a health update from a node/client and applies it
// to the token. Broadcasts the event to all watchers.
func (s *Store) ReportHealth(event TokenHealthEvent) {
	s.mu.Lock()
	t, ok := s.tokens[event.TokenID]
	if ok {
		if event.RateLimitHit {
			t.Health.RateLimitHit = true
			t.Health.RateLimitReset = event.RateLimitReset
		}
		if event.QuotaExhausted {
			t.Health.QuotaExhausted = true
		}
		if event.Error != "" {
			t.Health.LastError = event.Error
			t.Health.LastErrorAt = time.Now()
			t.Health.ConsecutiveFails++
			t.Health.SuccessRate = recalcSuccessRate(t)
		}
	}
	// Copy watchers while locked so we can call them unlocked.
	watchers := make([]func(TokenHealthEvent), len(s.watchers))
	copy(watchers, s.watchers)
	s.mu.Unlock()

	for _, w := range watchers {
		w(event)
	}
}

// ClearRateLimit clears the rate-limit flag on a token (e.g., after reset).
func (s *Store) ClearRateLimit(tokenID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tokens[tokenID]; ok {
		t.Health.RateLimitHit = false
		t.Health.RateLimitReset = nil
	}
}

// OnHealthChange registers a callback for health events.
func (s *Store) OnHealthChange(fn func(TokenHealthEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchers = append(s.watchers, fn)
}

// ── Carrier health integration ──────────────────────────────────────────

// IsCarrierHealthy checks if at least one active, non-rate-limited token
// exists for the given platform. carrierID is mapped to a platform via
// the bindings (e.g., "vk.messages" → "vk").
func (s *Store) IsCarrierHealthy(carrierID string) bool {
	platform := PlatformFromCarrierID(carrierID)
	if platform == "" {
		return true // unknown carrier, don't block
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, b := range s.bindings {
		if b.Platform != platform || !b.Enabled {
			continue
		}
		t, ok := s.tokens[b.TokenID]
		if !ok || !t.IsActive() {
			continue
		}
		if t.Health.IsHealthy() {
			return true
		}
	}

	// No bindings for this platform at all — don't block.
	hasBindings := false
	for _, b := range s.bindings {
		if b.Platform == platform {
			hasBindings = true
			break
		}
	}
	return !hasBindings
}

// ── Snapshot (admin API) ────────────────────────────────────────────────

// Snapshot returns a read-only view of all tokens and bindings.
func (s *Store) Snapshot() TokenStoreSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tokenViews := make([]TokenSnapshotView, 0, len(s.tokens))
	for _, t := range s.tokens {
		tokenViews = append(tokenViews, TokenSnapshotView{
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
		})
	}

	bindings := make([]Binding, len(s.bindings))
	copy(bindings, s.bindings)

	usageCopy := make(map[string]TokenUsage, len(s.usageLog))
	for k, v := range s.usageLog {
		usageCopy[k] = *v
	}

	return TokenStoreSnapshot{
		Tokens:   tokenViews,
		Bindings: bindings,
		UsageLog: usageCopy,
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────

func recalcSuccessRate(t *Token) float64 {
	total := t.Usage.RequestsTotal
	if total == 0 {
		return 1.0
	}
	ok := total - t.Usage.Errors
	if ok < 0 {
		ok = 0
	}
	return float64(ok) / float64(total)
}

// PlatformFromCarrierID extracts the platform name from a carrier ID.
// e.g., "vk.messages" → "vk", "wbstream.vp8" → "wbstream"
func PlatformFromCarrierID(carrierID string) string {
	for i, c := range carrierID {
		if c == '.' {
			return carrierID[:i]
		}
	}
	return ""
}
