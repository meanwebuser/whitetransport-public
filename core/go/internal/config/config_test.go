package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/tokens"
)

func TestRoutingConfigValidatesFullTunnelAndDestinationSplit(t *testing.T) {
	valid := []RoutingConfig{
		{Mode: RouteModeNone, FullTunnel: true},
		{Mode: RouteModeBypass, DestinationSplit: true, DestinationCIDRs: []string{"203.0.113.0/24"}},
		{Mode: RouteModeBypass, DestinationSplit: true, LANAccess: true, DestinationCIDRs: []string{"2001:db8::/48"}},
		{Mode: RouteModeOnly, DestinationSplit: true, DestinationCIDRs: []string{"198.51.100.0/24"}},
	}
	for _, routing := range valid {
		if _, err := routing.NormalizedRouteMode(); err != nil {
			t.Fatalf("valid routing %#v rejected: %v", routing, err)
		}
	}
	invalid := []RoutingConfig{
		{Mode: RouteModeNone, FullTunnel: true, DestinationSplit: true},
		{Mode: RouteModeNone, DestinationCIDRs: []string{"203.0.113.0/24"}},
		{Mode: RouteModeOnly, DestinationSplit: true},
		{Mode: RouteModeBypass},
		{Mode: RouteModeOnly, DestinationSplit: true, DestinationCIDRs: []string{"0.0.0.0/0"}},
	}
	for _, routing := range invalid {
		if _, err := routing.NormalizedRouteMode(); err == nil {
			t.Fatalf("unsafe routing %#v was accepted", routing)
		}
	}
}

func TestDefaultClientRoutingIsExplicitFullTunnel(t *testing.T) {
	cfg := DefaultClientCarrierConfigs(1080, 8765)
	if cfg.Routing.Mode != RouteModeNone || !cfg.Routing.FullTunnel || cfg.Routing.MTU != 1500 || len(cfg.Routing.DNSServers) == 0 {
		t.Fatalf("default client routing = %+v", cfg.Routing)
	}
}

func TestUpstreamProxyAppliesOnlyToClientEgressByDefault(t *testing.T) {
	cfg := Config{UpstreamProxy: UpstreamProxy{URL: "http://127.0.0.1:3128", ClientEgressOnly: true}}

	if cfg.UpstreamProxyFor(fabric.TrafficEgress) == "" {
		t.Fatal("expected client egress traffic to use upstream proxy")
	}
	if cfg.UpstreamProxyFor(fabric.TrafficStream) != "" {
		t.Fatal("stream carrier traffic must not use upstream proxy by default")
	}
	if cfg.UpstreamProxyFor(fabric.TrafficBulk) != "" {
		t.Fatal("bulk carrier traffic must not use upstream proxy by default")
	}
	if cfg.UpstreamProxyFor(fabric.TrafficBootstrap) != "" {
		t.Fatal("bootstrap carrier traffic must not use upstream proxy by default")
	}
	if cfg.UpstreamProxyFor(fabric.TrafficControl) != "" {
		t.Fatal("control carrier traffic must not use upstream proxy by default")
	}
}

func TestSessionSSHConfigRequiresNodeAndExplicitRuntimeBoundary(t *testing.T) {
	valid := SessionSSHConfig{
		Enabled: true, BaseDir: "/run/white-transport/ssh-sessions", SSHDPath: "/usr/sbin/sshd",
		Username: "wt-egress", ListenHost: "192.0.2.88", AdvertiseHost: "192.0.2.88",
		PortMin: 22000, PortMax: 22020, HostKeyFiles: []string{"/etc/ssh/ssh_host_ed25519_key"}, TTLSeconds: 120,
	}
	if err := (Config{Role: RoleNode, SessionSSH: valid}).Validate(); err != nil {
		t.Fatalf("valid node session SSH config: %v", err)
	}
	if err := (Config{Role: RoleClient, SessionSSH: valid}).Validate(); err == nil {
		t.Fatal("client accepted server-side session SSH issuer")
	}
	invalid := valid
	invalid.AdvertiseHost = ""
	if err := (Config{Role: RoleNode, SessionSSH: invalid}).Validate(); err == nil {
		t.Fatal("issuer without advertised host was accepted")
	}
}

func TestUpstreamProxyCanExplicitlyApplyToCarrierTraffic(t *testing.T) {
	cfg := Config{UpstreamProxy: UpstreamProxy{URL: "http://127.0.0.1:3128", ApplyToCarriers: true}}

	if cfg.UpstreamProxyFor(fabric.TrafficBootstrap) == "" {
		t.Fatal("expected explicit carrier proxy override to affect bootstrap")
	}
}

