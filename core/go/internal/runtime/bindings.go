package runtime

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

// BuildCarrierBindings creates executable carrier bindings from runtime config
// using the legacy hardcoded carrier factory.
// Descriptor-only planner config can omit carrier_configs; executors cannot.
func BuildCarrierBindings(cfg config.Config) (map[string]policy.CarrierBinding, error) {
	return buildBindings(cfg, nil)
}

// BuildCarrierBindingsWithRegistry creates executable carrier bindings from
// runtime config, trying the adapter registry for carriers the legacy factory
// does not know.
func BuildCarrierBindingsWithRegistry(cfg config.Config, registry *ProviderRegistry) (map[string]policy.CarrierBinding, error) {
	return buildBindings(cfg, registry)
}

// BuildCarrierBindingsWithRegistryAndTokens creates bindings with both adapter
// registry and optional token store support. When ts is non-nil, carriers
// resolve credentials via the token store first, falling back to legacy
// secretValue().
func BuildCarrierBindingsWithRegistryAndTokens(cfg config.Config, registry *ProviderRegistry, ts *tokens.Store) (map[string]policy.CarrierBinding, error) {
	return buildBindingsWithTokens(cfg, registry, ts)
}

func buildBindingsWithTokens(cfg config.Config, registry *ProviderRegistry, ts *tokens.Store) (map[string]policy.CarrierBinding, error) {
	cfg = addAutoDiscoveredXraySingBox(cfg)
	enabled := make(map[string]struct{}, len(cfg.EnabledCarriers))
	for _, id := range cfg.EnabledCarriers {
		enabledID := strings.TrimSpace(id)
		if enabledID != "" {
			enabled[enabledID] = struct{}{}
		}
	}

	bindings := make(map[string]policy.CarrierBinding, len(cfg.CarrierConfigs))
	for _, carrierConfig := range cfg.CarrierConfigs {
		if strings.TrimSpace(carrierConfig.ID) == "" {
			return nil, fmt.Errorf("carrier config id is required")
		}
		if !carrierConfigEnabled(enabled, carrierConfig) {
			continue
		}
		binding, err := buildCarrierBindingWithTokens(carrierConfig, ts, cfg.Role, registry)
		if err != nil {
			return nil, fmt.Errorf("carrier %s: %w", carrierConfig.ID, err)
		}
		// Channel expansion: when WT_CHANNEL_BINDINGS=1 and a VK/OK messages
		// carrier has channels configured, create one binding per role. A
		// TokenStore-backed channel gets its own carrier instance so its
		// credential is resolved against that role's concrete peer instead of
		// reusing the token selected for the discovery endpoint.
		if expanded := expandChannelBindings(carrierConfig, binding); expanded != nil {
			for key, b := range expanded {
				if ts != nil && channelBindingCarrier(carrierConfig) {
					channelConfig := carrierConfig
					channelConfig.Endpoint.Address = b.Endpoint.Address
					if channelConfig.VKMessages != nil {
						vkMessages := *channelConfig.VKMessages
						vkMessages.Channels = nil
						channelConfig.VKMessages = &vkMessages
					}
					if channelConfig.OKMessages != nil {
						okMessages := *channelConfig.OKMessages
						okMessages.Channels = nil
						channelConfig.OKMessages = &okMessages
					}
					channelBinding, err := buildCarrierBindingWithTokens(channelConfig, ts, cfg.Role, registry)
					if err != nil {
						return nil, fmt.Errorf("carrier %s channel %s: %w", carrierConfig.ID, key, err)
					}
					channelBinding.Endpoint.ID = key
					channelBinding.Role = b.Role
					b = channelBinding
				}
				if _, exists := bindings[key]; exists {
					return nil, fmt.Errorf("duplicate binding key %s", key)
				}
				bindings[key] = b
			}
		} else {
			if _, exists := bindings[carrierConfig.ID]; exists {
				return nil, fmt.Errorf("duplicate carrier config %s", carrierConfig.ID)
			}
			bindings[carrierConfig.ID] = binding
		}
	}
	for enabledID := range enabled {
		if !hasBindingForEnabledCarrier(bindings, enabledID) {
			return nil, fmt.Errorf("enabled carrier %s has no carrier_config", enabledID)
		}
	}
	return bindings, nil
}

func channelBindingCarrier(cfg config.CarrierConfig) bool {
	runtimeID := carrierRuntimeIDFromConfig(cfg)
	return (runtimeID == carriers.CarrierVKMessages && cfg.VKMessages != nil) ||
		(runtimeID == carriers.CarrierOKMessages && cfg.OKMessages != nil)
}

