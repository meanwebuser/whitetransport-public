package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
)

// GeneratedDaemonConfig describes a managed runtime config generated from a
// TokenStore source. It intentionally reports counts and paths only.
type GeneratedDaemonConfig struct {
	Path           string   `json:"path"`
	TokenStorePath string   `json:"token_store_path"`
	TokenCount     int      `json:"token_count"`
	BindingCount   int      `json:"binding_count"`
	Carriers       []string `json:"carriers"`
}

type tokenStoreFile struct {
	MasterKeyEnv string         `json:"master_key_env,omitempty"`
	Tokens       []tokenEntry   `json:"tokens"`
	Bindings     []bindingEntry `json:"bindings"`
}

type tokenEntry struct {
	ID                string            `json:"id"`
	Platform          string            `json:"platform"`
	Kind              string            `json:"kind"`
	Lifecycle         string            `json:"lifecycle,omitempty"`
	Status            string            `json:"status,omitempty"`
	Value             string            `json:"value,omitempty"`
	Parts             map[string]string `json:"parts,omitempty"`
	CanCreateChannels bool              `json:"can_create_channels,omitempty"`
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

type generatedConfigFile struct {
	Role               string                 `json:"role"`
	NodeID             string                 `json:"node_id"`
	ClientID           string                 `json:"client_id"`
	DisplayName        string                 `json:"display_name"`
	ListenAPI          string                 `json:"listen_api"`
	SocksListen        string                 `json:"socks_listen"`
	EnabledCarriers    []string               `json:"enabled_carriers"`
	CarrierConfigs     []json.RawMessage      `json:"carrier_configs"`
	TokenStore         json.RawMessage        `json:"token_store"`
	TokenStorePath     string                 `json:"token_store_path"`
	BootstrapSecret    string                 `json:"bootstrap_secret,omitempty"`
	UpstreamProxy      upstreamProxy          `json:"upstream_proxy"`
	ClientRoomCreation bool                   `json:"client_room_creation,omitempty"`
	Routing            routingConfig          `json:"routing,omitempty"`
	SessionEgress      *sessionEgressConfig   `json:"session_egress,omitempty"`
	AdminReporter      *clientTelemetryConfig `json:"admin_reporter,omitempty"`
}

// clientTelemetryConfig is a non-secret bundle sidecar. Its token_ref is
// resolved from the packaged TokenStore by the daemon and never copied here.
type clientTelemetryConfig struct {
	Enabled         bool   `json:"enabled"`
	AdminURL        string `json:"admin_url"`
	TokenRef        string `json:"token_ref"`
	RegisterPath    string `json:"register_path"`
	HeartbeatPath   string `json:"heartbeat_path"`
	IntervalSeconds int    `json:"interval_seconds"`
}

const defaultManagedWBStreamServerURL = "wss://stream.wb.ru"

func readBootstrapSecretFromEnv() (string, error) {
	source := strings.TrimSpace(os.Getenv("WT_CLIENT_BOOTSTRAP_SECRET_FILE"))
	if source == "" {
		source = strings.TrimSpace(os.Getenv("WT_BOOTSTRAP_SECRET_FILE"))
	}
	requested := strings.TrimSpace(os.Getenv("WT_BOOTSTRAP_KEY_V2")) == "1"
	provisioningOnly := strings.TrimSpace(os.Getenv("WT_NATIVE_GUI_PROVISIONING_ONLY")) == "1"
	if source == "" {
		if requested && !provisioningOnly {
			return "", fmt.Errorf("bootstrap secret file is required when v2 is requested")
		}
		return "", nil
	}
	if !filepath.IsAbs(source) {
		return "", fmt.Errorf("bootstrap secret source must be an absolute file path")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("stat bootstrap secret source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("bootstrap secret source must be a regular file")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("read bootstrap secret source: %w", err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", fmt.Errorf("bootstrap secret source must not be empty")
	}
	return secret, nil
}

func loadBundledClientTelemetry(tokenStorePath string) (*clientTelemetryConfig, error) {
	path := filepath.Join(filepath.Dir(tokenStorePath), "client-telemetry.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bundled client telemetry: %w", err)
	}
	var telemetry clientTelemetryConfig
	if err := json.Unmarshal(raw, &telemetry); err != nil {
		return nil, fmt.Errorf("parse bundled client telemetry: %w", err)
	}
	if !telemetry.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(telemetry.AdminURL) == "" || strings.TrimSpace(telemetry.TokenRef) == "" {
		return nil, fmt.Errorf("bundled client telemetry requires admin_url and token_ref")
	}
	if strings.TrimSpace(telemetry.RegisterPath) == "" || strings.TrimSpace(telemetry.HeartbeatPath) == "" {
		return nil, fmt.Errorf("bundled client telemetry requires register_path and heartbeat_path")
	}
	if telemetry.IntervalSeconds <= 0 {
		return nil, fmt.Errorf("bundled client telemetry requires a positive interval_seconds")
	}
	return &telemetry, nil
}

type upstreamProxy struct {
	URL              string `json:"url"`
	ClientEgressOnly bool   `json:"client_egress_only"`
}

type routingConfig struct {
	Mode             string   `json:"mode,omitempty"`
	FullTunnel       bool     `json:"full_tunnel,omitempty"`
	DestinationSplit bool     `json:"destination_split,omitempty"`
	DestinationCIDRs []string `json:"destination_cidrs,omitempty"`
	LANAccess        bool     `json:"lan_access,omitempty"`
	DNSServers       []string `json:"dns_servers,omitempty"`
	MTU              int      `json:"mtu,omitempty"`
	GeoIPURL         string   `json:"geoip_url,omitempty"`
	GeoSiteURL       string   `json:"geosite_url,omitempty"`
}

// sessionEgressConfig is deliberately limited to local sidecar startup data.
// Remote VLESS details arrive only in encrypted session answers.
type sessionEgressConfig struct {
	SingBox *sessionSingBoxRuntimeConfig `json:"sing_box,omitempty"`
}

type sessionSingBoxRuntimeConfig struct {
	BinaryPath       string `json:"binary_path,omitempty"`
	ConfigDir        string `json:"config_dir,omitempty"`
	LocalListen      string `json:"local_listen,omitempty"`
	StartTimeoutSecs int    `json:"start_timeout_secs,omitempty"`
}

// EnsureManagedDaemonConfig generates or refreshes the config owned by managed
// mode. Operator-supplied config candidates are never overwritten. A previous
// generated candidate is deliberately refreshed because local browser imports
// change the client-only room credentials after the application has started.
func EnsureManagedDaemonConfig(resources RuntimeResourceSummary, logs *LogSink) (RuntimeResourceSummary, GeneratedDaemonConfig, error) {
	if resources.Mode != ModeManaged {
		return resources, GeneratedDaemonConfig{}, nil
	}
	if hasExplicitSupervisorConfig(resources) {
		return resources, GeneratedDaemonConfig{}, nil
	}
	generated, err := GenerateManagedDaemonConfig(resources)
	if err != nil {
		return resources, GeneratedDaemonConfig{}, err
	}
	resources = resources.WithGeneratedDaemonConfig(generated.Path)
	if logs != nil {
		logs.Info("managed daemon config generated", map[string]string{
			"config_path":      generated.Path,
			"token_store_path": generated.TokenStorePath,
			"token_count":      fmt.Sprintf("%d", generated.TokenCount),
			"binding_count":    fmt.Sprintf("%d", generated.BindingCount),
			"carriers":         strings.Join(generated.Carriers, ","),
		})
	}
	return resources, generated, nil
}

// hasExplicitSupervisorConfig reports whether an operator-selected daemon
// configuration should win over the managed generated configuration.
func hasExplicitSupervisorConfig(resources RuntimeResourceSummary) bool {
	for _, candidate := range resources.Candidates {
		if candidate.Kind != ResourceDaemonConfig || candidate.Source == "generated" {
			continue
		}
		// Bundled macOS daemon.json is only a bootstrap envelope (role,
		// bootstrap secret, and packaged client credentials). The managed
		// runtime must regenerate carrier_configs and policy from the bundled
		// TokenStore instead of treating this sparse file as an operator config.
		if strings.HasPrefix(candidate.Source, "bundle") {
			continue
		}
		if candidateCanSatisfyRequired(candidate) {
			return true
		}
	}
	return false
}

// GenerateManagedDaemonConfig writes a client daemon config from TokenStore.
func GenerateManagedDaemonConfig(resources RuntimeResourceSummary) (GeneratedDaemonConfig, error) {
	tokenStoreCandidate, ok := resources.FirstFoundCandidate(ResourceTokenStore)
	if !ok {
		return GeneratedDaemonConfig{}, fmt.Errorf("managed daemon config generation requires a found %s candidate", ResourceTokenStore)
	}
	rawTokenStore, err := os.ReadFile(tokenStoreCandidate.Target)
	if err != nil {
		return GeneratedDaemonConfig{}, fmt.Errorf("read token store: %w", err)
	}
	var tokenStore tokenStoreFile
	if err := json.Unmarshal(rawTokenStore, &tokenStore); err != nil {
		return GeneratedDaemonConfig{}, fmt.Errorf("parse token store: %w", err)
	}
	telemetry, err := loadBundledClientTelemetry(tokenStoreCandidate.Target)
	if err != nil {
		return GeneratedDaemonConfig{}, err
	}
	bootstrapSecret := strings.TrimSpace(resources.BootstrapSecret)
	explicitBootstrapSecret, err := readBootstrapSecretFromEnv()
	if err != nil && bootstrapSecret == "" {
		return GeneratedDaemonConfig{}, err
	}
	if explicitBootstrapSecret != "" {
		bootstrapSecret = explicitBootstrapSecret
	}
	tokenStore, err = clientBootstrapTokenStore(tokenStore)
	if err != nil {
		return GeneratedDaemonConfig{}, fmt.Errorf("project client token store: %w", err)
	}
	clientCreds, _ := LoadClientCredentials()
	tokenStore = mergeClientCredentialTokenStore(tokenStore, clientCreds)
	rawTokenStore, err = json.Marshal(tokenStore)
	if err != nil {
		return GeneratedDaemonConfig{}, fmt.Errorf("encode bootstrap token store: %w", err)
	}
	configDir, err := DefaultRuntimeConfigDir()
	if err != nil {
		return GeneratedDaemonConfig{}, err
	}
	carriers, configs, err := buildCarrierConfigs(tokenStore)
	if err != nil {
		return GeneratedDaemonConfig{}, err
	}
	clientRoomCreation := HasClientRoomCredentials(clientCreds)
	if clientRoomCreation {
		carriers, configs = applyClientCredentialOverrides(carriers, configs, clientCreds)
	}
	// Also enable role reversal if the WBStream carrier already has an
	// access_token from the shared token-store (no client-tokens.json needed).
	if !clientRoomCreation {
		for _, raw := range configs {
			var c map[string]any
			if json.Unmarshal(raw, &c) != nil {
				continue
			}
			if id, _ := c["id"].(string); id == "wbstream.vp8" {
				if wb, ok := c["wbstream_legacy"].(map[string]any); ok {
					if token, _ := wb["access_token"].(string); token != "" {
						clientRoomCreation = true
						break
					}
				}
			}
		}
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return GeneratedDaemonConfig{}, fmt.Errorf("create native runtime config dir: %w", err)
	}
	clientID, err := loadOrCreateManagedClientIdentity(configDir)
	if err != nil {
		return GeneratedDaemonConfig{}, err
	}
	configPath := filepath.Join(configDir, "daemon.json")
	splitSettings, err := LoadSystemVPNSplitSettings(configDir)
	if err != nil {
		return GeneratedDaemonConfig{}, fmt.Errorf("load system VPN split settings: %w", err)
	}
	routing := routingConfig{
		Mode:             string(splitSettings.Mode),
		FullTunnel:       splitSettings.Mode == SystemVPNSplitNone,
		DestinationSplit: len(splitSettings.Destinations) > 0,
		DestinationCIDRs: append([]string(nil), splitSettings.Destinations...),
		LANAccess:        splitSettings.LANAccess,
		DNSServers:       []string{"1.1.1.1", "2606:4700:4700::1111"},
		MTU:              1500,
	}
	var sessionEgress *sessionEgressConfig
	if singBox, ok := resources.FirstFoundCandidate(ResourceSingBox); ok {
		sessionEgress = &sessionEgressConfig{SingBox: &sessionSingBoxRuntimeConfig{
			BinaryPath:       singBox.Target,
			ConfigDir:        configDir,
			LocalListen:      "127.0.0.1:0",
			StartTimeoutSecs: 15,
		}}
	}
	nodeID := ""
	if telemetry != nil {
		nodeID = clientID
	}
	config := generatedConfigFile{
		Role:            "client",
		NodeID:          nodeID,
		ClientID:        clientID,
		DisplayName:     "WhiteTransport Native GUI",
		ListenAPI:       runtimeAPIListenAddress(resources.RuntimeAPIURL),
		SocksListen:     socksListenAddress(),
		EnabledCarriers: carriers,
		CarrierConfigs:  configs,
		TokenStore:      json.RawMessage(rawTokenStore),
		TokenStorePath:  tokenStoreCandidate.Target,
		BootstrapSecret: bootstrapSecret,
		UpstreamProxy: upstreamProxy{
			URL:              "",
			ClientEgressOnly: true,
		},
		ClientRoomCreation: clientRoomCreation,
		Routing:            routing,
		SessionEgress:      sessionEgress,
		AdminReporter:      telemetry,
	}
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return GeneratedDaemonConfig{}, fmt.Errorf("encode managed daemon config: %w", err)
	}
	if err := os.WriteFile(configPath, append(payload, '\n'), 0o600); err != nil {
		return GeneratedDaemonConfig{}, fmt.Errorf("write managed daemon config: %w", err)
	}
	return GeneratedDaemonConfig{
		Path:           configPath,
		TokenStorePath: tokenStoreCandidate.Target,
		TokenCount:     len(tokenStore.Tokens),
		BindingCount:   len(tokenStore.Bindings),
		Carriers:       carriers,
	}, nil
}

