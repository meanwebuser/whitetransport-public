package transport

import (
	"fmt"
	"strings"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

// resolveAdminRelayConfig injects one identity-bound relay principal from the
// canonical TokenStore. Runtime startup rejects legacy inline/env credentials
// so production cannot silently fall back to a shared admin token.
func resolveAdminRelayConfig(cfg config.AdminRelayConfig, store *tokens.Store, defaultIdentity string) (config.AdminRelayConfig, error) {
	if !cfg.Enabled {
		return cfg, nil
	}
	if strings.TrimSpace(cfg.Token) != "" || strings.TrimSpace(cfg.TokenEnv) != "" {
		return config.AdminRelayConfig{}, fmt.Errorf("admin relay credentials must come from TokenStore, not token or token_env")
	}
	identity := strings.TrimSpace(cfg.Identity)
	if identity == "" {
		identity = strings.TrimSpace(defaultIdentity)
	}
	if identity == "" {
		return config.AdminRelayConfig{}, fmt.Errorf("admin relay identity is required")
	}
	if store == nil {
		return config.AdminRelayConfig{}, fmt.Errorf("admin relay TokenStore is required for identity %q", identity)
	}

	var credential *tokens.Token
	ref := strings.TrimSpace(cfg.TokenRef)
	if ref != "" {
		var ok bool
		credential, ok = store.Get(ref)
		if !ok || !credential.IsActive() {
			return config.AdminRelayConfig{}, fmt.Errorf("admin relay token_ref %q is missing or inactive", ref)
		}
	} else {
		var err error
		credential, err = store.ResolveOne("admin", "relay", identity)
		if err != nil {
			return config.AdminRelayConfig{}, fmt.Errorf("resolve admin relay credential for %q: %w", identity, err)
		}
	}
	if credential.Platform != "admin" {
		return config.AdminRelayConfig{}, fmt.Errorf("admin relay credential %q has platform %q, want admin", credential.ID, credential.Platform)
	}
	if strings.TrimSpace(credential.Value) == "" {
		return config.AdminRelayConfig{}, fmt.Errorf("admin relay credential %q has empty value", credential.ID)
	}
	cfg.Identity = identity
	cfg.Token = credential.Value
	cfg.TokenEnv = ""
	return cfg, nil
}
