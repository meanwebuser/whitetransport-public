package runtime

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

// CarrierFailureStage identifies the runtime phase that degraded one binding.
type CarrierFailureStage string

const (
	CarrierFailureConstruction CarrierFailureStage = "construction"
)

// CarrierBindingFailure is an internal, cause-preserving failure for one
// configured binding. API layers must expose Code and a sanitized message, not
// Err directly, because provider errors may contain credential material.
type CarrierBindingFailure struct {
	CarrierID     string
	BindingKey    string
	Stage         CarrierFailureStage
	Code          string
	Retryable     bool
	ResourceGroup string
	Err           error
}

func (f CarrierBindingFailure) Error() string {
	if f.Err == nil {
		return fmt.Sprintf("carrier %s binding %s failed during %s", f.CarrierID, f.BindingKey, f.Stage)
	}
	return f.Err.Error()
}

func (f CarrierBindingFailure) Unwrap() error { return f.Err }

// CarrierBindingBuildResult separates structural ambiguity (returned error)
// from independent carrier failures. An empty Bindings map with failures is a
// valid degraded result so Transport can keep its local API alive and blocked.
type CarrierBindingBuildResult struct {
	Bindings map[string]policy.CarrierBinding
	Failures []CarrierBindingFailure
}

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
// registry and optional token store support. Native credential resolution
// prefers explicit and environment values where configured.
func BuildCarrierBindingsWithRegistryAndTokens(cfg config.Config, registry *ProviderRegistry, ts *tokens.Store) (map[string]policy.CarrierBinding, error) {
	return buildBindingsWithTokens(cfg, registry, ts)
}

func buildBindingsWithTokens(cfg config.Config, registry *ProviderRegistry, ts *tokens.Store) (map[string]policy.CarrierBinding, error) {
	result, err := buildCarrierBindingsWithRegistryAndTokens(cfg, registry, ts, true)
	if err != nil {
		return nil, err
	}
	if len(result.Failures) > 0 {
		failures := make([]error, 0, len(result.Failures))
		for _, failure := range result.Failures {
			failures = append(failures, failure)
		}
		return nil, errors.Join(failures...)
	}
	return result.Bindings, nil
}

type carrierBindingPlan struct {
	config      config.CarrierConfig
	canonicalID string
	keys        []string
}

type expandedCarrierBindingPlan struct {
	config config.CarrierConfig
	key    string
	role   string
}

// BuildCarrierBindingsWithRegistryAndTokensIsolated performs a side-effect-free
// structural preflight before invoking any carrier constructor. Constructor
// failures are collected per binding while independent bindings survive.
func BuildCarrierBindingsWithRegistryAndTokensIsolated(cfg config.Config, registry *ProviderRegistry, ts *tokens.Store) (CarrierBindingBuildResult, error) {
	return buildCarrierBindingsWithRegistryAndTokens(cfg, registry, ts, false)
}

func buildCarrierBindingsWithRegistryAndTokens(cfg config.Config, registry *ProviderRegistry, ts *tokens.Store, stopOnConstructionFailure bool) (CarrierBindingBuildResult, error) {
	cfg = addAutoDiscoveredXraySingBox(cfg)
	plans, enabled, err := planCarrierBindings(cfg, registry)
	if err != nil {
		return CarrierBindingBuildResult{}, err
	}

	result := CarrierBindingBuildResult{
		Bindings: make(map[string]policy.CarrierBinding, len(cfg.CarrierConfigs)),
	}
	vkHistory := carriers.NewVKHistoryCache()
	for _, plan := range plans {
		carrierConfig := plan.config
		if expanded := expandedChannelConstructionPlans(plan); len(expanded) > 0 {
			for _, channelPlan := range expanded {
				binding, channelErr := buildCarrierBindingWithTokens(channelPlan.config, ts, cfg.Role, cfg.BootstrapSecret, cfg.ProviderWireEncryption, vkHistory, registry)
				if channelErr != nil {
					wrapped := fmt.Errorf("carrier %s channel %s: %w", carrierConfig.ID, channelPlan.key, channelErr)
					result.Failures = append(result.Failures, newCarrierBindingFailure(plan, channelPlan.key, wrapped))
					if stopOnConstructionFailure {
						sortCarrierBindingFailures(result.Failures)
						return result, nil
					}
					continue
				}
				binding.Endpoint.ID = channelPlan.key
				binding.Role = channelPlan.role
				result.Bindings[channelPlan.key] = binding
			}
			continue
		}
		binding, err := buildCarrierBindingWithTokens(carrierConfig, ts, cfg.Role, cfg.BootstrapSecret, cfg.ProviderWireEncryption, vkHistory, registry)
		if err != nil {
			result.Failures = append(result.Failures, newCarrierBindingFailure(plan, carrierConfig.ID, err))
			if stopOnConstructionFailure {
				sortCarrierBindingFailures(result.Failures)
				return result, nil
			}
			continue
		}
		result.Bindings[carrierConfig.ID] = binding
	}

	for enabledID := range enabled {
		if !hasPlannedEnabledCarrier(plans, enabledID) {
			return CarrierBindingBuildResult{}, fmt.Errorf("enabled carrier %s has no carrier_config", enabledID)
		}
	}
	sortCarrierBindingFailures(result.Failures)
	return result, nil
}