var (
	clientTokenPlatforms = map[string]bool{
		"dion": true, "ok": true, "sber": true, "telemost": true,
		"vk": true, "wbstream": true, "yandex": true,
	}
	clientTokenRoles = map[string]bool{
		"bootstrap": true, "client": true, "control": true, "discovery": true, "node-client": true,
	}
	clientTokenServerRoles = map[string]bool{
		"admin": true, "bulk": true, "creator": true, "logs": true, "node": true,
	}
	clientTokenConnections = map[string]bool{
		"datachannel": true, "docs.1024": true, "docs.256": true,
		"messages": true, "photos": true, "video": true, "vp8": true,
	}
	clientTokenBootstrapRoles = map[string]bool{
		"bootstrap": true, "discovery": true,
	}
	clientTokenCredentialParts = map[string]bool{
		"access_token":       true,
		"api_key":            true,
		"application_key":    true,
		"cookie":             true,
		"cookie_header":      true,
		"password":           true,
		"refresh_token":      true,
		"secret":             true,
		"session_secret_key": true,
		"token":              true,
	}
	clientTokenPathParts = map[string]bool{
		"cookies_file": true, "local_storage_file": true,
	}
)

// clientBootstrapTokenStore projects a TokenStore for packaged clients.
// Supported principals must be embedded, active, client-safe, and contain a
// bootstrap-plane binding. Ambiguous principals fail closed instead of being
// partially projected into a client config.
func clientBootstrapTokenStore(store tokenStoreFile) (tokenStoreFile, error) {
	tokenByID := make(map[string]tokenEntry, len(store.Tokens))
	for _, token := range store.Tokens {
		tokenID := normalizeTokenStoreField(token.ID)
		if tokenID == "" {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore token id is required")
		}
		if _, exists := tokenByID[tokenID]; exists {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore contains duplicate token id")
		}
		tokenByID[tokenID] = token
	}

	bindingsByTokenID := make(map[string][]bindingEntry)
	for _, binding := range store.Bindings {
		tokenID := normalizeTokenStoreField(binding.TokenID)
		token, ok := tokenByID[tokenID]
		if !ok {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore binding references an unknown token")
		}
		tokenPlatform := normalizeTokenStoreField(token.Platform)
		bindingPlatform := normalizeTokenStoreField(binding.Platform)
		if bindingPlatform == "" || bindingPlatform != tokenPlatform {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore binding platform does not match its token")
		}
		if !clientTokenPlatforms[tokenPlatform] {
			continue
		}
		bindingsByTokenID[tokenID] = append(bindingsByTokenID[tokenID], binding)
	}

	retainedTokenIDs := make(map[string]bool)
	clientFingerprints := make(map[[32]byte]struct{})
	serverFingerprints := make(map[[32]byte]struct{})
	hasBootstrapBinding := false
	for tokenID, bindings := range bindingsByTokenID {
		token := tokenByID[tokenID]
		roles := make(map[string]bool)
		for _, binding := range bindings {
			roles[normalizeTokenStoreField(binding.Role)] = true
		}
		var unknownRoles []string
		var clientRoles []string
		var serverRoles []string
		for role := range roles {
			switch {
			case clientTokenRoles[role]:
				clientRoles = append(clientRoles, role)
			case clientTokenServerRoles[role]:
				serverRoles = append(serverRoles, role)
			default:
				unknownRoles = append(unknownRoles, role)
			}
		}
		if len(unknownRoles) > 0 {
			sort.Strings(unknownRoles)
			return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q has unsafe or ambiguous roles: %v", token.ID, unknownRoles)
		}
		if len(clientRoles) > 0 && len(serverRoles) > 0 {
			sort.Strings(clientRoles)
			sort.Strings(serverRoles)
			return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q mixes client and server roles: client=%v, server=%v", token.ID, clientRoles, serverRoles)
		}
		if len(clientRoles) == 0 {
			continue
		}
		if normalizeTokenStoreField(token.Lifecycle) != "embedded" {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q requires embedded lifecycle", token.ID)
		}
		if normalizeTokenStoreField(token.Status) != "active" {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q requires active token status", token.ID)
		}
		if token.CanCreateChannels {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q cannot create channels", token.ID)
		}
		if normalizeTokenStoreField(token.Platform) == "ok" {
			for part := range token.Parts {
				switch normalizeTokenStoreField(part) {
				case "application_key", "session_secret_key":
					return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q has server-only OK docs credential material", token.ID)
				}
			}
		}
		for part := range token.Parts {
			partName := normalizeTokenStoreField(part)
			if strings.HasSuffix(partName, "_env") {
				return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q requires actual credential material", token.ID)
			}
			if !clientTokenCredentialParts[partName] && !clientTokenPathParts[partName] {
				return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q has unsafe or ambiguous credential part", token.ID)
			}
		}
		fingerprints := tokenCredentialFingerprints(token)
		if len(fingerprints) == 0 {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q requires credential material", token.ID)
		}
		for _, binding := range bindings {
			if !clientTokenConnections[normalizeTokenStoreField(binding.ConnectionType)] {
				return tokenStoreFile{}, fmt.Errorf("client TokenStore principal %q has an unsafe or ambiguous connection type", token.ID)
			}
			if binding.Enabled && clientTokenBootstrapRoles[normalizeTokenStoreField(binding.Role)] {
				hasBootstrapBinding = true
			}
		}
		retainedTokenIDs[tokenID] = true
		for fingerprint := range fingerprints {
			clientFingerprints[fingerprint] = struct{}{}
		}
	}
	for tokenID, token := range tokenByID {
		if retainedTokenIDs[tokenID] {
			continue
		}
		for fingerprint := range tokenCredentialFingerprints(token) {
			serverFingerprints[fingerprint] = struct{}{}
		}
	}
	for fingerprint := range clientFingerprints {
		if _, shared := serverFingerprints[fingerprint]; shared {
			return tokenStoreFile{}, fmt.Errorf("client TokenStore reuses credential material from a server principal")
		}
	}
	if len(retainedTokenIDs) == 0 {
		return tokenStoreFile{}, fmt.Errorf("client TokenStore has no whitelist bootstrap credentials")
	}
	if !hasBootstrapBinding {
		return tokenStoreFile{}, fmt.Errorf("client TokenStore has no enabled bootstrap-plane credential")
	}

	filtered := tokenStoreFile{MasterKeyEnv: store.MasterKeyEnv}
	for _, token := range store.Tokens {
		if retainedTokenIDs[normalizeTokenStoreField(token.ID)] {
			projected, err := projectClientToken(token)
			if err != nil {
				return tokenStoreFile{}, err
			}
			filtered.Tokens = append(filtered.Tokens, projected)
		}
	}
	for _, binding := range store.Bindings {
		if retainedTokenIDs[normalizeTokenStoreField(binding.TokenID)] {
			filtered.Bindings = append(filtered.Bindings, binding)
		}
	}
	return filtered, nil
}

