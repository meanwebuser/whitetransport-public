package tokens

import (
	"errors"
	"testing"
	"time"
)

func newTestStore() *Store {
	s := NewStore()

	s.Set(&Token{
		ID:        "vk-group-1",
		Platform:  "vk",
		Kind:      KindAPIKey,
		Lifecycle: LifecycleEmbedded,
		Status:    StatusActive,
		Value:     "vk1.a.test-token-1",
	})
	s.Set(&Token{
		ID:        "vk-group-2",
		Platform:  "vk",
		Kind:      KindAPIKey,
		Lifecycle: LifecycleEmbedded,
		Status:    StatusActive,
		Value:     "vk1.a.test-token-2",
	})
	s.Set(&Token{
		ID:                "wb-node-1",
		Platform:          "wbstream",
		Kind:              KindComposite,
		Lifecycle:         LifecycleEmbedded,
		Status:            StatusActive,
		Value:             "wb-jwt-token",
		CanCreateChannels: true,
		Parts:             map[string]string{"access_token": "jwt", "cookies_file": "/path"},
	})
	s.Set(&Token{
		ID:        "ok-set-1",
		Platform:  "ok",
		Kind:      KindComposite,
		Lifecycle: LifecycleEmbedded,
		Status:    StatusActive,
		Parts:     map[string]string{"access_token": "ok-token", "application_key": "ok-key"},
	})

	s.SetBindings([]Binding{
		{TokenID: "vk-group-1", Platform: "vk", ConnectionType: "messages", ChannelID: "2000000140", Role: "discovery", Priority: 10, Enabled: true},
		{TokenID: "vk-group-1", Platform: "vk", ConnectionType: "messages", ChannelID: "2000000142", Role: "node-client", Priority: 10, Enabled: true},
		{TokenID: "vk-group-1", Platform: "vk", ConnectionType: "messages", ChannelID: "2000000143", Role: "logs", Priority: 10, Enabled: true},
		{TokenID: "vk-group-1", Platform: "vk", ConnectionType: "messages", ChannelID: "2000000144", Role: "admin", Priority: 10, Enabled: true},
		{TokenID: "vk-group-1", Platform: "vk", ConnectionType: "docs.1024", ChannelID: "*", Role: "bulk", Priority: 10, Enabled: true},
		{TokenID: "vk-group-2", Platform: "vk", ConnectionType: "messages", ChannelID: "2000000140", Role: "discovery", Priority: 20, Enabled: true},
		{TokenID: "wb-node-1", Platform: "wbstream", ConnectionType: "vp8", ChannelID: "*", Role: "egress", Priority: 10, Enabled: true},
		{TokenID: "ok-set-1", Platform: "ok", ConnectionType: "messages", ChannelID: "chat:abc", Role: "control", Priority: 20, Enabled: true},
	})

	return s
}

func TestResolveExactMatch(t *testing.T) {
	s := newTestStore()

	tokens, err := s.Resolve("vk", "messages", "2000000140")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tokens) < 1 {
		t.Fatal("expected at least 1 token")
	}
	// First should be vk-group-1 (priority 10)
	if tokens[0].ID != "vk-group-1" {
		t.Errorf("expected vk-group-1 first, got %s", tokens[0].ID)
	}
	// Second should be vk-group-2 (priority 20) as fallback
	if len(tokens) < 2 {
		t.Fatal("expected 2 tokens (primary + fallback)")
	}
	if tokens[1].ID != "vk-group-2" {
		t.Errorf("expected vk-group-2 second, got %s", tokens[1].ID)
	}
}

func TestResolvePriorityOrdersSameChannelAccounts(t *testing.T) {
	s := NewStore()
	s.Set(&Token{ID: "vk-high", Platform: "vk", Kind: KindAPIKey, Lifecycle: LifecycleEmbedded, Status: StatusActive, Value: "high"})
	s.Set(&Token{ID: "vk-low", Platform: "vk", Kind: KindAPIKey, Lifecycle: LifecycleEmbedded, Status: StatusActive, Value: "low"})
	s.SetBindings([]Binding{
		{TokenID: "vk-low", Platform: "vk", ConnectionType: "messages", ChannelID: "2000000140", Role: "discovery", Priority: 20, Enabled: true},
		{TokenID: "vk-high", Platform: "vk", ConnectionType: "messages", ChannelID: "2000000140", Role: "discovery", Priority: 10, Enabled: true},
	})

	resolved, err := s.Resolve("vk", "messages", "2000000140")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 || resolved[0].ID != "vk-high" || resolved[1].ID != "vk-low" {
		t.Fatalf("unexpected priority order: %+v", resolved)
	}
}