func planCarrierBindings(cfg config.Config, registry *ProviderRegistry) ([]carrierBindingPlan, map[string]struct{}, error) {
	enabled := make(map[string]struct{}, len(cfg.EnabledCarriers))
	for _, id := range cfg.EnabledCarriers {
		enabledID := strings.TrimSpace(id)
		if enabledID != "" {
			enabled[enabledID] = struct{}{}
		}
	}

	seenConfigIDs := make(map[string]struct{}, len(cfg.CarrierConfigs))
	seenBindingKeys := make(map[string]struct{}, len(cfg.CarrierConfigs))
	plans := make([]carrierBindingPlan, 0, len(cfg.CarrierConfigs))
	for _, carrierConfig := range cfg.CarrierConfigs {
		carrierConfig.ID = strings.TrimSpace(carrierConfig.ID)
		if carrierConfig.ID == "" {
			return nil, nil, fmt.Errorf("carrier config id is required")
		}
		if _, duplicate := seenConfigIDs[carrierConfig.ID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate carrier config %s", carrierConfig.ID)
		}
		seenConfigIDs[carrierConfig.ID] = struct{}{}
		if !carrierConfigEnabled(enabled, carrierConfig) {
			continue
		}
		canonicalID := carrierRuntimeIDFromConfig(carrierConfig)
		if !knownRuntimeCarrier(canonicalID, registry) {
			return nil, nil, fmt.Errorf("unknown carrier %s", canonicalID)
		}
		keys, err := plannedBindingKeys(carrierConfig)
		if err != nil {
			return nil, nil, err
		}
		for _, key := range keys {
			if _, duplicate := seenBindingKeys[key]; duplicate {
				return nil, nil, fmt.Errorf("duplicate binding key %s", key)
			}
			seenBindingKeys[key] = struct{}{}
		}
		plans = append(plans, carrierBindingPlan{config: carrierConfig, canonicalID: canonicalID, keys: keys})
	}
	for enabledID := range enabled {
		if !hasPlannedEnabledCarrier(plans, enabledID) {
			return nil, nil, fmt.Errorf("enabled carrier %s has no carrier_config", enabledID)
		}
	}
	sort.SliceStable(plans, func(i, j int) bool { return plans[i].config.ID < plans[j].config.ID })
	return plans, enabled, nil
}

func plannedBindingKeys(cfg config.CarrierConfig) ([]string, error) {
	if !config.ChannelBindingsEnabled() {
		return []string{cfg.ID}, nil
	}
	switch carrierRuntimeIDFromConfig(cfg) {
	case carriers.CarrierVKMessages:
		if cfg.VKMessages == nil || len(cfg.VKMessages.Channels) == 0 {
			return []string{cfg.ID}, nil
		}
		if err := config.ValidateVKChannels(cfg.VKMessages.Channels); err != nil {
			return nil, fmt.Errorf("carrier %s: %w", cfg.ID, err)
		}
		keys := make([]string, 0, len(cfg.VKMessages.Channels))
		for _, channel := range cfg.VKMessages.Channels {
			keys = append(keys, policy.MakeBindingKey(cfg.ID, channel.Role))
		}
		sort.Strings(keys)
		return keys, nil
	case carriers.CarrierOKMessages:
		if cfg.OKMessages == nil || len(cfg.OKMessages.Channels) == 0 {
			return []string{cfg.ID}, nil
		}
		if err := config.ValidateOKChannels(cfg.OKMessages.Channels); err != nil {
			return nil, fmt.Errorf("carrier %s: %w", cfg.ID, err)
		}
		keys := make([]string, 0, len(cfg.OKMessages.Channels))
		for _, channel := range cfg.OKMessages.Channels {
			keys = append(keys, policy.MakeBindingKey(cfg.ID, channel.Role))
		}
		sort.Strings(keys)
		return keys, nil
	default:
		return []string{cfg.ID}, nil
	}
}

