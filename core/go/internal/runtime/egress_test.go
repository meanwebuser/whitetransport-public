package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/tunnel"
)

func TestControlPlaneDialEgressWithCarrierTunnel(t *testing.T) {
	controlEndpoint := carriers.Endpoint{
		ID:      carriers.CarrierVKMessages,
		Carrier: carriers.CarrierVKMessages,
		Address: "memory://shared-egress-test",
	}
	egressEndpoint := carriers.Endpoint{
		ID:      carriers.CarrierVKDocs1024,
		Carrier: carriers.CarrierVKDocs1024,
		Address: "memory://shared-egress-test-egress",
	}

	controlCarrier := newMemoryCarrierWithDescriptor(t, "egress-test-control", carriers.CarrierVKMessages)
	egressCarrier := newMemoryCarrierWithDescriptor(t, "egress-test-egress", carriers.CarrierVKDocs1024)

	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: egressCarrier, Endpoint: egressEndpoint},
	}

	nodeControl, err := newTestControlPlane(
		config.Config{
			Role:        config.RoleNode,
			NodeID:      "test-node",
			DisplayName: "Test Node",
			SocksListen: "127.0.0.1:0",
		},
		bindings,
		policy.DefaultAdaptivePolicy(),
		tunnel.NewCarrierTunnel("test-node", bindings),
	)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}

	clientControl, err := newTestControlPlane(
		config.Config{
			Role:        config.RoleClient,
			ClientID:    "test-client",
			SocksListen: "127.0.0.1:0",
		},
		bindings,
		policy.DefaultAdaptivePolicy(),
		tunnel.NewCarrierTunnel("test-client", bindings),
	)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	// Wait for node discovery
	deadline := time.Now().Add(5 * time.Second)
	var nodes []NodeView
	for time.Now().Before(deadline) {
		nodes = clientControl.ListNodes()
		if len(nodes) == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected one discovered node, got %+v", nodes)
	}

	// Connect session
	status, err := clientControl.Connect(ctx, "test-node")
	if err != nil {
		t.Fatalf("connect session: %v", err)
	}
	if status.State != statusStateConnected {
		t.Fatalf("expected connected, got %+v", status)
	}

	// Start a local TCP echo server to dial through the tunnel
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoListener.Close()
	echoAddr := echoListener.Addr().String()

	go func() {
		for {
			conn, err := echoListener.Accept()
			if err != nil {
				return
			}
			go io.Copy(conn, conn) // echo server
		}
	}()

	// Dial egress through the CarrierTunnel
	egressConn, route, err := clientControl.DialEgress(ctx, echoAddr)
	if err != nil {
		t.Fatalf("DialEgress: %v", err)
	}
	defer egressConn.Close()

	if route == "" {
		t.Fatal("expected non-empty route id")
	}

	// Send data through egress
	_, err = egressConn.Write([]byte("ping"))
	if err != nil {
		t.Fatalf("egress write: %v", err)
	}

	// Read echo response
	buf := make([]byte, 16)
	egressConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := egressConn.Read(buf)
	if err != nil {
		t.Fatalf("egress read: %v", err)
	}

	got := strings.TrimSpace(string(buf[:n]))
	if got != "ping" {
		t.Fatalf("expected echo 'ping', got %q", got)
	}
}

func TestControlPlaneSessionAnswerIncludesSingBoxEgressEndpoint(t *testing.T) {
	controlEndpoint := carriers.Endpoint{
		ID:      carriers.CarrierVKMessages,
		Carrier: carriers.CarrierVKMessages,
		Address: "memory://shared-singbox-control",
	}
	singBoxEndpoint := carriers.Endpoint{
		ID:      "singbox-example-exit-node",
		Carrier: carriers.CarrierSingBoxVLESS,
		Address: "exit-node.example.invalid:443",
	}

	controlCarrier := newMemoryCarrierWithDescriptor(t, "singbox-control", carriers.CarrierVKMessages)
	singBoxCarrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server:      "exit-node.example.invalid",
		ServerPort:  443,
		UUID:        "11111111-1111-4111-8111-111111111111",
		LocalListen: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages:   {Carrier: controlCarrier, Endpoint: controlEndpoint},
		carriers.CarrierSingBoxVLESS: {Carrier: singBoxCarrier, Endpoint: singBoxEndpoint},
	}

	nodeControl, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: "xray-backed-example-exit-node", SocksListen: "127.0.0.1:0"},
		bindings,
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	clientControl, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "singbox-client", SocksListen: "127.0.0.1:0"},
		bindings,
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer nodeControl.Stop()
	defer clientControl.Stop()
	nodeControl.Start(ctx)
	clientControl.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(clientControl.ListNodes()) == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	status, err := clientControl.Connect(ctx, "xray-backed-example-exit-node")
	if err != nil {
		t.Fatalf("connect session: %v", err)
	}
	if len(status.EgressEndpoints) == 0 {
		t.Fatalf("expected egress endpoints, got none")
	}
	var foundSingBox *carriers.Endpoint
	for i := range status.EgressEndpoints {
		if status.EgressEndpoints[i].Carrier == carriers.CarrierSingBoxVLESS && status.EgressEndpoints[i].ID == "singbox-example-exit-node" {
			foundSingBox = &status.EgressEndpoints[i]
			break
		}
	}
	if foundSingBox == nil {
		t.Fatalf("expected sing-box egress endpoint in %v", status.EgressEndpoints)
	}
}

