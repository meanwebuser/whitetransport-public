package carriers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"golang.org/x/net/proxy"
)

// TailscaleConfig describes an already-authenticated tailnet peer that exposes
// a WhiteTransport SOCKS endpoint. The adapter never changes tailscale state
// and never dials an Internet target directly.
type TailscaleConfig struct {
	Peer              string
	ProxyAddress      string
	UDPProbeAddress   string
	BinaryPath        string
	StatusTimeoutSecs int
}

type TailscaleCarrier struct {
	desc       Descriptor
	cfg        TailscaleConfig
	statusFunc func(context.Context) error
}

func NewTailscaleCarrier(cfg TailscaleConfig) (*TailscaleCarrier, error) {
	cfg.Peer = strings.TrimSpace(cfg.Peer)
	cfg.ProxyAddress = strings.TrimSpace(cfg.ProxyAddress)
	if cfg.Peer == "" {
		return nil, fmt.Errorf("tailscale peer is required")
	}
	if cfg.ProxyAddress == "" {
		return nil, fmt.Errorf("tailscale proxy_address is required")
	}
	if strings.TrimSpace(cfg.BinaryPath) == "" {
		cfg.BinaryPath = "tailscale"
	}
	desc, err := FindStandardDescriptor(CarrierTailscale)
	if err != nil {
		return nil, err
	}
	desc.Evidence = NewEvidenceState()
	if cfg.UDPProbeAddress != "" {
		desc.Capabilities = append(desc.Capabilities, CapUDP)
	}
	return &TailscaleCarrier{desc: desc, cfg: cfg}, nil
}

func (c *TailscaleCarrier) Descriptor() Descriptor  { return c.desc }
func (c *TailscaleCarrier) IsNative()               {}
func (c *TailscaleCarrier) Config() TailscaleConfig { return c.cfg }

func (c *TailscaleCarrier) Write(context.Context, Endpoint, fabric.Envelope) error {
	return errors.New("tailscale carrier is stream-only and has no envelope write path")
}

func (c *TailscaleCarrier) Read(context.Context, Endpoint, Cursor) (ReadResult, error) {
	return ReadResult{}, errors.New("tailscale carrier is stream-only and has no envelope read path")
}

func (c *TailscaleCarrier) DeleteMessage(context.Context, Endpoint, string) error {
	return errors.New("tailscale carrier does not support message deletion")
}

func (c *TailscaleCarrier) Probe(ctx context.Context, _ Endpoint) (Metrics, error) {
	started := time.Now()
	if err := c.checkAuthenticated(ctx); err != nil {
		metrics := Metrics{Healthy: false, FailureReason: "tailscale_authentication"}
		c.desc.Observe(MetricSourceProbe, metrics, time.Now().UTC())
		return metrics, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", c.cfg.ProxyAddress)
	if err != nil {
		metrics := Metrics{Healthy: false, FailureReason: "tailscale_proxy_unreachable"}
		c.desc.Observe(MetricSourceProbe, metrics, time.Now().UTC())
		return metrics, fmt.Errorf("tailscale proxy probe: %w", err)
	}
	_ = conn.Close()
	metrics := Metrics{Healthy: true, Latency: time.Since(started), LastOK: time.Now().UTC(), Reliability: 1}
	c.desc.Observe(MetricSourceProbe, metrics, time.Now().UTC())
	return metrics, nil
}

func (c *TailscaleCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	return c.Probe(ctx, endpoint)
}

// DialStream opens a SOCKS5 connection through the explicitly configured
// tailnet peer. The target is sent to that peer; it is never opened by the
// local host socket as a direct fallback.
func (c *TailscaleCarrier) DialStream(ctx context.Context, _ Endpoint, targetAddr string) (net.Conn, error) {
	if err := c.checkAuthenticated(ctx); err != nil {
		return nil, fmt.Errorf("tailscale authentication preflight: %w", err)
	}
	dialer, err := proxy.SOCKS5("tcp", c.cfg.ProxyAddress, nil, &net.Dialer{})
	if err != nil {
		return nil, fmt.Errorf("tailscale SOCKS5 dialer: %w", err)
	}
	if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
		return contextDialer.DialContext(ctx, "tcp", targetAddr)
	}
	result := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		conn, err := dialer.Dial("tcp", targetAddr)
		result <- struct {
			conn net.Conn
			err  error
		}{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		return result.conn, result.err
	}
}

// ProbeUDP measures only the explicitly configured tailnet UDP echo address.
// It is kept separate from consumer packet routing until a packet session
// endpoint is configured on the peer.
func (c *TailscaleCarrier) ProbeUDP(ctx context.Context) (time.Duration, error) {
	if strings.TrimSpace(c.cfg.UDPProbeAddress) == "" {
		return 0, errors.New("tailscale UDP probe address is not configured")
	}
	if err := c.checkAuthenticated(ctx); err != nil {
		return 0, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "udp", c.cfg.UDPProbeAddress)
	if err != nil {
		return 0, fmt.Errorf("tailscale UDP probe dial: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return 0, err
	}
	started := time.Now()
	if _, err := conn.Write([]byte("wt-udp-probe")); err != nil {
		return 0, err
	}
	buffer := make([]byte, 64)
	if _, err := conn.Read(buffer); err != nil {
		return 0, err
	}
	return time.Since(started), nil
}

func (c *TailscaleCarrier) checkAuthenticated(ctx context.Context) error {
	if c.statusFunc != nil {
		return c.statusFunc(ctx)
	}
	timeout := 5 * time.Second
	if c.cfg.StatusTimeoutSecs > 0 {
		timeout = time.Duration(c.cfg.StatusTimeoutSecs) * time.Second
	}
	statusCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(statusCtx, c.cfg.BinaryPath, "status", "--json").Output()
	if err != nil {
		return fmt.Errorf("tailscale status: %w", err)
	}
	var status struct {
		BackendState string `json:"BackendState"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return fmt.Errorf("tailscale status parse: %w", err)
	}
	if !strings.EqualFold(status.BackendState, "Running") {
		return fmt.Errorf("tailscale backend state is %q", status.BackendState)
	}
	if !strings.Contains(string(output), c.cfg.Peer) {
		return fmt.Errorf("tailscale peer is not present in authenticated status")
	}
	return nil
}
