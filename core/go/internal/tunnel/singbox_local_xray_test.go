package tunnel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

// TestManagedSingBoxVLESSLocalXrayResponseBytes proves the managed sidecar
// returns response bytes through a real local VLESS/Xray path. It is opt-in
// because both external runtime binaries must be explicitly provisioned by
// the test environment; it never needs tokens or internet access.
func TestManagedSingBoxVLESSLocalXrayResponseBytes(t *testing.T) {
	if os.Getenv("WT_SINGBOX_LOCAL_XRAY") != "1" {
		t.Skip("set WT_SINGBOX_LOCAL_XRAY=1 with WT_XRAY_BINARY and WT_SINGBOX_BINARY")
	}
	xrayBinary := strings.TrimSpace(os.Getenv("WT_XRAY_BINARY"))
	singBoxBinary := strings.TrimSpace(os.Getenv("WT_SINGBOX_BINARY"))
	if xrayBinary == "" || singBoxBinary == "" {
		t.Fatal("WT_XRAY_BINARY and WT_SINGBOX_BINARY are required")
	}
	for _, binary := range []string{xrayBinary, singBoxBinary} {
		if info, err := os.Stat(binary); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			t.Fatalf("runtime binary %q is not executable: %v", binary, err)
		}
	}

	const testUUID = "11111111-1111-4111-8111-111111111111"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	xrayAddr, xrayOutput := startLocalVLESSXray(t, ctx, xrayBinary, testUUID)

	const responseNonce = "managed-singbox-local-xray-response"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, responseNonce)
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse local target URL: %v", err)
	}

	serverHost, serverPortText, err := net.SplitHostPort(xrayAddr)
	if err != nil {
		t.Fatalf("split Xray address: %v", err)
	}
	serverPort, err := net.LookupPort("tcp", serverPortText)
	if err != nil {
		t.Fatalf("parse Xray port: %v", err)
	}
	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server:           serverHost,
		ServerPort:       serverPort,
		UUID:             testUUID,
		Network:          "tcp",
		TransportType:    "tcp",
		BinaryPath:       singBoxBinary,
		ConfigDir:        t.TempDir(),
		LocalListen:      "127.0.0.1:0",
		StartTimeoutSecs: 10,
	})
	if err != nil {
		t.Fatalf("new sing-box VLESS carrier: %v", err)
	}
	endpoint := carriers.Endpoint{ID: "local-xray-vless", Carrier: carriers.CarrierSingBoxVLESS, Address: xrayAddr}
	tunnel := NewSingBoxTunnel(map[string]policy.CarrierBinding{
		carriers.CarrierSingBoxVLESS: {Carrier: carrier, Endpoint: endpoint},
	})
	defer func() { _ = tunnel.Close() }()

	conn, err := tunnel.DialContext(ctx, endpoint, targetURL.Host)
	if err != nil {
		t.Fatalf("dial through managed sing-box sidecar: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET /response HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", targetURL.Host); err != nil {
		t.Fatalf("write HTTP request through sidecar: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set response deadline: %v", err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read response bytes through sidecar: %v; xray output=%s", err, xrayOutput.String())
	}
	if !bytes.Contains(response, []byte("200 OK")) || !bytes.Contains(response, []byte(responseNonce)) {
		t.Fatalf("unexpected sidecar response: %q", response)
	}
}

// startLocalVLESSXray launches a loopback-only VLESS server whose only
// outbound is direct access to the test HTTP listener.
func startLocalVLESSXray(t *testing.T, ctx context.Context, binary, uuid string) (string, *bytes.Buffer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Xray port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release Xray port: %v", err)
	}
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split reserved Xray address: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "xray-local-vless.json")
	config := fmt.Sprintf(`{
  "log": {"loglevel": "debug"},
  "inbounds": [{
    "listen": "127.0.0.1",
    "port": %s,
    "protocol": "vless",
    "settings": {"clients": [{"id": %q}], "decryption": "none"},
    "streamSettings": {"network": "tcp"}
  }],
  "outbounds": [{
    "protocol": "freedom",
    "tag": "direct",
    "settings": {"finalRules": [{"action": "allow"}]}
  }]
}`, portText, uuid)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write local Xray config: %v", err)
	}
	var output bytes.Buffer
	command := exec.CommandContext(ctx, binary, "run", "-c", configPath)
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start local Xray: %v", err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return address, &output
		}
		select {
		case <-ctx.Done():
			t.Fatalf("local Xray did not start: %v; output=%s", ctx.Err(), output.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("local Xray did not listen on %s; output=%s", address, output.String())
	return "", &output
}
