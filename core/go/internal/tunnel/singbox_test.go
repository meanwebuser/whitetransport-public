package tunnel

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

func TestResolveSingBoxServerKeepsTLSAndTransportNames(t *testing.T) {
	previousLookup := lookupSingBoxIP
	lookupSingBoxIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "vusa.example" {
			t.Fatalf("lookup host = %q", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("2001:db8::9")}, {IP: net.ParseIP("203.0.113.9")}}, nil
	}
	t.Cleanup(func() { lookupSingBoxIP = previousLookup })

	cfg := carriers.SingBoxVLESSConfig{
		Server:        "vusa.example",
		ServerPort:    443,
		UUID:          "11111111-1111-4111-8111-111111111111",
		TLSEnabled:    true,
		TLSServerName: "vusa.example",
		TransportType: "httpupgrade",
		TransportHost: "vusa.example",
		TransportPath: "/hup",
	}
	resolved, err := resolveSingBoxServer(context.Background(), cfg.Server)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server = resolved
	data, err := buildSingBoxConfigJSON(cfg, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	proxy := doc["outbounds"].([]any)[0].(map[string]any)
	if proxy["server"] != "203.0.113.9" {
		t.Fatalf("outbound server = %v, want pre-resolved IPv4", proxy["server"])
	}
	if proxy["tls"].(map[string]any)["server_name"] != "vusa.example" {
		t.Fatalf("TLS server name changed: %+v", proxy["tls"])
	}
	transport := proxy["transport"].(map[string]any)
	headers, ok := transport["headers"].(map[string]any)
	if !ok || headers["Host"] != "vusa.example" {
		t.Fatalf("HTTPUpgrade Host changed: %+v", transport)
	}
}