func TestControlPlanePrioritizesSingBoxEgressEndpoint(t *testing.T) {
	wbstreamEndpoint := carriers.Endpoint{
		ID:      "wbstream-static",
		Carrier: "wbstream",
		Address: "wbstream://room",
	}
	singBoxEndpoint := carriers.Endpoint{
		ID:      "xray-de-httpupgrade",
		Carrier: carriers.CarrierSingBoxVLESS,
		Address: "exit-node.example.invalid:443",
	}
	controlEndpoint := carriers.Endpoint{
		ID:      carriers.CarrierVKMessages,
		Carrier: carriers.CarrierVKMessages,
		Address: "memory://singbox-priority-control",
	}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "singbox-priority-control", carriers.CarrierVKMessages)
	wbstreamCarrier := &memoryCarrier{
		descriptor: carriers.Descriptor{
			ID:             "wbstream",
			Provider:       "wbstream",
			Mode:           carriers.DeliveryStream,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficEgress},
			Capabilities:   []carriers.Capability{carriers.CapStream, carriers.CapDuplex},
			Metrics: carriers.Metrics{
				Healthy:        true,
				QuotaRemaining: -1, // quota-aware scorer: treat test carriers as unlimited
			},
		},
		memory: carriers.NewMemoryCarrier("wbstream-egress"),
	}
	singBoxCarrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server:      "exit-node.example.invalid",
		ServerPort:  443,
		UUID:        "11111111-1111-4111-8111-111111111111",
		LocalListen: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages:   {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"wbstream":                   {Carrier: wbstreamCarrier, Endpoint: wbstreamEndpoint},
		carriers.CarrierSingBoxVLESS: {Carrier: singBoxCarrier, Endpoint: singBoxEndpoint},
	}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "singbox-priority", SocksListen: "127.0.0.1:0"},
		bindings,
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}
	endpoints := control.buildEgressEndpoints(context.Background())
	for _, endpoint := range endpoints {
		if sameSessionEndpoint(endpoint, controlEndpoint) {
			t.Fatalf("control endpoint leaked into session egress: %+v", endpoints)
		}
	}
	var firstNonVTA *carriers.Endpoint
	for i := range endpoints {
		if endpoints[i].Carrier == carriers.CarrierSingBoxVLESS || endpoints[i].Carrier == "wbstream" {
			firstNonVTA = &endpoints[i]
			break
		}
	}
	if firstNonVTA == nil {
		t.Fatalf("expected sing-box or wbstream egress endpoint in %+v", endpoints)
	}
	if firstNonVTA.Carrier != carriers.CarrierSingBoxVLESS {
		t.Fatalf("expected sing-box first, got %+v", *firstNonVTA)
	}
}

// TestControlPlaneExcludesControlOnlyMailboxFromEgress proves that a control
// carrier cannot be published as a tunnel endpoint merely because it can
// retain messages. A provider integration can use file.mailbox for isolated
// discovery while WBStream carries the actual egress session.
func TestControlPlaneExcludesControlOnlyMailboxFromEgress(t *testing.T) {
	mailbox, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("new file mailbox: %v", err)
	}
	wbstream := &memoryCarrier{
		descriptor: carriers.Descriptor{
			ID:             "wbstream-test",
			Provider:       "wbstream",
			Mode:           carriers.DeliveryStream,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficStream, fabric.TrafficEgress},
			Capabilities:   []carriers.Capability{carriers.CapStream, carriers.CapDuplex},
			Metrics:        carriers.Metrics{Healthy: true, QuotaRemaining: -1},
		},
		memory: carriers.NewMemoryCarrier("wbstream-test"),
	}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: "egress-eligibility", SocksListen: "127.0.0.1:0"},
		map[string]policy.CarrierBinding{
			carriers.CarrierFileMailbox: {
				Carrier:  mailbox,
				Endpoint: carriers.Endpoint{ID: "local-control", Carrier: carriers.CarrierFileMailbox, Address: "local-control"},
			},
			"wbstream-test": {
				Carrier:  wbstream,
				Endpoint: carriers.Endpoint{ID: "wbstream-egress", Carrier: "wbstream-test", Address: "wbstream://room"},
			},
		},
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}

	endpoints := control.buildEgressEndpoints(context.Background())
	if len(endpoints) != 1 || endpoints[0].ID != "wbstream-egress" {
		t.Fatalf("egress endpoints = %+v, want only WBStream", endpoints)
	}
}