func expandedChannelConstructionPlans(plan carrierBindingPlan) []expandedCarrierBindingPlan {
	if !config.ChannelBindingsEnabled() {
		return nil
	}
	byKey := make(map[string]expandedCarrierBindingPlan, len(plan.keys))
	switch plan.canonicalID {
	case carriers.CarrierVKMessages:
		if plan.config.VKMessages == nil || len(plan.config.VKMessages.Channels) == 0 {
			return nil
		}
		for _, channel := range plan.config.VKMessages.Channels {
			channelConfig := plan.config
			messages := *channelConfig.VKMessages
			messages.Channels = nil
			channelConfig.VKMessages = &messages
			channelConfig.Endpoint.Address = channel.PeerID
			key := policy.MakeBindingKey(plan.config.ID, channel.Role)
			byKey[key] = expandedCarrierBindingPlan{config: channelConfig, key: key, role: channel.Role}
		}
	case carriers.CarrierOKMessages:
		if plan.config.OKMessages == nil || len(plan.config.OKMessages.Channels) == 0 {
			return nil
		}
		for _, channel := range plan.config.OKMessages.Channels {
			channelConfig := plan.config
			messages := *channelConfig.OKMessages
			messages.Channels = nil
			channelConfig.OKMessages = &messages
			channelConfig.Endpoint.Address = channel.ChatID
			key := policy.MakeBindingKey(plan.config.ID, channel.Role)
			byKey[key] = expandedCarrierBindingPlan{config: channelConfig, key: key, role: channel.Role}
		}
	default:
		return nil
	}
	expanded := make([]expandedCarrierBindingPlan, 0, len(plan.keys))
	for _, key := range plan.keys {
		expanded = append(expanded, byKey[key])
	}
	return expanded
}

func knownRuntimeCarrier(carrierID string, registry *ProviderRegistry) bool {
	if _, err := carriers.FindStandardDescriptor(carrierID); err == nil {
		return true
	}
	if registry == nil {
		return false
	}
	registry.mu.RLock()
	_, ok := registry.factories[carrierID]
	registry.mu.RUnlock()
	return ok
}

func hasPlannedEnabledCarrier(plans []carrierBindingPlan, enabledID string) bool {
	for _, plan := range plans {
		if plan.config.ID == enabledID || plan.canonicalID == enabledID {
			return true
		}
	}
	return false
}

func newCarrierBindingFailure(plan carrierBindingPlan, bindingKey string, err error) CarrierBindingFailure {
	if bindingKey == plan.config.ID {
		err = fmt.Errorf("carrier %s: %w", plan.config.ID, err)
	}
	code, retryable := classifyCarrierConstructionFailure(err)
	return CarrierBindingFailure{
		CarrierID:     plan.canonicalID,
		BindingKey:    bindingKey,
		Stage:         CarrierFailureConstruction,
		Code:          code,
		Retryable:     retryable,
		ResourceGroup: plan.config.ID,
		Err:           err,
	}
}

func classifyCarrierConstructionFailure(err error) (string, bool) {
	message := strings.ToLower(err.Error())
	credentialTerms := []string{"token", "credential", "oauth", "cookie", "application key", "session secret", "private key"}
	for _, term := range credentialTerms {
		if strings.Contains(message, term) && (strings.Contains(message, "required") || strings.Contains(message, "missing")) {
			return "credential_missing", true
		}
	}
	remoteTerms := []string{
		"unable to connect",
		"connection refused",
		"could not resolve host",
		"couldn't connect",
		"connection timed out",
		"network is unreachable",
		"no route to host",
	}
	for _, term := range remoteTerms {
		if strings.Contains(message, term) {
			return "remote_unreachable", true
		}
	}
	if strings.Contains(message, "no runtime adapter") || strings.Contains(message, "unsupported") || strings.Contains(message, "unknown provider") {
		return "adapter_unavailable", false
	}
	if strings.Contains(message, "endpoint") || strings.Contains(message, "address") {
		return "invalid_endpoint", false
	}
	return "construction_failed", true
}

func sortCarrierBindingFailures(failures []CarrierBindingFailure) {
	sort.SliceStable(failures, func(i, j int) bool {
		if failures[i].CarrierID != failures[j].CarrierID {
			return failures[i].CarrierID < failures[j].CarrierID
		}
		if failures[i].BindingKey != failures[j].BindingKey {
			return failures[i].BindingKey < failures[j].BindingKey
		}
		return failures[i].Stage < failures[j].Stage
	})
}

func buildCarrierBindingWithTokens(cfg config.CarrierConfig, ts *tokens.Store, role config.Role, bootstrapSecret string, providerWireEncryption bool, vkHistory *carriers.VKHistoryCache, registry *ProviderRegistry) (policy.CarrierBinding, error) {
	endpoint, err := endpointFromConfig(cfg)
	if err != nil {
		return policy.CarrierBinding{}, err
	}
	var vkCredentialID string
	carrier, err := carrierFromConfigWithTokens(cfg, ts, &vkCredentialID, vkHistory)
	if err != nil {
		if isNoRuntimeAdapterError(err) && registry != nil {
			carrier, err = carrierFromProviderRegistry(cfg, endpoint, registry, ts, role, bootstrapSecret, providerWireEncryption)
			if err == nil {
				return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint, Role: cfg.Role}, nil
			}
		}
		return policy.CarrierBinding{}, err
	}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint, Role: cfg.Role}
	return binding.WithVKCredentialID(vkCredentialID), nil
}

