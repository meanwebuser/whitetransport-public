package tokens

import (
	"testing"
	"time"
)

func TestTokenIsExpired(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)

	tests := []struct {
		name   string
		token  Token
		expect bool
	}{
		{"no expiry", Token{}, false},
		{"future expiry", Token{ExpiresAt: &future}, false},
		{"past expiry", Token{ExpiresAt: &past}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.IsExpired(); got != tt.expect {
				t.Errorf("IsExpired() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestTokenIsActive(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)

	tests := []struct {
		name   string
		token  Token
		expect bool
	}{
		{"active no expiry", Token{Status: StatusActive}, true},
		{"active with future expiry", Token{Status: StatusActive, ExpiresAt: &future}, true},
		{"active but expired", Token{Status: StatusActive, ExpiresAt: &past}, false},
		{"revoked", Token{Status: StatusRevoked}, false},
		{"expired status", Token{Status: StatusExpired}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.IsActive(); got != tt.expect {
				t.Errorf("IsActive() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestTokenIsRateLimited(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)

	tests := []struct {
		name   string
		token  Token
		expect bool
	}{
		{"not limited", Token{}, false},
		{"limited no reset", Token{Health: TokenHealth{RateLimitHit: true}}, true},
		{"limited with future reset", Token{Health: TokenHealth{RateLimitHit: true, RateLimitReset: &future}}, true},
		{"limited but reset passed", Token{Health: TokenHealth{RateLimitHit: true, RateLimitReset: &past}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.IsRateLimited(); got != tt.expect {
				t.Errorf("IsRateLimited() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestTokenMaskedValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"short", "abc", "***"},
		{"exact 12", "123456789012", "***"},
		{"long", "vk1.a.6gQ-f8bM7d2szxrFOxW", "vk1.a.6g...FOxW"},
		{"very long", "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9", "eyJhbGci...VCJ9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := Token{Value: tt.value}
			if got := tok.MaskedValue(); got != tt.want {
				t.Errorf("MaskedValue() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTokenHealthIsHealthy(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)

	tests := []struct {
		name   string
		health TokenHealth
		expect bool
	}{
		{"healthy default", TokenHealth{}, true},
		{"rate limited", TokenHealth{RateLimitHit: true}, false},
		{"rate limited future reset", TokenHealth{RateLimitHit: true, RateLimitReset: &future}, false},
		{"rate limited past reset", TokenHealth{RateLimitHit: true, RateLimitReset: &past}, true},
		{"quota exhausted", TokenHealth{QuotaExhausted: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.health.IsHealthy(); got != tt.expect {
				t.Errorf("IsHealthy() = %v, want %v", got, tt.expect)
			}
		})
	}
}
