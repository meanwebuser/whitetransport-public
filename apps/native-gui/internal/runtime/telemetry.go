package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTelemetryIPURL   = "https://api.ipify.org"
	defaultTelemetryTimeout = 8 * time.Second
)

// TelemetryProbe measures the active egress route through the daemon SOCKS
// listener. Implementations must be bounded and safe to skip in fast tests.
type TelemetryProbe interface {
	Probe(ctx context.Context, socksListen string) (TelemetryProbeResult, error)
}

// TelemetryProbeResult is the raw output from one post-connect probe.
type TelemetryProbeResult struct {
	ExternalIP string
	LatencyMS  int
}

// HTTPIPTelemetryProbe discovers the external IP by issuing one HTTP request
// through the local daemon SOCKS listener.
type HTTPIPTelemetryProbe struct {
	EndpointURL string
	Timeout     time.Duration
}

// NewHTTPIPTelemetryProbeFromEnv creates the default product telemetry probe.
func NewHTTPIPTelemetryProbeFromEnv() HTTPIPTelemetryProbe {
	endpoint := strings.TrimSpace(os.Getenv("WT_NATIVE_GUI_TELEMETRY_IP_URL"))
	if endpoint == "" {
		endpoint = defaultTelemetryIPURL
	}
	timeout := defaultTelemetryTimeout
	if raw := strings.TrimSpace(os.Getenv("WT_NATIVE_GUI_TELEMETRY_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return HTTPIPTelemetryProbe{EndpointURL: endpoint, Timeout: timeout}
}

// Probe returns the external IP and elapsed request latency through SOCKS.
func (p HTTPIPTelemetryProbe) Probe(ctx context.Context, socksListen string) (TelemetryProbeResult, error) {
	socksListen = strings.TrimSpace(socksListen)
	if socksListen == "" {
		return TelemetryProbeResult{}, fmt.Errorf("telemetry probe requires a SOCKS listen address")
	}
	endpoint := strings.TrimSpace(p.EndpointURL)
	if endpoint == "" {
		endpoint = defaultTelemetryIPURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return TelemetryProbeResult{}, fmt.Errorf("parse telemetry url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return TelemetryProbeResult{}, fmt.Errorf("telemetry url must be http or https")
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTelemetryTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, address string) (net.Conn, error) {
			return dialSOCKS5(ctx, socksListen, address)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TelemetryProbeResult{}, err
	}

	startedAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return TelemetryProbeResult{}, fmt.Errorf("telemetry request through SOCKS: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return TelemetryProbeResult{}, fmt.Errorf("telemetry endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil {
		return TelemetryProbeResult{}, fmt.Errorf("read telemetry response: %w", err)
	}
	externalIP := strings.TrimSpace(string(body))
	if net.ParseIP(externalIP) == nil {
		return TelemetryProbeResult{}, fmt.Errorf("telemetry endpoint returned non-IP response")
	}
	return TelemetryProbeResult{
		ExternalIP: externalIP,
		LatencyMS:  int(time.Since(startedAt).Round(time.Millisecond) / time.Millisecond),
	}, nil
}

func dialSOCKS5(ctx context.Context, socksListen string, target string) (net.Conn, error) {
	host, portString, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("split telemetry target %q: %w", target, err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid telemetry target port %q", portString)
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", socksListen)
	if err != nil {
		return nil, fmt.Errorf("dial SOCKS %s: %w", socksListen, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			conn.Close()
			return nil, err
		}
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write SOCKS greeting: %w", err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read SOCKS greeting: %w", err)
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("unexpected SOCKS greeting response %v", greeting)
	}
	request, err := socks5ConnectRequest(host, port)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Write(request); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write SOCKS connect: %w", err)
	}
	if err := readSOCKS5ConnectResponse(conn); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func socks5ConnectRequest(host string, port int) ([]byte, error) {
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("SOCKS target host is too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, []byte(host)...)
	}
	request = append(request, byte(port>>8), byte(port))
	return request, nil
}

func readSOCKS5ConnectResponse(conn net.Conn) error {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return fmt.Errorf("read SOCKS connect header: %w", err)
	}
	if header[0] != 0x05 {
		return fmt.Errorf("unexpected SOCKS version %d", header[0])
	}
	if header[1] != 0x00 {
		return fmt.Errorf("SOCKS connect failed with status %d", header[1])
	}
	var skip int
	switch header[3] {
	case 0x01:
		skip = 4
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return fmt.Errorf("read SOCKS domain length: %w", err)
		}
		skip = int(length[0])
	case 0x04:
		skip = 16
	default:
		return fmt.Errorf("unsupported SOCKS bind address type %d", header[3])
	}
	if skip > 0 {
		if _, err := io.CopyN(io.Discard, conn, int64(skip)); err != nil {
			return fmt.Errorf("read SOCKS bind address: %w", err)
		}
	}
	if _, err := io.CopyN(io.Discard, conn, 2); err != nil {
		return fmt.Errorf("read SOCKS bind port: %w", err)
	}
	return nil
}
