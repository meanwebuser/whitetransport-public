package runtime

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"testing"
)

const explicitXrayTestUUID = "22222222-2222-4222-8222-222222222222"

// TestXrayRealityProfileKeepsInboundSNIAndFlow prevents generic TLS proxy
// settings from corrupting a Reality route. Reality accepts only the SNI
// configured on its own inbound and requires the client's flow value.
func TestXrayRealityProfileKeepsInboundSNIAndFlow(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(xrayPublicHostEnv, "exit-node.example.invalid")
	t.Setenv(xrayPublicSNIEnv, "exit-node.example.invalid")

	carrierConfig, err := xrayRealityToSingBoxConfig(xrayInbound{
		Tag:    "de-reality",
		Listen: "0.0.0.0",
		Port:   23443,
		Settings: xrayInboundSetting{Clients: []xrayClient{{
			ID:   "11111111-1111-4111-8111-111111111111",
			Flow: "xtls-rprx-vision",
		}}},
		StreamSettings: xrayStreamSettings{
			Network:  "tcp",
			Security: "reality",
			RealitySettings: &xrayRealitySettings{
				PrivateKey:  base64.RawURLEncoding.EncodeToString(privateKey.Bytes()),
				ServerNames: []string{"ya.ru"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(carrierConfig.SingBox.URI)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("sni"); got != "ya.ru" {
		t.Fatalf("Reality SNI = %q, want inbound server name ya.ru", got)
	}
	if got := parsed.Query().Get("flow"); got != "xtls-rprx-vision" {
		t.Fatalf("Reality flow = %q, want xtls-rprx-vision", got)
	}
}

func TestXrayGRPCServiceNameNormalizesLeadingSlash(t *testing.T) {
	_, serviceName := xrayTransportSettings(xrayStreamSettings{
		GRPCSettings: &xrayGRPCSettings{ServiceName: "/grpc"},
	}, "grpc")
	if serviceName != "grpc" {
		t.Fatalf("gRPC service name = %q, want grpc", serviceName)
	}
}

// TestXrayTransportRejectsExplicitUUIDMissingFromInbound prevents a stale
// service override from advertising a VLESS profile that Xray cannot accept.
func TestXrayTransportRejectsExplicitUUIDMissingFromInbound(t *testing.T) {
	t.Setenv(xrayClientUUIDEnv, explicitXrayTestUUID)
	t.Setenv(xrayPublicHostEnv, "exit-node.example.invalid")
	inbound := xrayInbound{
		Tag:      "us-httpupgrade",
		Listen:   "0.0.0.0",
		Port:     443,
		Protocol: "vless",
		Settings: xrayInboundSetting{Clients: []xrayClient{{
			ID:    "11111111-1111-4111-8111-111111111111",
			Email: "active@example.invalid",
		}}},
		StreamSettings: xrayStreamSettings{
			Network: "httpupgrade",
			HTTPUpgradeSettings: &xrayHTTPUpgradeSettings{
				Host: "exit-node.example.invalid",
				Path: "/transport",
			},
		},
	}

	if _, err := xrayInboundToSingBoxConfig(inbound); err == nil {
		t.Fatal("stale explicit UUID was accepted despite a non-empty inbound client list")
	}
}

// TestXrayRealityExplicitUUIDDoesNotRequireInboundClient protects staged
// rotation when the local server config intentionally omits its client list.
func TestXrayRealityExplicitUUIDDoesNotRequireInboundClient(t *testing.T) {
	t.Setenv(xrayClientUUIDEnv, explicitXrayTestUUID)
	t.Setenv(xrayClientEmailEnv, "missing@example.invalid")

	carrierConfig, err := xrayRealityToSingBoxConfig(testXrayRealityInbound(t, nil))
	if err != nil {
		t.Fatalf("explicit UUID rejected: %v", err)
	}
	parsed, err := url.Parse(carrierConfig.SingBox.URI)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.User.Username(); got != explicitXrayTestUUID {
		t.Fatalf("Reality UUID = %q, want explicit override %q", got, explicitXrayTestUUID)
	}
	if got := parsed.Query().Get("flow"); got != "" {
		t.Fatalf("Reality flow = %q, want empty for an UUID absent from inbound clients", got)
	}
}

func TestXrayRealityRejectsExplicitUUIDMissingFromInbound(t *testing.T) {
	t.Setenv(xrayClientUUIDEnv, explicitXrayTestUUID)
	inbound := testXrayRealityInbound(t, []xrayClient{{ID: "11111111-1111-4111-8111-111111111111"}})
	if _, err := xrayRealityToSingBoxConfig(inbound); err == nil {
		t.Fatal("stale explicit UUID was accepted despite a non-empty inbound client list")
	}
}

// TestXrayRealityExplicitUUIDKeepsMatchingNonFirstClientFlow ensures explicit
// selection is by UUID, not by client order or an unrelated email selector.
func TestXrayRealityExplicitUUIDKeepsMatchingNonFirstClientFlow(t *testing.T) {
	t.Setenv(xrayClientUUIDEnv, explicitXrayTestUUID)
	t.Setenv(xrayClientEmailEnv, "first@example.invalid")
	inbound := testXrayRealityInbound(t, []xrayClient{
		{ID: "11111111-1111-4111-8111-111111111111", Email: "first@example.invalid", Flow: "first-flow"},
		{ID: explicitXrayTestUUID, Email: "second@example.invalid", Flow: "xtls-rprx-vision"},
	})

	carrierConfig, err := xrayRealityToSingBoxConfig(inbound)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(carrierConfig.SingBox.URI)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.User.Username(); got != explicitXrayTestUUID {
		t.Fatalf("Reality UUID = %q, want explicit override %q", got, explicitXrayTestUUID)
	}
	if got := parsed.Query().Get("flow"); got != "xtls-rprx-vision" {
		t.Fatalf("Reality flow = %q, want matching non-first client flow", got)
	}
}

func testXrayRealityInbound(t *testing.T, clients []xrayClient) xrayInbound {
	t.Helper()
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(xrayPublicHostEnv, "exit-node.example.invalid")
	return xrayInbound{
		Tag:      "de-reality",
		Listen:   "0.0.0.0",
		Port:     23443,
		Settings: xrayInboundSetting{Clients: clients},
		StreamSettings: xrayStreamSettings{
			Network:  "tcp",
			Security: "reality",
			RealitySettings: &xrayRealitySettings{
				PrivateKey:  base64.RawURLEncoding.EncodeToString(privateKey.Bytes()),
				ServerNames: []string{"ya.ru"},
			},
		},
	}
}