func normalizeTokenStoreField(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func tokenCredentialFingerprints(token tokenEntry) map[[32]byte]struct{} {
	values := make([]string, 0, 1+len(token.Parts))
	values = append(values, token.Value)
	for key, value := range token.Parts {
		if clientTokenCredentialParts[normalizeTokenStoreField(key)] {
			values = append(values, value)
		}
	}
	fingerprints := make(map[[32]byte]struct{})
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		fingerprints[sha256.Sum256([]byte(value))] = struct{}{}
	}
	return fingerprints
}

func projectClientToken(token tokenEntry) (tokenEntry, error) {
	projected := tokenEntry{
		ID:        token.ID,
		Platform:  token.Platform,
		Kind:      token.Kind,
		Lifecycle: "embedded",
		Status:    "active",
		Value:     token.Value,
	}
	if len(token.Parts) == 0 {
		return projected, nil
	}
	projected.Parts = make(map[string]string)
	for key, value := range token.Parts {
		partName := normalizeTokenStoreField(key)
		if clientTokenCredentialParts[partName] || clientTokenPathParts[partName] {
			projected.Parts[key] = value
		}
	}
	if len(projected.Parts) == 0 {
		projected.Parts = nil
	}
	return projected, nil
}

// DefaultRuntimeConfigDir returns the native GUI runtime config directory.
func DefaultRuntimeConfigDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("WT_NATIVE_GUI_CONFIG_DIR")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for native runtime config: %w", err)
	}
	switch goruntime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "WhiteTransport", "runtime"), nil
	case "windows":
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return filepath.Join(localAppData, "WhiteTransport", "runtime"), nil
		}
		return filepath.Join(home, "AppData", "Local", "WhiteTransport", "runtime"), nil
	default:
		if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" {
			return filepath.Join(stateHome, "WhiteTransport", "runtime"), nil
		}
		return filepath.Join(home, ".local", "state", "WhiteTransport", "runtime"), nil
	}
}

