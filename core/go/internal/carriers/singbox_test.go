package carriers

import "testing"

const testVLESSUUID = "11111111-1111-4111-8111-111111111111"
const testVLESSURI = "vless://" + testVLESSUUID + "@exit-node.example.invalid:443?type=httpupgrade&host=&path=/hup&security=tls&sni=exit-node.example.invalid&fp=chrome&allowInsecure=0#DE-HTTPUpgrade"

func TestParseSingBoxVLESSURI(t *testing.T) {
	cfg, err := ParseSingBoxVLESSURI(testVLESSURI)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "exit-node.example.invalid" || cfg.ServerPort != 443 {
		t.Fatalf("unexpected server: %+v", cfg)
	}
	if cfg.UUID != testVLESSUUID {
		t.Fatalf("unexpected uuid %q", cfg.UUID)
	}
	if !cfg.TLSEnabled || cfg.TLSServerName != "exit-node.example.invalid" || cfg.TLSInsecure {
		t.Fatalf("unexpected tls config: %+v", cfg)
	}
	if cfg.UTLSFingerprint != "chrome" {
		t.Fatalf("unexpected utls fingerprint %q", cfg.UTLSFingerprint)
	}
	if cfg.TransportType != "httpupgrade" || cfg.TransportPath != "/hup" || cfg.TransportHost != "" {
		t.Fatalf("unexpected transport config: %+v", cfg)
	}
}

// TestParseSingBoxVLESSURIKeepsRealityFlowAndGRPCServiceName protects fields
// which are not interchangeable with generic transport paths. Losing either
// produces a syntactically valid sidecar profile that cannot authenticate.
func TestParseSingBoxVLESSURIKeepsRealityFlowAndGRPCServiceName(t *testing.T) {
	grpcURI := "vless://" + testVLESSUUID + "@exit-node.example.invalid:443?type=grpc&serviceName=Tun&security=tls&sni=exit-node.example.invalid&fp=chrome&allowInsecure=0"
	grpc, err := ParseSingBoxVLESSURI(grpcURI)
	if err != nil {
		t.Fatal(err)
	}
	if grpc.TransportPath != "Tun" {
		t.Fatalf("gRPC service name = %q, want Tun", grpc.TransportPath)
	}

	realityURI := "vless://" + testVLESSUUID + "@exit-node.example.invalid:23443?type=tcp&security=reality&sni=ya.ru&fp=chrome&pbk=public-key&sid=abcd&flow=xtls-rprx-vision"
	reality, err := ParseSingBoxVLESSURI(realityURI)
	if err != nil {
		t.Fatal(err)
	}
	if reality.Flow != "xtls-rprx-vision" {
		t.Fatalf("Reality flow = %q, want xtls-rprx-vision", reality.Flow)
	}
}

func TestNewSingBoxVLESSCarrierParsesURIAndKeepsLocalSettings(t *testing.T) {
	carrier, err := NewSingBoxVLESSCarrier(SingBoxVLESSConfig{
		URI:              testVLESSURI,
		BinaryPath:       "/usr/local/bin/sing-box",
		LocalListen:      "127.0.0.1:18080",
		StartTimeoutSecs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := carrier.Config()
	if cfg.BinaryPath != "/usr/local/bin/sing-box" || cfg.LocalListen != "127.0.0.1:18080" || cfg.StartTimeoutSecs != 2 {
		t.Fatalf("local settings were not preserved: %+v", cfg)
	}
	if carrier.Descriptor().ID != CarrierSingBoxVLESS || carrier.Descriptor().Mode != DeliveryStream {
		t.Fatalf("unexpected descriptor %+v", carrier.Descriptor())
	}
}