func buildCarrierBindingWithTokens(cfg config.CarrierConfig, ts *tokens.Store, role config.Role, adapters ...*ProviderRegistry) (policy.CarrierBinding, error) {
	endpoint, err := endpointFromConfig(cfg)
	if err != nil {
		return policy.CarrierBinding{}, err
	}
	carrier, err := carrierFromConfigWithTokens(cfg, ts)
	if err != nil {
		if isNoRuntimeAdapterError(err) && len(adapters) > 0 && adapters[0] != nil {
			carrier, err = carrierFromProviderRegistry(cfg, endpoint, adapters[0], ts, role)
			if err == nil {
				return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}, nil
			}
		}
		return policy.CarrierBinding{}, err
	}
	return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}, nil
}

// carrierFromConfigWithTokens is the token-store-aware version of carrierFromConfig.
// When ts is non-nil, credentials are resolved via the token store first.
func carrierFromConfigWithTokens(cfg config.CarrierConfig, ts *tokens.Store) (carriers.Carrier, error) {
	// resolve is a helper: try token store, fall back to legacy secretValue.
	resolve := func(platform, connType, channelID, legacyValue, legacyEnv string) string {
		if explicit := strings.TrimSpace(legacyValue); explicit != "" {
			return explicit
		}
		if envValue := secretValue("", legacyEnv); strings.TrimSpace(envValue) != "" {
			return envValue
		}
		if ts != nil {
			tok, err := ts.ResolveOne(platform, connType, channelID)
			if err == nil {
				if tok.Value != "" {
					return tok.Value
				}
				// Composite tokens store credentials in Parts, not Value.
				if len(tok.Parts) > 0 {
					if v := tok.Parts["access_token"]; v != "" {
						return v
					}
					if v := tok.Parts["token"]; v != "" {
						return v
					}
				}
			}
			if err != nil {
				log.Printf("token store resolve %s/%s/%s: %v (falling back to legacy)", platform, connType, channelID, err)
			}
		}
		return ""
	}
	resolveComposite := func(platform, connType, channelID string) map[string]string {
		if ts != nil {
			tok, err := ts.ResolveOne(platform, connType, channelID)
			if err == nil && len(tok.Parts) > 0 {
				return tok.Parts
			}
		}
		return nil
	}

	runtimeID := carrierRuntimeIDFromConfig(cfg)
	switch runtimeID {
	case carriers.CarrierVKMessages:
		if cfg.VKMessages == nil {
			return nil, fmt.Errorf("vk_messages config is required")
		}
		addr := cfg.Endpoint.Address
		return carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{
			Token:      resolve("vk", "messages", addr, cfg.VKMessages.Token, cfg.VKMessages.TokenEnv),
			BaseURL:    cfg.VKMessages.BaseURL,
			APIVersion: cfg.VKMessages.APIVersion,
		})
	case carriers.CarrierOKMessages:
		if cfg.OKMessages == nil {
			return nil, fmt.Errorf("ok_messages config is required")
		}
		addr := cfg.Endpoint.Address
		return carriers.NewOKMessagesCarrier(carriers.OKMessagesConfig{
			Token:    resolve("ok", "messages", addr, cfg.OKMessages.Token, cfg.OKMessages.TokenEnv),
			BaseURL:  cfg.OKMessages.BaseURL,
			SendPath: cfg.OKMessages.SendPath,
			ReadPath: cfg.OKMessages.ReadPath,
		})
	case carriers.CarrierVKDocs256, carriers.CarrierVKDocs1024:
		if cfg.VKDocs == nil {
			return nil, fmt.Errorf("vk_docs config is required")
		}
		connType := "docs.256"
		if runtimeID == carriers.CarrierVKDocs1024 {
			connType = "docs.1024"
		}
		return carriers.NewVKDocsCarrier(carriers.VKDocsConfig{
			Token:        resolve("vk", connType, "*", cfg.VKDocs.Token, cfg.VKDocs.TokenEnv),
			BaseURL:      cfg.VKDocs.BaseURL,
			APIVersion:   cfg.VKDocs.APIVersion,
			DescriptorID: runtimeID,
		})
	case carriers.CarrierOKDocs256:
		if cfg.OKDocs == nil {
			return nil, fmt.Errorf("ok_docs config is required")
		}
		parts := resolveComposite("ok", "docs.256", "*")
		if parts != nil {
			return carriers.NewOKDocsCarrier(carriers.OKDocsConfig{
				AccessToken:      parts["access_token"],
				ApplicationKey:   parts["application_key"],
				SessionSecretKey: parts["session_secret_key"],
				BaseURL:          cfg.OKDocs.BaseURL,
				DescriptorID:     runtimeID,
			})
		}
		return carriers.NewOKDocsCarrier(carriers.OKDocsConfig{
			AccessToken:      secretValue(cfg.OKDocs.AccessToken, cfg.OKDocs.AccessTokenEnv),
			ApplicationKey:   secretValue(cfg.OKDocs.ApplicationKey, cfg.OKDocs.ApplicationKeyEnv),
			SessionSecretKey: secretValue(cfg.OKDocs.SessionSecretKey, cfg.OKDocs.SessionSecretKeyEnv),
			BaseURL:          cfg.OKDocs.BaseURL,
			DescriptorID:     runtimeID,
		})
	case carriers.CarrierYandexDisk:
		if cfg.YandexDisk == nil {
			return nil, fmt.Errorf("yandex_disk config is required")
		}
		cleanupAfterRead := false
		if cfg.YandexDisk.CleanupAfterRead != nil {
			cleanupAfterRead = *cfg.YandexDisk.CleanupAfterRead
		}
		minSendInterval := time.Duration(cfg.YandexDisk.MinSendIntervalMs) * time.Millisecond
		oauthToken := resolve("yandex", "disk.files", "*", cfg.YandexDisk.OAuthToken, cfg.YandexDisk.OAuthTokenEnv)
		var cookieHeader string
		if strings.TrimSpace(oauthToken) == "" {
			// Try cookie-based auth from token store.
			if parts := resolveComposite("yandex", "disk.files", "*"); parts != nil {
				cookieHeader = parts["cookie_header"]
			}
		}
		return carriers.NewYandexDiskCarrier(carriers.YandexDiskConfig{
			OAuthToken:       oauthToken,
			CookieHeader:     cookieHeader,
			BaseURL:          cfg.YandexDisk.BaseURL,
			BasePath:         cfg.YandexDisk.BasePath,
			MaxFileSizeBytes: cfg.YandexDisk.MaxFileSizeBytes,
			CleanupAfterRead: cleanupAfterRead,
			MinSendInterval:  minSendInterval,
		})
	case carriers.CarrierSSHTCP:
		if cfg.SSH == nil {
			return nil, fmt.Errorf("ssh config is required")
		}
		return carriers.NewSSHCarrier(carriers.SSHConfig{
			Username:                cfg.SSH.Username,
			Password:                resolve("ssh", "tcp", cfg.Endpoint.Address, cfg.SSH.Password, cfg.SSH.PasswordEnv),
			PrivateKey:              resolve("ssh", "private_key", cfg.Endpoint.Address, cfg.SSH.PrivateKey, cfg.SSH.PrivateKeyEnv),
			PrivateKeyPath:          cfg.SSH.PrivateKeyPath,
			PrivateKeyPassphrase:    secretValue(cfg.SSH.PrivateKeyPassphrase, cfg.SSH.PrivateKeyPassphraseEnv),
			UseAgent:                cfg.SSH.UseAgent,
			AgentSocketPath:         cfg.SSH.AgentSocketPath,
			HostKeys:                append([]string(nil), cfg.SSH.HostKeys...),
			ServerAliveIntervalSecs: cfg.SSH.ServerAliveIntervalSecs,
		})
	case carriers.CarrierSSHFabric:
		if cfg.SSHFabric == nil {
			return nil, fmt.Errorf("ssh_fabric config is required")
		}
		client := cfg.SSHFabric.Client
		return newSSHFabricCarrierFromConfig(carriers.SSHConfig{
			Username:                client.Username,
			Password:                resolve("ssh", "fabric.password", cfg.Endpoint.Address, client.Password, client.PasswordEnv),
			PrivateKey:              resolve("ssh", "fabric.private_key", cfg.Endpoint.Address, client.PrivateKey, client.PrivateKeyEnv),
			PrivateKeyPath:          client.PrivateKeyPath,
			PrivateKeyPassphrase:    secretValue(client.PrivateKeyPassphrase, client.PrivateKeyPassphraseEnv),
			UseAgent:                client.UseAgent,
			AgentSocketPath:         client.AgentSocketPath,
			HostKeys:                append([]string(nil), client.HostKeys...),
			ServerAliveIntervalSecs: client.ServerAliveIntervalSecs,
		}, cfg.SSHFabric.Server)
	case carriers.CarrierSingBoxVLESS:
		if cfg.SingBox == nil {
			return nil, fmt.Errorf("sing_box config is required")
		}
		return carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
			URI:              resolve("sing-box", "vless.uri", cfg.Endpoint.Address, cfg.SingBox.URI, cfg.SingBox.URIEnv),
			BinaryPath:       secretValue(cfg.SingBox.BinaryPath, cfg.SingBox.BinaryPathEnv),
			ConfigDir:        secretValue(cfg.SingBox.ConfigDir, cfg.SingBox.ConfigDirEnv),
			Server:           cfg.SingBox.Server,
			ServerPort:       cfg.SingBox.ServerPort,
			UUID:             resolve("sing-box", "vless.uuid", cfg.Endpoint.Address, cfg.SingBox.UUID, cfg.SingBox.UUIDEnv),
			Network:          cfg.SingBox.Network,
			Flow:             cfg.SingBox.Flow,
			TLSEnabled:       cfg.SingBox.TLSEnabled,
			TLSServerName:    cfg.SingBox.TLSServerName,
			TLSInsecure:      cfg.SingBox.TLSInsecure,
			UTLSFingerprint:  cfg.SingBox.UTLSFingerprint,
			TransportType:    cfg.SingBox.TransportType,
			TransportHost:    cfg.SingBox.TransportHost,
			TransportPath:    cfg.SingBox.TransportPath,
			LocalListen:      cfg.SingBox.LocalListen,
			StartTimeoutSecs: cfg.SingBox.StartTimeoutSecs,
		})
	case carriers.CarrierFileMailbox:
		if cfg.FileMailbox == nil {
			return nil, fmt.Errorf("file_mailbox config is required")
		}
		return carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{
			Dir:         secretValue(cfg.FileMailbox.Dir, cfg.FileMailbox.DirEnv),
			AllowEgress: cfg.FileMailbox.AllowEgress,
		})

	default:
		return nil, fmt.Errorf("no runtime adapter for carrier %s", runtimeID)
	}
}