func TestAdminReporterValidateRequiresAdminURL(t *testing.T) {
	cfg := Config{
		AdminReporter: AdminReporterConfig{
			Enabled: true,
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validate error when admin_reporter is enabled without admin_url")
	}
}

func TestAdminReporterTokenValueFromEnv(t *testing.T) {
	const envName = "WT_ADMIN_REPORTER_TOKEN_TEST"
	t.Setenv(envName, "secret-token")

	cfg := AdminReporterConfig{TokenEnv: envName}
	if got := cfg.TokenValue(); got != "secret-token" {
		t.Fatalf("expected token from env, got %q", got)
	}

	cfg.Token = "inline-token"
	if got := cfg.TokenValue(); got != "inline-token" {
		t.Fatalf("expected inline token to win, got %q", got)
	}
}

func TestAdminReporterTokenValueFromStoreRef(t *testing.T) {
	store := TokenStoreConfig{Tokens: []TokenEntry{{
		ID:     "mac-manual-telemetry",
		Value:  "fixture-telemetry-token",
		Status: "active",
	}}}
	cfg := AdminReporterConfig{TokenRef: "mac-manual-telemetry"}
	if got := cfg.TokenValueFromStore(store); got != "fixture-telemetry-token" {
		t.Fatalf("TokenValueFromStore = %q, want fixture token", got)
	}
}

func TestCarrierDescriptorsDefaultToStandardSet(t *testing.T) {
	cfg := Config{}

	descriptors, err := cfg.CarrierDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != len(carriers.StandardDescriptors()) {
		t.Fatalf("expected standard carrier set, got %d descriptors", len(descriptors))
	}
}

func TestCarrierDescriptorsValidateExplicitIDs(t *testing.T) {
	cfg := Config{EnabledCarriers: []string{carriers.CarrierVKMessages, carriers.CarrierWBStreamVP8}}

	descriptors, err := cfg.CarrierDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 2 {
		t.Fatalf("expected two configured descriptors, got %d", len(descriptors))
	}
	if err := (Config{EnabledCarriers: []string{"yandex.disk"}}).Validate(); err == nil {
		t.Fatal("expected unknown carrier validation error")
	}
}

func TestVKCallAdapterCarrierIsValidatedOutsideStandardCatalog(t *testing.T) {
	cfg := Config{
		EnabledCarriers: []string{"vkcall"},
		CarrierConfigs: []CarrierConfig{{
			ID:       "vkcall",
			Endpoint: EndpointConfig{Address: "2000000001"},
			VKCall:   &VKCallConfig{PeerID: "2000000001"},
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate VK Call adapter carrier: %v", err)
	}
	descriptors, err := cfg.CarrierDescriptors()
	if err != nil {
		t.Fatalf("CarrierDescriptors VK Call adapter carrier: %v", err)
	}
	if len(descriptors) != 0 {
		t.Fatalf("adapter carrier descriptors = %+v, want none", descriptors)
	}
}

func TestDeploymentConfigsParse(t *testing.T) {
	templateDir := filepath.Join("..", "..", "..", "..", "config", "templates")
	manifestPath := filepath.Join(templateDir, "runtime-config-manifest.txt")
	manifest, err := os.Open(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Deployment topology is intentionally absent from source-only public snapshots.
			t.Skip("private deployment templates are not available")
		}
		t.Fatalf("open deployment template manifest: %v", err)
	}
	defer manifest.Close()

	var deploymentConfigs []string
	scanner := bufio.NewScanner(manifest)
	for scanner.Scan() {
		name := scanner.Text()
		if name != "" {
			deploymentConfigs = append(deploymentConfigs, filepath.Join(templateDir, name))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read deployment template manifest: %v", err)
	}
	if len(deploymentConfigs) == 0 {
		t.Fatal("deployment template manifest is empty")
	}
	for _, path := range deploymentConfigs {
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("validate %s: %v", path, err)
		}
		if len(cfg.CarrierConfigs) == 0 {
			t.Fatalf("expected at least one carrier config in %s", path)
		}
		t.Logf("role=%s node=%s carriers=%d", cfg.Role, cfg.NodeID, len(cfg.CarrierConfigs))
	}
}

func TestExampleConfigLoadsCarrierRuntimeConfigs(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.CarrierConfigs) != 3 {
		t.Fatalf("expected three example carrier configs, got %d", len(cfg.CarrierConfigs))
	}
	if cfg.CarrierConfigs[0].VKMessages == nil || cfg.CarrierConfigs[1].OKMessages == nil || cfg.CarrierConfigs[2].WhitelistBypass == nil {
		t.Fatalf("example carrier configs did not decode into typed provider blocks: %+v", cfg.CarrierConfigs)
	}
}

func TestTokenStoreConfigLoads(t *testing.T) {
	data := `{
		"role": "node",
		"node_id": "test-node",
		"listen_api": "127.0.0.1:0",
		"socks_listen": "",
		"enabled_carriers": ["vk.messages"],
		"carrier_configs": [
			{
				"id": "vk.messages",
				"token_ref": "vk-group-1",
				"endpoint": {"id": "vk-discovery", "address": "2000000140"},
				"vk_messages": {"token": "legacy-token-fallback"}
			}
		],
		"token_store": {
			"tokens": [
				{
					"id": "vk-group-1",
					"platform": "vk",
					"kind": "api_key",
					"lifecycle": "embedded",
					"status": "active",
					"value": "plaintext-vk-token-value",
					"can_create_channels": false,
					"tags": {"source": "fixture-community"}
				}
			],
			"bindings": [
				{"token_id": "vk-group-1", "platform": "vk", "connection_type": "messages", "channel_id": "2000000140", "role": "discovery", "priority": 10, "enabled": true},
				{"token_id": "vk-group-1", "platform": "vk", "connection_type": "messages", "channel_id": "2000000142", "role": "node-client", "priority": 10, "enabled": true}
			]
		}
	}`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.TokenStore == nil {
		t.Fatal("expected token_store block to be loaded")
	}
	if len(cfg.TokenStore.Tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(cfg.TokenStore.Tokens))
	}
	if len(cfg.TokenStore.Bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(cfg.TokenStore.Bindings))
	}

	tok := cfg.TokenStore.Tokens[0]
	if tok.ID != "vk-group-1" {
		t.Errorf("token id = %q, want vk-group-1", tok.ID)
	}
	if tok.Kind != tokens.KindAPIKey {
		t.Errorf("token kind = %q, want api_key", tok.Kind)
	}
	if tok.Value != "plaintext-vk-token-value" {
		t.Errorf("token value = %q, want plaintext-vk-token-value", tok.Value)
	}
	if tok.CanCreateChannels {
		t.Error("vk group token should not be able to create channels")
	}

	if cfg.CarrierConfigs[0].TokenRef != "vk-group-1" {
		t.Errorf("carrier token_ref = %q, want vk-group-1", cfg.CarrierConfigs[0].TokenRef)
	}
}

