// Package tokens provides a universal credential layer for WhiteTransport.
//
// A Token is the universal credential unit: it can be an API key, JWT,
// cookies, localStorage, or composite (multi-part) credential. Tokens are
// bound to (Platform, ConnectionType, Channel) triples, enabling one token
// to serve many channels.
package tokens

import (
	"time"
)

// TokenKind classifies the credential format.
type TokenKind string

const (
	KindAPIKey       TokenKind = "api_key"
	KindJWT          TokenKind = "jwt"
	KindCookies      TokenKind = "cookies"
	KindLocalStorage TokenKind = "local_storage"
	KindOAuthToken   TokenKind = "oauth_token"
	KindSymmetricKey TokenKind = "symmetric_key"
	KindComposite    TokenKind = "composite" // multi-part: OK's 3-field set, WB's JWT+cookies+localStorage
)

// Lifecycle controls how a token is obtained and refreshed.
type Lifecycle string

const (
	LifecycleEmbedded  Lifecycle = "embedded"  // long-lived, shipped with config
	LifecycleSession   Lifecycle = "session"   // obtained from node/admin at runtime
	LifecycleEphemeral Lifecycle = "ephemeral" // single-use or very short-lived
)

// Status tracks the token's operational state.
type Status string

const (
	StatusActive  Status = "active"
	StatusExpired Status = "expired"
	StatusRevoked Status = "revoked"
	StatusLimited Status = "limited"
)

// Token is the universal credential unit for all platforms.
type Token struct {
	ID                string            `json:"id"`
	Platform          string            `json:"platform"` // vk, ok, wbstream, dion, yandex
	Kind              TokenKind         `json:"kind"`
	Lifecycle         Lifecycle         `json:"lifecycle"`
	Status            Status            `json:"status"`
	Value             string            `json:"value"`             // may be enc:v1:... when encrypted
	Parts             map[string]string `json:"parts,omitempty"`   // for KindComposite
	Refresh           string            `json:"refresh,omitempty"` // refresh token or URL
	CanCreateChannels bool              `json:"can_create_channels"`
	ExpiresAt         *time.Time        `json:"expires_at,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	LastUsed          time.Time         `json:"last_used"`
	Tags              map[string]string `json:"tags,omitempty"` // "role": "node", "phone": "+7..."
	Health            TokenHealth       `json:"health"`
	Usage             TokenUsage        `json:"usage"`
}

// IsExpired returns true if the token has a set expiry and it has passed.
func (t *Token) IsExpired() bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*t.ExpiresAt)
}

// IsActive returns true when the token can be used (not revoked, not expired).
func (t *Token) IsActive() bool {
	return t.Status == StatusActive && !t.IsExpired()
}

// IsRateLimited returns true if the token is currently rate-limited.
func (t *Token) IsRateLimited() bool {
	if !t.Health.RateLimitHit {
		return false
	}
	// If the reset time has passed, the limit no longer applies.
	if t.Health.RateLimitReset != nil && time.Now().After(*t.Health.RateLimitReset) {
		return false
	}
	return true
}

// MaskedValue returns a redacted version of the token value for admin display.
func (t *Token) MaskedValue() string {
	v := t.Value
	if len(v) <= 12 {
		return "***"
	}
	return v[:8] + "..." + v[len(v)-4:]
}

// TokenHealth tracks token-specific health signals from platform APIs.
type TokenHealth struct {
	SuccessRate      float64    `json:"success_rate"`
	RateLimitHit     bool       `json:"rate_limit_hit"`
	RateLimitReset   *time.Time `json:"rate_limit_reset,omitempty"`
	QuotaExhausted   bool       `json:"quota_exhausted"`
	LastError        string     `json:"last_error,omitempty"`
	LastErrorAt      time.Time  `json:"last_error_at,omitempty"`
	ConsecutiveFails int        `json:"consecutive_fails"`
}

// IsHealthy returns true when the token can serve requests.
func (h TokenHealth) IsHealthy() bool {
	if h.QuotaExhausted {
		return false
	}
	if h.RateLimitHit {
		// Still unhealthy unless the reset time has passed.
		if h.RateLimitReset == nil || time.Now().Before(*h.RateLimitReset) {
			return false
		}
	}
	return true
}

// TokenUsage tracks per-token counters at configurable granularity.
type TokenUsage struct {
	MessagesSent  int64 `json:"messages_sent"`
	MessagesRecv  int64 `json:"messages_recv"`
	BytesSent     int64 `json:"bytes_sent"`
	BytesRecv     int64 `json:"bytes_recv"`
	Errors        int64 `json:"errors"`
	RequestsTotal int64 `json:"requests_total"`
}

// Binding addresses a token to a specific platform/connection/channel triple.
type Binding struct {
	TokenID        string `json:"token_id"`
	Platform       string `json:"platform"`
	ConnectionType string `json:"connection_type"` // messages, docs.256, docs.1024, vp8, datachannel, photos
	ChannelID      string `json:"channel_id"`      // peer_id, chat_id, room_id, or "*" for wildcard
	Role           string `json:"role"`            // discovery, node-client, logs, admin, bulk, egress
	Priority       int    `json:"priority"`        // lower = preferred
	Enabled        bool   `json:"enabled"`
}

// TokenHealthEvent is a health report from a node/client about a token.
type TokenHealthEvent struct {
	TokenID        string     `json:"token_id"`
	RateLimitHit   bool       `json:"rate_limit_hit"`
	RateLimitReset *time.Time `json:"rate_limit_reset,omitempty"`
	QuotaExhausted bool       `json:"quota_exhausted"`
	Error          string     `json:"error,omitempty"`
	ReporterID     string     `json:"reporter_id,omitempty"`
}

// TokenStoreSnapshot is a read-only view for admin API display.
type TokenStoreSnapshot struct {
	Tokens   []TokenSnapshotView   `json:"tokens"`
	Bindings []Binding             `json:"bindings"`
	UsageLog map[string]TokenUsage `json:"usage_log"`
}

// TokenSnapshotView is one token in a snapshot, with value masked.
type TokenSnapshotView struct {
	ID                string            `json:"id"`
	Platform          string            `json:"platform"`
	Kind              TokenKind         `json:"kind"`
	Lifecycle         Lifecycle         `json:"lifecycle"`
	Status            Status            `json:"status"`
	MaskedValue       string            `json:"masked_value"`
	CanCreateChannels bool              `json:"can_create_channels"`
	ExpiresAt         *time.Time        `json:"expires_at,omitempty"`
	LastUsed          time.Time         `json:"last_used"`
	Tags              map[string]string `json:"tags,omitempty"`
	Health            TokenHealth       `json:"health"`
	Usage             TokenUsage        `json:"usage"`
}

// usageKey builds a composite key for the usage log.
func usageKey(tokenID, connectionType, channelID string) string {
	return tokenID + ":" + connectionType + ":" + channelID
}
