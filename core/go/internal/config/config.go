package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

// Role controls daemon behavior.
type Role string

const (
	RoleClient Role = "client"
	RoleNode   Role = "node"
)

// UpstreamProxy configures optional client egress proxying in node mode.
type UpstreamProxy struct {
	URL              string `json:"url"`
	ClientEgressOnly bool   `json:"client_egress_only"`
	ApplyToCarriers  bool   `json:"apply_to_carriers"`
}

// TokenEntry is the on-disk representation of one token in the token_store block.
type TokenEntry struct {
	ID                string            `json:"id"`
	Platform          string            `json:"platform"`
	Kind              tokens.TokenKind  `json:"kind"`
	Lifecycle         tokens.Lifecycle  `json:"lifecycle"`
	Status            tokens.Status     `json:"status"`
	Value             string            `json:"value,omitempty"`
	Parts             map[string]string `json:"parts,omitempty"`
	Refresh           string            `json:"refresh,omitempty"`
	CanCreateChannels bool              `json:"can_create_channels"`
	ExpiresAt         *string           `json:"expires_at,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
}

// BindingEntry is the on-disk representation of one binding in the token_store block.
type BindingEntry struct {
	TokenID        string `json:"token_id"`
	Platform       string `json:"platform"`
	ConnectionType string `json:"connection_type"`
	ChannelID      string `json:"channel_id"`
	Role           string `json:"role"`
	Priority       int    `json:"priority"`
	Enabled        bool   `json:"enabled"`
}

// TokenStoreConfig is the top-level token_store block in the config file.
type TokenStoreConfig struct {
	MasterKeyEnv string         `json:"master_key_env,omitempty"`
	Tokens       []TokenEntry   `json:"tokens,omitempty"`
	Bindings     []BindingEntry `json:"bindings,omitempty"`
}

// Config holds daemon runtime configuration.
type Config struct {
	Role            Role                 `json:"role"`
	NodeID          string               `json:"node_id"`
	ClientID        string               `json:"client_id"`
	DisplayName     string               `json:"display_name,omitempty"`
	Country         string               `json:"country,omitempty"`
	Region          string               `json:"region,omitempty"`
	ListenAPI       string               `json:"listen_api"`
	SocksListen     string               `json:"socks_listen"`
	AdminReporter   AdminReporterConfig  `json:"admin_reporter,omitempty"`
	AdminDiscovery  AdminDiscoveryConfig `json:"admin_discovery,omitempty"`
	AdminRelay      AdminRelayConfig     `json:"admin_relay,omitempty"`
	EnabledCarriers []string             `json:"enabled_carriers"`
	CarrierConfigs  []CarrierConfig      `json:"carrier_configs"`
	UpstreamProxy   UpstreamProxy        `json:"upstream_proxy"`
	// BootstrapSecret is an optional WhiteTransport-only secret used to wrap
	// per-session keys. It is deliberately separate from provider API tokens.
	// Nodes and clients must be provisioned with the same value for v2 peers.
	BootstrapSecret string            `json:"bootstrap_secret,omitempty"`
	TokenStore      *TokenStoreConfig `json:"token_store,omitempty"`
	StateFile       string            `json:"state_file,omitempty"`
	// ClientRoomCreation enables the role-reversal flow where the client
	// creates the egress room locally using its own platform credentials and
	// the node joins as guest. Requires local video tunnel credentials.
	// Default false — legacy node-creates-room behavior.
	ClientRoomCreation bool                `json:"client_room_creation,omitempty"`
	Routing            RoutingConfig       `json:"routing,omitempty"`
	SessionSSH         SessionSSHConfig    `json:"session_ssh,omitempty"`
	SessionEgress      SessionEgressConfig `json:"session_egress,omitempty"`
}

// SessionSSHConfig enables a node-owned, per-session OpenSSH direct-tcpip
// endpoint. Credentials are generated at offer time and delivered only inside
// the encrypted session answer; they are not part of TokenStore.
type SessionSSHConfig struct {
	Enabled             bool     `json:"enabled,omitempty"`
	BaseDir             string   `json:"base_dir,omitempty"`
	SSHDPath            string   `json:"sshd_path,omitempty"`
	Username            string   `json:"username,omitempty"`
	ListenHost          string   `json:"listen_host,omitempty"`
	AdvertiseHost       string   `json:"advertise_host,omitempty"`
	PortMin             int      `json:"port_min,omitempty"`
	PortMax             int      `json:"port_max,omitempty"`
	HostKeyFiles        []string `json:"host_key_files,omitempty"`
	TTLSeconds          int      `json:"ttl_seconds,omitempty"`
	StartupTimeoutSecs  int      `json:"startup_timeout_seconds,omitempty"`
	AllowWildcardListen bool     `json:"allow_wildcard_listen,omitempty"`
}

// SessionEgressConfig holds client-local runtime settings for encrypted,
// per-session egress profiles. It intentionally excludes remote credentials.
type SessionEgressConfig struct {
	SingBox *SessionSingBoxRuntimeConfig `json:"sing_box,omitempty"`
}

// SessionSingBoxRuntimeConfig contains only trusted local sidecar settings.
// Server-issued profiles supply remote VLESS fields for one session in memory.
type SessionSingBoxRuntimeConfig struct {
	BinaryPath       string `json:"binary_path,omitempty"`
	ConfigDir        string `json:"config_dir,omitempty"`
	LocalListen      string `json:"local_listen,omitempty"`
	StartTimeoutSecs int    `json:"start_timeout_secs,omitempty"`
}

// RoutingConfig controls the host system-VPN route policy. Mode is the
// daemon-facing normalized value: none (full tunnel), bypass (full tunnel
// with exact destination routes excluded), or only (only exact destination
// routes included). FullTunnel and DestinationSplit are explicit config
// contract flags used by generated configs; they must not contradict Mode.
type RoutingConfig struct {
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

const (
	RouteModeNone   = "none"
	RouteModeBypass = "bypass"
	RouteModeOnly   = "only"
)

// NormalizedRouteMode validates and returns the user-facing route mode.
// Legacy all_proxy is accepted as an alias for the full-tunnel none mode.
func (r RoutingConfig) NormalizedRouteMode() (string, error) {
	mode := strings.TrimSpace(strings.ToLower(r.Mode))
	if mode == "" || mode == "all_proxy" {
		mode = RouteModeNone
	}
	if mode != RouteModeNone && mode != RouteModeBypass && mode != RouteModeOnly {
		return "", fmt.Errorf("routing mode %q is unsupported", mode)
	}
	if r.FullTunnel && r.DestinationSplit {
		return "", fmt.Errorf("routing full_tunnel and destination_split cannot both be true")
	}
	if r.FullTunnel && mode != RouteModeNone {
		return "", fmt.Errorf("routing full_tunnel requires mode none")
	}
	if r.DestinationSplit && mode == RouteModeNone {
		return "", fmt.Errorf("routing destination_split requires bypass or only mode")
	}
	if mode == RouteModeNone && len(r.DestinationCIDRs) != 0 {
		return "", fmt.Errorf("full-tunnel routing cannot include destination CIDRs")
	}
	if (mode == RouteModeOnly || (mode == RouteModeBypass && r.DestinationSplit)) && len(r.DestinationCIDRs) == 0 {
		return "", fmt.Errorf("routing mode %s requires destination CIDRs", mode)
	}
	if mode == RouteModeBypass && len(r.DestinationCIDRs) == 0 && !r.LANAccess {
		return "", fmt.Errorf("routing mode bypass requires destination CIDRs or lan_access")
	}
	seen := make(map[string]struct{}, len(r.DestinationCIDRs))
	for _, raw := range r.DestinationCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return "", fmt.Errorf("routing destination %q is not a CIDR: %w", raw, err)
		}
		prefix = prefix.Masked()
		if prefix.Bits() == 0 {
			return "", fmt.Errorf("routing destination %q must not be a default route", raw)
		}
		if _, exists := seen[prefix.String()]; exists {
			return "", fmt.Errorf("routing destination %q is duplicated", prefix)
		}
		seen[prefix.String()] = struct{}{}
	}
	if r.MTU != 0 && (r.MTU < 576 || r.MTU > 65535) {
		return "", fmt.Errorf("routing mtu %d is outside 576..65535", r.MTU)
	}
	for _, server := range r.DNSServers {
		if net.ParseIP(strings.TrimSpace(server)) == nil {
			return "", fmt.Errorf("routing dns server %q is invalid", server)
		}
	}
	return mode, nil
}

type AdminReporterConfig struct {
	Enabled         bool   `json:"enabled"`
	AdminURL        string `json:"admin_url,omitempty"`
	Token           string `json:"token,omitempty"`
	TokenEnv        string `json:"token_env,omitempty"`
	RegisterPath    string `json:"register_path,omitempty"`
	HeartbeatPath   string `json:"heartbeat_path,omitempty"`
	LogsPath        string `json:"logs_path,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	IP              string `json:"ip,omitempty"`
	APIHost         string `json:"api_host,omitempty"`
	APIPort         int    `json:"api_port,omitempty"`
	NodeVersion     string `json:"node_version,omitempty"`
	UploadLogs      bool   `json:"upload_logs,omitempty"` // periodically upload recent logs
}