func buildBindings(cfg config.Config, registry *ProviderRegistry) (map[string]policy.CarrierBinding, error) {
	cfg = addAutoDiscoveredXraySingBox(cfg)
	enabled := make(map[string]struct{}, len(cfg.EnabledCarriers))
	for _, id := range cfg.EnabledCarriers {
		enabledID := strings.TrimSpace(id)
		if enabledID != "" {
			enabled[enabledID] = struct{}{}
		}
	}

	bindings := make(map[string]policy.CarrierBinding, len(cfg.CarrierConfigs))
	for _, carrierConfig := range cfg.CarrierConfigs {
		if strings.TrimSpace(carrierConfig.ID) == "" {
			return nil, fmt.Errorf("carrier config id is required")
		}
		if !carrierConfigEnabled(enabled, carrierConfig) {
			continue
		}
		binding, err := buildCarrierBinding(carrierConfig, registry)
		if err != nil {
			return nil, fmt.Errorf("carrier %s: %w", carrierConfig.ID, err)
		}
		// Channel expansion (same logic as buildBindingsWithTokens).
		if expanded := expandChannelBindings(carrierConfig, binding); expanded != nil {
			for key, b := range expanded {
				if _, exists := bindings[key]; exists {
					return nil, fmt.Errorf("duplicate binding key %s", key)
				}
				bindings[key] = b
			}
		} else {
			if _, exists := bindings[carrierConfig.ID]; exists {
				return nil, fmt.Errorf("duplicate carrier config %s", carrierConfig.ID)
			}
			bindings[carrierConfig.ID] = binding
		}
	}
	for enabledID := range enabled {
		if !hasBindingForEnabledCarrier(bindings, enabledID) {
			return nil, fmt.Errorf("enabled carrier %s has no carrier_config", enabledID)
		}
	}
	return bindings, nil
}