func TestBuildSingBoxConfigJSONForVLESSHTTPUpgrade(t *testing.T) {
	data, err := buildSingBoxConfigJSON(carriers.SingBoxVLESSConfig{
		Server:          "node.example.invalid",
		ServerPort:      443,
		UUID:            "11111111-1111-4111-8111-111111111111",
		Network:         "tcp",
		TLSEnabled:      true,
		TLSServerName:   "node.example.invalid",
		UTLSFingerprint: "chrome",
		TransportType:   "httpupgrade",
		TransportPath:   "/hup",
	}, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	outbounds := doc["outbounds"].([]any)
	proxy := outbounds[0].(map[string]any)
	if proxy["type"] != "vless" || proxy["server"] != "node.example.invalid" || proxy["server_port"].(float64) != 443 {
		t.Fatalf("unexpected proxy outbound: %+v", proxy)
	}
	tlsConfig := proxy["tls"].(map[string]any)
	if tlsConfig["server_name"] != "node.example.invalid" || tlsConfig["insecure"].(bool) {
		t.Fatalf("unexpected tls config: %+v", tlsConfig)
	}
	transport := proxy["transport"].(map[string]any)
	if transport["type"] != "httpupgrade" || transport["path"] != "/hup" {
		t.Fatalf("unexpected transport: %+v", transport)
	}
}

// TestBuildSingBoxConfigJSONForVLESSGRPC makes the gRPC service explicit.
// A generic transport.path is accepted by JSON encoding but ignored by
// sing-box, turning the configured service into an invalid request path.
func TestBuildSingBoxConfigJSONForVLESSGRPC(t *testing.T) {
	data, err := buildSingBoxConfigJSON(carriers.SingBoxVLESSConfig{
		Server:        "node.example.invalid",
		ServerPort:    443,
		UUID:          "11111111-1111-4111-8111-111111111111",
		Network:       "tcp",
		TLSEnabled:    true,
		TLSServerName: "node.example.invalid",
		TransportType: "grpc",
		TransportPath: "Tun",
	}, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	proxy := doc["outbounds"].([]any)[0].(map[string]any)
	transport := proxy["transport"].(map[string]any)
	if transport["type"] != "grpc" || transport["service_name"] != "Tun" {
		t.Fatalf("unexpected gRPC transport: %+v", transport)
	}
	if _, hasPath := transport["path"]; hasPath {
		t.Fatalf("gRPC transport must not use generic path: %+v", transport)
	}
}

// TestBuildSingBoxConfigJSONOmitsTCPTransport protects Reality profiles: TCP
// is the absence of a sing-box transport block, not a valid transport type.
func TestBuildSingBoxConfigJSONOmitsTCPTransport(t *testing.T) {
	data, err := buildSingBoxConfigJSON(carriers.SingBoxVLESSConfig{
		Server:           "exit-node.example.invalid",
		ServerPort:       23443,
		UUID:             "11111111-1111-4111-8111-111111111111",
		Network:          "tcp",
		TransportType:    "tcp",
		TLSEnabled:       true,
		RealityEnabled:   true,
		RealityPublicKey: "public-key",
	}, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	proxy := doc["outbounds"].([]any)[0].(map[string]any)
	if _, found := proxy["transport"]; found {
		t.Fatalf("tcp profile emitted unsupported transport block: %+v", proxy["transport"])
	}
}

func TestSingBoxTunnelDialsThroughMockMixedInbound(t *testing.T) {
	echoAddr, stopEcho := startTCPEchoServer(t)
	defer stopEcho()

	restoreRunner := SetSingBoxRunnerForTest(mockSingBoxRunner{})
	defer restoreRunner()

	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server:      "node.example.invalid",
		ServerPort:  443,
		UUID:        "11111111-1111-4111-8111-111111111111",
		LocalListen: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierSingBoxVLESS: {
			Carrier: carrier,
			Endpoint: carriers.Endpoint{
				ID:      "singbox-example",
				Carrier: carriers.CarrierSingBoxVLESS,
				Address: "node.example.invalid:443",
			},
		},
	}
	tunnel := NewSingBoxTunnel(bindings)
	if tunnel == nil {
		t.Fatal("expected sing-box tunnel")
	}
	defer tunnel.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := tunnel.DialContext(ctx, bindings[carriers.CarrierSingBoxVLESS].Endpoint, echoAddr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("expected ping echo, got %q", string(buf))
	}
}

func TestSingBoxTunnelRedactsBoundedSidecarTailOnSOCKSFailure(t *testing.T) {
	restoreRunner := SetSingBoxRunnerForTest(tailSingBoxRunner{tail: "vless://11111111-1111-4111-8111-111111111111@edge.example:443?token=secret-should-not-escape" + strings.Repeat("x", 9000)})
	defer restoreRunner()

	endpoint := carriers.Endpoint{ID: "xray-session-1", Carrier: carriers.CarrierSingBoxVLESS, Address: "edge.example:443"}
	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server:      "edge.example",
		ServerPort:  443,
		UUID:        "11111111-1111-4111-8111-111111111111",
		LocalListen: "127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	tunnel := NewSingBoxTunnel(map[string]policy.CarrierBinding{
		carriers.CarrierSingBoxVLESS: {Carrier: carrier, Endpoint: endpoint},
	})
	defer tunnel.Close()

	_, err = tunnel.DialContext(context.Background(), endpoint, "example.com:80")
	if err == nil {
		t.Fatal("DialContext succeeded with an unavailable sidecar listener")
	}
	message := err.Error()
	if !strings.Contains(message, "vless://[REDACTED]") || strings.Contains(message, "11111111-1111-4111-8111-111111111111") || strings.Contains(message, "edge.example:443") || len(message) > 9000 {
		t.Fatalf("unsafe or unbounded sidecar diagnostic: %q", message)
	}
}