func (c AdminReporterConfig) TokenValue() string {
	if strings.TrimSpace(c.Token) != "" {
		return strings.TrimSpace(c.Token)
	}
	if strings.TrimSpace(c.TokenEnv) != "" {
		return strings.TrimSpace(os.Getenv(c.TokenEnv))
	}
	return ""
}

// AdminDiscoveryConfig enables simple HTTP-based node discovery via the
// admin panel. Clients poll GET /api/discovery/nodes to find live nodes
// instead of (or in addition to) scanning VK/OK bootstrap carriers.
type AdminDiscoveryConfig struct {
	Enabled         bool   `json:"enabled"`
	AdminURL        string `json:"admin_url,omitempty"`
	Token           string `json:"token,omitempty"`
	TokenEnv        string `json:"token_env,omitempty"`
	PollIntervalSec int    `json:"poll_interval_sec,omitempty"` // default: 30
	StatusFilter    string `json:"status_filter,omitempty"`     // default: "online"
}

func (c AdminDiscoveryConfig) TokenValue() string {
	if strings.TrimSpace(c.Token) != "" {
		return strings.TrimSpace(c.Token)
	}
	if strings.TrimSpace(c.TokenEnv) != "" {
		return strings.TrimSpace(os.Getenv(c.TokenEnv))
	}
	return ""
}