func buildCarrierConfigs(tokenStore tokenStoreFile) ([]string, []json.RawMessage, error) {
	enabledBindings := make([]bindingEntry, 0, len(tokenStore.Bindings))
	for _, binding := range tokenStore.Bindings {
		if binding.Enabled {
			enabledBindings = append(enabledBindings, binding)
		}
	}
	tokenByID := make(map[string]tokenEntry, len(tokenStore.Tokens))
	for _, token := range tokenStore.Tokens {
		tokenByID[token.ID] = token
	}

	carrierIDs := map[string]bool{}
	for _, binding := range enabledBindings {
		switch {
		case binding.Platform == "vk" && binding.ConnectionType == "messages":
			carrierIDs["vk.messages"] = true
		case binding.Platform == "vk" && (binding.ConnectionType == "docs.256" || binding.ConnectionType == "docs.1024"):
			carrierIDs["vk."+binding.ConnectionType] = true
		case binding.Platform == "ok" && binding.ConnectionType == "messages":
			carrierIDs["ok.messages"] = true
		case binding.Platform == "wbstream" && binding.ConnectionType == "vp8":
			carrierIDs["wbstream.vp8"] = true
		case binding.Platform == "dion" && binding.ConnectionType == "video":
			carrierIDs["dion"] = true
		}
	}

	var carriers []string
	var configs []json.RawMessage
	addCarrierConfig := func(config map[string]any) error {
		payload, err := json.Marshal(config)
		if err != nil {
			return err
		}
		configs = append(configs, payload)
		return nil
	}
	addCarrier := func(id string, config map[string]any) error {
		carriers = append(carriers, id)
		return addCarrierConfig(config)
	}

	vkMsgBindings := filterBindings(enabledBindings, "vk", "messages")
	vkDiscovery := firstBindingWithRole(vkMsgBindings, "discovery")
	vkChannels := make([]map[string]string, 0, len(vkMsgBindings))
	seenVKRoles := make(map[string]struct{}, len(vkMsgBindings))
	for _, binding := range vkMsgBindings {
		// The typed Go carrier schema permits one peer per role. Multiple
		// client-only bootstrap principals can repeat the same role, so keep
		// the first metadata-defined peer and retain the full TokenStore below.
		if _, seen := seenVKRoles[binding.Role]; seen {
			continue
		}
		seenVKRoles[binding.Role] = struct{}{}
		vkChannels = append(vkChannels, map[string]string{"peer_id": binding.ChannelID, "role": binding.Role})
	}
	if carrierIDs["vk.messages"] && vkDiscovery != nil && tokenHasSimpleValue(tokenByID, vkDiscovery.TokenID) {
		if err := addCarrier("vk.messages", map[string]any{
			"id":          "vk.messages",
			"token_ref":   vkDiscovery.TokenID,
			"vk_messages": map[string]any{"channels": vkChannels},
			"endpoint":    map[string]string{"id": "vk-discovery", "address": vkDiscovery.ChannelID},
		}); err != nil {
			return nil, nil, err
		}
	}
	if carrierIDs["vk.docs.256"] && vkDiscovery != nil && tokenHasSimpleValue(tokenByID, vkDiscovery.TokenID) {
		if err := addCarrier("vk.docs.256", map[string]any{
			"id":        "vk.docs.256",
			"token_ref": vkDiscovery.TokenID,
			"vk_docs":   map[string]any{},
			"endpoint":  map[string]string{"id": "vk-bulk-256", "address": vkDiscovery.ChannelID},
		}); err != nil {
			return nil, nil, err
		}
	}
	if carrierIDs["vk.docs.1024"] && vkDiscovery != nil && tokenHasSimpleValue(tokenByID, vkDiscovery.TokenID) {
		if err := addCarrier("vk.docs.1024", map[string]any{
			"id":        "vk.docs.1024",
			"token_ref": vkDiscovery.TokenID,
			"vk_docs":   map[string]any{},
			"endpoint":  map[string]string{"id": "vk-bulk-1024", "address": vkDiscovery.ChannelID},
		}); err != nil {
			return nil, nil, err
		}
	}

	okMsg := firstBinding(filterBindings(enabledBindings, "ok", "messages"))
	if carrierIDs["ok.messages"] && okMsg != nil && tokenHasSimpleValue(tokenByID, okMsg.TokenID) {
		if err := addCarrier("ok.messages", map[string]any{
			"id":          "ok.messages",
			"token_ref":   okMsg.TokenID,
			"ok_messages": map[string]any{},
			"endpoint":    map[string]string{"id": "ok-control", "address": okMsg.ChannelID},
		}); err != nil {
			return nil, nil, err
		}
	}

	wbBindings := filterBindings(enabledBindings, "wbstream", "vp8")
	wbBinding := bindingWithRole(wbBindings, "client")
	if carrierIDs["wbstream.vp8"] && wbBinding == nil {
		return nil, nil, fmt.Errorf("managed daemon config requires a wbstream/vp8 binding with role=client")
	}
	if carrierIDs["wbstream.vp8"] && wbBinding != nil {
		token := tokenByID[wbBinding.TokenID]
		if token.Parts["access_token"] != "" {
			if err := addCarrier("wbstream.vp8", map[string]any{
				"id":           "wbstream.vp8",
				"carrier_type": "wbstream",
				"token_ref":    wbBinding.TokenID,
				"wbstream": map[string]string{
					"server_url": defaultManagedWBStreamServerURL,
				},
				"wbstream_legacy": map[string]string{
					"access_token":       token.Parts["access_token"],
					"cookie_header":      token.Parts["cookie_header"],
					"cookies_file":       token.Parts["cookies_file"],
					"local_storage_file": token.Parts["local_storage_file"],
				},
				"endpoint": map[string]any{"id": "wb-egress", "address": "*"},
			}); err != nil {
				return nil, nil, err
			}
		}
	}

	// DION video tunnel carrier
	if carrierIDs["dion"] {
		dionBinding := firstBindingWithRole(filterBindings(enabledBindings, "dion", "video"), "creator")
		if dionBinding == nil {
			dionBinding = firstBinding(filterBindings(enabledBindings, "dion", "video"))
		}
		if dionBinding != nil {
			dionToken := tokenByID[dionBinding.TokenID]
			config := map[string]any{
				"id":        "dion",
				"token_ref": dionBinding.TokenID,
				"endpoint":  map[string]any{"id": "dion-egress", "address": "*"},
			}
			if dionToken.Parts["access_token"] != "" || dionToken.Parts["refresh_token"] != "" {
				config["dion"] = map[string]string{
					"role":          "creator",
					"access_token":  dionToken.Parts["access_token"],
					"refresh_token": dionToken.Parts["refresh_token"],
				}
			}
			if err := addCarrier("dion", config); err != nil {
				return nil, nil, err
			}
		}
	}

	// Telemost video tunnel carrier
	if carrierIDs["telemost"] {
		tmBinding := firstBindingWithRole(filterBindings(enabledBindings, "telemost", "video"), "creator")
		if tmBinding == nil {
			tmBinding = firstBinding(filterBindings(enabledBindings, "telemost", "video"))
		}
		if tmBinding != nil {
			tmToken := tokenByID[tmBinding.TokenID]
			config := map[string]any{
				"id":        "telemost",
				"token_ref": tmBinding.TokenID,
				"endpoint":  map[string]any{"id": "telemost-egress", "address": "*"},
			}
			if tmToken.Parts["cookie"] != "" {
				config["telemost"] = map[string]string{
					"cookie": tmToken.Parts["cookie"],
					"role":   "creator",
				}
			}
			if err := addCarrier("telemost", config); err != nil {
				return nil, nil, err
			}
		}
	}

	if len(carriers) == 0 {
		return nil, nil, fmt.Errorf("token store produced no usable managed daemon carriers")
	}

	return carriers, configs, nil
}

