package carriers

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestParseHysteria2URI(t *testing.T) {
	cfg, err := ParseHysteria2URI("hysteria2://secret-pass@example.test:8443/?sni=relay.example&insecure=1&obfs=salamander&obfs-password=obfs-pass")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "example.test" || cfg.ServerPort != 8443 || cfg.Password != "secret-pass" || cfg.TLSServerName != "relay.example" || !cfg.TLSInsecure || cfg.ObfsType != "salamander" || cfg.ObfsPassword != "obfs-pass" {
		t.Fatalf("parsed Hysteria2 config = %+v", cfg)
	}
}

func TestHysteria2AdapterUsesCommonManagedStreamContract(t *testing.T) {
	carrier, err := NewSingBoxHysteria2Carrier(Hysteria2Config{
		URI:         "hy2://secret-pass@example.test:443?sni=relay.example",
		BinaryPath:  "/bundle/sing-box",
		LocalListen: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	var _ Carrier = carrier
	var _ StreamDialer = carrier
	var _ ManagedStreamCarrier = carrier
	if carrier.Descriptor().ID != CarrierHysteria2 || !HasCapability(carrier.Descriptor(), CapAuthenticated) {
		t.Fatalf("descriptor = %+v", carrier.Descriptor())
	}
	data, err := carrier.SidecarSpec().ConfigJSON("127.0.0.1:0", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	outbounds := doc["outbounds"].([]any)
	outbound := outbounds[0].(map[string]any)
	if outbound["type"] != "hysteria2" || outbound["server"] != "203.0.113.10" {
		t.Fatalf("sidecar outbound = %+v", outbound)
	}
	tls, ok := outbound["tls"].(map[string]any)
	if !ok || tls["alpn"] == nil {
		t.Fatalf("Hysteria2 sidecar TLS config = %+v, want explicit h3 ALPN", outbound["tls"])
	}
	alpn, ok := tls["alpn"].([]any)
	if !ok || len(alpn) != 1 || alpn[0] != "h3" {
		t.Fatalf("Hysteria2 sidecar ALPN = %+v, want [h3]", tls["alpn"])
	}
	if strings.Contains(string(data), "secret-pass") {
		// The generated child config is intentionally private runtime data. This
		// assertion only documents that it contains the credential for sing-box;
		// diagnostics and receipts must never print this byte slice.
		t.Log("sidecar config contains the in-memory authentication field")
	}
}

func TestTailscaleProbeRequiresAuthenticatedStatusAndProxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	carrier, err := NewTailscaleCarrier(TailscaleConfig{Peer: "100.64.0.10", ProxyAddress: listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	carrier.statusFunc = func(context.Context) error { return nil }
	metrics, err := carrier.Probe(context.Background(), Endpoint{ID: "tailnet", Carrier: CarrierTailscale})
	if err != nil || !metrics.Healthy || metrics.Latency <= 0 {
		t.Fatalf("probe metrics=%+v err=%v", metrics, err)
	}
	if latest, ok := carrier.Descriptor().Evidence.Latest(); !ok || latest.Source != MetricSourceProbe {
		t.Fatalf("posterior evidence missing: %+v/%v", latest, ok)
	}
}

func TestTailscaleDialRequiresAuthenticatedPeerBeforeProxyUse(t *testing.T) {
	carrier, err := NewTailscaleCarrier(TailscaleConfig{Peer: "100.64.0.10", ProxyAddress: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	carrier.statusFunc = func(context.Context) error { return context.Canceled }
	if _, err := carrier.DialStream(context.Background(), Endpoint{ID: "tailnet", Carrier: CarrierTailscale}, "example.com:443"); err == nil || !strings.Contains(err.Error(), "authentication preflight") {
		t.Fatalf("DialStream error = %v, want authenticated preflight failure", err)
	}
}