func TestResolveWildcardChannel(t *testing.T) {
	s := newTestStore()

	// Wildcard channel match for docs
	tokens, err := s.Resolve("vk", "docs.1024", "any-channel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if tokens[0].ID != "vk-group-1" {
		t.Errorf("expected vk-group-1, got %s", tokens[0].ID)
	}
}

func TestResolveWBStreamWildcard(t *testing.T) {
	s := newTestStore()

	tokens, err := s.Resolve("wbstream", "vp8", "any-room")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ID != "wb-node-1" {
		t.Errorf("expected wb-node-1, got %v", tokens)
	}
	if !tokens[0].CanCreateChannels {
		t.Error("WBStream token should have CanCreateChannels=true")
	}
}

func TestResolveOneForRoleRequiresExactEnabledBinding(t *testing.T) {
	s := NewStore()
	s.Set(&Token{ID: "wb-node", Platform: "wbstream", Kind: KindComposite, Lifecycle: LifecycleEmbedded, Status: StatusActive, Parts: map[string]string{"access_token": "node"}})
	s.Set(&Token{ID: "wb-client", Platform: "wbstream", Kind: KindComposite, Lifecycle: LifecycleEmbedded, Status: StatusActive, Parts: map[string]string{"access_token": "client"}})
	s.SetBindings([]Binding{
		{TokenID: "wb-node", Platform: "wbstream", ConnectionType: "vp8", ChannelID: "*", Role: "node", Priority: 1, Enabled: true},
		{TokenID: "wb-client", Platform: "wbstream", ConnectionType: "vp8", ChannelID: "*", Role: "client", Priority: 10, Enabled: true},
	})

	got, err := s.ResolveOneForRole("wbstream", "vp8", "room-1", "client")
	if err != nil {
		t.Fatalf("ResolveOneForRole: %v", err)
	}
	if got.ID != "wb-client" {
		t.Fatalf("resolved token = %q, want wb-client", got.ID)
	}

	if _, err := s.ResolveOneForRole("wbstream", "vp8", "room-1", "missing"); err == nil {
		t.Fatal("expected exact role mismatch to fail closed")
	}
}

func TestResolveNoMatch(t *testing.T) {
	s := newTestStore()

	_, err := s.Resolve("dion", "vp8", "room1")
	if err == nil {
		t.Error("expected error for non-existent platform")
	}
}

func TestResolveDisabledBinding(t *testing.T) {
	s := NewStore()
	s.Set(&Token{ID: "tok1", Platform: "vk", Kind: KindAPIKey, Status: StatusActive, Value: "test"})
	s.AddBinding(Binding{TokenID: "tok1", Platform: "vk", ConnectionType: "messages", ChannelID: "123", Priority: 10, Enabled: false})

	_, err := s.Resolve("vk", "messages", "123")
	if err == nil {
		t.Error("expected error for disabled binding")
	}
}

func TestResolveRevokedToken(t *testing.T) {
	s := NewStore()
	s.Set(&Token{ID: "tok1", Platform: "vk", Kind: KindAPIKey, Status: StatusRevoked, Value: "test"})
	s.AddBinding(Binding{TokenID: "tok1", Platform: "vk", ConnectionType: "messages", ChannelID: "123", Priority: 10, Enabled: true})

	_, err := s.Resolve("vk", "messages", "123")
	if err == nil {
		t.Error("expected error for revoked token")
	}
}