func buildCarrierBinding(cfg config.CarrierConfig, adapters ...*ProviderRegistry) (policy.CarrierBinding, error) {
	endpoint, err := endpointFromConfig(cfg)
	if err != nil {
		return policy.CarrierBinding{}, err
	}
	carrier, err := carrierFromConfig(cfg)
	if err != nil {
		if isNoRuntimeAdapterError(err) && len(adapters) > 0 && adapters[0] != nil {
			carrier, err = carrierFromProviderRegistry(cfg, endpoint, adapters[0], nil, "")
			if err == nil {
				return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}, nil
			}
		}
		return policy.CarrierBinding{}, err
	}
	return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint}, nil
}

func isNoRuntimeAdapterError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "no runtime adapter for carrier ")
}

func carrierFromProviderRegistry(cfg config.CarrierConfig, endpoint carriers.Endpoint, registry *ProviderRegistry, tokenStore *tokens.Store, role config.Role) (carriers.Carrier, error) {
	// Registry factories are keyed by platform name (e.g. "wbstream"),
	// but carrier config IDs are qualified (e.g. "wbstream.vp8").
	// Extract the platform prefix for the registry lookup.
	providerName := cfg.ID
	if idx := strings.IndexByte(cfg.ID, '.'); idx > 0 {
		providerName = cfg.ID[:idx]
	}
	providerConfig, err := BuildProviderConfigWithTokenStore(cfg, tokenStore, role)
	if err != nil {
		return nil, err
	}
	adapter, err := registry.Create(providerName, providerConfig)
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	wrapped, err := carriers.NewProviderCarrier(adapter, endpoint)
	if err != nil {
		return nil, fmt.Errorf("wrap adapter: %w", err)
	}
	// Log if this carrier implements VideoTunnelAdapter for video tunnel functionality
	videoTunnelImpl, _ := adapter.(VideoTunnelAdapter)
	log.Printf("[runtime] carrier %s registry: video_tunnel_impl=%t adapter_type=%T", cfg.ID, videoTunnelImpl != nil, adapter)
	return wrapped, nil
}