func TestControlPlaneKeepsCombinedControlStreamEndpointForEgress(t *testing.T) {
	const bindingID = "combined-control-egress"
	descriptor := carriers.Descriptor{
		ID:             "combined.control.stream",
		Provider:       "test",
		Mode:           carriers.DeliveryStream,
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficStream, fabric.TrafficEgress},
		Capabilities:   []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox, carriers.CapRetained, carriers.CapStream, carriers.CapDuplex, carriers.CapOrdered},
		Metrics:        carriers.Metrics{Healthy: true, QuotaRemaining: -1},
	}
	combined := &controlStreamCarrier{memoryCarrier: &memoryCarrier{
		descriptor: descriptor,
		memory:     carriers.NewMemoryCarrier(bindingID),
	}}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: "combined-stream-node", SocksListen: "127.0.0.1:0"},
		map[string]policy.CarrierBinding{
			bindingID: {
				Carrier:  combined,
				Endpoint: carriers.Endpoint{ID: "shared-control-egress", Carrier: descriptor.ID, Address: "127.0.0.1:2222"},
			},
		},
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}

	endpoints := control.buildEgressEndpoints(context.Background())
	if len(endpoints) != 1 || endpoints[0].ID != "shared-control-egress" || endpoints[0].Carrier != bindingID {
		t.Fatalf("egress endpoints = %+v, want combined stream alias %q", endpoints, bindingID)
	}
}

func TestControlPlaneDialsCombinedControlStreamEndpoint(t *testing.T) {
	const (
		bindingID = "combined-control-egress"
		target    = "127.0.0.1:18080"
		nonce     = "combined-control-stream-nonce"
	)
	descriptor := carriers.Descriptor{
		ID:             "combined.control.stream",
		Provider:       "test",
		Mode:           carriers.DeliveryStream,
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficStream, fabric.TrafficEgress},
		Capabilities:   []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox, carriers.CapRetained, carriers.CapStream, carriers.CapDuplex, carriers.CapOrdered},
		Metrics:        carriers.Metrics{Healthy: true, QuotaRemaining: -1},
	}
	combined := &controlStreamCarrier{memoryCarrier: &memoryCarrier{
		descriptor: descriptor,
		memory:     carriers.NewMemoryCarrier(bindingID),
	}}
	endpoint := carriers.Endpoint{ID: "shared-control-egress", Carrier: bindingID, Address: "127.0.0.1:2222"}
	bindings := map[string]policy.CarrierBinding{
		bindingID: {Carrier: combined, Endpoint: endpoint},
	}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "combined-stream-client", SocksListen: "127.0.0.1:0"},
		bindings,
		policy.DefaultAdaptivePolicy(),
		tunnel.NewUnifiedCarrierTunnel("combined-stream-client", bindings),
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}
	control.mu.Lock()
	control.active = &activeSession{
		NodeID:          "combined-stream-node",
		SessionID:       "combined-stream-session",
		ControlEndpoint: endpoint,
		ControlBinding:  bindings[bindingID],
		EgressEndpoints: []carriers.Endpoint{endpoint},
		UpdatedAt:       time.Now().UTC(),
	}
	control.mu.Unlock()

	connection, route, err := control.DialEgress(context.Background(), target)
	if err != nil {
		t.Fatalf("DialEgress combined control stream: %v", err)
	}
	defer connection.Close()
	if route != endpoint.ID {
		t.Fatalf("route = %q, want %q", route, endpoint.ID)
	}
	if _, err := io.WriteString(connection, nonce); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(nonce))
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != nonce {
		t.Fatalf("stream payload = %q, want %q", response, nonce)
	}
	if got := combined.lastTarget(); got != target {
		t.Fatalf("StreamDialer target = %q, want %q", got, target)
	}
}

func TestControlPlaneSelectsCombinedControlStreamEndpoint(t *testing.T) {
	const bindingID = "combined-control-egress"
	descriptor := carriers.Descriptor{
		ID:             "combined.control.stream",
		Provider:       "test",
		Mode:           carriers.DeliveryStream,
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficStream, fabric.TrafficEgress},
		Capabilities:   []carriers.Capability{carriers.CapRendezvous, carriers.CapMailbox, carriers.CapRetained, carriers.CapStream, carriers.CapDuplex, carriers.CapOrdered},
		Metrics:        carriers.Metrics{Healthy: true, QuotaRemaining: -1},
	}
	combined := &controlStreamCarrier{memoryCarrier: &memoryCarrier{
		descriptor: descriptor,
		memory:     carriers.NewMemoryCarrier(bindingID),
	}}
	endpoint := carriers.Endpoint{ID: "shared-control-egress", Carrier: bindingID, Address: "127.0.0.1:2222"}
	bindings := map[string]policy.CarrierBinding{
		bindingID: {Carrier: combined, Endpoint: endpoint},
	}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "combined-stream-client", SocksListen: "127.0.0.1:0"},
		bindings,
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}
	control.mu.Lock()
	control.active = &activeSession{
		NodeID:          "combined-stream-node",
		SessionID:       "combined-stream-session",
		ControlEndpoint: endpoint,
		ControlBinding:  bindings[bindingID],
		EgressEndpoints: []carriers.Endpoint{endpoint},
		UpdatedAt:       time.Now().UTC(),
	}
	control.mu.Unlock()

	status, err := control.SelectEgressEndpoint(endpoint.ID)
	if err != nil {
		t.Fatalf("SelectEgressEndpoint combined control stream: %v", err)
	}
	if status.SelectedEgressEndpointID != endpoint.ID {
		t.Fatalf("selected endpoint = %q, want %q", status.SelectedEgressEndpointID, endpoint.ID)
	}
}

