package provider

import (
	"context"
	"time"
)

type Type string

const (
	TypeMessaging    Type = "messaging"
	TypeFileTransfer Type = "file_transfer"
	TypeVideoCall    Type = "video_call"
	TypeAudioCall    Type = "audio_call"
	TypeCloudStorage Type = "cloud_storage"
	TypeSocial       Type = "social"
)

type Category string

const (
	CategorySocial Category = "social"
	CategoryCloud  Category = "cloud"
	CategoryVideo  Category = "video"
	CategoryAudio  Category = "audio"
	CategoryOther  Category = "other"
)

type KeyType string

const (
	KeyPermanent KeyType = "permanent"
	KeyTemporary KeyType = "temporary"
	KeyAnonymous KeyType = "anonymous"
)

type KeyStatus string

const (
	KeyActive  KeyStatus = "active"
	KeyExpired KeyStatus = "expired"
	KeyRevoked KeyStatus = "revoked"
	KeyLimited KeyStatus = "limited"
)

type Health struct {
	SuccessRate float64       `json:"success_rate"`
	AvgLatency  time.Duration `json:"avg_latency"`
	ErrorCount  int64         `json:"error_count"`
	LastCheck   time.Time     `json:"last_check"`
}

type Limits struct {
	MaxPayloadBytes  int   `json:"max_payload_bytes"`
	MaxRatePerMinute int   `json:"max_rate_per_minute"`
	MaxDailyBytes    int64 `json:"max_daily_bytes"`
}

type Channel struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Limits  Limits `json:"limits"`
	Enabled bool   `json:"enabled"`
}

type Metrics struct {
	SentBytes     int64         `json:"sent_bytes"`
	ReceivedBytes int64         `json:"received_bytes"`
	MessagesSent  int64         `json:"messages_sent"`
	MessagesRecv  int64         `json:"messages_recv"`
	Errors        int64         `json:"errors"`
	AvgLatency    time.Duration `json:"avg_latency"`
	Uptime        time.Duration `json:"uptime"`
}

type Schema struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Version     string  `json:"version"`
	Fields      []Field `json:"fields"`
}

type Field struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
}

type ProviderConfig struct {
	Type        Type              `json:"type"`
	Category    Category          `json:"category"`
	Endpoints   map[string]string `json:"endpoints"`
	Credentials map[string]string `json:"credentials"`
	Settings    map[string]any    `json:"settings"`
}

// Provider is the platform plugin interface for transport providers such as
// VK, OK, Telemost, DION, and WBStream. A Provider handles configuration,
// schema discovery, health tracking, and lifecycle management. It is wrapped
// into a Carrier by ProviderCarrier for use by the runtime.
type Provider interface {
	ID() string
	Type() Type
	Category() Category
	Version() string
	Configure(config ProviderConfig) error
	GetSchema() Schema
	Send(ctx context.Context, payload []byte) error
	Receive(ctx context.Context) ([]byte, error)
	Health() Health
	GetLimits() Limits
	GetMetrics() Metrics
	UpdateMetrics(metrics Metrics)
	Load() error
	Unload() error
}

// SafeEgressRecoveryProber is an opt-in provider capability for an autonomous
// recovery check. Implementations must not create a room, join or start a
// call, or send egress payload data. It is deliberately separate from the
// Carrier-level recovery capability because the provider bridge itself may
// carry active-session traffic.
type SafeEgressRecoveryProber interface {
	SafeEgressRecoveryProbe(ctx context.Context) error
}