// AdminRelayConfig enables HTTP-based relay messaging through the admin
// panel. Nodes and clients exchange envelopes via named channels
// (discovery, control, logs, relay) when direct P2P is blocked by NAT.
type AdminRelayConfig struct {
	Enabled         bool     `json:"enabled"`
	AdminURL        string   `json:"admin_url,omitempty"`
	TokenRef        string   `json:"token_ref,omitempty"`
	Token           string   `json:"token,omitempty"`             // deprecated: runtime deployments resolve TokenStore
	TokenEnv        string   `json:"token_env,omitempty"`         // deprecated: secrets must not use environment variables
	Channels        []string `json:"channels,omitempty"`          // default: ["discovery","control","logs","relay"]
	PollIntervalSec int      `json:"poll_interval_sec,omitempty"` // default: 3
	Identity        string   `json:"identity,omitempty"`          // node_id or client_id to use as sender
}

func (c AdminRelayConfig) TokenValue() string {
	if strings.TrimSpace(c.Token) != "" {
		return strings.TrimSpace(c.Token)
	}
	if strings.TrimSpace(c.TokenEnv) != "" {
		return strings.TrimSpace(os.Getenv(c.TokenEnv))
	}
	return ""
}

func (c AdminRelayConfig) EffectiveChannels() []string {
	if len(c.Channels) > 0 {
		return c.Channels
	}
	return []string{"discovery", "control", "logs", "relay"}
}

