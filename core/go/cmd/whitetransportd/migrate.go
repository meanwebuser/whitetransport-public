package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

// migrateTokensCmd reads legacy secrets/production/*.json files and a legacy
// deployment config, then generates a new config with a token_store block
// containing encrypted tokens and bindings.
//
// Usage: whitetransportd migrate-tokens --legacy-config <path> --secrets-dir <dir> --output <path>
func migrateTokensCmd() error {
	cfgPath := ""
	secretsDir := "secrets/production"
	outputPath := ""

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--legacy-config":
			if i+1 < len(args) {
				cfgPath = args[i+1]
				i++
			}
		case "--secrets-dir":
			if i+1 < len(args) {
				secretsDir = args[i+1]
				i++
			}
		case "--output":
			if i+1 < len(args) {
				outputPath = args[i+1]
				i++
			}
		}
	}

	if cfgPath == "" {
		return errors.New("--legacy-config is required")
	}
	if outputPath == "" {
		return errors.New("--output is required")
	}

	// Load master key
	masterKey, err := tokens.LoadMasterKey()
	if err != nil {
		return fmt.Errorf("load master key: %w", err)
	}

	// Parse legacy config
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read legacy config: %w", err)
	}
	var legacyCfg legacyConfig
	if err := json.Unmarshal(cfgData, &legacyCfg); err != nil {
		return fmt.Errorf("parse legacy config: %w", err)
	}

	// Build new config
	result := legacyCfg.toNewConfig()

	// Load secrets and populate token_store
	if err := loadSecrets(secretsDir, &result, masterKey); err != nil {
		return fmt.Errorf("load secrets: %w", err)
	}

	// Write output
	outData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}
	if err := os.WriteFile(outputPath, outData, 0644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	fmt.Printf("migrated config written to %s\n", outputPath)
	fmt.Printf("  tokens: %d\n", len(result.TokenStore.Tokens))
	fmt.Printf("  bindings: %d\n", len(result.TokenStore.Bindings))
	return nil
}

// ── Legacy config types ─────────────────────────────────────────────────────

type legacyConfig struct {
	Role            string                `json:"role"`
	NodeID          string                `json:"node_id"`
	ClientID        string                `json:"client_id"`
	DisplayName     string                `json:"display_name"`
	Country         string                `json:"country"`
	Region          string                `json:"region"`
	ListenAPI       string                `json:"listen_api"`
	SocksListen     string                `json:"socks_listen"`
	EnabledCarriers []string              `json:"enabled_carriers"`
	CarrierConfigs  []legacyCarrierConfig `json:"carrier_configs"`
	UpstreamProxy   legacyUpstreamProxy   `json:"upstream_proxy"`
}

type legacyCarrierConfig struct {
	ID              string                  `json:"id"`
	Endpoint        legacyEndpoint          `json:"endpoint,omitempty"`
	VKMessages      *legacyVKMessagesConfig `json:"vk_messages,omitempty"`
	OKMessages      *legacyOKMessagesConfig `json:"ok_messages,omitempty"`
	VKDocs          *legacyVKDocsConfig     `json:"vk_docs,omitempty"`
	OKDocs          *legacyOKDocsConfig     `json:"ok_docs,omitempty"`
	WBStream        *legacyWBStreamConfig   `json:"wbstream,omitempty"`
	WhitelistBypass *legacyWhitelistBypass  `json:"whitelist_bypass,omitempty"`
	Name            string                  `json:"name,omitempty"`
	Address         string                  `json:"address,omitempty"`
}

