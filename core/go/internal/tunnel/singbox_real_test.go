package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

// TestRealSingBoxVLESSEgressHTTP proves the managed sing-box path against a
// real VLESS/Xray-compatible endpoint. It is opt-in because it needs a live
// server profile and starts an external sing-box binary.
func TestRealSingBoxVLESSEgressHTTP(t *testing.T) {
	if os.Getenv("WT_SINGBOX_REAL") != "1" {
		t.Skip("set WT_SINGBOX_REAL=1 to run the live sing-box VLESS smoke")
	}
	vlessURI := strings.TrimSpace(os.Getenv("WT_SINGBOX_VLESS_URI"))
	binaryPath := strings.TrimSpace(os.Getenv("WT_SINGBOX_BINARY"))
	if vlessURI == "" || binaryPath == "" {
		t.Skip("WT_SINGBOX_VLESS_URI and WT_SINGBOX_BINARY are required")
	}

	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		URI:              vlessURI,
		BinaryPath:       binaryPath,
		ConfigDir:        t.TempDir(),
		LocalListen:      "127.0.0.1:0",
		StartTimeoutSecs: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierSingBoxVLESS: {
			Carrier: carrier,
			Endpoint: carriers.Endpoint{
				ID:      "singbox-live",
				Carrier: carriers.CarrierSingBoxVLESS,
				Address: net.JoinHostPort(carrier.Config().Server, strconv.Itoa(carrier.Config().ServerPort)),
			},
		},
	}
	tunnel := NewSingBoxTunnel(bindings)
	defer tunnel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := tunnel.DialContext(ctx, bindings[carriers.CarrierSingBoxVLESS].Endpoint, "ifconfig.me:80")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	if err := writeHTTPRequest(conn, "ifconfig.me"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	statusLine, body, err := readHTTPStatusAndBody(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("unexpected status %q body=%q", statusLine, body)
	}
	t.Logf("sing-box live egress ifconfig.me response=%q", strings.TrimSpace(body))
}

func writeHTTPRequest(conn net.Conn, host string) error {
	_, err := fmt.Fprintf(conn, "GET /ip HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: whitetransport-singbox-smoke\r\n\r\n", host)
	return err
}

func readHTTPStatusAndBody(conn net.Conn) (string, string, error) {
	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return "", "", err
	}
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		if line == "\r\n" {
			rest, err := io.ReadAll(reader)
			if err != nil {
				return "", "", err
			}
			return strings.TrimSpace(statusLine), string(rest), nil
		}
	}
}