// CarrierConfig binds one carrier id to a runtime endpoint and provider
// credentials.
type CarrierConfig struct {
	ID string `json:"id"`
	// CarrierType is the canonical carrier implementation for this binding.
	// ID stays unique per configured endpoint, so several profiles can share
	// one implementation such as singbox.vless.
	CarrierType     string                 `json:"carrier_type,omitempty"`
	Endpoint        EndpointConfig         `json:"endpoint"`
	VKMessages      *VKMessagesConfig      `json:"vk_messages,omitempty"`
	OKMessages      *OKMessagesConfig      `json:"ok_messages,omitempty"`
	VKDocs          *VKDocsConfig          `json:"vk_docs,omitempty"`
	OKDocs          *OKDocsConfig          `json:"ok_docs,omitempty"`
	YandexDisk      *YandexDiskConfig      `json:"yandex_disk,omitempty"`
	SSH             *SSHConfig             `json:"ssh,omitempty"`
	SSHFabric       *SSHFabricConfig       `json:"ssh_fabric,omitempty"`
	SingBox         *SingBoxConfig         `json:"sing_box,omitempty"`
	WBStream        *WBStreamConfig        `json:"wbstream_legacy,omitempty"`
	WhitelistBypass *WhitelistBypassConfig `json:"wbstream,omitempty"`
	FileMailbox     *FileMailboxConfig     `json:"file_mailbox,omitempty"`
	Telemost        *TelemostConfig        `json:"telemost,omitempty"`
	Dion            *DionConfig            `json:"dion,omitempty"`
	TokenRef        string                 `json:"token_ref,omitempty"`

	// Legacy top-level fields — kept for backward compatibility with
	// existing deployment configs that use "name" and "address" at the
	// carrier config level instead of an "endpoint" object.
	LegacyName    string `json:"name,omitempty"`
	LegacyAddress string `json:"address,omitempty"`
}

// populateEndpoint fills Endpoint from legacy top-level fields when the
// new nested endpoint block is absent.
func (cc *CarrierConfig) populateEndpoint() {
	if cc.Endpoint.Address == "" && cc.Endpoint.ID == "" && len(cc.Endpoint.Metadata) == 0 {
		if cc.LegacyAddress != "" {
			cc.Endpoint.Address = cc.LegacyAddress
		}
		if cc.LegacyName != "" {
			cc.Endpoint.ID = cc.LegacyName
		}
	}
}

// EndpointConfig is a session/runtime address for one carrier.
type EndpointConfig struct {
	ID       string            `json:"id"`
	Address  string            `json:"address"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// VKChannelConfig assigns a VK peer ID to a specific traffic role.
// Multiple channels per VK token allow flexible routing: discovery, session
// control, log upload, and admin can each target a different peer ID.
type VKChannelConfig struct {
	PeerID string `json:"peer_id"`
	Role   string `json:"role"` // "discovery", "node-client", "logs", "admin", "flex"
	Label  string `json:"label,omitempty"`
}

// VKMessagesConfig contains VK message adapter settings.
type VKMessagesConfig struct {
	Token      string            `json:"token,omitempty"`
	TokenEnv   string            `json:"token_env,omitempty"`
	APIVersion string            `json:"api_version,omitempty"`
	BaseURL    string            `json:"base_url,omitempty"`
	Channels   []VKChannelConfig `json:"channels,omitempty"`
}

// OKChannelConfig assigns an OK chat ID to a specific traffic role.
// Analogous to VKChannelConfig but uses OK chat_id instead of VK peer_id.
type OKChannelConfig struct {
	ChatID string `json:"chat_id"`
	Role   string `json:"role"` // "discovery", "node-client", "logs", "admin", "flex"
	Label  string `json:"label,omitempty"`
}

// OKMessagesConfig contains OK Graph message adapter settings.
type OKMessagesConfig struct {
	Token    string            `json:"token,omitempty"`
	TokenEnv string            `json:"token_env,omitempty"`
	BaseURL  string            `json:"base_url,omitempty"`
	SendPath string            `json:"send_path,omitempty"`
	ReadPath string            `json:"read_path,omitempty"`
	Channels []OKChannelConfig `json:"channels,omitempty"`
}

// VKDocsConfig contains VK document adapter settings.
type VKDocsConfig struct {
	Token      string `json:"token,omitempty"`
	TokenEnv   string `json:"token_env,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
}