func TestLegacyConfigWithoutTokenStoreStillWorks(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TokenStore != nil {
		t.Fatal("example config should not have a token_store block")
	}
}

func TestValidateChannelsFlagOn(t *testing.T) {
	t.Setenv("WT_CHANNEL_BINDINGS", "1")

	// Valid channels pass validation.
	cfg := Config{
		CarrierConfigs: []CarrierConfig{
			{
				ID: "vk.messages",
				VKMessages: &VKMessagesConfig{
					Channels: []VKChannelConfig{
						{PeerID: "100", Role: "discovery"},
						{PeerID: "200", Role: "logs"},
					},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid channels to pass, got: %v", err)
	}

	// Duplicate role fails.
	cfg.CarrierConfigs[0].VKMessages.Channels = []VKChannelConfig{
		{PeerID: "100", Role: "discovery"},
		{PeerID: "200", Role: "discovery"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate role error")
	}

	// Empty peer_id fails.
	cfg.CarrierConfigs[0].VKMessages.Channels = []VKChannelConfig{
		{PeerID: "", Role: "discovery"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected empty peer_id error")
	}

	// OK channels: invalid role fails.
	cfg.CarrierConfigs = []CarrierConfig{
		{
			ID: "ok.messages",
			OKMessages: &OKMessagesConfig{
				Channels: []OKChannelConfig{
					{ChatID: "chat1", Role: "bogus"},
				},
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid role error for OK channel")
	}
}

// TestValidateAcceptsEnabledCarrierAliasWithStandardCarrierType proves that
// multiple independently-addressed routes can share a standard runtime
// implementation. Local failover uses this for primary and backup
// file.mailbox bindings, and production uses the same contract for profiles.
func TestValidateAcceptsEnabledCarrierAliasWithStandardCarrierType(t *testing.T) {
	cfg := Config{
		EnabledCarriers: []string{"local.egress.primary"},
		CarrierConfigs: []CarrierConfig{{
			ID:          "local.egress.primary",
			CarrierType: carriers.CarrierFileMailbox,
			Endpoint:    EndpointConfig{Address: "primary"},
			FileMailbox: &FileMailboxConfig{Dir: t.TempDir(), AllowEgress: true},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate alias carrier: %v", err)
	}
	descriptors, err := cfg.CarrierDescriptors()
	if err != nil {
		t.Fatalf("CarrierDescriptors alias carrier: %v", err)
	}
	if len(descriptors) != 1 || descriptors[0].ID != carriers.CarrierFileMailbox {
		t.Fatalf("descriptors = %+v, want file.mailbox", descriptors)
	}
}

func TestValidateAcceptsGitRepositoryCarrierWithExplicitRole(t *testing.T) {
	cfg := Config{
		EnabledCarriers: []string{"git.control"},
		CarrierConfigs: []CarrierConfig{{
			ID:          "git.control",
			CarrierType: carriers.CarrierGitRepository,
			Role:        "discovery",
			Endpoint:    EndpointConfig{Address: "control"},
			GitRepository: &GitRepositoryConfig{
				RemoteURL: "git://127.0.0.1/transport.git",
				WorkDir:   t.TempDir(),
				WriterID:  "client-control",
			},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate Git carrier: %v", err)
	}
	cfg.CarrierConfigs[0].Role = "bootstrap-and-secret-backup"
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid generic carrier role was accepted")
	}
}

func TestValidateAcceptsMailIMAPSMTPCarrierWithoutInlineSecrets(t *testing.T) {
	cfg := Config{
		EnabledCarriers: []string{"mail.control"},
		CarrierConfigs: []CarrierConfig{{
			ID:          "mail.control",
			CarrierType: carriers.CarrierMailIMAPSMTP,
			Role:        "discovery",
			Endpoint:    EndpointConfig{Address: "account-a"},
			MailIMAPSMTP: &MailIMAPSMTPConfig{
				SMTPAddress:   "mail.example.test:465",
				IMAPAddress:   "mail.example.test:993",
				AccountID:     "account-a",
				Mailbox:       "INBOX",
				FromAddress:   "sender@example.test",
				ToAddress:     "receiver@example.test",
				TLSServerName: "mail.example.test",
				CAFile:        "/etc/ssl/certs/ca-certificates.crt",
			},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate mail carrier: %v", err)
	}
	raw, err := json.Marshal(cfg.CarrierConfigs[0].MailIMAPSMTP)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"smtp_username", "smtp_password", "imap_username", "imap_password"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("mail config serialized forbidden credential field %q: %s", forbidden, raw)
		}
	}
}

func TestValidateChannelsFlagOff(t *testing.T) {
	// Ensure flag is off.
	t.Setenv("WT_CHANNEL_BINDINGS", "")

	// Invalid channels should NOT be validated when flag is off.
	cfg := Config{
		CarrierConfigs: []CarrierConfig{
			{
				ID: "vk.messages",
				VKMessages: &VKMessagesConfig{
					Channels: []VKChannelConfig{
						{PeerID: "", Role: "bogus"},
					},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("channels should be skipped when flag is off, got: %v", err)
	}
}

func TestTokenStoreCompositeTokenLoads(t *testing.T) {
	data := `{
		"role": "node",
		"node_id": "test-node",
		"listen_api": "127.0.0.1:0",
		"socks_listen": "",
		"enabled_carriers": ["wbstream.vp8"],
		"carrier_configs": [
			{
				"id": "wbstream.vp8",
				"token_ref": "wb-node-1",
				"endpoint": {"id": "wbstream-client", "address": "room-123"},
				"wbstream_legacy": {}
			}
		],
		"token_store": {
			"tokens": [
				{
					"id": "wb-node-1",
					"platform": "wbstream",
					"kind": "composite",
					"lifecycle": "embedded",
					"status": "active",
					"parts": {
						"access_token": "wb-jwt-token",
						"cookies_file": "secrets/wb/cookies.json",
						"local_storage_file": "secrets/wb/localstorage.tsv"
					},
					"can_create_channels": true,
					"tags": {"role": "node"}
				}
			],
			"bindings": [
				{"token_id": "wb-node-1", "platform": "wbstream", "connection_type": "vp8", "channel_id": "*", "role": "egress", "priority": 10, "enabled": true}
			]
		}
	}`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	tok := cfg.TokenStore.Tokens[0]
	if tok.Kind != tokens.KindComposite {
		t.Errorf("token kind = %q, want composite", tok.Kind)
	}
	if len(tok.Parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(tok.Parts))
	}
	if tok.Parts["access_token"] != "wb-jwt-token" {
		t.Errorf("access_token = %q, want wb-jwt-token", tok.Parts["access_token"])
	}
	if !tok.CanCreateChannels {
		t.Error("wb token should be able to create channels")
	}
}