// TestSingBoxTunnelSupportsSessionXrayEndpoint ensures a node-issued Xray
// binding identity stays on the SingBox tunnel. Without this, CompositeTunnel
// can fall through to an envelope/control tunnel after a remote session answer.
func TestSingBoxTunnelSupportsSessionXrayEndpoint(t *testing.T) {
	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server:      "de.example.invalid",
		ServerPort:  443,
		UUID:        "11111111-1111-4111-8111-111111111111",
		LocalListen: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseBinding := policy.CarrierBinding{
		Carrier: carrier,
		Endpoint: carriers.Endpoint{
			ID:      "singbox-de",
			Carrier: carriers.CarrierSingBoxVLESS,
			Address: "de.example.invalid:443",
		},
	}
	tunnel := NewSingBoxTunnel(map[string]policy.CarrierBinding{carriers.CarrierSingBoxVLESS: baseBinding})
	if tunnel == nil {
		t.Fatal("expected sing-box tunnel")
	}
	defer tunnel.Close()

	sessionEndpoint := carriers.Endpoint{
		ID:      "xray-de-reality",
		Carrier: "xray-de-reality",
		Address: "de.example.invalid:23443",
	}
	tunnel.SetSessionBinding(sessionEndpoint, baseBinding)
	if !tunnel.SupportsEndpoint(sessionEndpoint) {
		t.Fatalf("session Xray endpoint is not supported: %+v", sessionEndpoint)
	}
}