// tokenCredentialValue matches the native string credential selection order.
func tokenCredentialValue(token *tokens.Token) string {
	if token.Value != "" {
		return token.Value
	}
	if value := token.Parts["access_token"]; value != "" {
		return value
	}
	return token.Parts["token"]
}

func uniqueVKCredentialID(ts *tokens.Store, value string) string {
	if ts == nil {
		return ""
	}
	var matched *tokens.Token
	for _, token := range ts.ListByPlatform("vk") {
		if tokenCredentialValue(token) != value {
			continue
		}
		if matched != nil {
			return ""
		}
		matched = token
	}
	if matched == nil {
		return ""
	}
	return matched.ID
}

// carrierFromConfigWithTokens is the token-store-aware version of carrierFromConfig.
// vkCredentialID records provenance only for the native VK adapters.
func carrierFromConfigWithTokens(cfg config.CarrierConfig, ts *tokens.Store, vkCredentialID *string, vkHistory *carriers.VKHistoryCache) (carriers.Carrier, error) {
	resolve := func(platform, connType, channelID, legacyValue, legacyEnv string) string {
		if explicit := strings.TrimSpace(legacyValue); explicit != "" {
			if platform == "vk" {
				*vkCredentialID = uniqueVKCredentialID(ts, explicit)
			}
			return explicit
		}
		if envValue := secretValue("", legacyEnv); strings.TrimSpace(envValue) != "" {
			if platform == "vk" {
				*vkCredentialID = uniqueVKCredentialID(ts, envValue)
			}
			return envValue
		}
		if ts != nil {
			tok, err := ts.ResolveOne(platform, connType, channelID)
			if err == nil {
				if value := tokenCredentialValue(tok); value != "" {
					if platform == "vk" {
						*vkCredentialID = tok.ID
					}
					return value
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
			Token:        resolve("vk", "messages", addr, cfg.VKMessages.Token, cfg.VKMessages.TokenEnv),
			BaseURL:      cfg.VKMessages.BaseURL,
			APIVersion:   cfg.VKMessages.APIVersion,
			HistoryCache: vkHistory,
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
		channelID := strings.TrimSpace(cfg.Endpoint.Address)
		if channelID == "" {
			channelID = "*"
		}
		return carriers.NewVKDocsCarrier(carriers.VKDocsConfig{
			Token:        resolve("vk", connType, channelID, cfg.VKDocs.Token, cfg.VKDocs.TokenEnv),
			BaseURL:      cfg.VKDocs.BaseURL,
			APIVersion:   cfg.VKDocs.APIVersion,
			DescriptorID: runtimeID,
			HistoryCache: vkHistory,
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
	case carriers.CarrierDNSTXT:
		if cfg.DNSTXT == nil {
			return nil, fmt.Errorf("dns_txt config is required")
		}
		return carriers.NewDNSTXTCarrier(carriers.DNSTXTConfig{
			Zone:             cfg.DNSTXT.Zone,
			ResolverAddr:     cfg.DNSTXT.ResolverAddr,
			DirectAddr:       cfg.DNSTXT.DirectAddr,
			QueryTimeout:     time.Duration(cfg.DNSTXT.QueryTimeoutMs) * time.Millisecond,
			MinQueryInterval: time.Duration(cfg.DNSTXT.MinQueryIntervalMs) * time.Millisecond,
			MaxUpChunkBytes:  cfg.DNSTXT.MaxUpChunkBytes,
			MaxMessageBytes:  cfg.DNSTXT.MaxMessageBytes,
		})
	case carriers.CarrierGitRepository:
		if cfg.GitRepository == nil {
			return nil, fmt.Errorf("git_repository config is required")
		}
		return carriers.NewGitRepositoryCarrier(carriers.GitRepositoryConfig{
			RemoteURL:      cfg.GitRepository.RemoteURL,
			WorkDir:        cfg.GitRepository.WorkDir,
			WriterID:       cfg.GitRepository.WriterID,
			GitPath:        cfg.GitRepository.GitPath,
			CommandTimeout: time.Duration(cfg.GitRepository.CommandTimeoutSeconds) * time.Second,
		})
	case carriers.CarrierMailIMAPSMTP:
		if cfg.MailIMAPSMTP == nil {
			return nil, fmt.Errorf("mail_imap_smtp config is required")
		}
		parts := resolveComposite("mail", "imap_smtp", cfg.MailIMAPSMTP.AccountID)
		if parts == nil {
			parts = map[string]string{}
		}
		return carriers.NewMailIMAPSMTPCarrier(carriers.MailIMAPSMTPConfig{
			SMTPAddress:   cfg.MailIMAPSMTP.SMTPAddress,
			IMAPAddress:   cfg.MailIMAPSMTP.IMAPAddress,
			AccountID:     cfg.MailIMAPSMTP.AccountID,
			Mailbox:       cfg.MailIMAPSMTP.Mailbox,
			FromAddress:   cfg.MailIMAPSMTP.FromAddress,
			ToAddress:     cfg.MailIMAPSMTP.ToAddress,
			TLSServerName: cfg.MailIMAPSMTP.TLSServerName,
			CAFile:        cfg.MailIMAPSMTP.CAFile,
			SMTPUsername:  parts["smtp_username"],
			SMTPPassword:  parts["smtp_password"],
			IMAPUsername:  parts["imap_username"],
			IMAPPassword:  parts["imap_password"],
			Timeout:       time.Duration(cfg.MailIMAPSMTP.TimeoutSeconds) * time.Second,
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
	case carriers.CarrierHysteria2:
		if cfg.Hysteria2 == nil {
			return nil, fmt.Errorf("hysteria2 config is required")
		}
		password := resolve("hysteria2", "transport", cfg.Endpoint.Address, cfg.Hysteria2.Password, cfg.Hysteria2.PasswordEnv)
		obfsPassword := resolve("hysteria2", "obfs", cfg.Endpoint.Address, cfg.Hysteria2.ObfsPassword, cfg.Hysteria2.ObfsPasswordEnv)
		if parts := resolveComposite("hysteria2", "transport", cfg.Endpoint.Address); parts != nil {
			if password == "" {
				password = firstNonEmpty(parts["password"], parts["auth"], parts["access_token"], parts["token"])
			}
		}
		if parts := resolveComposite("hysteria2", "obfs", cfg.Endpoint.Address); parts != nil && obfsPassword == "" {
			obfsPassword = firstNonEmpty(parts["password"], parts["auth"], parts["token"])
		}
		return carriers.NewSingBoxHysteria2Carrier(carriers.Hysteria2Config{
			URI:              resolve("hysteria2", "transport", cfg.Endpoint.Address, cfg.Hysteria2.URI, cfg.Hysteria2.URIEnv),
			BinaryPath:       secretValue(cfg.Hysteria2.BinaryPath, cfg.Hysteria2.BinaryPathEnv),
			ConfigDir:        secretValue(cfg.Hysteria2.ConfigDir, cfg.Hysteria2.ConfigDirEnv),
			Server:           cfg.Hysteria2.Server,
			ServerPort:       cfg.Hysteria2.ServerPort,
			Password:         password,
			TLSServerName:    cfg.Hysteria2.TLSServerName,
			TLSInsecure:      cfg.Hysteria2.TLSInsecure,
			ObfsType:         cfg.Hysteria2.ObfsType,
			ObfsPassword:     obfsPassword,
			LocalListen:      cfg.Hysteria2.LocalListen,
			StartTimeoutSecs: cfg.Hysteria2.StartTimeoutSecs,
		})
	case carriers.CarrierTailscale:
		if cfg.Tailscale == nil {
			return nil, fmt.Errorf("tailscale config is required")
		}
		return carriers.NewTailscaleCarrier(carriers.TailscaleConfig{
			Peer:              cfg.Tailscale.Peer,
			ProxyAddress:      cfg.Tailscale.ProxyAddress,
			UDPProbeAddress:   cfg.Tailscale.UDPProbeAddress,
			BinaryPath:        cfg.Tailscale.BinaryPath,
			StatusTimeoutSecs: cfg.Tailscale.StatusTimeoutSecs,
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
	vkHistory := carriers.NewVKHistoryCache()
	for _, carrierConfig := range cfg.CarrierConfigs {
		if strings.TrimSpace(carrierConfig.ID) == "" {
			return nil, fmt.Errorf("carrier config id is required")
		}
		if !carrierConfigEnabled(enabled, carrierConfig) {
			continue
		}
		binding, err := buildCarrierBinding(carrierConfig, cfg.BootstrapSecret, cfg.ProviderWireEncryption, vkHistory, registry)
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

func buildCarrierBinding(cfg config.CarrierConfig, bootstrapSecret string, providerWireEncryption bool, vkHistory *carriers.VKHistoryCache, registry *ProviderRegistry) (policy.CarrierBinding, error) {
	endpoint, err := endpointFromConfig(cfg)
	if err != nil {
		return policy.CarrierBinding{}, err
	}
	carrier, err := carrierFromConfig(cfg, vkHistory)
	if err != nil {
		if isNoRuntimeAdapterError(err) && registry != nil {
			carrier, err = carrierFromProviderRegistry(cfg, endpoint, registry, nil, "", bootstrapSecret, providerWireEncryption)
			if err == nil {
				return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint, Role: cfg.Role}, nil
			}
		}
		return policy.CarrierBinding{}, err
	}
	return policy.CarrierBinding{Carrier: carrier, Endpoint: endpoint, Role: cfg.Role}, nil
}

func isNoRuntimeAdapterError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "no runtime adapter for carrier ")
}

func carrierFromProviderRegistry(cfg config.CarrierConfig, endpoint carriers.Endpoint, registry *ProviderRegistry, tokenStore *tokens.Store, role config.Role, bootstrapSecret string, providerWireEncryption bool) (carriers.Carrier, error) {
	// Registry factories are keyed by platform name (e.g. "wbstream"),
	// but carrier config IDs are qualified (e.g. "wbstream.vp8").
	// Extract the platform prefix for the registry lookup.
	providerName := carrierRuntimeIDFromConfig(cfg)
	if idx := strings.IndexByte(providerName, '.'); idx > 0 {
		providerName = providerName[:idx]
	}
	providerConfig, err := BuildProviderConfigWithTokenStore(cfg, tokenStore, role)
	if err != nil {
		return nil, err
	}
	adapter, err := registry.Create(providerName, providerConfig)
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	if providerName == "max" && !providerWireEncryption {
		return nil, fmt.Errorf("max carrier requires provider_wire_encryption between WhiteTransport instances")
	}
	var wrapped *carriers.ProviderCarrier
	if providerWireEncryption {
		if strings.TrimSpace(bootstrapSecret) == "" {
			return nil, fmt.Errorf("provider wire encryption requires bootstrap_secret")
		}
		wireKey := fabric.DeriveProviderWireKey(bootstrapSecret, adapter.ID())
		wrapped, err = carriers.NewEncryptedProviderCarrier(adapter, endpoint, wireKey)
	} else {
		wrapped, err = carriers.NewProviderCarrier(adapter, endpoint)
	}
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
		if cfg.WhitelistBypass.Reliable {
			settings["reliable"] = true
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
	if cfg.VKCall != nil {
		if cfg.VKCall.JoinLink != "" {
			creds["join_link"] = cfg.VKCall.JoinLink
		}
		if cfg.VKCall.PeerID != "" {
			creds["peer_id"] = cfg.VKCall.PeerID
		}
		if cfg.VKCall.AppID != "" {
			settings["app_id"] = cfg.VKCall.AppID
		}
		if cfg.VKCall.APIVersion != "" {
			settings["api_version"] = cfg.VKCall.APIVersion
		}
		if cfg.VKCall.AppVersion != "" {
			settings["app_version"] = cfg.VKCall.AppVersion
		}
		if cfg.VKCall.ProtocolVersion != "" {
			settings["protocol_version"] = cfg.VKCall.ProtocolVersion
		}
		if cfg.VKCall.TunnelMode != "" {
			settings["tunnel_mode"] = cfg.VKCall.TunnelMode
		}
		if cfg.VKCall.VP8FPS != 0 {
			settings["vp8_fps"] = cfg.VKCall.VP8FPS
		}
		if cfg.VKCall.VP8Batch != 0 {
			settings["vp8_batch"] = cfg.VKCall.VP8Batch
		}
		if cfg.VKCall.DualTrack {
			settings["dual_track"] = true
		}
	}
	if cfg.MAX != nil {
		if cfg.MAX.PeerID != "" {
			settings["peer_id"] = cfg.MAX.PeerID
		}
		if cfg.MAX.WebSocketURL != "" {
			endpoints["websocket_url"] = cfg.MAX.WebSocketURL
		}
		if cfg.MAX.AllowInsecureTestEndpoint {
			settings["allow_insecure_test_endpoint"] = true
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

// BuildProviderConfigWithTokenStore resolves provider credentials that must
// come from role-scoped TokenStore bindings. Client artifacts strip inline and
// environment credentials before startup, so a missing credential fails closed
// instead of selecting a different principal or starting unauthenticated.
func BuildProviderConfigWithTokenStore(cfg config.CarrierConfig, tokenStore *tokens.Store, role config.Role) (provider.ProviderConfig, error) {
	providerConfig := BuildProviderConfig(cfg)
	runtimeID := carrierRuntimeIDFromConfig(cfg)
	switch runtimeID {
	case "wbstream":
		if role != config.RoleClient {
			return providerConfig, nil
		}
		if cfg.WBStream != nil && strings.TrimSpace(cfg.WBStream.AccessToken) != "" && strings.TrimSpace(cfg.WBStream.CookieHeader) != "" {
			// Mobile/desktop native adapters may provide a short-lived local
			// session in memory. It is intentionally not projected into the
			// bootstrap TokenStore and must take precedence for client room creation.
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
		providerConfig.Credentials = map[string]string{
			"access_token":  accessToken,
			"cookie_header": cookieHeader,
		}
	case "vkcall":
		if tokenStore == nil {
			return provider.ProviderConfig{}, fmt.Errorf("vkcall TokenStore is required")
		}
		channelID := strings.TrimSpace(cfg.Endpoint.Address)
		if channelID == "" && cfg.VKCall != nil {
			channelID = strings.TrimSpace(cfg.VKCall.PeerID)
		}
		if channelID == "" {
			channelID = "*"
		}
		tok, err := tokenStore.ResolveOneForRole("vk", "calls", channelID, string(role))
		if err != nil {
			return provider.ProviderConfig{}, fmt.Errorf("resolve vkcall %s TokenStore credential: %w", role, err)
		}
		cookie := strings.TrimSpace(tok.Parts["cookie_header"])
		if cookie == "" {
			cookie = strings.TrimSpace(tok.Parts["cookie"])
		}
		if cookie == "" {
			cookie = strings.TrimSpace(tok.Value)
		}
		if cookie == "" {
			return provider.ProviderConfig{}, fmt.Errorf("vkcall TokenStore credential must include cookie_header, cookie, or value")
		}
		providerConfig.Credentials["cookie"] = cookie
	case "max":
		if tokenStore == nil {
			return provider.ProviderConfig{}, fmt.Errorf("max TokenStore is required")
		}
		channelID := strings.TrimSpace(cfg.Endpoint.Address)
		if channelID == "" && cfg.MAX != nil {
			channelID = strings.TrimSpace(cfg.MAX.PeerID)
		}
		if channelID == "" {
			channelID = "*"
		}
		tok, err := tokenStore.ResolveOneForRole("max", "calls", channelID, string(role))
		if err != nil {
			return provider.ProviderConfig{}, fmt.Errorf("resolve max %s TokenStore credential: %w", role, err)
		}
		token := strings.TrimSpace(tok.Parts["max_token"])
		if token == "" {
			token = strings.TrimSpace(tok.Parts["token"])
		}
		if token == "" {
			token = strings.TrimSpace(tok.Value)
		}
		if token == "" {
			return provider.ProviderConfig{}, fmt.Errorf("max TokenStore credential must include max_token, token, or value")
		}
		if providerConfig.Credentials == nil {
			providerConfig.Credentials = make(map[string]string)
		}
		if providerConfig.Settings == nil {
			providerConfig.Settings = make(map[string]any)
		}
		providerConfig.Credentials["max_token"] = token
		providerConfig.Settings["role"] = string(role)
	case "dion":
		if tokenStore == nil {
			return provider.ProviderConfig{}, fmt.Errorf("dion TokenStore is required")
		}
		tok, err := tokenStore.ResolveOneForRole("dion", "video", "*", "creator")
		if err != nil {
			return provider.ProviderConfig{}, fmt.Errorf("resolve dion creator TokenStore credential: %w", err)
		}
		if providerConfig.Credentials == nil {
			providerConfig.Credentials = make(map[string]string)
		}
		for _, key := range []string{"access_token", "refresh_token", "cookies_file"} {
			if value := strings.TrimSpace(tok.Parts[key]); value != "" {
				providerConfig.Credentials[key] = value
			}
		}
		if providerConfig.Credentials["access_token"] == "" && providerConfig.Credentials["cookies_file"] == "" {
			return provider.ProviderConfig{}, fmt.Errorf("dion creator TokenStore credential requires access_token or cookies_file")
		}
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

func carrierFromConfig(cfg config.CarrierConfig, vkHistory *carriers.VKHistoryCache) (carriers.Carrier, error) {
	runtimeID := carrierRuntimeIDFromConfig(cfg)
	switch runtimeID {
	case carriers.CarrierVKMessages:
		if cfg.VKMessages == nil {
			return nil, fmt.Errorf("vk_messages config is required")
		}
		return carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{
			Token:        secretValue(cfg.VKMessages.Token, cfg.VKMessages.TokenEnv),
			BaseURL:      cfg.VKMessages.BaseURL,
			APIVersion:   cfg.VKMessages.APIVersion,
			HistoryCache: vkHistory,
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
			HistoryCache: vkHistory,
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
	case carriers.CarrierDNSTXT:
		if cfg.DNSTXT == nil {
			return nil, fmt.Errorf("dns_txt config is required")
		}
		return carriers.NewDNSTXTCarrier(carriers.DNSTXTConfig{
			Zone:             cfg.DNSTXT.Zone,
			ResolverAddr:     cfg.DNSTXT.ResolverAddr,
			DirectAddr:       cfg.DNSTXT.DirectAddr,
			QueryTimeout:     time.Duration(cfg.DNSTXT.QueryTimeoutMs) * time.Millisecond,
			MinQueryInterval: time.Duration(cfg.DNSTXT.MinQueryIntervalMs) * time.Millisecond,
			MaxUpChunkBytes:  cfg.DNSTXT.MaxUpChunkBytes,
			MaxMessageBytes:  cfg.DNSTXT.MaxMessageBytes,
		})
	case carriers.CarrierGitRepository:
		if cfg.GitRepository == nil {
			return nil, fmt.Errorf("git_repository config is required")
		}
		return carriers.NewGitRepositoryCarrier(carriers.GitRepositoryConfig{
			RemoteURL:      cfg.GitRepository.RemoteURL,
			WorkDir:        cfg.GitRepository.WorkDir,
			WriterID:       cfg.GitRepository.WriterID,
			GitPath:        cfg.GitRepository.GitPath,
			CommandTimeout: time.Duration(cfg.GitRepository.CommandTimeoutSeconds) * time.Second,
		})
	case carriers.CarrierMailIMAPSMTP:
		if cfg.MailIMAPSMTP == nil {
			return nil, fmt.Errorf("mail_imap_smtp config is required")
		}
		return carriers.NewMailIMAPSMTPCarrier(carriers.MailIMAPSMTPConfig{
			SMTPAddress:   cfg.MailIMAPSMTP.SMTPAddress,
			IMAPAddress:   cfg.MailIMAPSMTP.IMAPAddress,
			AccountID:     cfg.MailIMAPSMTP.AccountID,
			Mailbox:       cfg.MailIMAPSMTP.Mailbox,
			FromAddress:   cfg.MailIMAPSMTP.FromAddress,
			ToAddress:     cfg.MailIMAPSMTP.ToAddress,
			TLSServerName: cfg.MailIMAPSMTP.TLSServerName,
			CAFile:        cfg.MailIMAPSMTP.CAFile,
			Timeout:       time.Duration(cfg.MailIMAPSMTP.TimeoutSeconds) * time.Second,
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
	case carriers.CarrierHysteria2:
		if cfg.Hysteria2 == nil {
			return nil, fmt.Errorf("hysteria2 config is required")
		}
		return carriers.NewSingBoxHysteria2Carrier(carriers.Hysteria2Config{
			URI:              secretValue(cfg.Hysteria2.URI, cfg.Hysteria2.URIEnv),
			BinaryPath:       secretValue(cfg.Hysteria2.BinaryPath, cfg.Hysteria2.BinaryPathEnv),
			ConfigDir:        secretValue(cfg.Hysteria2.ConfigDir, cfg.Hysteria2.ConfigDirEnv),
			Server:           cfg.Hysteria2.Server,
			ServerPort:       cfg.Hysteria2.ServerPort,
			Password:         secretValue(cfg.Hysteria2.Password, cfg.Hysteria2.PasswordEnv),
			TLSServerName:    cfg.Hysteria2.TLSServerName,
			TLSInsecure:      cfg.Hysteria2.TLSInsecure,
			ObfsType:         cfg.Hysteria2.ObfsType,
			ObfsPassword:     secretValue(cfg.Hysteria2.ObfsPassword, cfg.Hysteria2.ObfsPasswordEnv),
			LocalListen:      cfg.Hysteria2.LocalListen,
			StartTimeoutSecs: cfg.Hysteria2.StartTimeoutSecs,
		})
	case carriers.CarrierTailscale:
		if cfg.Tailscale == nil {
			return nil, fmt.Errorf("tailscale config is required")
		}
		return carriers.NewTailscaleCarrier(carriers.TailscaleConfig{
			Peer:              cfg.Tailscale.Peer,
			ProxyAddress:      cfg.Tailscale.ProxyAddress,
			UDPProbeAddress:   cfg.Tailscale.UDPProbeAddress,
			BinaryPath:        cfg.Tailscale.BinaryPath,
			StatusTimeoutSecs: cfg.Tailscale.StatusTimeoutSecs,
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
		carriers.CarrierGitRepository,
		carriers.CarrierMailIMAPSMTP,
		carriers.CarrierSSHTCP,
		carriers.CarrierSSHFabric,
		carriers.CarrierSingBoxVLESS,
		carriers.CarrierHysteria2,
		carriers.CarrierTailscale,
		carriers.CarrierFileMailbox:
		return cfg.ID
	}
	switch {
	case cfg.SSHFabric != nil:
		return carriers.CarrierSSHFabric
	case cfg.SingBox != nil:
		return carriers.CarrierSingBoxVLESS
	case cfg.Hysteria2 != nil:
		return carriers.CarrierHysteria2
	case cfg.Tailscale != nil:
		return carriers.CarrierTailscale
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
