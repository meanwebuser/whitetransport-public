package runtime

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
)

func TestBuildProviderConfigAcceptsInMemoryWBStreamClientSession(t *testing.T) {
	providerConfig, err := BuildProviderConfigWithTokenStore(config.CarrierConfig{
		ID:          "wbstream.vp8",
		CarrierType: "wbstream",
		WBStream: &config.WBStreamConfig{
			AccessToken:  "local-access",
			CookieHeader: "local-cookie",
		},
	}, nil, config.RoleClient)
	if err != nil {
		t.Fatalf("BuildProviderConfigWithTokenStore: %v", err)
	}
	if got := providerConfig.Credentials["access_token"]; got != "local-access" {
		t.Fatalf("access token = %q, want local in-memory value", got)
	}
	if got := providerConfig.Credentials["cookie_header"]; got != "local-cookie" {
		t.Fatalf("cookie header = %q, want local in-memory value", got)
	}
}