func TestControlPlaneRejectsSelectingControlOnlyMailbox(t *testing.T) {
	const bindingID = carriers.CarrierVKMessages
	mailbox := newMemoryCarrierWithDescriptor(t, "control-only-mailbox", bindingID)
	endpoint := carriers.Endpoint{ID: "control-only", Carrier: bindingID, Address: "memory://control-only"}
	bindings := map[string]policy.CarrierBinding{
		bindingID: {Carrier: mailbox, Endpoint: endpoint},
	}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "control-only-client", SocksListen: "127.0.0.1:0"},
		bindings,
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}
	control.mu.Lock()
	control.active = &activeSession{
		NodeID:          "control-only-node",
		SessionID:       "control-only-session",
		ControlEndpoint: endpoint,
		ControlBinding:  bindings[bindingID],
		EgressEndpoints: []carriers.Endpoint{endpoint},
		UpdatedAt:       time.Now().UTC(),
	}
	control.mu.Unlock()

	status, err := control.SelectEgressEndpoint(endpoint.ID)
	if err == nil || !strings.Contains(err.Error(), "cannot select control endpoint") {
		t.Fatalf("SelectEgressEndpoint error = %v, want control-only rejection", err)
	}
	if status.SelectedEgressEndpointID != "" {
		t.Fatalf("selected endpoint = %q, want empty after rejection", status.SelectedEgressEndpointID)
	}
}

type controlStreamCarrier struct {
	*memoryCarrier
	mu     sync.Mutex
	target string
}

func (c *controlStreamCarrier) DialStream(_ context.Context, _ carriers.Endpoint, target string) (net.Conn, error) {
	c.mu.Lock()
	c.target = target
	c.mu.Unlock()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

func (c *controlStreamCarrier) lastTarget() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target
}

// TestControlPlaneAllowsExplicitLocalMailboxEgress keeps the deterministic
// two-daemon lane functional without making provider mailboxes egress routes.
func TestControlPlaneAllowsExplicitLocalMailboxEgress(t *testing.T) {
	mailbox, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{
		Dir:         t.TempDir(),
		AllowEgress: true,
	})
	if err != nil {
		t.Fatalf("new file mailbox: %v", err)
	}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: "local-egress", SocksListen: "127.0.0.1:0"},
		map[string]policy.CarrierBinding{
			carriers.CarrierFileMailbox: {
				Carrier:  mailbox,
				Endpoint: carriers.Endpoint{ID: "local-egress", Carrier: carriers.CarrierFileMailbox, Address: "local-egress"},
			},
		},
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}

	endpoints := control.buildEgressEndpoints(context.Background())
	if len(endpoints) != 1 || endpoints[0].Carrier != carriers.CarrierFileMailbox {
		t.Fatalf("egress endpoints = %+v, want explicit local mailbox", endpoints)
	}
}

// TestLocalFileMailboxAliasesKeepDistinctEgressIdentity protects the
// deterministic failover lane: two file-mailbox egress bindings must retain
// their configuration aliases in a session answer so the client can select
// the matching local tunnel after the primary carrier fails.
func TestLocalFileMailboxAliasesKeepDistinctEgressIdentity(t *testing.T) {
	controlMailbox, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("new control mailbox: %v", err)
	}
	primaryMailbox, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir(), AllowEgress: true})
	if err != nil {
		t.Fatalf("new primary mailbox: %v", err)
	}
	backupMailbox, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir(), AllowEgress: true})
	if err != nil {
		t.Fatalf("new backup mailbox: %v", err)
	}
	const (
		controlID = "local.control"
		primaryID = "local.egress.primary"
		backupID  = "local.egress.backup"
	)
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: "local-failover", SocksListen: "127.0.0.1:0"},
		map[string]policy.CarrierBinding{
			controlID: {Carrier: controlMailbox, Endpoint: carriers.Endpoint{ID: controlID, Carrier: carriers.CarrierFileMailbox, Address: "control"}},
			primaryID: {Carrier: primaryMailbox, Endpoint: carriers.Endpoint{ID: primaryID, Carrier: carriers.CarrierFileMailbox, Address: "primary"}},
			backupID:  {Carrier: backupMailbox, Endpoint: carriers.Endpoint{ID: backupID, Carrier: carriers.CarrierFileMailbox, Address: "backup"}},
		},
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}

	got := control.buildEgressEndpoints(context.Background())
	byID := make(map[string]carriers.Endpoint, len(got))
	for _, endpoint := range got {
		byID[endpoint.ID] = endpoint
	}
	if gotPrimary, ok := byID[primaryID]; !ok || gotPrimary.Carrier != primaryID {
		t.Fatalf("primary endpoint = %+v, want carrier %q", gotPrimary, primaryID)
	}
	if gotBackup, ok := byID[backupID]; !ok || gotBackup.Carrier != backupID {
		t.Fatalf("backup endpoint = %+v, want carrier %q", gotBackup, backupID)
	}
	var replyControl carriers.Endpoint
	for _, endpoint := range control.replyEndpoints {
		if endpoint.ID == controlID {
			replyControl = endpoint
			break
		}
	}
	if replyControl.Carrier != controlID {
		t.Fatalf("reply control endpoint = %+v, want carrier %q", replyControl, controlID)
	}
}