func filterBindings(bindings []bindingEntry, platform string, connectionType string) []bindingEntry {
	out := make([]bindingEntry, 0)
	for _, binding := range bindings {
		if binding.Platform == platform && binding.ConnectionType == connectionType {
			out = append(out, binding)
		}
	}
	return out
}

func firstBinding(bindings []bindingEntry) *bindingEntry {
	if len(bindings) == 0 {
		return nil
	}
	return &bindings[0]
}

func firstBindingWithRole(bindings []bindingEntry, role string) *bindingEntry {
	for index := range bindings {
		if bindings[index].Role == role {
			return &bindings[index]
		}
	}
	return firstBinding(bindings)
}

func bindingWithRole(bindings []bindingEntry, role string) *bindingEntry {
	for index := range bindings {
		if bindings[index].Role == role {
			return &bindings[index]
		}
	}
	return nil
}

func tokenHasSimpleValue(tokens map[string]tokenEntry, tokenID string) bool {
	token, ok := tokens[tokenID]
	return ok && strings.TrimSpace(token.Value) != ""
}

func runtimeAPIListenAddress(rawAPIURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawAPIURL))
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return "127.0.0.1:17680"
}

func socksListenAddress() string {
	host := firstNonEmptyEnv("WT_NATIVE_GUI_SOCKS_HOST", "WT_DESKTOP_SOCKS_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := firstNonEmptyEnv("WT_NATIVE_GUI_SOCKS_PORT", "WT_DESKTOP_SOCKS_PORT")
	if port == "" {
		port = "8809"
	}
	return net.JoinHostPort(host, port)
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// applyClientCredentialOverrides patches carrier configs with the client's own
// platform credentials from client-tokens.json. This is what makes role-reversal
// work: the WBStream/Telemost/DION adapter on the client side uses the client's
// personal access token (not the shared node token) to create the egress room.
// If a video-tunnel carrier is not present in the generated configs, it is added.
func applyClientCredentialOverrides(carrierIDs []string, configs []json.RawMessage, creds []ClientCredential) ([]string, []json.RawMessage) {
	byPlatform := make(map[string]ClientCredential)
	for _, cred := range creds {
		if cred.Token == "" && cred.Cookie == "" {
			continue
		}
		byPlatform[cred.Platform] = cred
	}

	patched := make([]json.RawMessage, len(configs))
	copy(patched, configs)

	hasCarrier := func(id string) int {
		for i, cid := range carrierIDs {
			if cid == id {
				return i
			}
		}
		return -1
	}

	if cred, ok := byPlatform["wbstream"]; ok {
		idx := hasCarrier("wbstream.vp8")
		if idx >= 0 {
			patched[idx] = overrideCarrierConfig(configs[idx], map[string]any{
				"wbstream_legacy": map[string]string{
					"access_token":  cred.Token,
					"cookie_header": cred.Cookie,
				},
			})
		}
	}

	if cred, ok := byPlatform["telemost"]; ok {
		idx := hasCarrier("telemost")
		if idx >= 0 {
			telemost := map[string]string{
				"cookie": cred.Cookie,
				"role":   "creator",
			}
			if cred.Extra != "" {
				telemost["join_link"] = cred.Extra
			}
			patched[idx] = overrideCarrierConfig(configs[idx], map[string]any{
				"telemost": telemost,
			})
		}
	}

	if cred, ok := byPlatform["dion"]; ok {
		idx := hasCarrier("dion")
		if idx >= 0 {
			patched[idx] = overrideCarrierConfig(configs[idx], map[string]any{
				"dion": map[string]string{
					"role":          "creator",
					"access_token":  cred.Token,
					"refresh_token": cred.Extra,
				},
			})
		} else {
			carrierIDs = append(carrierIDs, "dion")
			payload, err := json.Marshal(map[string]any{
				"id":       "dion",
				"endpoint": map[string]any{"id": "dion-egress", "address": "*"},
				"dion": map[string]string{
					"role":          "creator",
					"access_token":  cred.Token,
					"refresh_token": cred.Extra,
				},
			})
			if err == nil {
				patched = append(patched, payload)
			}
		}
	}

	return carrierIDs, patched
}

// mergeClientCredentialTokenStore turns a device-local DION browser session
// into a local-only creator binding. The generated config stays mode 0600 and
// this credential never propagates back into the shared source TokenStore.
func mergeClientCredentialTokenStore(store tokenStoreFile, creds []ClientCredential) tokenStoreFile {
	const tokenID = "client-dion-browseros"
	var credential *ClientCredential
	for index := range creds {
		if creds[index].Platform == "dion" && strings.TrimSpace(creds[index].Token) != "" {
			credential = &creds[index]
		}
	}
	if credential == nil {
		return store
	}

	filteredTokens := store.Tokens[:0]
	for _, token := range store.Tokens {
		if token.ID != tokenID {
			filteredTokens = append(filteredTokens, token)
		}
	}
	store.Tokens = append(filteredTokens, tokenEntry{
		ID: tokenID, Platform: "dion", Kind: "composite", Lifecycle: "embedded", Status: "active",
		Parts: map[string]string{"access_token": credential.Token, "refresh_token": credential.Extra},
	})

	filteredBindings := store.Bindings[:0]
	for _, binding := range store.Bindings {
		if binding.TokenID != tokenID {
			filteredBindings = append(filteredBindings, binding)
		}
	}
	store.Bindings = append(filteredBindings, bindingEntry{
		TokenID: tokenID, Platform: "dion", ConnectionType: "video", ChannelID: "*", Role: "creator", Priority: 1000, Enabled: true,
	})
	return store
}

func overrideCarrierConfig(raw json.RawMessage, overrides map[string]any) json.RawMessage {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	for key, val := range overrides {
		if patchMap, ok := val.(map[string]string); ok {
			existing, _ := obj[key].(map[string]any)
			if existing == nil {
				existing = map[string]any{}
			}
			for k, v := range patchMap {
				if v != "" {
					existing[k] = v
				}
			}
			obj[key] = existing
		} else {
			obj[key] = val
		}
	}
	payload, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return payload
}
