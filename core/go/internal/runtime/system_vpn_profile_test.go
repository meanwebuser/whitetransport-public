package runtime

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	whitelistadapter "github.com/meanwebuser/whitetransport/core/internal/adapters/whitelist"
	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	providerapi "github.com/meanwebuser/whitetransport/core/internal/provider"
	"github.com/meanwebuser/whitetransport/core/pkg/runtimeapi"
)

type fixedSystemVPNClock struct{ now time.Time }

func (c fixedSystemVPNClock) Now() time.Time { return c.now }

type mapSystemVPNResolver struct {
	entries map[string]SystemVPNResolution
}

func (r mapSystemVPNResolver) Resolve(_ context.Context, host string) (SystemVPNResolution, error) {
	entry, ok := r.entries[host]
	if !ok {
		return SystemVPNResolution{}, errSystemVPNHostMissing
	}
	return entry, nil
}

var errSystemVPNHostMissing = &systemVPNTestError{"host missing"}

type systemVPNTestError struct{ message string }

func (e *systemVPNTestError) Error() string { return e.message }

func TestSystemVPNProfileBuilderUsesBoundSocksAndResolvedIPv4IPv6Dependencies(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	resolver := mapSystemVPNResolver{entries: map[string]SystemVPNResolution{
		"api.vk.test":    {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.10"), netip.MustParseAddr("2001:db8::10")}, ExpiresAt: now.Add(10 * time.Minute)},
		"api.ok.test":    {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.20")}, ExpiresAt: now.Add(10 * time.Minute)},
		"egress.example": {Addresses: []netip.Addr{netip.MustParseAddr("2001:db8::20")}, ExpiresAt: now.Add(10 * time.Minute)},
	}}
	builder := NewSystemVPNProfileBuilder(fixedSystemVPNClock{now: now}, resolver)
	input := SystemVPNProfileInput{
		Config: config.Config{
			Role: config.RoleClient,
			Routing: config.RoutingConfig{
				Mode:       config.RouteModeNone,
				FullTunnel: true,
				DNSServers: []string{"1.1.1.1", "2606:4700:4700::1111"},
				MTU:        1500,
			},
			CarrierConfigs: []config.CarrierConfig{
				{ID: "vk.messages", Endpoint: config.EndpointConfig{Address: "peer"}, VKMessages: &config.VKMessagesConfig{BaseURL: "https://api.vk.test/method"}},
				{ID: "ok.messages", Endpoint: config.EndpointConfig{Address: "chat"}, OKMessages: &config.OKMessagesConfig{BaseURL: "https://api.ok.test/graph"}},
				{ID: "vk.docs.256", Endpoint: config.EndpointConfig{Address: "docs"}, VKDocs: &config.VKDocsConfig{BaseURL: "https://egress.example/api"}},
			},
		},
		ActualSocksListen: "127.0.0.1:41723",
		DaemonInstanceID:  "daemon-instance-1",
		ProfileRevision:   12,
		SessionID:         "session-1",
		SelectedNodeID:    "node-1",
		Bindings: []SystemVPNBindingInput{
			{ID: "vk.messages", Carrier: "vk.messages", Purpose: runtimeapi.SystemVPNDependencyDiscovery},
			{ID: "ok.messages", Carrier: "ok.messages", Purpose: runtimeapi.SystemVPNDependencyControl},
			{ID: "vk.docs.256", Carrier: "vk.docs.256", Purpose: runtimeapi.SystemVPNDependencyEgress},
		},
	}
	profile, err := builder.BuildSystemVPNProfile(context.Background(), input)
	if err != nil {
		t.Fatalf("BuildSystemVPNProfile: %v", err)
	}
	if profile.SocksListen != "127.0.0.1:41723" {
		t.Fatalf("profile socks listener = %q, want actual bound address", profile.SocksListen)
	}
	if profile.ProfileRevision != 12 || profile.DaemonInstanceID != "daemon-instance-1" || profile.SessionID != "session-1" {
		t.Fatalf("profile identity = %+v", profile)
	}
	if want := now.Add(runtimeapi.SystemVPNProfileMaxLifetime); !profile.ExpiresAt.Equal(want) {
		t.Fatalf("profile expiry = %s, want bounded renewal interval %s", profile.ExpiresAt, want)
	}
	if err := profile.Validate(now); err != nil {
		t.Fatalf("profile validation: %v", err)
	}
	if len(profile.Dependencies) != 3 || len(profile.Dependencies[0].Addresses) == 0 {
		t.Fatalf("profile dependencies = %+v", profile.Dependencies)
	}
	for _, origin := range profile.CarrierControlOrigins {
		if origin != "https://api.ok.test" && origin != "https://api.vk.test" {
			t.Fatalf("unexpected or leaked control origin %q", origin)
		}
	}

	resolver.entries["api.ok.test"] = SystemVPNResolution{
		Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.20")},
		ExpiresAt: now.Add(3 * time.Minute),
	}
	heterogeneous, err := builder.BuildSystemVPNProfile(context.Background(), input)
	if err != nil {
		t.Fatalf("BuildSystemVPNProfile heterogeneous TTLs: %v", err)
	}
	if want := now.Add(3 * time.Minute); !heterogeneous.ExpiresAt.Equal(want) {
		t.Fatalf("heterogeneous profile expiry = %s, want earliest %s", heterogeneous.ExpiresAt, want)
	}
	for _, dependency := range heterogeneous.Dependencies {
		if !dependency.DNSExpiresAt.Equal(heterogeneous.ExpiresAt) {
			t.Fatalf("effective dependency deadline = %s, want clamped profile deadline %s", dependency.DNSExpiresAt, heterogeneous.ExpiresAt)
		}
	}
}

func TestSystemVPNProfileBuilderFailsClosedForDynamicOrStaleInputs(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	base := SystemVPNProfileInput{
		Config: config.Config{
			Role:    config.RoleClient,
			Routing: config.RoutingConfig{Mode: config.RouteModeNone, FullTunnel: true, DNSServers: []string{"1.1.1.1"}, MTU: 1500},
			CarrierConfigs: []config.CarrierConfig{
				{ID: "vk.messages", Endpoint: config.EndpointConfig{Address: "peer"}, VKMessages: &config.VKMessagesConfig{}},
				{ID: "ok.messages", Endpoint: config.EndpointConfig{Address: "chat"}, OKMessages: &config.OKMessagesConfig{}},
				{ID: "wbstream", Endpoint: config.EndpointConfig{Address: "room"}, WhitelistBypass: &config.WhitelistBypassConfig{}},
			},
		},
		ActualSocksListen: "127.0.0.1:41723", DaemonInstanceID: "daemon-instance-1", ProfileRevision: 1, SessionID: "session-1", SelectedNodeID: "node-1",
		Bindings: []SystemVPNBindingInput{{ID: "vk.messages", Carrier: "vk.messages", Purpose: runtimeapi.SystemVPNDependencyDiscovery}, {ID: "ok.messages", Carrier: "ok.messages", Purpose: runtimeapi.SystemVPNDependencyControl}, {ID: "wbstream", Carrier: "wbstream", Purpose: runtimeapi.SystemVPNDependencyEgress}},
	}
	resolver := mapSystemVPNResolver{entries: map[string]SystemVPNResolution{
		"api.vk.com": {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.1")}, ExpiresAt: now.Add(time.Minute)},
		"api.ok.ru":  {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.2")}, ExpiresAt: now.Add(time.Minute)},
	}}
	builder := NewSystemVPNProfileBuilder(fixedSystemVPNClock{now: now}, resolver)
	if _, err := builder.BuildSystemVPNProfile(context.Background(), base); err == nil {
		t.Fatal("dynamic WBStream provider without authoritative server origin was accepted")
	}
	base.Bindings[2] = SystemVPNBindingInput{ID: "vk.messages", Carrier: "vk.messages", Purpose: runtimeapi.SystemVPNDependencyEgress}
	base.Config.CarrierConfigs = append(base.Config.CarrierConfigs, config.CarrierConfig{ID: "egress.example", Endpoint: config.EndpointConfig{Address: "egress.example"}})
	base.Bindings[2] = SystemVPNBindingInput{ID: "egress.example", Carrier: "egress.example", Purpose: runtimeapi.SystemVPNDependencyEgress}
	resolver.entries["egress.example"] = SystemVPNResolution{Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.3")}, ExpiresAt: now.Add(-time.Second)}
	if _, err := builder.BuildSystemVPNProfile(context.Background(), base); err == nil {
		t.Fatal("stale DNS resolution was accepted")
	}
}

func TestSystemVPNProfileBuilderAcceptsCanonicalWBStreamCarrierType(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	resolver := mapSystemVPNResolver{entries: map[string]SystemVPNResolution{
		"api.vk.com":   {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.1")}, ExpiresAt: now.Add(time.Minute)},
		"stream.wb.ru": {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.2")}, ExpiresAt: now.Add(time.Minute)},
	}}
	input := SystemVPNProfileInput{
		Config: config.Config{
			Role:    config.RoleClient,
			Routing: config.RoutingConfig{Mode: config.RouteModeNone, FullTunnel: true, DNSServers: []string{"1.1.1.1"}, MTU: 1500},
			CarrierConfigs: []config.CarrierConfig{
				{ID: "vk.messages", Endpoint: config.EndpointConfig{Address: "peer"}, VKMessages: &config.VKMessagesConfig{}},
				{ID: "wbstream.vp8", CarrierType: "wbstream", Endpoint: config.EndpointConfig{Address: "*"}, WhitelistBypass: &config.WhitelistBypassConfig{ServerURL: "wss://stream.wb.ru"}},
			},
		},
		ActualSocksListen: "127.0.0.1:41723", DaemonInstanceID: "daemon-1", ProfileRevision: 1, SessionID: "session-1", SelectedNodeID: "node-1",
		Bindings: []SystemVPNBindingInput{
			{ID: "vk.messages", Carrier: "vk.messages", Purpose: runtimeapi.SystemVPNDependencyDiscovery},
			{ID: "vk.messages", Carrier: "vk.messages", Purpose: runtimeapi.SystemVPNDependencyControl},
			{ID: "wbstream.vp8", Carrier: "wbstream.vp8", Purpose: runtimeapi.SystemVPNDependencyEgress},
		},
	}
	if _, err := NewSystemVPNProfileBuilder(fixedSystemVPNClock{now: now}, resolver).BuildSystemVPNProfile(context.Background(), input); err != nil {
		t.Fatalf("canonical wbstream carrier rejected: %v", err)
	}
}

func TestSystemVPNBindingInputsUseActiveProviderNetworkOrigin(t *testing.T) {
	adapter := &whitelistadapter.Provider{}
	if err := adapter.Configure(providerapi.ProviderConfig{
		Type:      providerapi.TypeVideoCall,
		Endpoints: map[string]string{"server_url": "wss://livekit.example.com"},
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	carrier, err := carriers.NewProviderCarrier(adapter, carriers.Endpoint{ID: "wb-egress", Carrier: "wbstream.vp8"})
	if err != nil {
		t.Fatalf("NewProviderCarrier: %v", err)
	}
	control := &ControlPlane{egress: []carrierRef{{
		ID:         "wbstream.vp8",
		Descriptor: carrier.Descriptor(),
		Binding:    policy.CarrierBinding{Carrier: carrier, Endpoint: carriers.Endpoint{ID: "wb-egress", Carrier: "wbstream.vp8"}},
	}}}
	inputs := control.systemVPNBindingInputsLocked(nil)
	if len(inputs) != 2 {
		t.Fatalf("system VPN binding inputs = %+v", inputs)
	}
	origins := map[string]bool{}
	for _, input := range inputs {
		if input.Purpose != runtimeapi.SystemVPNDependencyEgress {
			t.Fatalf("system VPN binding purpose = %q, want egress", input.Purpose)
		}
		origins[input.OriginOverride] = true
	}
	if !origins["wss://livekit.example.com"] || !origins["https://wb-stream-turn-1.wb.ru"] {
		t.Fatalf("system VPN binding origins = %v", origins)
	}
}

func TestControlPlaneSystemVPNProfileIsAtomicAndAbsentAfterDisconnect(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	clock := &fixedSystemVPNClock{now: now}
	resolver := mapSystemVPNResolver{entries: map[string]SystemVPNResolution{
		"api.vk.com":  {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.1")}, ExpiresAt: now.Add(time.Minute)},
		"api.ok.ru":   {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.2")}, ExpiresAt: now.Add(time.Minute)},
		"egress.test": {Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.3")}, ExpiresAt: now.Add(time.Minute)},
	}}
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient, Routing: config.RoutingConfig{Mode: config.RouteModeNone, FullTunnel: true, DNSServers: []string{"1.1.1.1"}, MTU: 1500}, CarrierConfigs: []config.CarrierConfig{
			{ID: "vk.messages", VKMessages: &config.VKMessagesConfig{}}, {ID: "ok.messages", OKMessages: &config.OKMessagesConfig{}}, {ID: "egress.test", Endpoint: config.EndpointConfig{Address: "https://egress.test"}},
		}},
		state: statusStateConnected, daemonInstanceID: "daemon-1", profileRevision: 2, actualSocksListen: "127.0.0.1:40111", profileBuilder: NewSystemVPNProfileBuilder(clock, resolver), stopCh: make(chan struct{}),
		active: &activeSession{NodeID: "node-1", SessionID: "session-1", EgressEndpoints: []carriers.Endpoint{
			{ID: "egress", Carrier: "egress.test"},
			{ID: "ssh-direct", Carrier: carriers.CarrierSSHTCP, Address: "212.192.31.128:22000"},
		}},
		bootstrap: []carrierRef{{ID: "vk.messages", Descriptor: carriers.Descriptor{ID: "vk.messages"}}},
		control:   []carrierRef{{ID: "ok.messages", Descriptor: carriers.Descriptor{ID: "ok.messages"}}},
		egress:    []carrierRef{{ID: "egress.test", Descriptor: carriers.Descriptor{ID: "egress.test"}}},
	}
	control.refreshSystemVPNProfile()
	status := control.Status()
	if status.SystemVPNProfile == nil || status.SystemVPNProfile.SocksListen != "127.0.0.1:40111" {
		t.Fatalf("connected status profile = %+v", status.SystemVPNProfile)
	}
	if routes := status.SystemVPNProfile.CarrierControlRoutes["212.192.31.128"]; len(routes) != 1 || routes[0] != "212.192.31.128/32" {
		t.Fatalf("session SSH endpoint routes = %#v, want exact physical bypass", routes)
	}
	revision := status.SystemVPNProfile.ProfileRevision
	firstExpiry := status.SystemVPNProfile.ExpiresAt
	clock.now = now.Add(45 * time.Second)
	for host := range resolver.entries {
		entry := resolver.entries[host]
		entry.ExpiresAt = clock.now.Add(time.Minute)
		resolver.entries[host] = entry
	}
	refreshed := control.Status()
	if refreshed.SystemVPNProfile == nil || refreshed.SystemVPNProfile.ProfileRevision != revision || refreshed.SystemVPNProfile.ProfileHash != status.SystemVPNProfile.ProfileHash || !refreshed.SystemVPNProfile.ExpiresAt.After(firstExpiry) {
		t.Fatalf("near-expiry status did not atomically refresh profile: before=%+v after=%+v", status.SystemVPNProfile, refreshed.SystemVPNProfile)
	}
	resolver.entries["api.vk.com"] = SystemVPNResolution{Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.99")}, ExpiresAt: clock.now.Add(time.Minute)}
	clock.now = clock.now.Add(45 * time.Second)
	for host := range resolver.entries {
		entry := resolver.entries[host]
		entry.ExpiresAt = clock.now.Add(time.Minute)
		resolver.entries[host] = entry
	}
	changed := control.Status()
	if changed.SystemVPNProfile == nil || changed.SystemVPNProfile.ProfileRevision <= revision || changed.SystemVPNProfile.ProfileHash == status.SystemVPNProfile.ProfileHash {
		t.Fatalf("route-changing refresh retained stale profile identity: before=%+v after=%+v", status.SystemVPNProfile, changed.SystemVPNProfile)
	}
	revision = changed.SystemVPNProfile.ProfileRevision
	changedHash := changed.SystemVPNProfile.ProfileHash
	control.recordEgressResult("session-1", errors.New("target dial failed"))
	degraded := control.Status()
	if degraded.SystemVPNProfile == nil || degraded.SystemVPNProfile.ProfileHash != changedHash {
		t.Fatalf("one target dial failure invalidated the still-authoritative VPN routes: %+v", degraded)
	}
	control.recordEgressResult("session-1", nil)
	recovered := control.Status()
	if recovered.SystemVPNProfile == nil || recovered.SystemVPNProfile.ProfileHash != changedHash {
		t.Fatalf("target dial recovery replaced unchanged VPN routes: %+v", recovered)
	}
	control.Disconnect()
	disconnected := control.Status()
	if disconnected.SystemVPNProfile != nil || disconnected.SystemVPNProfileReadiness == nil || disconnected.SystemVPNProfileReadiness.Ready {
		t.Fatalf("disconnected status retained profile: %+v", disconnected)
	}
	if disconnected.SystemVPNProfileReadiness.Reason == "" || disconnected.SystemVPNProfileReadiness.Reason == "session-1" {
		t.Fatalf("readiness reason is not redacted/disconnected: %+v", disconnected.SystemVPNProfileReadiness)
	}
	if control.profileRevision <= revision {
		t.Fatalf("profile revision did not advance across disconnect: before=%d after=%d", revision, control.profileRevision)
	}
}

func TestSystemVPNSessionEndpointOriginAcceptsOnlyKnownStreamCarriers(t *testing.T) {
	tests := []struct {
		name     string
		endpoint carriers.Endpoint
		want     string
		ok       bool
	}{
		{name: "ssh", endpoint: carriers.Endpoint{Carrier: carriers.CarrierSSHTCP, Address: "212.192.31.128:22000"}, want: "ssh://212.192.31.128:22000", ok: true},
		{name: "sing-box", endpoint: carriers.Endpoint{Carrier: carriers.CarrierSingBoxVLESS, Address: "[2001:db8::8]:443"}, want: "tls://[2001:db8::8]:443", ok: true},
		{name: "mailbox", endpoint: carriers.Endpoint{Carrier: carriers.CarrierVKMessages, Address: "212.192.31.128:443"}},
		{name: "missing port", endpoint: carriers.Endpoint{Carrier: carriers.CarrierSSHTCP, Address: "212.192.31.128"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := systemVPNSessionEndpointOrigin(test.endpoint)
			if got != test.want || ok != test.ok {
				t.Fatalf("systemVPNSessionEndpointOrigin() = %q, %v; want %q, %v", got, ok, test.want, test.ok)
			}
		})
	}
}