func TestControlPlaneIncludesAllSingBoxEgressEndpoints(t *testing.T) {
	controlEndpoint := carriers.Endpoint{
		ID:      carriers.CarrierVKMessages,
		Carrier: carriers.CarrierVKMessages,
		Address: "memory://singbox-multi-control",
	}
	firstEndpoint := carriers.Endpoint{
		ID:      "xray-de-httpupgrade",
		Carrier: carriers.CarrierSingBoxVLESS,
		Address: "de.example.invalid:443",
	}
	secondEndpoint := carriers.Endpoint{
		ID:      "xray-nl-httpupgrade",
		Carrier: carriers.CarrierSingBoxVLESS,
		Address: "nl.example.invalid:443",
	}
	controlCarrier := newMemoryCarrierWithDescriptor(t, "singbox-multi-control", carriers.CarrierVKMessages)
	firstCarrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server:      "de.example.invalid",
		ServerPort:  443,
		UUID:        "11111111-1111-4111-8111-111111111111",
		LocalListen: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondCarrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server:      "nl.example.invalid",
		ServerPort:  443,
		UUID:        "22222222-2222-4222-8222-222222222222",
		LocalListen: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		"xray-de-httpupgrade":      {Carrier: firstCarrier, Endpoint: firstEndpoint},
		"xray-nl-httpupgrade":      {Carrier: secondCarrier, Endpoint: secondEndpoint},
	}
	control, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "singbox-multi", SocksListen: "127.0.0.1:0"},
		bindings,
		policy.DefaultAdaptivePolicy(),
		nil,
	)
	if err != nil {
		t.Fatalf("new control plane: %v", err)
	}
	endpoints := control.buildEgressEndpoints(context.Background())
	var singBoxEndpoints []carriers.Endpoint
	for _, ep := range endpoints {
		if strings.HasPrefix(ep.Carrier, "xray-") {
			singBoxEndpoints = append(singBoxEndpoints, ep)
		}
	}
	if len(singBoxEndpoints) != 2 {
		t.Fatalf("expected two sing-box endpoints, got %+v", singBoxEndpoints)
	}
	if singBoxEndpoints[0].ID != "xray-de-httpupgrade" || singBoxEndpoints[1].ID != "xray-nl-httpupgrade" {
		t.Fatalf("unexpected sing-box endpoint order: %+v", singBoxEndpoints)
	}
}

// TestControlPlaneDialEgressWithoutSession ensures DialEgress fails gracefully
// when no active session exists.
func TestControlPlaneDialEgressWithoutSession(t *testing.T) {
	control := &ControlPlane{
		cfg:    config.Config{Role: config.RoleClient},
		tunnel: tunnel.NewCarrierTunnel("", nil),
	}

	_, route, err := control.DialEgress(context.Background(), "example.com:443")
	if err == nil || !strings.Contains(err.Error(), "no active carrier session") {
		t.Fatalf("expected no active session error, got route=%q err=%v", route, err)
	}
}

// TestControlPlaneDialEgressFallbackOnFailure verifies that DialEgress dials
// endpoints in parallel and falls back when the highest-scored one fails.
// It uses real carrier IDs so the score-based sort is exercised.
func TestControlPlaneDialEgressFallbackOnFailure(t *testing.T) {
	// ssh.tcp scores higher for egress than vk.docs.1024.
	failEP := carriers.Endpoint{ID: "fail-ep", Carrier: carriers.CarrierSSHTCP}
	okEP := carriers.Endpoint{ID: "ok-ep", Carrier: carriers.CarrierVKDocs1024}

	control := &ControlPlane{
		cfg:       config.Config{Role: config.RoleClient},
		state:     statusStateDegraded,
		lastError: "previous egress failure",
		active: &activeSession{
			NodeID:          "test-node",
			SessionID:       "sess-1",
			EgressEndpoints: []carriers.Endpoint{failEP, okEP},
		},
		tunnel: &fallbackTestTunnel{failCarrier: carriers.CarrierSSHTCP, okEndpoint: okEP},
		policy: policy.DefaultAdaptivePolicy(),
	}

	conn, route, err := control.DialEgress(context.Background(), "example.com:443")
	if err != nil {
		t.Fatalf("DialEgress should succeed via fallback: %v", err)
	}
	defer conn.Close()
	if route != "ok-ep" {
		t.Fatalf("expected route ok-ep, got %q", route)
	}
	recoveredStatus := control.Status()
	if recoveredStatus.State != statusStateConnected || recoveredStatus.LastError != "" {
		t.Fatalf("status after successful egress = %+v, want connected with cleared error", recoveredStatus)
	}
}