func BuildProviderConfig(cfg config.CarrierConfig) provider.ProviderConfig {
	creds := make(map[string]string)
	endpoints := make(map[string]string)
	settings := make(map[string]any)

	if cfg.VKMessages != nil {
		if cfg.VKMessages.Token != "" {
			creds["token"] = cfg.VKMessages.Token
		}
		if cfg.VKMessages.BaseURL != "" {
			endpoints["api_url"] = cfg.VKMessages.BaseURL
		}
		if cfg.VKMessages.APIVersion != "" {
			settings["api_version"] = cfg.VKMessages.APIVersion
		}
	}
	if cfg.OKMessages != nil {
		if cfg.OKMessages.Token != "" {
			creds["token"] = cfg.OKMessages.Token
		}
		if cfg.OKMessages.BaseURL != "" {
			endpoints["api_url"] = cfg.OKMessages.BaseURL
		}
	}
	if cfg.WBStream != nil {
		if cfg.WBStream.AccessToken != "" {
			creds["access_token"] = cfg.WBStream.AccessToken
		}
		if cfg.WBStream.CookieHeader != "" {
			creds["cookie_header"] = cfg.WBStream.CookieHeader
		}
		if cfg.WBStream.LocalStoragePath != "" {
			creds["local_storage_path"] = cfg.WBStream.LocalStoragePath
		}
		if cfg.WBStream.CookiesFile != "" {
			creds["cookies_file"] = cfg.WBStream.CookiesFile
		}
		if cfg.WBStream.LocalStorageFile != "" {
			creds["local_storage_file"] = cfg.WBStream.LocalStorageFile
		}
	}
	if cfg.WhitelistBypass != nil {
		if url := secretValue(cfg.WhitelistBypass.ServerURL, cfg.WhitelistBypass.ServerURLEnv); url != "" {
			endpoints["server_url"] = url
		}
		if tok := secretValue(cfg.WhitelistBypass.AccessToken, cfg.WhitelistBypass.AccessTokenEnv); tok != "" {
			creds["access_token"] = tok
		}
		if cfg.WhitelistBypass.RoomToken != "" {
			creds["room_token"] = cfg.WhitelistBypass.RoomToken
		}
		if cfg.WhitelistBypass.DisplayName != "" {
			settings["display_name"] = cfg.WhitelistBypass.DisplayName
		}
		if cfg.WhitelistBypass.TunnelMode != "" {
			settings["tunnel_mode"] = cfg.WhitelistBypass.TunnelMode
		}
	}
	if cfg.Telemost != nil {
		if cfg.Telemost.JoinLink != "" {
			creds["join_link"] = cfg.Telemost.JoinLink
		}
		if cookie := secretValue(cfg.Telemost.Cookie, cfg.Telemost.CookieEnv); cookie != "" {
			creds["cookie"] = cookie
		}
		if cfg.Telemost.DisplayName != "" {
			settings["display_name"] = cfg.Telemost.DisplayName
		}
		if cfg.Telemost.Role != "" {
			settings["role"] = cfg.Telemost.Role
		}
	}
	if cfg.Dion != nil {
		if cfg.Dion.EventID != "" {
			creds["event_id"] = cfg.Dion.EventID
		}
		if tok := secretValue(cfg.Dion.AccessToken, cfg.Dion.AccessTokenEnv); tok != "" {
			creds["access_token"] = tok
		}
		if tok := secretValue(cfg.Dion.RefreshToken, cfg.Dion.RefreshTokenEnv); tok != "" {
			creds["refresh_token"] = tok
		}
		if cfg.Dion.CookiesFile != "" {
			creds["cookies_file"] = cfg.Dion.CookiesFile
		}
		if cfg.Dion.DisplayName != "" {
			settings["display_name"] = cfg.Dion.DisplayName
		}
		if cfg.Dion.Role != "" {
			settings["role"] = cfg.Dion.Role
		}
	}

	if address := cfg.Endpoint.Address; address != "" {
		endpoints["address"] = address
	}

	return provider.ProviderConfig{
		Credentials: creds,
		Endpoints:   endpoints,
		Settings:    settings,
	}
}