// OKDocsConfig contains OK fb.do document adapter settings.
type OKDocsConfig struct {
	AccessToken         string `json:"access_token,omitempty"`
	AccessTokenEnv      string `json:"access_token_env,omitempty"`
	ApplicationKey      string `json:"application_key,omitempty"`
	ApplicationKeyEnv   string `json:"application_key_env,omitempty"`
	SessionSecretKey    string `json:"session_secret_key,omitempty"`
	SessionSecretKeyEnv string `json:"session_secret_key_env,omitempty"`
	BaseURL             string `json:"base_url,omitempty"`
}

// YandexDiskConfig contains Yandex Disk file-mailbox adapter settings.
type YandexDiskConfig struct {
	OAuthToken        string `json:"oauth_token,omitempty"`
	OAuthTokenEnv     string `json:"oauth_token_env,omitempty"`
	CookieHeader      string `json:"cookie_header,omitempty"`
	CookieHeaderEnv   string `json:"cookie_header_env,omitempty"`
	BaseURL           string `json:"base_url,omitempty"`
	BasePath          string `json:"base_path,omitempty"`
	MaxFileSizeBytes  int    `json:"max_file_size_bytes,omitempty"`
	CleanupAfterRead  *bool  `json:"cleanup_after_read,omitempty"`
	MinSendIntervalMs int    `json:"min_send_interval_ms,omitempty"`
}

// SSHConfig contains NekoBox/sing-box-style SSH outbound settings.
type SSHConfig struct {
	Username                string   `json:"username,omitempty"`
	Password                string   `json:"password,omitempty"`
	PasswordEnv             string   `json:"password_env,omitempty"`
	PrivateKey              string   `json:"private_key,omitempty"`
	PrivateKeyEnv           string   `json:"private_key_env,omitempty"`
	PrivateKeyPath          string   `json:"private_key_path,omitempty"`
	PrivateKeyPassphrase    string   `json:"private_key_passphrase,omitempty"`
	PrivateKeyPassphraseEnv string   `json:"private_key_passphrase_env,omitempty"`
	UseAgent                bool     `json:"use_agent,omitempty"`
	AgentSocketPath         string   `json:"agent_socket_path,omitempty"`
	HostKeys                []string `json:"host_keys,omitempty"`
	ServerAliveIntervalSecs int      `json:"server_alive_interval_secs,omitempty"`
}

// SSHFabricConfig contains client credentials and optional future server
// listener settings for the combined control-and-egress SSH carrier.
type SSHFabricConfig struct {
	Client SSHConfig              `json:"client"`
	Server *SSHFabricServerConfig `json:"server,omitempty"`
}

// SSHFabricServerConfig describes an authenticated SSH fabric listener.
// Runtime server startup is intentionally wired separately from config parsing.
type SSHFabricServerConfig struct {
	ListenAddress            string   `json:"listen_address"`
	LocalClientAddress       string   `json:"local_client_address,omitempty"`
	HostPrivateKey           string   `json:"host_private_key,omitempty"`
	HostPrivateKeyPath       string   `json:"host_private_key_path,omitempty"`
	HostPrivateKeyPassphrase string   `json:"host_private_key_passphrase,omitempty"`
	AuthorizedClientKeys     []string `json:"authorized_client_keys,omitempty"`
	RetentionLimit           int      `json:"retention_limit,omitempty"`
	AllowedTargets           []string `json:"allowed_targets,omitempty"`
}

