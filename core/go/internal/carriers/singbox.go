package carriers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const singboxControlDefaultPort = "17680"
const singboxControlDialTimeout = 10 * time.Second

const defaultSingBoxVLESSNetwork = "tcp"

// SingBoxVLESSConfig contains a sing-box VLESS outbound profile.
type SingBoxVLESSConfig struct {
	URI              string
	BinaryPath       string
	ConfigDir        string
	Server           string
	ServerPort       int
	UUID             string
	Network          string
	Flow             string
	TLSEnabled       bool
	TLSServerName    string
	TLSInsecure      bool
	UTLSFingerprint  string
	RealityEnabled   bool
	RealityPublicKey string
	RealityShortID   string
	TransportType    string
	TransportHost    string
	TransportPath    string
	LocalListen      string
	StartTimeoutSecs int
}

// SingBoxVLESSCarrier is an egress-only carrier backed by a local sing-box
// process and a VLESS-compatible remote server.
type SingBoxVLESSCarrier struct {
	desc Descriptor
	cfg  SingBoxVLESSConfig
	// StreamDialFunc is set by the tunnel layer after the sing-box process
	// starts. It enables the carrier to implement StreamDialer without
	// owning the process lifecycle directly.
	StreamDialFunc func(ctx context.Context, endpoint Endpoint, targetAddr string) (net.Conn, error)
}

// NewSingBoxVLESSCarrier creates a VLESS egress carrier and validates the
// remote endpoint before runtime dialing starts.
func NewSingBoxVLESSCarrier(cfg SingBoxVLESSConfig) (*SingBoxVLESSCarrier, error) {
	if strings.TrimSpace(cfg.URI) != "" {
		parsed, err := ParseSingBoxVLESSURI(cfg.URI)
		if err != nil {
			return nil, err
		}
		cfg = mergeSingBoxVLESSConfig(cfg, parsed)
	}
	if strings.TrimSpace(cfg.Server) == "" {
		return nil, fmt.Errorf("singbox.vless server is required")
	}
	if cfg.ServerPort <= 0 || cfg.ServerPort > 65535 {
		return nil, fmt.Errorf("singbox.vless server_port must be 1..65535")
	}
	if strings.TrimSpace(cfg.UUID) == "" {
		return nil, fmt.Errorf("singbox.vless uuid is required")
	}
	if strings.TrimSpace(cfg.Network) == "" {
		cfg.Network = defaultSingBoxVLESSNetwork
	}
	if strings.TrimSpace(cfg.BinaryPath) == "" {
		cfg.BinaryPath = "sing-box"
	}
	if strings.TrimSpace(cfg.TransportType) == "httpupgrade" && strings.TrimSpace(cfg.TransportPath) == "" {
		cfg.TransportPath = "/"
	}
	desc, err := FindStandardDescriptor(CarrierSingBoxVLESS)
	if err != nil {
		return nil, err
	}
	return &SingBoxVLESSCarrier{desc: desc, cfg: cfg}, nil
}

func (c *SingBoxVLESSCarrier) Descriptor() Descriptor { return c.desc }
func (c *SingBoxVLESSCarrier) IsNative()              {}

// Config returns a copy of the local sing-box process and VLESS settings.
func (c *SingBoxVLESSCarrier) Config() SingBoxVLESSConfig { return c.cfg }

func (c *SingBoxVLESSCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	if c.StreamDialFunc == nil {
		return fmt.Errorf("singbox control: sing-box process not started")
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("singbox control: marshal envelope: %w", err)
	}
	apiAddr := controlAPIAddr(endpoint)
	conn, err := dialWithTimeout(ctx, c.StreamDialFunc, endpoint, apiAddr)
	if err != nil {
		return fmt.Errorf("singbox control write dial: %w", err)
	}
	defer conn.Close()
	req := "POST /carrier/envelope HTTP/1.1\r\nHost: carrier\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(payload)) + "\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("singbox control write request: %w", err)
	}
	if _, err := conn.Write(payload); err != nil {
		return fmt.Errorf("singbox control write body: %w", err)
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("singbox control write response: %w", err)
	}
	if !strings.Contains(statusLine, "200") && !strings.Contains(statusLine, "201") {
		return fmt.Errorf("singbox control write rejected: %s", strings.TrimSpace(statusLine))
	}
	return nil
}