// BuildProviderConfigWithTokenStore resolves the mobile client WBStream
// credential strictly from the embedded TokenStore. Client artifacts strip
// inline and environment credentials before startup, so a missing or
// incomplete composite token must fail closed instead of selecting a node
// principal or silently creating an unauthenticated adapter.
func BuildProviderConfigWithTokenStore(cfg config.CarrierConfig, tokenStore *tokens.Store, role config.Role) (provider.ProviderConfig, error) {
	providerConfig := BuildProviderConfig(cfg)
	if carrierRuntimeIDFromConfig(cfg) != "wbstream" || role != config.RoleClient {
		return providerConfig, nil
	}
	if tokenStore == nil {
		return provider.ProviderConfig{}, fmt.Errorf("wbstream client TokenStore is required")
	}

	channelID := strings.TrimSpace(cfg.Endpoint.Address)
	if channelID == "" {
		channelID = "*"
	}
	tok, err := tokenStore.ResolveOneForRole("wbstream", "vp8", channelID, "client")
	if err != nil {
		return provider.ProviderConfig{}, fmt.Errorf("resolve wbstream client TokenStore credential: %w", err)
	}
	accessToken := strings.TrimSpace(tok.Parts["access_token"])
	cookieHeader := strings.TrimSpace(tok.Parts["cookie_header"])
	if accessToken == "" || cookieHeader == "" {
		return provider.ProviderConfig{}, fmt.Errorf("wbstream client TokenStore credential must include access_token and cookie_header")
	}

	// Deliberately replace any config/env-derived credentials. Packaged mobile
	// clients may only consume the role-scoped embedded TokenStore projection.
	providerConfig.Credentials = map[string]string{
		"access_token":  accessToken,
		"cookie_header": cookieHeader,
	}
	return providerConfig, nil
}

func endpointFromConfig(cfg config.CarrierConfig) (carriers.Endpoint, error) {
	address := strings.TrimSpace(cfg.Endpoint.Address)
	if address == "" && len(cfg.Endpoint.Metadata) == 0 {
		return carriers.Endpoint{}, fmt.Errorf("endpoint address or metadata is required")
	}
	endpointID := strings.TrimSpace(cfg.Endpoint.ID)
	if endpointID == "" {
		endpointID = cfg.ID
	}
	return carriers.Endpoint{
		ID:       endpointID,
		Carrier:  carrierRuntimeIDFromConfig(cfg),
		Address:  address,
		Metadata: cloneMetadata(cfg.Endpoint.Metadata),
	}, nil
}