// SingBoxConfig contains VLESS outbound settings for a managed sing-box process.
type SingBoxConfig struct {
	URI              string `json:"uri,omitempty"`
	URIEnv           string `json:"uri_env,omitempty"`
	BinaryPath       string `json:"binary_path,omitempty"`
	BinaryPathEnv    string `json:"binary_path_env,omitempty"`
	ConfigDir        string `json:"config_dir,omitempty"`
	ConfigDirEnv     string `json:"config_dir_env,omitempty"`
	Server           string `json:"server,omitempty"`
	ServerPort       int    `json:"server_port,omitempty"`
	UUID             string `json:"uuid,omitempty"`
	UUIDEnv          string `json:"uuid_env,omitempty"`
	Network          string `json:"network,omitempty"`
	Flow             string `json:"flow,omitempty"`
	TLSEnabled       bool   `json:"tls_enabled,omitempty"`
	TLSServerName    string `json:"tls_server_name,omitempty"`
	TLSInsecure      bool   `json:"tls_insecure,omitempty"`
	UTLSFingerprint  string `json:"utls_fingerprint,omitempty"`
	TransportType    string `json:"transport_type,omitempty"`
	TransportHost    string `json:"transport_host,omitempty"`
	TransportPath    string `json:"transport_path,omitempty"`
	LocalListen      string `json:"local_listen,omitempty"`
	StartTimeoutSecs int    `json:"start_timeout_secs,omitempty"`
}

// FileMailboxConfig contains the local file-backed mailbox carrier settings,
// used for deterministic cross-process control testing in place of VK/OK.
type FileMailboxConfig struct {
	Dir         string `json:"dir,omitempty"`
	DirEnv      string `json:"dir_env,omitempty"`
	AllowEgress bool   `json:"allow_egress,omitempty"`
}

// WhitelistBypassConfig contains WBStream adapter settings (LiveKit DataChannel).
type WhitelistBypassConfig struct {
	ServerURL      string `json:"server_url,omitempty"`
	ServerURLEnv   string `json:"server_url_env,omitempty"`
	AccessToken    string `json:"access_token,omitempty"`
	AccessTokenEnv string `json:"access_token_env,omitempty"`
	RoomToken      string `json:"room_token,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	TunnelMode     string `json:"tunnel_mode,omitempty"`
}

// WBStreamConfig contains WBStream WebRTC carrier settings.
type WBStreamConfig struct {
	AccessToken      string `json:"access_token,omitempty"`
	AccessTokenEnv   string `json:"access_token_env,omitempty"`
	CookieHeader     string `json:"cookie_header,omitempty"`
	CookieHeaderEnv  string `json:"cookie_header_env,omitempty"`
	LocalStoragePath string `json:"local_storage_path,omitempty"`
	// CookiesFile is a path to a browser-exported cookies JSON file used by
	// upstream whitelist-bypass adapters for authenticated WBStream access.
	CookiesFile string `json:"cookies_file,omitempty"`
	// LocalStorageFile is a path to a browser-exported localStorage TSV file
	// containing the wb_auth_auth_slice JWT and related keys.
	LocalStorageFile string `json:"local_storage_file,omitempty"`
}

// TelemostConfig contains Yandex Telemost video call adapter settings.
type TelemostConfig struct {
	JoinLink    string `json:"join_link,omitempty"`
	Cookie      string `json:"cookie,omitempty"`
	CookieEnv   string `json:"cookie_env,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role,omitempty"` // "creator" or "joiner"
}

// DionConfig contains DION video call adapter settings.
type DionConfig struct {
	EventID         string `json:"event_id,omitempty"`
	AccessToken     string `json:"access_token,omitempty"`
	AccessTokenEnv  string `json:"access_token_env,omitempty"`
	RefreshToken    string `json:"refresh_token,omitempty"`
	RefreshTokenEnv string `json:"refresh_token_env,omitempty"`
	CookiesFile     string `json:"cookies_file,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
	Role            string `json:"role,omitempty"` // "creator" or "joiner"
}

