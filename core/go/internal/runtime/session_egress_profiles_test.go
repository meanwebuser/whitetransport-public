package runtime

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestEncryptedVLESSProfileRetainsTrustedLocalSidecarRuntime(t *testing.T) {
	endpoint := carriers.Endpoint{
		ID:      "xray-session-1",
		Carrier: carriers.CarrierSingBoxVLESS,
		Address: "edge.example:443",
	}
	profile := session.EgressProfile{
		Version:    session.EgressProfileVersion,
		EndpointID: endpoint.ID,
		Carrier:    endpoint.Carrier,
		URI:        "vless://11111111-1111-4111-8111-111111111111@edge.example:443?security=tls#test",
	}
	localRuntime := config.SessionEgressConfig{SingBox: &config.SessionSingBoxRuntimeConfig{
		BinaryPath:       "/bundle/sing-box",
		ConfigDir:        "/private/runtime",
		LocalListen:      "127.0.0.1:0",
		StartTimeoutSecs: 17,
	}}

	binding, err := sessionBindingFromEgressProfileWithRuntime(endpoint, profile, localRuntime)
	if err != nil {
		t.Fatalf("sessionBindingFromEgressProfile: %v", err)
	}
	carrier, ok := binding.Carrier.(*carriers.SingBoxVLESSCarrier)
	if !ok {
		t.Fatalf("carrier type = %T, want *SingBoxVLESSCarrier", binding.Carrier)
	}
	got := carrier.Config()
	if got.BinaryPath != localRuntime.SingBox.BinaryPath || got.ConfigDir != localRuntime.SingBox.ConfigDir || got.LocalListen != localRuntime.SingBox.LocalListen || got.StartTimeoutSecs != localRuntime.SingBox.StartTimeoutSecs {
		t.Fatalf("local sidecar settings were lost: %+v", got)
	}
	if got.Server != "edge.example" || got.UUID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("encrypted profile was not retained in memory: %+v", got)
	}
}