func carrierFromConfig(cfg config.CarrierConfig) (carriers.Carrier, error) {
	runtimeID := carrierRuntimeIDFromConfig(cfg)
	switch runtimeID {
	case carriers.CarrierVKMessages:
		if cfg.VKMessages == nil {
			return nil, fmt.Errorf("vk_messages config is required")
		}
		return carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{
			Token:      secretValue(cfg.VKMessages.Token, cfg.VKMessages.TokenEnv),
			BaseURL:    cfg.VKMessages.BaseURL,
			APIVersion: cfg.VKMessages.APIVersion,
		})
	case carriers.CarrierOKMessages:
		if cfg.OKMessages == nil {
			return nil, fmt.Errorf("ok_messages config is required")
		}
		return carriers.NewOKMessagesCarrier(carriers.OKMessagesConfig{
			Token:    secretValue(cfg.OKMessages.Token, cfg.OKMessages.TokenEnv),
			BaseURL:  cfg.OKMessages.BaseURL,
			SendPath: cfg.OKMessages.SendPath,
			ReadPath: cfg.OKMessages.ReadPath,
		})
	case carriers.CarrierVKDocs256, carriers.CarrierVKDocs1024:
		if cfg.VKDocs == nil {
			return nil, fmt.Errorf("vk_docs config is required")
		}
		return carriers.NewVKDocsCarrier(carriers.VKDocsConfig{
			Token:        secretValue(cfg.VKDocs.Token, cfg.VKDocs.TokenEnv),
			BaseURL:      cfg.VKDocs.BaseURL,
			APIVersion:   cfg.VKDocs.APIVersion,
			DescriptorID: runtimeID,
		})
	case carriers.CarrierOKDocs256:
		if cfg.OKDocs == nil {
			return nil, fmt.Errorf("ok_docs config is required")
		}
		return carriers.NewOKDocsCarrier(carriers.OKDocsConfig{
			AccessToken:      secretValue(cfg.OKDocs.AccessToken, cfg.OKDocs.AccessTokenEnv),
			ApplicationKey:   secretValue(cfg.OKDocs.ApplicationKey, cfg.OKDocs.ApplicationKeyEnv),
			SessionSecretKey: secretValue(cfg.OKDocs.SessionSecretKey, cfg.OKDocs.SessionSecretKeyEnv),
			BaseURL:          cfg.OKDocs.BaseURL,
			DescriptorID:     runtimeID,
		})
	case carriers.CarrierYandexDisk:
		if cfg.YandexDisk == nil {
			return nil, fmt.Errorf("yandex_disk config is required")
		}
		cleanupAfterRead := false
		if cfg.YandexDisk.CleanupAfterRead != nil {
			cleanupAfterRead = *cfg.YandexDisk.CleanupAfterRead
		}
		return carriers.NewYandexDiskCarrier(carriers.YandexDiskConfig{
			OAuthToken:       secretValue(cfg.YandexDisk.OAuthToken, cfg.YandexDisk.OAuthTokenEnv),
			CookieHeader:     secretValue(cfg.YandexDisk.CookieHeader, cfg.YandexDisk.CookieHeaderEnv),
			BaseURL:          cfg.YandexDisk.BaseURL,
			BasePath:         cfg.YandexDisk.BasePath,
			MaxFileSizeBytes: cfg.YandexDisk.MaxFileSizeBytes,
			CleanupAfterRead: cleanupAfterRead,
			MinSendInterval:  time.Duration(cfg.YandexDisk.MinSendIntervalMs) * time.Millisecond,
		})
	case carriers.CarrierSSHTCP:
		if cfg.SSH == nil {
			return nil, fmt.Errorf("ssh config is required")
		}
		return carriers.NewSSHCarrier(carriers.SSHConfig{
			Username:                cfg.SSH.Username,
			Password:                secretValue(cfg.SSH.Password, cfg.SSH.PasswordEnv),
			PrivateKey:              secretValue(cfg.SSH.PrivateKey, cfg.SSH.PrivateKeyEnv),
			PrivateKeyPath:          cfg.SSH.PrivateKeyPath,
			PrivateKeyPassphrase:    secretValue(cfg.SSH.PrivateKeyPassphrase, cfg.SSH.PrivateKeyPassphraseEnv),
			UseAgent:                cfg.SSH.UseAgent,
			AgentSocketPath:         cfg.SSH.AgentSocketPath,
			HostKeys:                append([]string(nil), cfg.SSH.HostKeys...),
			ServerAliveIntervalSecs: cfg.SSH.ServerAliveIntervalSecs,
		})
	case carriers.CarrierSSHFabric:
		if cfg.SSHFabric == nil {
			return nil, fmt.Errorf("ssh_fabric config is required")
		}
		client := cfg.SSHFabric.Client
		return newSSHFabricCarrierFromConfig(carriers.SSHConfig{
			Username:                client.Username,
			Password:                secretValue(client.Password, client.PasswordEnv),
			PrivateKey:              secretValue(client.PrivateKey, client.PrivateKeyEnv),
			PrivateKeyPath:          client.PrivateKeyPath,
			PrivateKeyPassphrase:    secretValue(client.PrivateKeyPassphrase, client.PrivateKeyPassphraseEnv),
			UseAgent:                client.UseAgent,
			AgentSocketPath:         client.AgentSocketPath,
			HostKeys:                append([]string(nil), client.HostKeys...),
			ServerAliveIntervalSecs: client.ServerAliveIntervalSecs,
		}, cfg.SSHFabric.Server)
	case carriers.CarrierSingBoxVLESS:
		if cfg.SingBox == nil {
			return nil, fmt.Errorf("sing_box config is required")
		}
		return carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
			URI:              secretValue(cfg.SingBox.URI, cfg.SingBox.URIEnv),
			BinaryPath:       secretValue(cfg.SingBox.BinaryPath, cfg.SingBox.BinaryPathEnv),
			ConfigDir:        secretValue(cfg.SingBox.ConfigDir, cfg.SingBox.ConfigDirEnv),
			Server:           cfg.SingBox.Server,
			ServerPort:       cfg.SingBox.ServerPort,
			UUID:             secretValue(cfg.SingBox.UUID, cfg.SingBox.UUIDEnv),
			Network:          cfg.SingBox.Network,
			Flow:             cfg.SingBox.Flow,
			TLSEnabled:       cfg.SingBox.TLSEnabled,
			TLSServerName:    cfg.SingBox.TLSServerName,
			TLSInsecure:      cfg.SingBox.TLSInsecure,
			UTLSFingerprint:  cfg.SingBox.UTLSFingerprint,
			TransportType:    cfg.SingBox.TransportType,
			TransportHost:    cfg.SingBox.TransportHost,
			TransportPath:    cfg.SingBox.TransportPath,
			LocalListen:      cfg.SingBox.LocalListen,
			StartTimeoutSecs: cfg.SingBox.StartTimeoutSecs,
		})
	case carriers.CarrierFileMailbox:
		if cfg.FileMailbox == nil {
			return nil, fmt.Errorf("file_mailbox config is required")
		}
		return carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{
			Dir:         secretValue(cfg.FileMailbox.Dir, cfg.FileMailbox.DirEnv),
			AllowEgress: cfg.FileMailbox.AllowEgress,
		})

	default:
		return nil, fmt.Errorf("no runtime adapter for carrier %s", runtimeID)
	}
}

func carrierConfigEnabled(enabled map[string]struct{}, cfg config.CarrierConfig) bool {
	if _, ok := enabled[cfg.ID]; ok {
		return true
	}
	_, ok := enabled[carrierRuntimeIDFromConfig(cfg)]
	return ok
}