// Load reads a JSON config file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	for i := range cfg.CarrierConfigs {
		cfg.CarrierConfigs[i].populateEndpoint()
	}

	// Decrypt encrypted token values if a master key is available.
	if cfg.TokenStore != nil {
		if err := cfg.decryptTokenStore(); err != nil {
			return Config{}, fmt.Errorf("token_store: %w", err)
		}
	}

	return cfg, nil
}

// decryptTokenStore decrypts any enc:v1: token values in the token store.
func (c *Config) decryptTokenStore() error {
	masterKey, err := tokens.LoadMasterKey()
	if err != nil {
		// If no master key is available, skip decryption (tokens may be plaintext).
		return nil
	}
	for i, te := range c.TokenStore.Tokens {
		if strings.HasPrefix(te.Value, "enc:v1:") {
			plain, err := tokens.DecryptToken(te.Value, masterKey)
			if err != nil {
				return fmt.Errorf("token %s: %w", te.ID, err)
			}
			c.TokenStore.Tokens[i].Value = plain
		}
		for k, v := range te.Parts {
			if strings.HasPrefix(v, "enc:v1:") {
				plain, err := tokens.DecryptToken(v, masterKey)
				if err != nil {
					return fmt.Errorf("token %s part %s: %w", te.ID, k, err)
				}
				c.TokenStore.Tokens[i].Parts[k] = plain
			}
		}
	}
	return nil
}

// adapterRegistryNames lists carrier IDs served by the adapter registry
// rather than the standard descriptor catalog. Kept in sync with
// runtime.ProviderRegistry.registerBuiltins().
var adapterRegistryNames = map[string]bool{
	"wbstream": true,
	"telemost": true,
	"dion":     true,
}

// enabledCarrierRuntimeID resolves an enabled binding alias to the concrete
// carrier implementation declared by its matching config. IDs remain unique
// route identities, while CarrierType selects the shared implementation.
func (c Config) enabledCarrierRuntimeID(id string) string {
	trimmedID := strings.TrimSpace(id)
	for _, carrierConfig := range c.CarrierConfigs {
		if strings.TrimSpace(carrierConfig.ID) != trimmedID {
			continue
		}
		if carrierType := strings.TrimSpace(carrierConfig.CarrierType); carrierType != "" {
			return carrierType
		}
		break
	}
	return trimmedID
}

// Validate checks config-level invariants.
func (c Config) Validate() error {
	if _, err := c.Routing.NormalizedRouteMode(); err != nil {
		return err
	}
	if c.SessionSSH.Enabled {
		if c.Role != RoleNode {
			return fmt.Errorf("session_ssh is available only in node role")
		}
		if strings.TrimSpace(c.SessionSSH.BaseDir) == "" || strings.TrimSpace(c.SessionSSH.SSHDPath) == "" || strings.TrimSpace(c.SessionSSH.Username) == "" {
			return fmt.Errorf("session_ssh requires base_dir, sshd_path and username")
		}
		if strings.TrimSpace(c.SessionSSH.ListenHost) == "" || strings.TrimSpace(c.SessionSSH.AdvertiseHost) == "" {
			return fmt.Errorf("session_ssh requires listen_host and advertise_host")
		}
		if c.SessionSSH.PortMin < 1024 || c.SessionSSH.PortMax > 65535 || c.SessionSSH.PortMin > c.SessionSSH.PortMax {
			return fmt.Errorf("session_ssh requires a valid high port range")
		}
		if len(c.SessionSSH.HostKeyFiles) == 0 || c.SessionSSH.TTLSeconds <= 0 {
			return fmt.Errorf("session_ssh requires host_key_files and a positive ttl_seconds")
		}
	}
	if c.AdminReporter.Enabled {
		if strings.TrimSpace(c.AdminReporter.AdminURL) == "" {
			return fmt.Errorf("admin_reporter.admin_url is required when admin_reporter.enabled=true")
		}
	}

	for _, id := range c.EnabledCarriers {
		runtimeID := c.enabledCarrierRuntimeID(id)
		if adapterRegistryNames[runtimeID] {
			continue // adapter-registry carrier, not in standard catalog
		}
		if _, err := carriers.FindStandardDescriptor(runtimeID); err != nil {
			return fmt.Errorf("enabled_carriers: %w", err)
		}
	}
	for _, carrierConfig := range c.CarrierConfigs {
		carrierType := strings.TrimSpace(carrierConfig.CarrierType)
		if carrierType == "" || adapterRegistryNames[carrierType] {
			continue
		}
		if _, err := carriers.FindStandardDescriptor(carrierType); err != nil {
			return fmt.Errorf("carrier config %s carrier_type: %w", carrierConfig.ID, err)
		}
	}

	// Channel validation when feature flag is active.
	if ChannelBindingsEnabled() {
		for _, cc := range c.CarrierConfigs {
			if cc.VKMessages != nil && len(cc.VKMessages.Channels) > 0 {
				if err := ValidateVKChannels(cc.VKMessages.Channels); err != nil {
					return fmt.Errorf("carrier %s: %w", cc.ID, err)
				}
			}
			if cc.OKMessages != nil && len(cc.OKMessages.Channels) > 0 {
				if err := ValidateOKChannels(cc.OKMessages.Channels); err != nil {
					return fmt.Errorf("carrier %s: %w", cc.ID, err)
				}
			}
		}
	}

	return nil
}