func TestResolveExpiredToken(t *testing.T) {
	s := NewStore()
	past := time.Now().Add(-1 * time.Hour)
	s.Set(&Token{ID: "tok1", Platform: "vk", Kind: KindAPIKey, Status: StatusActive, Value: "test", ExpiresAt: &past})
	s.AddBinding(Binding{TokenID: "tok1", Platform: "vk", ConnectionType: "messages", ChannelID: "123", Priority: 10, Enabled: true})

	_, err := s.Resolve("vk", "messages", "123")
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestResolveRateLimitedFallback(t *testing.T) {
	s := NewStore()
	s.Set(&Token{
		ID: "tok1", Platform: "vk", Kind: KindAPIKey, Status: StatusActive, Value: "primary",
		Health: TokenHealth{RateLimitHit: true},
	})
	s.Set(&Token{
		ID: "tok2", Platform: "vk", Kind: KindAPIKey, Status: StatusActive, Value: "fallback",
	})
	s.SetBindings([]Binding{
		{TokenID: "tok1", Platform: "vk", ConnectionType: "messages", ChannelID: "*", Priority: 10, Enabled: true},
		{TokenID: "tok2", Platform: "vk", ConnectionType: "messages", ChannelID: "*", Priority: 20, Enabled: true},
	})

	tokens, err := s.Resolve("vk", "messages", "any")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token (rate-limited filtered out), got %d", len(tokens))
	}
	if tokens[0].ID != "tok2" {
		t.Errorf("expected tok2 (fallback), got %s", tokens[0].ID)
	}
}

func TestResolveAllRateLimited(t *testing.T) {
	s := NewStore()
	s.Set(&Token{
		ID: "tok1", Platform: "vk", Kind: KindAPIKey, Status: StatusActive, Value: "limited1",
		Health: TokenHealth{RateLimitHit: true},
	})
	s.AddBinding(Binding{TokenID: "tok1", Platform: "vk", ConnectionType: "messages", ChannelID: "*", Priority: 10, Enabled: true})

	// When ALL are rate-limited, still return them (better than nothing)
	tokens, err := s.Resolve("vk", "messages", "any")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token (all rate-limited), got %d", len(tokens))
	}
}

func TestRecordUsage(t *testing.T) {
	s := newTestStore()

	s.RecordUsage("vk-group-1", "messages", "2000000140", 1, 0, nil)
	s.RecordUsage("vk-group-1", "messages", "2000000140", 1, 0, nil)
	s.RecordUsage("vk-group-1", "messages", "2000000140", 0, 0, errors.New("rate limited"))

	// Check usage log
	snap := s.Snapshot()
	key := "vk-group-1:messages:2000000140"
	u, ok := snap.UsageLog[key]
	if !ok {
		t.Fatalf("usage log key %q not found", key)
	}
	if u.RequestsTotal != 3 {
		t.Errorf("expected 3 requests, got %d", u.RequestsTotal)
	}
	if u.Errors != 1 {
		t.Errorf("expected 1 error, got %d", u.Errors)
	}
	if u.MessagesSent != 2 {
		t.Errorf("expected 2 messages sent, got %d", u.MessagesSent)
	}
}

func TestReportHealth(t *testing.T) {
	s := newTestStore()

	// Report rate limit
	s.ReportHealth(TokenHealthEvent{
		TokenID:      "vk-group-1",
		RateLimitHit: true,
	})

	tok, ok := s.Get("vk-group-1")
	if !ok {
		t.Fatal("token not found")
	}
	if !tok.Health.RateLimitHit {
		t.Error("expected RateLimitHit=true")
	}

	// Clear rate limit
	s.ClearRateLimit("vk-group-1")
	tok, _ = s.Get("vk-group-1")
	if tok.Health.RateLimitHit {
		t.Error("expected RateLimitHit=false after clear")
	}
}

func TestReportHealthError(t *testing.T) {
	s := newTestStore()

	s.ReportHealth(TokenHealthEvent{
		TokenID: "vk-group-1",
		Error:   "403 Forbidden",
	})

	tok, _ := s.Get("vk-group-1")
	if tok.Health.LastError != "403 Forbidden" {
		t.Errorf("expected error '403 Forbidden', got %q", tok.Health.LastError)
	}
	if tok.Health.ConsecutiveFails != 1 {
		t.Errorf("expected 1 consecutive fail, got %d", tok.Health.ConsecutiveFails)
	}
}

func TestHealthWatcher(t *testing.T) {
	s := newTestStore()

	var received TokenHealthEvent
	s.OnHealthChange(func(e TokenHealthEvent) {
		received = e
	})

	s.ReportHealth(TokenHealthEvent{
		TokenID:    "vk-group-1",
		ReporterID: "node-1",
		Error:      "timeout",
	})

	if received.TokenID != "vk-group-1" {
		t.Errorf("watcher received wrong token: %s", received.TokenID)
	}
	if received.ReporterID != "node-1" {
		t.Errorf("watcher received wrong reporter: %s", received.ReporterID)
	}
}