func newSSHFabricCarrierFromConfig(clientConfig carriers.SSHConfig, serverConfig *config.SSHFabricServerConfig) (carriers.Carrier, error) {
	if serverConfig == nil {
		return carriers.NewSSHFabricCarrier(clientConfig)
	}
	return carriers.NewSSHFabricCarrierWithListener(clientConfig, carriers.SSHFabricListenerConfig{
		ListenAddress:            serverConfig.ListenAddress,
		LocalClientAddress:       serverConfig.LocalClientAddress,
		HostPrivateKey:           serverConfig.HostPrivateKey,
		HostPrivateKeyPath:       serverConfig.HostPrivateKeyPath,
		HostPrivateKeyPassphrase: serverConfig.HostPrivateKeyPassphrase,
		AuthorizedClientKeys:     append([]string(nil), serverConfig.AuthorizedClientKeys...),
		RetentionLimit:           serverConfig.RetentionLimit,
		AllowedTargets:           append([]string(nil), serverConfig.AllowedTargets...),
	})
}

func hasBindingForEnabledCarrier(bindings map[string]policy.CarrierBinding, enabledID string) bool {
	// Exact match.
	if _, ok := bindings[enabledID]; ok {
		return true
	}
	// Compound key match: "vk.messages" should match "vk.messages:discovery".
	for key := range bindings {
		if policy.HasBindingKeyPrefix(key, enabledID) {
			return true
		}
	}
	// Descriptor / endpoint carrier match.
	for _, binding := range bindings {
		if binding.Endpoint.Carrier == enabledID || binding.Carrier.Descriptor().ID == enabledID {
			return true
		}
	}
	return false
}

func carrierRuntimeIDFromConfig(cfg config.CarrierConfig) string {
	if carrierType := strings.TrimSpace(cfg.CarrierType); carrierType != "" {
		return carrierType
	}
	switch cfg.ID {
	case carriers.CarrierVKMessages,
		carriers.CarrierOKMessages,
		carriers.CarrierVKDocs256,
		carriers.CarrierVKDocs1024,
		carriers.CarrierOKDocs256,
		carriers.CarrierYandexDisk,
		carriers.CarrierSSHTCP,
		carriers.CarrierSSHFabric,
		carriers.CarrierSingBoxVLESS,
		carriers.CarrierFileMailbox:
		return cfg.ID
	}
	switch {
	case cfg.SSHFabric != nil:
		return carriers.CarrierSSHFabric
	case cfg.SingBox != nil:
		return carriers.CarrierSingBoxVLESS
	}
	return cfg.ID
}

func secretValue(value string, envName string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	if strings.TrimSpace(envName) == "" {
		return ""
	}
	return os.Getenv(envName)
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

// expandChannelBindings checks whether a carrier config qualifies for
// per-role channel expansion and, if so, returns one binding per channel.
// The original binding's Carrier instance is shared across all expanded
// bindings (stateless carriers like VK/OK messages can serve multiple
// peer_ids from one token+http.Client).
//
// Returns nil when:
//   - WT_CHANNEL_BINDINGS is off
//   - The carrier is not VK messages or OK messages
//   - No channels are configured
func expandChannelBindings(cfg config.CarrierConfig, base policy.CarrierBinding) map[string]policy.CarrierBinding {
	if !config.ChannelBindingsEnabled() {
		return nil
	}

	runtimeID := carrierRuntimeIDFromConfig(cfg)

	switch runtimeID {
	case carriers.CarrierVKMessages:
		if cfg.VKMessages == nil || len(cfg.VKMessages.Channels) == 0 {
			return nil
		}
		if err := config.ValidateVKChannels(cfg.VKMessages.Channels); err != nil {
			log.Printf("expandChannelBindings: vk channels invalid for %s: %v (falling back to single binding)", cfg.ID, err)
			return nil
		}
		result := make(map[string]policy.CarrierBinding, len(cfg.VKMessages.Channels))
		for _, ch := range cfg.VKMessages.Channels {
			key := policy.MakeBindingKey(cfg.ID, ch.Role)
			ep := base.Endpoint
			ep.ID = key
			ep.Address = ch.PeerID
			result[key] = policy.CarrierBinding{
				Carrier:  base.Carrier,
				Endpoint: ep,
				Role:     ch.Role,
			}
		}
		return result

	case carriers.CarrierOKMessages:
		if cfg.OKMessages == nil || len(cfg.OKMessages.Channels) == 0 {
			return nil
		}
		if err := config.ValidateOKChannels(cfg.OKMessages.Channels); err != nil {
			log.Printf("expandChannelBindings: ok channels invalid for %s: %v (falling back to single binding)", cfg.ID, err)
			return nil
		}
		result := make(map[string]policy.CarrierBinding, len(cfg.OKMessages.Channels))
		for _, ch := range cfg.OKMessages.Channels {
			key := policy.MakeBindingKey(cfg.ID, ch.Role)
			ep := base.Endpoint
			ep.ID = key
			ep.Address = ch.ChatID
			result[key] = policy.CarrierBinding{
				Carrier:  base.Carrier,
				Endpoint: ep,
				Role:     ch.Role,
			}
		}
		return result
	}

	return nil
}