// CarrierDescriptors resolves enabled carriers into planner descriptors.
// Adapter-registry carriers (wbstream, telemost, dion) are resolved at
// runtime via ProviderCarrier, not via the standard catalog.
func (c Config) CarrierDescriptors() ([]carriers.Descriptor, error) {
	if len(c.EnabledCarriers) == 0 {
		return carriers.StandardDescriptors(), nil
	}

	out := make([]carriers.Descriptor, 0, len(c.EnabledCarriers))
	for _, id := range c.EnabledCarriers {
		runtimeID := c.enabledCarrierRuntimeID(id)
		if adapterRegistryNames[runtimeID] {
			continue // resolved at runtime via adapter registry
		}
		desc, err := carriers.FindStandardDescriptor(runtimeID)
		if err != nil {
			return nil, fmt.Errorf("enabled_carriers: %w", err)
		}
		out = append(out, desc)
	}

	return out, nil
}

// Identity returns the preferred runtime identity for the configured role.
func (c Config) Identity() string {
	if c.Role == RoleClient && strings.TrimSpace(c.ClientID) != "" {
		return strings.TrimSpace(c.ClientID)
	}
	if strings.TrimSpace(c.NodeID) != "" {
		return strings.TrimSpace(c.NodeID)
	}
	if strings.TrimSpace(c.ClientID) != "" {
		return strings.TrimSpace(c.ClientID)
	}
	return string(c.Role)
}

// DisplayLabel returns the user-facing node label.
func (c Config) DisplayLabel() string {
	if strings.TrimSpace(c.DisplayName) != "" {
		return strings.TrimSpace(c.DisplayName)
	}
	if strings.TrimSpace(c.NodeID) != "" {
		return strings.TrimSpace(c.NodeID)
	}
	return c.Identity()
}

// UpstreamProxyFor returns the upstream proxy URL for a traffic class. The
// default is intentionally client-egress only; carrier/control/bootstrap/bulk
// traffic stays direct unless ApplyToCarriers is explicitly enabled.
func (c Config) UpstreamProxyFor(traffic fabric.TrafficClass) string {
	if c.UpstreamProxy.URL == "" {
		return ""
	}

	if traffic == fabric.TrafficEgress {
		return c.UpstreamProxy.URL
	}

	if c.UpstreamProxy.ApplyToCarriers && !c.UpstreamProxy.ClientEgressOnly {
		return c.UpstreamProxy.URL
	}

	return ""
}