func (c *SingBoxVLESSCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	if c.StreamDialFunc == nil {
		return ReadResult{}, fmt.Errorf("singbox control: sing-box process not started")
	}
	apiAddr := controlAPIAddr(endpoint)
	conn, err := dialWithTimeout(ctx, c.StreamDialFunc, endpoint, apiAddr)
	if err != nil {
		return ReadResult{}, fmt.Errorf("singbox control read dial: %w", err)
	}
	defer conn.Close()
	req := "GET /carrier/envelopes?cursor=" + url.QueryEscape(string(cursor)) + " HTTP/1.1\r\nHost: carrier\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return ReadResult{}, fmt.Errorf("singbox control read request: %w", err)
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return ReadResult{}, fmt.Errorf("singbox control read response: %w", err)
	}
	if !strings.Contains(statusLine, "200") {
		return ReadResult{}, fmt.Errorf("singbox control read rejected: %s", strings.TrimSpace(statusLine))
	}
	body, err := readHTTPBody(br)
	if err != nil {
		return ReadResult{}, fmt.Errorf("singbox control read body: %w", err)
	}
	var resp struct {
		Envelopes []fabric.Envelope `json:"envelopes"`
		Cursor    string            `json:"cursor"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ReadResult{}, fmt.Errorf("singbox control read parse: %w", err)
	}
	return ReadResult{Envelopes: resp.Envelopes, Cursor: Cursor(resp.Cursor)}, nil
}

func (c *SingBoxVLESSCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	if c.StreamDialFunc == nil {
		return fmt.Errorf("singbox control: sing-box process not started")
	}
	apiAddr := controlAPIAddr(endpoint)
	conn, err := dialWithTimeout(ctx, c.StreamDialFunc, endpoint, apiAddr)
	if err != nil {
		return fmt.Errorf("singbox control delete dial: %w", err)
	}
	defer conn.Close()
	req := "DELETE /carrier/envelope/" + url.PathEscape(messageID) + " HTTP/1.1\r\nHost: carrier\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("singbox control delete request: %w", err)
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("singbox control delete response: %w", err)
	}
	if !strings.Contains(statusLine, "200") && !strings.Contains(statusLine, "204") {
		return fmt.Errorf("singbox control delete rejected: %s", strings.TrimSpace(statusLine))
	}
	return nil
}

func (c *SingBoxVLESSCarrier) Probe(context.Context, Endpoint) (Metrics, error) {
	return c.desc.Metrics, nil
}

// DialStream opens a TCP connection to targetAddr through the local sing-box
// mixed inbound. It implements the StreamDialer interface. The tunnel layer
// must set StreamDialFunc before this is called (typically after starting the
// sing-box process).
func (c *SingBoxVLESSCarrier) DialStream(ctx context.Context, endpoint Endpoint, targetAddr string) (net.Conn, error) {
	if c.StreamDialFunc == nil {
		return nil, fmt.Errorf("singbox stream dialer: sing-box process not started (StreamDialFunc is nil)")
	}
	return c.StreamDialFunc(ctx, endpoint, targetAddr)
}

// ParseSingBoxVLESSURI converts a vless:// import URL into a VLESS carrier
// config. It accepts the Xray/NekoBox style fields used by sing-box.
func ParseSingBoxVLESSURI(raw string) (SingBoxVLESSConfig, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return SingBoxVLESSConfig{}, fmt.Errorf("parse vless uri: %w", err)
	}
	if u.Scheme != "vless" {
		return SingBoxVLESSConfig{}, fmt.Errorf("singbox.vless uri must use vless scheme")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return SingBoxVLESSConfig{}, fmt.Errorf("parse vless port: %w", err)
	}
	query := u.Query()
	insecure := false
	switch strings.TrimSpace(query.Get("allowInsecure")) {
	case "1", "true":
		insecure = true
	case "", "0", "false":
		insecure = false
	default:
		return SingBoxVLESSConfig{}, fmt.Errorf("parse vless allowInsecure: unsupported value %q", query.Get("allowInsecure"))
	}
	cfg := SingBoxVLESSConfig{
		Server:           u.Hostname(),
		ServerPort:       port,
		UUID:             u.User.Username(),
		Network:          defaultSingBoxVLESSNetwork,
		TLSEnabled:       query.Get("security") == "tls" || query.Get("security") == "reality",
		TLSServerName:    query.Get("sni"),
		TLSInsecure:      insecure,
		UTLSFingerprint:  query.Get("fp"),
		TransportType:    query.Get("type"),
		TransportHost:    query.Get("host"),
		TransportPath:    firstNonEmpty(query.Get("serviceName"), query.Get("path")),
		Flow:             query.Get("flow"),
		RealityEnabled:   query.Get("security") == "reality",
		RealityPublicKey: query.Get("pbk"),
		RealityShortID:   query.Get("sid"),
	}
	if cfg.TLSServerName == "" {
		cfg.TLSServerName = cfg.Server
	}
	return cfg, nil
}

func mergeSingBoxVLESSConfig(base SingBoxVLESSConfig, parsed SingBoxVLESSConfig) SingBoxVLESSConfig {
	out := parsed
	out.URI = base.URI
	out.BinaryPath = firstNonEmpty(base.BinaryPath, parsed.BinaryPath)
	out.ConfigDir = firstNonEmpty(base.ConfigDir, parsed.ConfigDir)
	out.LocalListen = firstNonEmpty(base.LocalListen, parsed.LocalListen)
	if base.Server != "" {
		out.Server = base.Server
	}
	if base.ServerPort != 0 {
		out.ServerPort = base.ServerPort
	}
	if base.UUID != "" {
		out.UUID = base.UUID
	}
	if base.Network != "" {
		out.Network = base.Network
	}
	if base.Flow != "" {
		out.Flow = base.Flow
	}
	if base.TLSEnabled {
		out.TLSEnabled = true
	}
	if base.TLSServerName != "" {
		out.TLSServerName = base.TLSServerName
	}
	if base.TLSInsecure {
		out.TLSInsecure = true
	}
	if base.UTLSFingerprint != "" {
		out.UTLSFingerprint = base.UTLSFingerprint
	}
	if base.TransportType != "" {
		out.TransportType = base.TransportType
	}
	if base.TransportHost != "" {
		out.TransportHost = base.TransportHost
	}
	if base.TransportPath != "" {
		out.TransportPath = base.TransportPath
	}
	if base.StartTimeoutSecs != 0 {
		out.StartTimeoutSecs = base.StartTimeoutSecs
	}
	return out
}

func firstNonEmpty(first string, second string) string {
	if strings.TrimSpace(first) != "" {
		return first
	}
	return second
}

// SplitHostPort validates and normalizes host:port strings used by sing-box
// local inbounds.
func SplitHostPort(addr string) (string, int, error) {
	host, portString, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func controlAPIAddr(endpoint Endpoint) string {
	if port := endpoint.Metadata["control_port"]; port != "" {
		return "127.0.0.1:" + port
	}
	return "127.0.0.1:" + singboxControlDefaultPort
}

func dialWithTimeout(ctx context.Context, dial func(context.Context, Endpoint, string) (net.Conn, error), endpoint Endpoint, addr string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, singboxControlDialTimeout)
	defer cancel()
	return dial(dialCtx, endpoint, addr)
}

func readHTTPBody(br *bufio.Reader) ([]byte, error) {
	var contentLength int
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			val := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "content-length:"))
			contentLength, _ = strconv.Atoi(val)
		}
	}
	if contentLength > 0 {
		return io.ReadAll(io.LimitReader(br, int64(contentLength)))
	}
	return io.ReadAll(br)
}