// TestDialBatchReturnsFirstSuccessWithoutWaitingForSlowCarrier protects the
// adaptive race contract: a ready route must not be held hostage by a sibling
// such as WBStream that is still waiting for its DataTunnel.
func TestDialBatchReturnsFirstSuccessWithoutWaitingForSlowCarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	fast := carriers.Endpoint{ID: "ssh-fast", Carrier: carriers.CarrierSSHTCP}
	slow := carriers.Endpoint{ID: "wbstream-slow", Carrier: carriers.CarrierWBStreamVP8}

	startedAt := time.Now()
	conn, route, err := dialBatch(ctx, &fastAndBlockingTunnel{fastID: fast.ID}, []carriers.Endpoint{fast, slow}, "example.com:443", time.Second)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("dialBatch: %v", err)
	}
	defer conn.Close()
	if route != fast.ID {
		t.Fatalf("route = %q, want %q", route, fast.ID)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("first success returned after %v; slow sibling blocked adaptive failover", elapsed)
	}
}

// TestControlPlaneDialEgressDoesNotSkipSamePlatformFallback verifies that
// diversity batching never drops an untried route. The second SSH candidate
// is intentionally omitted from the first diverse batch; it must still be
// attempted after the first batch fails.
func TestControlPlaneDialEgressDoesNotSkipSamePlatformFallback(t *testing.T) {
	firstSSH := carriers.Endpoint{ID: "ssh-first", Carrier: carriers.CarrierSSHTCP}
	secondSSH := carriers.Endpoint{ID: "ssh-second", Carrier: carriers.CarrierSSHTCP}
	singBox := carriers.Endpoint{ID: "singbox-fail", Carrier: carriers.CarrierSingBoxVLESS}
	vkDocs := carriers.Endpoint{ID: "vk-fail", Carrier: carriers.CarrierVKDocs1024}
	tunnel := &singleEndpointSuccessTunnel{successID: secondSSH.ID}
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:          "test-node",
			SessionID:       "session-diverse-fallback",
			EgressEndpoints: []carriers.Endpoint{firstSSH, secondSSH, singBox, vkDocs},
		},
		tunnel: tunnel,
		policy: policy.DefaultAdaptivePolicy(),
	}

	conn, route, err := control.DialEgress(context.Background(), "example.com:443")
	if err != nil {
		t.Fatalf("DialEgress should reach untried same-platform fallback: %v", err)
	}
	defer conn.Close()
	if route != secondSSH.ID {
		t.Fatalf("route = %q, want %q", route, secondSSH.ID)
	}
}

// TestControlPlaneDialEgressUsesSelectedEndpointExclusively protects the
// diagnostic route override: an operator-selected Xray endpoint must report
// its own failure instead of silently succeeding through a different route.
func TestControlPlaneDialEgressUsesSelectedEndpointExclusively(t *testing.T) {
	selected := carriers.Endpoint{ID: "xray-de-httpupgrade", Carrier: carriers.CarrierSingBoxVLESS}
	fallback := carriers.Endpoint{ID: "xray-us-httpupgrade", Carrier: carriers.CarrierSingBoxVLESS}
	tunnel := &exclusiveEndpointTunnel{failID: selected.ID}
	control := &ControlPlane{
		cfg:   config.Config{Role: config.RoleClient},
		state: statusStateConnected,
		active: &activeSession{
			NodeID:          "example-exit-node",
			SessionID:       "session-xray-diagnostic",
			EgressEndpoints: []carriers.Endpoint{selected, fallback},
		},
		tunnel: tunnel,
		policy: policy.DefaultAdaptivePolicy(),
	}

	status, err := control.SelectEgressEndpoint(selected.ID)
	if err != nil {
		t.Fatalf("SelectEgressEndpoint: %v", err)
	}
	if status.SelectedEgressEndpointID != selected.ID {
		t.Fatalf("selected endpoint = %q, want %q", status.SelectedEgressEndpointID, selected.ID)
	}

	_, _, err = control.DialEgress(context.Background(), "example.com:443")
	if err == nil || !strings.Contains(err.Error(), "forced route failed") {
		t.Fatalf("DialEgress error = %v, want selected route failure", err)
	}
	if got := strings.Join(tunnel.dialed, ","); got != selected.ID {
		t.Fatalf("DialEgress dialed %q, want only selected endpoint %q", got, selected.ID)
	}
	failedStatus := control.Status()
	if failedStatus.State != statusStateDegraded {
		t.Fatalf("state after selected egress failure = %q, want %q", failedStatus.State, statusStateDegraded)
	}
	if !strings.Contains(failedStatus.LastError, "forced route failed") {
		t.Fatalf("last error after selected egress failure = %q, want route failure", failedStatus.LastError)
	}
}