func TestIsCarrierHealthy(t *testing.T) {
	s := newTestStore()

	// VK has active tokens → healthy
	if !s.IsCarrierHealthy("vk.messages") {
		t.Error("expected vk.messages to be healthy")
	}

	// Rate-limit the only VK token for messages
	s.ReportHealth(TokenHealthEvent{TokenID: "vk-group-1", RateLimitHit: true})
	s.ReportHealth(TokenHealthEvent{TokenID: "vk-group-2", RateLimitHit: true})

	if s.IsCarrierHealthy("vk.messages") {
		t.Error("expected vk.messages to be unhealthy (all tokens rate-limited)")
	}

	// Unknown carrier → don't block
	if !s.IsCarrierHealthy("unknown.carrier") {
		t.Error("unknown carrier should be healthy (don't block)")
	}
}

func TestSnapshot(t *testing.T) {
	s := newTestStore()

	snap := s.Snapshot()
	if len(snap.Tokens) != 4 {
		t.Errorf("expected 4 tokens in snapshot, got %d", len(snap.Tokens))
	}
	if len(snap.Bindings) != 8 {
		t.Errorf("expected 8 bindings in snapshot, got %d", len(snap.Bindings))
	}

	// Verify masking
	for _, tv := range snap.Tokens {
		if tv.MaskedValue == tv.ID {
			t.Errorf("token %s value should be masked", tv.ID)
		}
	}
}

func TestResolveOne(t *testing.T) {
	s := newTestStore()

	tok, err := s.ResolveOne("vk", "messages", "2000000140")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.ID != "vk-group-1" {
		t.Errorf("expected vk-group-1, got %s", tok.ID)
	}
}

func TestListByPlatform(t *testing.T) {
	s := newTestStore()

	vkTokens := s.ListByPlatform("vk")
	if len(vkTokens) != 2 {
		t.Errorf("expected 2 VK tokens, got %d", len(vkTokens))
	}

	okTokens := s.ListByPlatform("ok")
	if len(okTokens) != 1 {
		t.Errorf("expected 1 OK token, got %d", len(okTokens))
	}
}

func TestPlatformFromCarrierID(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"vk.messages", "vk"},
		{"vk.docs.1024", "vk"},
		{"ok.messages", "ok"},
		{"wbstream.vp8", "wbstream"},
		{"file.mailbox", "file"},
		{"nope", ""},
	}
	for _, tt := range tests {
		if got := PlatformFromCarrierID(tt.id); got != tt.want {
			t.Errorf("PlatformFromCarrierID(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestResolveWildcardConnectionType(t *testing.T) {
	s := NewStore()

	// Token with wildcard connection type
	s.Set(&Token{
		ID:        "vk-wildcard",
		Platform:  "vk",
		Kind:      KindAPIKey,
		Lifecycle: LifecycleEmbedded,
		Status:    StatusActive,
		Value:     "vk1.a.wildcard-token",
	})
	s.AddBinding(Binding{
		TokenID:        "vk-wildcard",
		Platform:       "vk",
		ConnectionType: "*",
		ChannelID:      "2000000140",
		Role:           "any",
		Priority:       10,
		Enabled:        true,
	})

	// Should match any connection type
	tok, err := s.ResolveOne("vk", "messages", "2000000140")
	if err != nil {
		t.Fatalf("unexpected error for messages: %v", err)
	}
	if tok.ID != "vk-wildcard" {
		t.Errorf("expected vk-wildcard for messages, got %s", tok.ID)
	}

	tok, err = s.ResolveOne("vk", "docs.256", "2000000140")
	if err != nil {
		t.Fatalf("unexpected error for docs.256: %v", err)
	}
	if tok.ID != "vk-wildcard" {
		t.Errorf("expected vk-wildcard for docs.256, got %s", tok.ID)
	}

	tok, err = s.ResolveOne("vk", "docs.1024", "2000000140")
	if err != nil {
		t.Fatalf("unexpected error for docs.1024: %v", err)
	}
	if tok.ID != "vk-wildcard" {
		t.Errorf("expected vk-wildcard for docs.1024, got %s", tok.ID)
	}

	// Non-matching channel should fail
	_, err = s.ResolveOne("vk", "messages", "999999999")
	if err == nil {
		t.Error("expected error for non-matching channel")
	}
}
