package carriers

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

// Hysteria2Config contains one authenticated Hysteria 2 outbound profile.
// The local sidecar settings are deliberately kept beside the protocol
// fields so the same managed stream lifecycle can serve VLESS and Hysteria 2.
type Hysteria2Config struct {
	URI              string
	BinaryPath       string
	ConfigDir        string
	Server           string
	ServerPort       int
	Password         string
	TLSServerName    string
	TLSInsecure      bool
	ObfsType         string
	ObfsPassword     string
	LocalListen      string
	StartTimeoutSecs int
}

// SingBoxHysteria2Carrier is a protocol-specific adapter that shares only the
// sidecar process/control implementation with the VLESS adapter. Policy sees
// its own descriptor and never branches on this concrete type.
type SingBoxHysteria2Carrier struct {
	base *SingBoxVLESSCarrier
	cfg  Hysteria2Config
	desc Descriptor
}

func NewSingBoxHysteria2Carrier(cfg Hysteria2Config) (*SingBoxHysteria2Carrier, error) {
	if strings.TrimSpace(cfg.URI) != "" {
		parsed, err := ParseHysteria2URI(cfg.URI)
		if err != nil {
			return nil, err
		}
		cfg = mergeHysteria2Config(cfg, parsed)
	}
	if strings.TrimSpace(cfg.Server) == "" {
		return nil, fmt.Errorf("hysteria2 server is required")
	}
	if cfg.ServerPort <= 0 || cfg.ServerPort > 65535 {
		return nil, fmt.Errorf("hysteria2 server_port must be 1..65535")
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return nil, fmt.Errorf("hysteria2 password is required")
	}
	if strings.TrimSpace(cfg.BinaryPath) == "" {
		cfg.BinaryPath = "sing-box"
	}
	if strings.TrimSpace(cfg.TLSServerName) == "" {
		cfg.TLSServerName = cfg.Server
	}
	desc, err := FindStandardDescriptor(CarrierHysteria2)
	if err != nil {
		return nil, err
	}
	desc.Evidence = NewEvidenceState()
	base := &SingBoxVLESSCarrier{
		desc: desc,
		cfg: SingBoxVLESSConfig{
			OutboundType:         "hysteria2",
			BinaryPath:           cfg.BinaryPath,
			ConfigDir:            cfg.ConfigDir,
			Server:               cfg.Server,
			ServerPort:           cfg.ServerPort,
			Password:             cfg.Password,
			TLSEnabled:           true,
			TLSServerName:        cfg.TLSServerName,
			TLSInsecure:          cfg.TLSInsecure,
			HysteriaObfsType:     cfg.ObfsType,
			HysteriaObfsPassword: cfg.ObfsPassword,
			LocalListen:          cfg.LocalListen,
			StartTimeoutSecs:     cfg.StartTimeoutSecs,
		},
	}
	return &SingBoxHysteria2Carrier{base: base, cfg: cfg, desc: desc}, nil
}

func (c *SingBoxHysteria2Carrier) Descriptor() Descriptor { return c.desc }
func (c *SingBoxHysteria2Carrier) IsNative()              {}

func (c *SingBoxHysteria2Carrier) Config() Hysteria2Config { return c.cfg }

func (c *SingBoxHysteria2Carrier) SetStreamDialFunc(fn func(context.Context, Endpoint, string) (net.Conn, error)) {
	c.base.SetStreamDialFunc(fn)
}

func (c *SingBoxHysteria2Carrier) SidecarSpec() SidecarSpec { return c.base.SidecarSpec() }

func (c *SingBoxHysteria2Carrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	return c.base.Write(ctx, endpoint, envelope)
}

func (c *SingBoxHysteria2Carrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	return c.base.Read(ctx, endpoint, cursor)
}

func (c *SingBoxHysteria2Carrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	return c.base.DeleteMessage(ctx, endpoint, messageID)
}

func (c *SingBoxHysteria2Carrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.base.Probe(ctx, endpoint)
}

func (c *SingBoxHysteria2Carrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *SingBoxHysteria2Carrier) DialStream(ctx context.Context, endpoint Endpoint, targetAddr string) (net.Conn, error) {
	return c.base.DialStream(ctx, endpoint, targetAddr)
}

// ParseHysteria2URI accepts the common hysteria2:// and hy2:// import forms.
// The password is kept in memory only; callers must resolve it from the
// TokenStore or an encrypted session profile before constructing the adapter.
func ParseHysteria2URI(raw string) (Hysteria2Config, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Hysteria2Config{}, fmt.Errorf("parse hysteria2 uri: %w", err)
	}
	if u.Scheme != "hysteria2" && u.Scheme != "hy2" {
		return Hysteria2Config{}, fmt.Errorf("hysteria2 uri must use hysteria2 or hy2 scheme")
	}
	port := 443
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return Hysteria2Config{}, fmt.Errorf("parse hysteria2 port: %w", err)
		}
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
		if candidate, ok := u.User.Password(); ok {
			password = candidate
		}
	}
	query := u.Query()
	insecure := query.Get("insecure") == "1" || strings.EqualFold(query.Get("insecure"), "true")
	return Hysteria2Config{
		URI:           raw,
		Server:        u.Hostname(),
		ServerPort:    port,
		Password:      password,
		TLSServerName: firstNonEmpty(query.Get("sni"), u.Hostname()),
		TLSInsecure:   insecure,
		ObfsType:      query.Get("obfs"),
		ObfsPassword:  firstNonEmpty(query.Get("obfs-password"), query.Get("obfs_password")),
	}, nil
}

func mergeHysteria2Config(base, parsed Hysteria2Config) Hysteria2Config {
	out := parsed
	if strings.TrimSpace(base.URI) != "" {
		out.URI = base.URI
	}
	if strings.TrimSpace(base.BinaryPath) != "" {
		out.BinaryPath = base.BinaryPath
	}
	if strings.TrimSpace(base.ConfigDir) != "" {
		out.ConfigDir = base.ConfigDir
	}
	if strings.TrimSpace(base.Server) != "" {
		out.Server = base.Server
	}
	if base.ServerPort != 0 {
		out.ServerPort = base.ServerPort
	}
	if strings.TrimSpace(base.Password) != "" {
		out.Password = base.Password
	}
	if strings.TrimSpace(base.TLSServerName) != "" {
		out.TLSServerName = base.TLSServerName
	}
	if base.TLSInsecure {
		out.TLSInsecure = true
	}
	if strings.TrimSpace(base.ObfsType) != "" {
		out.ObfsType = base.ObfsType
	}
	if strings.TrimSpace(base.ObfsPassword) != "" {
		out.ObfsPassword = base.ObfsPassword
	}
	if strings.TrimSpace(base.LocalListen) != "" {
		out.LocalListen = base.LocalListen
	}
	if base.StartTimeoutSecs != 0 {
		out.StartTimeoutSecs = base.StartTimeoutSecs
	}
	return out
}