// TestEgressDescriptorAliasesDynamicXrayRoute ensures node-issued Xray route
// identities retain the canonical SingBox egress score instead of sorting as
// unknown carriers behind unrelated fallback routes.
func TestEgressDescriptorAliasesDynamicXrayRoute(t *testing.T) {
	xrayDescriptor, err := egressDescriptor(carriers.Endpoint{ID: "xray-de-httpupgrade", Carrier: "xray-de-httpupgrade"})
	if err != nil {
		t.Fatalf("egressDescriptor dynamic Xray: %v", err)
	}
	singBoxDescriptor, err := carriers.FindStandardDescriptor(carriers.CarrierSingBoxVLESS)
	if err != nil {
		t.Fatalf("find canonical SingBox descriptor: %v", err)
	}
	if xrayDescriptor.ID != carriers.CarrierSingBoxVLESS {
		t.Fatalf("dynamic Xray descriptor = %q, want %q", xrayDescriptor.ID, carriers.CarrierSingBoxVLESS)
	}
	policy := policy.DefaultAdaptivePolicy()
	if got, want := policy.Scorer.Score(xrayDescriptor, fabric.TrafficEgress), policy.Scorer.Score(singBoxDescriptor, fabric.TrafficEgress); got != want {
		t.Fatalf("dynamic Xray score = %v, want canonical SingBox score %v", got, want)
	}
}

// TestEgressDescriptorForDynamicXrayEndpoint makes auto-discovered Xray
// routes participate in the same policy contract as singbox.vless. Their
// endpoint identity remains dynamic, but treating it as an unknown carrier
// silently made ordering lexical rather than health/capability based.
func TestEgressDescriptorForDynamicXrayEndpoint(t *testing.T) {
	descriptor, err := egressDescriptor(carriers.Endpoint{ID: "xray-de-httpupgrade", Carrier: "xray-de-httpupgrade"})
	if err != nil {
		t.Fatalf("egressDescriptor: %v", err)
	}
	if descriptor.ID != carriers.CarrierSingBoxVLESS {
		t.Fatalf("descriptor ID = %q, want %q", descriptor.ID, carriers.CarrierSingBoxVLESS)
	}
}

// TestControlPlaneDialEgressAllFail verifies that DialEgress returns a joined
// error when every endpoint fails.
func TestControlPlaneDialEgressAllFail(t *testing.T) {
	failEP1 := carriers.Endpoint{ID: "fail-1", Carrier: carriers.CarrierSSHTCP}
	failEP2 := carriers.Endpoint{ID: "fail-2", Carrier: carriers.CarrierVKDocs1024}

	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:          "test-node",
			SessionID:       "sess-1",
			EgressEndpoints: []carriers.Endpoint{failEP1, failEP2},
		},
		tunnel: &allFailTunnel{},
		policy: policy.DefaultAdaptivePolicy(),
	}

	_, _, err := control.DialEgress(context.Background(), "example.com:443")
	if err == nil {
		t.Fatal("expected error when all endpoints fail")
	}
	if !strings.Contains(err.Error(), "all egress endpoints failed") {
		t.Fatalf("unexpected error message: %v", err)
	}
	// Verify both failures are present in the joined error.
	errStr := err.Error()
	if !strings.Contains(errStr, "ssh.tcp") || !strings.Contains(errStr, "vk.docs.1024") {
		t.Fatalf("expected both carrier names in joined error, got: %v", err)
	}
}

// TestControlPlaneDialEgressRejectsControlFallback proves an answer with no
// locally-loaded egress carrier cannot silently turn the VK discovery channel
// into a SOCKS route. The endpoint mix mirrors a remote session: an unloaded
// Xray profile plus the control endpoint that carried session negotiation.
func TestControlPlaneDialEgressRejectsControlFallback(t *testing.T) {
	controlEndpoint := carriers.Endpoint{
		ID:      "vk.messages:discovery",
		Carrier: carriers.CarrierVKMessages,
		Address: "2000000001",
	}
	unloadedXrayEndpoint := carriers.Endpoint{
		ID:      "xray-de-reality",
		Carrier: carriers.CarrierSingBoxVLESS,
		Address: "de.example.invalid:443",
	}
	tunnel := &controlOnlyTunnel{controlEndpoint: controlEndpoint}
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:          "example-exit-node",
			SessionID:       "remote-session",
			ControlEndpoint: controlEndpoint,
			EgressEndpoints: []carriers.Endpoint{unloadedXrayEndpoint, controlEndpoint},
		},
		tunnel: tunnel,
		policy: policy.DefaultAdaptivePolicy(),
	}

	_, route, err := control.DialEgress(context.Background(), "example.com:443")
	if err == nil {
		t.Fatal("DialEgress unexpectedly accepted a control endpoint")
	}
	if route != "carrier-session" {
		t.Fatalf("route = %q, want carrier-session", route)
	}
	if tunnel.dialed {
		t.Fatal("DialEgress dialed the control endpoint")
	}
	if !strings.Contains(err.Error(), "control endpoint") || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected explicit control and unsupported errors, got %v", err)
	}
}