type legacyEndpoint struct {
	ID       string            `json:"id"`
	Address  string            `json:"address"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type legacyVKMessagesConfig struct {
	Token    string `json:"token,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

type legacyOKMessagesConfig struct {
	Token    string `json:"token,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

type legacyVKDocsConfig struct {
	Token    string `json:"token,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

type legacyOKDocsConfig struct {
	AccessToken         string `json:"access_token,omitempty"`
	AccessTokenEnv      string `json:"access_token_env,omitempty"`
	ApplicationKey      string `json:"application_key,omitempty"`
	ApplicationKeyEnv   string `json:"application_key_env,omitempty"`
	SessionSecretKey    string `json:"session_secret_key,omitempty"`
	SessionSecretKeyEnv string `json:"session_secret_key_env,omitempty"`
}

type legacyWBStreamConfig struct {
	AccessToken    string `json:"access_token,omitempty"`
	AccessTokenEnv string `json:"access_token_env,omitempty"`
	CookiesFile    string `json:"cookies_file,omitempty"`
	LocalStorage   string `json:"local_storage,omitempty"`
}

type legacyWhitelistBypass struct {
	AccessToken string `json:"access_token,omitempty"`
}

type legacyUpstreamProxy struct {
	URL              string `json:"url"`
	ClientEgressOnly bool   `json:"client_egress_only"`
}

// ── New config output types ─────────────────────────────────────────────────

type newConfig struct {
	Role            string              `json:"role"`
	NodeID          string              `json:"node_id"`
	ClientID        string              `json:"client_id,omitempty"`
	DisplayName     string              `json:"display_name,omitempty"`
	Country         string              `json:"country,omitempty"`
	Region          string              `json:"region,omitempty"`
	ListenAPI       string              `json:"listen_api"`
	SocksListen     string              `json:"socks_listen"`
	EnabledCarriers []string            `json:"enabled_carriers"`
	CarrierConfigs  []newCarrierConfig  `json:"carrier_configs"`
	UpstreamProxy   legacyUpstreamProxy `json:"upstream_proxy"`
	TokenStore      newTokenStoreConfig `json:"token_store"`
}

type newTokenStoreConfig struct {
	MasterKeyEnv string           `json:"master_key_env,omitempty"`
	Tokens       []tokenEntry     `json:"tokens,omitempty"`
	Bindings     []bindingEntry   `json:"bindings,omitempty"`
}

type tokenEntry struct {
	ID                string            `json:"id"`
	Platform          string            `json:"platform"`
	Kind              string            `json:"kind"`
	Lifecycle         string            `json:"lifecycle"`
	Value             string            `json:"value,omitempty"`
	Parts             map[string]string `json:"parts,omitempty"`
	Refresh           string            `json:"refresh,omitempty"`
	CanCreateChannels bool              `json:"can_create_channels"`
	ExpiresAt         *string           `json:"expires_at,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
}

type bindingEntry struct {
	TokenID        string `json:"token_id"`
	Platform       string `json:"platform"`
	ConnectionType string `json:"connection_type"`
	ChannelID      string `json:"channel_id"`
	Role           string `json:"role"`
	Priority       int    `json:"priority"`
	Enabled        bool   `json:"enabled"`
}

type newCarrierConfig struct {
	ID       string         `json:"id"`
	TokenRef string         `json:"token_ref,omitempty"`
	Endpoint legacyEndpoint `json:"endpoint,omitempty"`
}

// ── Conversion ──────────────────────────────────────────────────────────────

func (lc legacyConfig) toNewConfig() newConfig {
	carriers := make([]newCarrierConfig, len(lc.CarrierConfigs))
	for i, cc := range lc.CarrierConfigs {
		tokenRef := ""
		if strings.HasPrefix(cc.ID, "vk.") {
			tokenRef = "vk-group-1"
		} else if strings.HasPrefix(cc.ID, "ok.") {
			tokenRef = "ok-set-1"
		} else if strings.HasPrefix(cc.ID, "wbstream.") {
			tokenRef = "wb-node-1"
		}
		ep := cc.Endpoint
		if ep.ID == "" && cc.Name != "" {
			ep.ID = cc.Name
		}
		if ep.Address == "" && cc.Address != "" {
			ep.Address = cc.Address
		}
		carriers[i] = newCarrierConfig{
			ID:       cc.ID,
			TokenRef: tokenRef,
			Endpoint: ep,
		}
	}

	return newConfig{
		Role:            lc.Role,
		NodeID:          lc.NodeID,
		ClientID:        lc.ClientID,
		DisplayName:     lc.DisplayName,
		Country:         lc.Country,
		Region:          lc.Region,
		ListenAPI:       lc.ListenAPI,
		SocksListen:     lc.SocksListen,
		EnabledCarriers: lc.EnabledCarriers,
		CarrierConfigs:  carriers,
		UpstreamProxy:   lc.UpstreamProxy,
	}
}

// encryptedToken encrypts a plaintext value or keeps env var references intact.
func encryptedToken(value string, masterKey [32]byte) (string, error) {
	if value == "" || strings.HasPrefix(value, "enc:v1:") {
		return value, nil
	}
	if strings.HasPrefix(value, "${") || strings.HasPrefix(value, "env:") {
		return value, nil // env var reference, keep as-is
	}
	enc, err := tokens.EncryptToken(value, masterKey)
	if err != nil {
		return "", fmt.Errorf("encrypt token: %w", err)
	}
	return enc, nil
}

// ── Secrets file loaders ────────────────────────────────────────────────────

func loadSecrets(dir string, cfg *newConfig, masterKey [32]byte) error {
	// Load VK tokens
	vkTokens, vkBindings, err := loadVKTokens(dir+"/vk-tokens.json", masterKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: load vk tokens: %v\n", err)
	} else {
		cfg.TokenStore.Tokens = append(cfg.TokenStore.Tokens, vkTokens...)
		cfg.TokenStore.Bindings = append(cfg.TokenStore.Bindings, vkBindings...)
	}

	// Load OK tokens
	okTokens, okBindings, err := loadOKTokens(dir+"/ok-tokens.json", masterKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: load ok tokens: %v\n", err)
	} else {
		cfg.TokenStore.Tokens = append(cfg.TokenStore.Tokens, okTokens...)
		cfg.TokenStore.Bindings = append(cfg.TokenStore.Bindings, okBindings...)
	}

	// Load WBStream tokens
	wbTokens, wbBindings, err := loadWBStreamTokens(dir+"/wbstream-tokens.json", masterKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: load wbstream tokens: %v\n", err)
	} else {
		cfg.TokenStore.Tokens = append(cfg.TokenStore.Tokens, wbTokens...)
		cfg.TokenStore.Bindings = append(cfg.TokenStore.Bindings, wbBindings...)
	}

	return nil
}

// ── VK ──────────────────────────────────────────────────────────────────────

type vkSecretsFile struct {
	Accounts []vkAccount `json:"accounts"`
}

type vkAccount struct {
	ID         string      `json:"id"`
	Token      string      `json:"token"`
	APIVersion string      `json:"api_version"`
	Source     string      `json:"source"`
	Channels   []vkChannel `json:"channels"`
}

type vkChannel struct {
	PeerID string `json:"peer_id"`
	Role   string `json:"role"`
	Label  string `json:"label"`
}

func loadVKTokens(path string, masterKey [32]byte) ([]tokenEntry, []bindingEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var file vkSecretsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, err
	}

	var entries []tokenEntry
	var bindings []bindingEntry
	for _, acct := range file.Accounts {
		tokenID := "vk-" + acct.ID
		encVal, err := encryptedToken(acct.Token, masterKey)
		if err != nil {
			return nil, nil, fmt.Errorf("encrypt vk token for %s: %w", acct.ID, err)
		}
		entries = append(entries, tokenEntry{
			ID:                tokenID,
			Platform:          "vk",
			Kind:              "api_key",
			Lifecycle:         "embedded",
			Value:             encVal,
			CanCreateChannels: false,
			Tags: map[string]string{
				"source": acct.Source,
			},
		})

		for _, ch := range acct.Channels {
			if ch.PeerID == "" || ch.PeerID == "TBD" {
				continue
			}
			bindings = append(bindings, bindingEntry{
				TokenID:        tokenID,
				Platform:       "vk",
				ConnectionType: "messages",
				ChannelID:      ch.PeerID,
				Role:           ch.Role,
				Priority:       10,
				Enabled:        true,
			})
		}

		// Also add docs bindings
		bindings = append(bindings, bindingEntry{
			TokenID:        tokenID,
			Platform:       "vk",
			ConnectionType: "docs.256",
			ChannelID:      "*",
			Role:           "bulk",
			Priority:       10,
			Enabled:        true,
		}, bindingEntry{
			TokenID:        tokenID,
			Platform:       "vk",
			ConnectionType: "docs.1024",
			ChannelID:      "*",
			Role:           "bulk",
			Priority:       10,
			Enabled:        true,
		})
	}
	return entries, bindings, nil
}

// ── OK ──────────────────────────────────────────────────────────────────────

type okSecretsFile struct {
	OKMessages okMessagesEntry `json:"ok_messages"`
	OKDocs     okDocsEntry     `json:"ok_docs"`
	OKPhotos   okDocsEntry     `json:"ok_photos"`
}

type okMessagesEntry struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
}

type okDocsEntry struct {
	AccessToken      string `json:"access_token"`
	ApplicationKey   string `json:"application_key"`
	SessionSecretKey string `json:"session_secret_key"`
	BaseURL          string `json:"base_url"`
}

func loadOKTokens(path string, masterKey [32]byte) ([]tokenEntry, []bindingEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var file okSecretsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, err
	}

	parts := make(map[string]string)
	if file.OKMessages.Token != "" {
		encVal, err := encryptedToken(file.OKMessages.Token, masterKey)
		if err != nil {
			return nil, nil, fmt.Errorf("encrypt ok access_token: %w", err)
		}
		parts["access_token"] = encVal
	} else if file.OKDocs.AccessToken != "" {
		encVal, err := encryptedToken(file.OKDocs.AccessToken, masterKey)
		if err != nil {
			return nil, nil, fmt.Errorf("encrypt ok access_token: %w", err)
		}
		parts["access_token"] = encVal
	}
	if file.OKDocs.ApplicationKey != "" {
		encVal, err := encryptedToken(file.OKDocs.ApplicationKey, masterKey)
		if err != nil {
			return nil, nil, fmt.Errorf("encrypt ok application_key: %w", err)
		}
		parts["application_key"] = encVal
	}
	if file.OKDocs.SessionSecretKey != "" {
		encVal, err := encryptedToken(file.OKDocs.SessionSecretKey, masterKey)
		if err != nil {
			return nil, nil, fmt.Errorf("encrypt ok session_secret_key: %w", err)
		}
		parts["session_secret_key"] = encVal
	}

	entries := []tokenEntry{
		{
			ID:        "ok-set-1",
			Platform:  "ok",
			Kind:      "composite",
			Lifecycle: "embedded",
			Parts:     parts,
		},
	}

	bindings := []bindingEntry{
		{TokenID: "ok-set-1", Platform: "ok", ConnectionType: "messages", ChannelID: "*", Role: "control", Priority: 20, Enabled: true},
		{TokenID: "ok-set-1", Platform: "ok", ConnectionType: "docs.256", ChannelID: "*", Role: "bulk", Priority: 20, Enabled: true},
		{TokenID: "ok-set-1", Platform: "ok", ConnectionType: "photos", ChannelID: "*", Role: "bulk", Priority: 20, Enabled: true},
	}

	return entries, bindings, nil
}

// ── WBStream ────────────────────────────────────────────────────────────────

type wbstreamSecretsFile struct {
	Accounts []wbstreamAccount `json:"accounts"`
}

type wbstreamAccount struct {
	ID               string `json:"id"`
	Phone            string `json:"phone"`
	Role             string `json:"role"`
	AccessToken      string `json:"access_token"`
	CookiesFile      string `json:"cookies_file"`
	LocalStorageFile string `json:"local_storage_file"`
}

func loadWBStreamTokens(path string, masterKey [32]byte) ([]tokenEntry, []bindingEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var file wbstreamSecretsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, err
	}

	var entries []tokenEntry
	var bindings []bindingEntry
	for _, acct := range file.Accounts {
		tokenID := "wb-" + acct.ID
		parts := make(map[string]string)
		if acct.AccessToken != "" {
			encVal, err := encryptedToken(acct.AccessToken, masterKey)
			if err != nil {
				return nil, nil, fmt.Errorf("encrypt wbstream access_token for %s: %w", acct.ID, err)
			}
			parts["access_token"] = encVal
		}
		if acct.CookiesFile != "" {
			parts["cookies_file"] = acct.CookiesFile
		}
		if acct.LocalStorageFile != "" {
			parts["local_storage_file"] = acct.LocalStorageFile
		}

		isNode := acct.Role == "node"
		entries = append(entries, tokenEntry{
			ID:                tokenID,
			Platform:          "wbstream",
			Kind:              "composite",
			Lifecycle:         "embedded",
			Parts:             parts,
			CanCreateChannels: isNode,
			Tags: map[string]string{
				"role":  acct.Role,
				"phone": acct.Phone,
			},
		})
		bindings = append(bindings, bindingEntry{
			TokenID:        tokenID,
			Platform:       "wbstream",
			ConnectionType: "vp8",
			ChannelID:      "*",
			Role:           acct.Role,
			Priority:       10,
			Enabled:        true,
		})
	}
	return entries, bindings, nil
}