// TestCompositeTunnelDialsSessionXrayEndpointThroughSingBox protects the
// dispatch order: a dynamic Xray binding must reach SingBox before the generic
// envelope tunnel sees its session binding.
func TestCompositeTunnelDialsSessionXrayEndpointThroughSingBox(t *testing.T) {
	echoAddr, stopEcho := startTCPEchoServer(t)
	defer stopEcho()
	restoreRunner := SetSingBoxRunnerForTest(mockSingBoxRunner{})
	defer restoreRunner()

	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server: "de.example.invalid", ServerPort: 443, UUID: "11111111-1111-4111-8111-111111111111", LocalListen: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseBinding := policy.CarrierBinding{Carrier: carrier, Endpoint: carriers.Endpoint{ID: "singbox-de", Carrier: carriers.CarrierSingBoxVLESS, Address: "de.example.invalid:443"}}
	bindings := map[string]policy.CarrierBinding{carriers.CarrierSingBoxVLESS: baseBinding}
	singBoxTunnel := NewSingBoxTunnel(bindings)
	if singBoxTunnel == nil {
		t.Fatal("expected sing-box tunnel")
	}
	defer singBoxTunnel.Close()
	composite := NewCompositeTunnel(NewCarrierTunnel("client", bindings), singBoxTunnel)
	sessionEndpoint := carriers.Endpoint{ID: "xray-de-reality", Carrier: "xray-de-reality", Address: "de.example.invalid:23443"}
	composite.SetSessionBinding(sessionEndpoint, baseBinding)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := composite.DialContext(ctx, sessionEndpoint, echoAddr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("xray")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "xray" {
		t.Fatalf("echo = %q, want xray", buf)
	}
}

func TestSingBoxTunnelKeepsOneProcessPerSessionEndpoint(t *testing.T) {
	restoreRunner := SetSingBoxRunnerForTest(mockSingBoxRunner{})
	defer restoreRunner()
	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{Server: "de.example.invalid", ServerPort: 443, UUID: "11111111-1111-4111-8111-111111111111", LocalListen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: carriers.Endpoint{ID: "singbox", Carrier: carriers.CarrierSingBoxVLESS}}
	tunnel := NewSingBoxTunnel(map[string]policy.CarrierBinding{carriers.CarrierSingBoxVLESS: binding})
	defer tunnel.Close()
	first := carriers.Endpoint{ID: "xray-de", Carrier: "xray-de", Address: "de.example.invalid:443"}
	second := carriers.Endpoint{ID: "xray-us", Carrier: "xray-us", Address: "us.example.invalid:443"}
	tunnel.SetSessionBinding(first, binding)
	tunnel.SetSessionBinding(second, binding)
	if _, err := tunnel.ensureProcess(context.Background(), first); err != nil {
		t.Fatalf("first process: %v", err)
	}
	if _, err := tunnel.ensureProcess(context.Background(), second); err != nil {
		t.Fatalf("second process: %v", err)
	}
	if len(tunnel.processes) != 2 {
		t.Fatalf("processes = %d, want one per endpoint", len(tunnel.processes))
	}
}

// TestSingBoxTunnelClearsStaleSessionProfileProcess reproduces reconnecting
// after the node rotates a profile under the same endpoint ID. The old
// sidecar must be stopped; otherwise its stale server address survives the
// new encrypted profile and all later SOCKS attempts use the old route.
func TestSingBoxTunnelClearsStaleSessionProfileProcess(t *testing.T) {
	restoreRunner := SetSingBoxRunnerForTest(mockSingBoxRunner{})
	defer restoreRunner()
	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{Server: "old.example.invalid", ServerPort: 443, UUID: "11111111-1111-4111-8111-111111111111", LocalListen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := carriers.Endpoint{ID: "xray-de-httpupgrade", Carrier: "xray-de-httpupgrade"}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}
	tunnel := NewSingBoxTunnel(map[string]policy.CarrierBinding{carriers.CarrierSingBoxVLESS: binding})
	defer tunnel.Close()
	tunnel.SetSessionBinding(endpoint, binding)
	if _, err := tunnel.ensureProcess(context.Background(), endpoint); err != nil {
		t.Fatalf("start old sidecar: %v", err)
	}
	state := tunnel.processes[endpoint.ID]
	process := state.process.(*mockSingBoxProcess)

	tunnel.ClearSessionBinding(endpoint)
	if len(tunnel.processes) != 0 {
		t.Fatalf("stale processes = %d, want 0", len(tunnel.processes))
	}
	select {
	case <-process.done:
	default:
		t.Fatal("stale sing-box process was not closed")
	}
}

// TestSingBoxTunnelClearSessionBindingRestoresCanonicalAlias protects the
// immutable configured route from a temporary session profile. A disconnected
// session must neither retain its credentials nor erase the configured alias.
func TestSingBoxTunnelClearSessionBindingRestoresCanonicalAlias(t *testing.T) {
	staticCarrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{Server: "static.example.invalid", ServerPort: 443, UUID: "11111111-1111-4111-8111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	dynamicCarrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{Server: "session.example.invalid", ServerPort: 443, UUID: "22222222-2222-4222-8222-222222222222"})
	if err != nil {
		t.Fatal(err)
	}
	staticBinding := policy.CarrierBinding{Carrier: staticCarrier, Endpoint: carriers.Endpoint{ID: "static-vless", Carrier: carriers.CarrierSingBoxVLESS}}
	dynamicEndpoint := carriers.Endpoint{ID: "xray-session", Carrier: "xray-session"}
	dynamicBinding := policy.CarrierBinding{Carrier: dynamicCarrier, Endpoint: dynamicEndpoint}
	tunnel := NewSingBoxTunnel(map[string]policy.CarrierBinding{carriers.CarrierSingBoxVLESS: staticBinding})

	tunnel.SetSessionBinding(dynamicEndpoint, dynamicBinding)
	tunnel.ClearSessionBinding(dynamicEndpoint)

	if _, found := tunnel.bindings[dynamicEndpoint.Carrier]; found {
		t.Fatal("dynamic session binding survived ClearSessionBinding")
	}
	restored, found := tunnel.bindings[carriers.CarrierSingBoxVLESS]
	if !found || restored.Carrier != staticCarrier {
		t.Fatalf("canonical binding = %+v, want original configured binding", restored.Endpoint)
	}
}

// TestSingBoxTunnelClearSessionBindingRemovesSyntheticCanonicalAlias covers a
// client with no configured VLESS route: disconnect must leave no alias that
// can reuse the prior session's address or credentials.
func TestSingBoxTunnelClearSessionBindingRemovesSyntheticCanonicalAlias(t *testing.T) {
	dynamicCarrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{Server: "session.example.invalid", ServerPort: 443, UUID: "22222222-2222-4222-8222-222222222222"})
	if err != nil {
		t.Fatal(err)
	}
	dynamicEndpoint := carriers.Endpoint{ID: "xray-session", Carrier: "xray-session"}
	tunnel := NewSingBoxTunnel(nil)
	tunnel.SetSessionBinding(dynamicEndpoint, policy.CarrierBinding{Carrier: dynamicCarrier, Endpoint: dynamicEndpoint})

	tunnel.ClearSessionBinding(dynamicEndpoint)

	if _, found := tunnel.bindings[carriers.CarrierSingBoxVLESS]; found {
		t.Fatal("synthetic canonical alias survived ClearSessionBinding")
	}
}

// TestUnifiedCarrierTunnelCloseClosesDynamicSingBoxSidecars ensures Close owns
// the delegated SingBoxTunnel lifecycle, including sidecars created only after
// a session answer installs an encrypted profile.
func TestUnifiedCarrierTunnelCloseClosesDynamicSingBoxSidecars(t *testing.T) {
	restoreRunner := SetSingBoxRunnerForTest(mockSingBoxRunner{})
	defer restoreRunner()
	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{Server: "session.example.invalid", ServerPort: 443, UUID: "22222222-2222-4222-8222-222222222222", LocalListen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := carriers.Endpoint{ID: "xray-session", Carrier: "xray-session"}
	tunnel := NewUnifiedCarrierTunnel("client", nil)
	tunnel.SetSessionBinding(endpoint, policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint})
	if _, err := tunnel.singBoxTunnel.ensureProcess(context.Background(), endpoint); err != nil {
		t.Fatalf("start dynamic sidecar: %v", err)
	}
	process := tunnel.singBoxTunnel.processes[endpoint.ID].process.(*mockSingBoxProcess)

	if err := tunnel.Close(); err != nil {
		t.Fatalf("close unified tunnel: %v", err)
	}
	select {
	case <-process.done:
	default:
		t.Fatal("UnifiedCarrierTunnel.Close left dynamic sing-box sidecar running")
	}
}

type mockSingBoxRunner struct{}

type tailSingBoxRunner struct{ tail string }

func (r tailSingBoxRunner) Start(_ context.Context, _ *carriers.SingBoxVLESSCarrier, _ carriers.Endpoint, _ string) (SingBoxProcess, string, error) {
	return tailSingBoxProcess{tail: r.tail}, "127.0.0.1:1", nil
}

type tailSingBoxProcess struct{ tail string }

func (p tailSingBoxProcess) Close() error { return nil }

func (p tailSingBoxProcess) DiagnosticTail() string { return p.tail }

func (mockSingBoxRunner) Start(ctx context.Context, carrier *carriers.SingBoxVLESSCarrier, endpoint carriers.Endpoint, localListen string) (SingBoxProcess, string, error) {
	listener, err := net.Listen("tcp", localListen)
	if err != nil {
		return nil, "", err
	}
	process := &mockSingBoxProcess{listener: listener, done: make(chan struct{})}
	go process.serve()
	return process, listener.Addr().String(), nil
}

type mockSingBoxProcess struct {
	listener net.Listener
	done     chan struct{}
}

func (p *mockSingBoxProcess) Close() error {
	err := p.listener.Close()
	<-p.done
	return err
}

func (p *mockSingBoxProcess) serve() {
	defer close(p.done)
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go handleMockSocksConn(conn)
	}
}

func handleMockSocksConn(conn net.Conn) {
	defer conn.Close()
	if err := readMockSocksGreeting(conn); err != nil {
		return
	}
	targetAddr, err := readMockSocksConnect(conn)
	if err != nil {
		return
	}
	target, err := net.Dial("tcp", targetAddr)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0})
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(target, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, target)
		done <- struct{}{}
	}()
	<-done
}

func readMockSocksGreeting(conn net.Conn) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	_, err := conn.Write([]byte{0x05, 0x00})
	return err
}

func readMockSocksConnect(conn net.Conn) (string, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", err
	}
	if header[1] != 0x01 {
		return "", fmt.Errorf("unsupported command %d", header[1])
	}
	var host string
	switch header[3] {
	case 0x01:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return "", err
		}
		host = net.IP(addr).String()
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", err
		}
		addr := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, addr); err != nil {
			return "", err
		}
		host = string(addr)
	default:
		return "", fmt.Errorf("unsupported address type %d", header[3])
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(portBytes))), nil
}