func TestNormalizeSessionEgressEndpointUsesQualifiedBindingCarrier(t *testing.T) {
	answerEndpoint := carriers.Endpoint{
		ID:      "wbstream:session-1",
		Carrier: "wbstream",
		Address: "wbstream://room-1",
	}
	binding := policy.CarrierBinding{
		Endpoint: carriers.Endpoint{
			ID:      "wb-egress",
			Carrier: carriers.CarrierWBStreamVP8,
			Address: "*",
		},
	}

	got := normalizeSessionEgressEndpoint(answerEndpoint, carriers.CarrierWBStreamVP8, binding)
	if got.Carrier != carriers.CarrierWBStreamVP8 {
		t.Fatalf("Carrier = %q, want %q", got.Carrier, carriers.CarrierWBStreamVP8)
	}
	if got.ID != answerEndpoint.ID || got.Address != answerEndpoint.Address {
		t.Fatalf("normalized endpoint changed identity/address: got %+v want %+v", got, answerEndpoint)
	}
}

func TestNormalizeSessionEgressEndpointKeepsBindingKeyOverStaleEndpointCarrier(t *testing.T) {
	answerEndpoint := carriers.Endpoint{
		ID:      "fake-video:session-1",
		Carrier: "fake-video",
		Address: "fake-video://room-test",
	}
	binding := policy.CarrierBinding{
		Endpoint: carriers.Endpoint{
			ID:      "vk-docs",
			Carrier: carriers.CarrierVKDocs1024,
			Address: "*",
		},
	}

	got := normalizeSessionEgressEndpoint(answerEndpoint, "fake-video", binding)
	if got.Carrier != "fake-video" {
		t.Fatalf("Carrier = %q, want fake-video", got.Carrier)
	}
	if got.ID != answerEndpoint.ID || got.Address != answerEndpoint.Address {
		t.Fatalf("normalized endpoint changed identity/address: got %+v want %+v", got, answerEndpoint)
	}
}

// fallbackTestTunnel is a minimal CarrierTunnel mock that fails on one
// carrier and succeeds on another using a net.Pipe.
type fallbackTestTunnel struct {
	failCarrier string
	okEndpoint  carriers.Endpoint
}

// singleEndpointSuccessTunnel fails every candidate except one stable
// endpoint ID, making skipped failover candidates observable in the runtime
// integration contract.
type singleEndpointSuccessTunnel struct {
	successID string
}

type fastAndBlockingTunnel struct {
	fastID string
}

func (t *fastAndBlockingTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }

func (t *fastAndBlockingTunnel) DialContext(ctx context.Context, endpoint carriers.Endpoint, _ string) (net.Conn, error) {
	if endpoint.ID == t.fastID {
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (t *singleEndpointSuccessTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }

func (t *singleEndpointSuccessTunnel) DialContext(_ context.Context, ep carriers.Endpoint, _ string) (net.Conn, error) {
	if ep.ID != t.successID {
		return nil, fmt.Errorf("dial failed on %s (simulated)", ep.ID)
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (t *fallbackTestTunnel) SupportsEndpoint(ep carriers.Endpoint) bool { return true }
func (t *fallbackTestTunnel) DialContext(_ context.Context, ep carriers.Endpoint, _ string) (net.Conn, error) {
	if ep.Carrier == t.failCarrier {
		return nil, fmt.Errorf("dial failed on %s (simulated)", t.failCarrier)
	}
	client, server := net.Pipe()
	defer server.Close()
	return client, nil
}

// allFailTunnel fails every dial with the carrier name in the error.
type allFailTunnel struct{}

func (t *allFailTunnel) SupportsEndpoint(ep carriers.Endpoint) bool { return true }
func (t *allFailTunnel) DialContext(_ context.Context, ep carriers.Endpoint, _ string) (net.Conn, error) {
	return nil, fmt.Errorf("%s: dial refused (simulated)", ep.Carrier)
}

// exclusiveEndpointTunnel records each dial so the selected-route contract
// cannot accidentally fall back to another healthy endpoint.
type exclusiveEndpointTunnel struct {
	failID string
	dialed []string
}

func (t *exclusiveEndpointTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }

func (t *exclusiveEndpointTunnel) DialContext(_ context.Context, ep carriers.Endpoint, _ string) (net.Conn, error) {
	t.dialed = append(t.dialed, ep.ID)
	if ep.ID == t.failID {
		return nil, errors.New("forced route failed")
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

// controlOnlyTunnel models a client with only its VK discovery binding loaded.
// It makes a control fallback observable without contacting an external node.
type controlOnlyTunnel struct {
	controlEndpoint carriers.Endpoint
	dialed          bool
}

func (t *controlOnlyTunnel) SupportsEndpoint(ep carriers.Endpoint) bool {
	return ep.ID == t.controlEndpoint.ID && ep.Carrier == t.controlEndpoint.Carrier && ep.Address == t.controlEndpoint.Address
}

func (t *controlOnlyTunnel) DialContext(_ context.Context, _ carriers.Endpoint, _ string) (net.Conn, error) {
	t.dialed = true
	return nil, errors.New("control endpoint must never be dialed for SOCKS egress")
}
