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
	"github.com/meanwebuser/whitetransport/core/internal/proxy"
	"github.com/meanwebuser/whitetransport/core/internal/router"
	"github.com/meanwebuser/whitetransport/core/internal/session"
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

// TestSOCKSProxyViaBulkCarrierTunnel proves the full local vertical slice for
// a retained bulk carrier: SOCKS5 -> session route selection -> CarrierTunnel
// -> node egress -> payload. proxy.Server has no direct fallback, so a passing
// payload cannot silently bypass the selected document route.
func TestSOCKSProxyViaBulkCarrierTunnel(t *testing.T) {
	testSOCKSProxyViaBulkCarrierTunnel(t, carriers.CarrierVKDocs1024)
}

func TestSOCKSProxyViaOKDocsCarrierTunnel(t *testing.T) {
	testSOCKSProxyViaBulkCarrierTunnel(t, carriers.CarrierOKDocs256)
}

func testSOCKSProxyViaBulkCarrierTunnel(t *testing.T, bulkCarrier string) {
	t.Helper()
	controlEndpoint := carriers.Endpoint{ID: carriers.CarrierVKMessages, Carrier: carriers.CarrierVKMessages, Address: "memory://bulk-socks-control"}
	egressEndpoint := carriers.Endpoint{ID: bulkCarrier, Carrier: bulkCarrier, Address: "memory://bulk-socks-egress"}
	bindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: newMemoryCarrierWithDescriptor(t, "bulk-socks-control", carriers.CarrierVKMessages), Endpoint: controlEndpoint},
		bulkCarrier:                {Carrier: newMemoryCarrierWithDescriptor(t, "bulk-socks-egress", bulkCarrier), Endpoint: egressEndpoint},
	}

	node, err := newTestControlPlane(config.Config{Role: config.RoleNode, NodeID: "bulk-node", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), tunnel.NewCarrierTunnel("bulk-node", bindings))
	if err != nil {
		t.Fatalf("new node control plane: %v", err)
	}
	client, err := newTestControlPlane(config.Config{Role: config.RoleClient, ClientID: "bulk-client", SocksListen: "127.0.0.1:0"}, bindings, policy.DefaultAdaptivePolicy(), tunnel.NewCarrierTunnel("bulk-client", bindings))
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	node.Start(ctx)
	client.Start(ctx)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if len(client.ListNodes()) == 1 {
			break
		}
	}
	if len(client.ListNodes()) != 1 {
		t.Fatalf("expected one discovered node, got %+v", client.ListNodes())
	}
	if status, err := client.Connect(ctx, "bulk-node"); err != nil || status.State != statusStateConnected {
		t.Fatalf("connect bulk session status=%+v err=%v", status, err)
	}

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoListener.Close()
	go func() {
		for {
			conn, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				return
			}
			go io.Copy(conn, conn)
		}
	}()

	var selectedRoute string
	var routeMu sync.Mutex
	socks := proxy.Server{
		ListenAddr: "127.0.0.1:0",
		EgressDialer: func(dialCtx context.Context, target string) (net.Conn, string, error) {
			conn, route, dialErr := client.DialEgress(dialCtx, target)
			routeMu.Lock()
			selectedRoute = route
			routeMu.Unlock()
			return conn, route, dialErr
		},
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- socks.ListenAndServe(ctx) }()
	for deadline := time.Now().Add(2 * time.Second); socks.Addr() == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if socks.Addr() == "" {
		t.Fatal("SOCKS server did not start")
	}

	conn, err := net.DialTimeout("tcp", socks.Addr(), time.Second)
	if err != nil {
		t.Fatalf("dial SOCKS: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write SOCKS greeting: %v", err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil || greeting[0] != 0x05 || greeting[1] != 0x00 {
		t.Fatalf("SOCKS greeting reply=%v err=%v", greeting, err)
	}
	port := echoListener.Addr().(*net.TCPAddr).Port
	connectRequest := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, byte(port >> 8), byte(port)}
	if _, err := conn.Write(connectRequest); err != nil {
		t.Fatalf("write SOCKS connect: %v", err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil || connectReply[1] != 0x00 {
		t.Fatalf("SOCKS connect reply=%v err=%v", connectReply, err)
	}

	payload := []byte("bulk-carrier-socks-nonce")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write SOCKS payload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
		t.Fatalf("SOCKS payload got=%q err=%v", got, err)
	}
	routeMu.Lock()
	route := selectedRoute
	routeMu.Unlock()
	if route != egressEndpoint.ID {
		t.Fatalf("selected route=%q, want bulk endpoint %q", route, egressEndpoint.ID)
	}
	cancel()
	if err := <-serverErr; err != nil {
		t.Fatalf("SOCKS server: %v", err)
	}
}

// TestActiveSessionEgressFallsBackFromSSHToBulkCarrierTunnel exercises the
// automatic selector at the real carrier boundary. The preferred SSH-shaped
// stream binding rejects tunnel.open; the same active session must still carry
// a payload over the lower-ranked retained bulk document carrier.
func TestActiveSessionEgressFallsBackFromSSHToBulkCarrierTunnel(t *testing.T) {
	primaryEndpoint := carriers.Endpoint{ID: "ssh.primary", Carrier: carriers.CarrierSSHTCP, Address: "memory://ssh-primary"}
	bulkEndpoint := carriers.Endpoint{ID: "vk.docs.bulk", Carrier: carriers.CarrierVKDocs1024, Address: "memory://bulk-fallback"}
	primary := &writeFailingCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "ssh-primary", carriers.CarrierSSHTCP)}
	bulk := newMemoryCarrierWithDescriptor(t, "bulk-fallback", carriers.CarrierVKDocs1024)
	clientBindings := map[string]policy.CarrierBinding{
		carriers.CarrierSSHTCP:     {Carrier: primary, Endpoint: primaryEndpoint},
		carriers.CarrierVKDocs1024: {Carrier: bulk, Endpoint: bulkEndpoint},
	}
	nodeBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKDocs1024: {Carrier: bulk, Endpoint: bulkEndpoint},
	}
	clientTunnel := tunnel.NewCarrierTunnel("failover-client", clientBindings)
	nodeTunnel := tunnel.NewCarrierTunnel("failover-node", nodeBindings)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = nodeTunnel.ServeEgress(ctx, nodeBindings) }()
	time.Sleep(50 * time.Millisecond)

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoListener.Close()
	go func() {
		for {
			conn, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				return
			}
			go io.Copy(conn, conn)
		}
	}()
	selectionPolicy := policy.DefaultAdaptivePolicy()
	selectionPolicy.Scorer = &policy.CapabilityScorer{
		Requirements: policy.DefaultTrafficRequirements(),
		Overrides: map[string]map[fabric.TrafficClass]float64{
			carriers.CarrierSSHTCP:     {fabric.TrafficEgress: 100},
			carriers.CarrierVKDocs1024: {fabric.TrafficEgress: 10},
		},
	}

	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:          "failover-node",
			SessionID:       "failover-session",
			EgressEndpoints: []carriers.Endpoint{primaryEndpoint, bulkEndpoint},
		},
		policy: selectionPolicy,
		tunnel: clientTunnel,
	}
	conn, route, err := control.DialEgress(ctx, echoListener.Addr().String())
	if err != nil {
		t.Fatalf("DialEgress: %v", err)
	}
	defer conn.Close()
	if route != bulkEndpoint.ID {
		t.Fatalf("route=%q, want successful bulk route %q", route, bulkEndpoint.ID)
	}
	for deadline := time.Now().Add(time.Second); primary.writeCount("tunnel.open") == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if primary.writeCount("tunnel.open") == 0 {
		t.Fatal("preferred SSH route was never attempted at the carrier boundary")
	}
	payload := []byte("ssh-to-bulk-failover-nonce")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write fallback payload: %v", err)
	}
	got := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
		t.Fatalf("fallback payload got=%q err=%v", got, err)
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

func TestScoredEgressKeepsLocalFixtureBehindAnyNetworkCarrier(t *testing.T) {
	mailDescriptor, err := carriers.FindStandardDescriptor(carriers.CarrierMailIMAPSMTP)
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir(), AllowEgress: true})
	if err != nil {
		t.Fatal(err)
	}
	refs := []carrierRef{
		{ID: "file.backup", Descriptor: mailbox.Descriptor()},
		{ID: "mail.primary", Descriptor: mailDescriptor},
	}
	sortEgressRefsScored(refs, policy.DefaultScorer())
	if refs[0].ID != "mail.primary" || refs[1].ID != "file.backup" {
		t.Fatalf("scored routes = [%s %s], want network carrier before local test fixture", refs[0].ID, refs[1].ID)
	}
}

func TestRankedEgressEndpointsResolvesConfiguredCarrierAliases(t *testing.T) {
	mailDescriptor, err := carriers.FindStandardDescriptor(carriers.CarrierMailIMAPSMTP)
	if err != nil {
		t.Fatal(err)
	}
	fileCarrier, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir(), AllowEgress: true})
	if err != nil {
		t.Fatal(err)
	}
	mailCarrier := carriers.NewMemoryCarrierWithDescriptor(mailDescriptor)
	control := &ControlPlane{
		bindings: map[string]policy.CarrierBinding{
			"file.backup":  {Carrier: fileCarrier, Endpoint: carriers.Endpoint{ID: "file.backup", Carrier: carriers.CarrierFileMailbox, Address: "backup"}},
			"mail.primary": {Carrier: mailCarrier, Endpoint: carriers.Endpoint{ID: "mail.primary", Carrier: carriers.CarrierMailIMAPSMTP, Address: "primary"}},
		},
		policy: policy.DefaultAdaptivePolicy(),
	}
	ranked := control.rankedEgressEndpoints([]carriers.Endpoint{
		{ID: "file.backup", Carrier: "file.backup", Address: "backup"},
		{ID: "mail.primary", Carrier: "mail.primary", Address: "primary"},
	})
	if len(ranked) != 2 || ranked[0].ID != "mail.primary" {
		t.Fatalf("alias-ranked endpoints = %+v, want mail.primary first", ranked)
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

func TestControlPlaneDialEgressDoesNotSerializeHealthyAutomaticDials(t *testing.T) {
	endpoint := carriers.Endpoint{ID: "healthy-provider", Carrier: "healthy-provider"}
	tunnel := &concurrentHealthyTunnel{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:          "healthy-node",
			SessionID:       "healthy-session",
			EgressEndpoints: []carriers.Endpoint{endpoint},
		},
		tunnel: tunnel,
		policy: policy.DefaultAdaptivePolicy(),
	}

	type dialResult struct {
		conn net.Conn
		err  error
	}
	results := make(chan dialResult, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for range 2 {
		go func() {
			conn, _, err := control.DialEgress(ctx, "example.test:443")
			results <- dialResult{conn: conn, err: err}
		}()
	}

	for range 2 {
		select {
		case <-tunnel.started:
		case <-time.After(500 * time.Millisecond):
			close(tunnel.release)
			t.Fatal("healthy automatic dials were serialized behind node auto-heal lock")
		}
	}
	close(tunnel.release)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("healthy automatic dial: %v", result.err)
		}
		_ = result.conn.Close()
	}
}

// TestControlPlaneDialEgressReselectsAnotherNodeAfterCarrierExhaustion proves
// that node selection starts only after the active session has exhausted its
// own carrier routes. A carrier-classified failure on node A must preserve
// carrier-level failover semantics, then establish a fresh session with B.
func TestControlPlaneDialEgressReselectsAnotherNodeAfterCarrierExhaustion(t *testing.T) {
	const (
		nodeAStreamCarrier = "provider-stream-a"
		nodeBStreamCarrier = "provider-stream-b"
	)
	controlEndpoint := carriers.Endpoint{
		ID:      carriers.CarrierVKMessages,
		Carrier: carriers.CarrierVKMessages,
		Address: "memory://node-autoheal-control",
	}
	nodeAEgress := carriers.Endpoint{
		ID:      "node-a-egress",
		Carrier: nodeAStreamCarrier,
		Address: "memory://node-autoheal-a",
	}
	nodeBEgress := carriers.Endpoint{
		ID:      "node-b-egress",
		Carrier: nodeBStreamCarrier,
		Address: "memory://node-autoheal-b",
	}

	controlCarrier := newMemoryCarrierWithDescriptor(t, "node-autoheal-control", carriers.CarrierVKMessages)
	nodeACarrier := newProviderStreamLikeMemoryCarrier("node-autoheal-a", nodeAStreamCarrier)
	nodeBCarrier := newProviderStreamLikeMemoryCarrier("node-autoheal-b", nodeBStreamCarrier)
	nodeABindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		nodeAStreamCarrier:         {Carrier: nodeACarrier, Endpoint: nodeAEgress},
	}
	nodeBBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		nodeBStreamCarrier:         {Carrier: nodeBCarrier, Endpoint: nodeBEgress},
	}
	clientBindings := map[string]policy.CarrierBinding{
		carriers.CarrierVKMessages: {Carrier: controlCarrier, Endpoint: controlEndpoint},
		nodeAStreamCarrier:         {Carrier: nodeACarrier, Endpoint: nodeAEgress},
		nodeBStreamCarrier:         {Carrier: nodeBCarrier, Endpoint: nodeBEgress},
	}

	nodeA, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: "node-a", DisplayName: "A", SocksListen: "127.0.0.1:0"},
		nodeABindings, policy.DefaultAdaptivePolicy(), nil,
	)
	if err != nil {
		t.Fatalf("new node A control plane: %v", err)
	}
	nodeB, err := newTestControlPlane(
		config.Config{Role: config.RoleNode, NodeID: "node-b", DisplayName: "B", SocksListen: "127.0.0.1:0"},
		nodeBBindings, policy.DefaultAdaptivePolicy(), nil,
	)
	if err != nil {
		t.Fatalf("new node B control plane: %v", err)
	}
	client, err := newTestControlPlane(
		config.Config{Role: config.RoleClient, ClientID: "node-autoheal-client", SocksListen: "127.0.0.1:0"},
		clientBindings, policy.DefaultAdaptivePolicy(), &nodeSelectiveTunnel{failedEndpointID: nodeAEgress.ID, healthyEndpointID: nodeBEgress.ID},
	)
	if err != nil {
		t.Fatalf("new client control plane: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	nodeACtx, cancelNodeA := context.WithCancel(ctx)
	defer cancelNodeA()
	defer nodeA.Stop()
	defer nodeB.Stop()
	defer client.Stop()
	nodeA.Start(nodeACtx)
	nodeB.Start(ctx)
	client.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		nodes := client.ListNodes()
		if len(nodes) == 2 && nodes[0].Available && nodes[1].Available {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if nodes := client.ListNodes(); len(nodes) != 2 {
		t.Fatalf("discovered nodes = %+v, want both node A and B", nodes)
	}
	if _, err := client.Connect(ctx, "node-a"); err != nil {
		t.Fatalf("connect node A: %v", err)
	}
	if status := client.Status(); status.ActiveNodeID != "node-a" {
		t.Fatalf("initial active node = %q, want node-a", status.ActiveNodeID)
	}

	// A target/application error must not reselect a live node or create a
	// node B session.
	client.mu.Lock()
	client.tunnel = targetApplicationErrorTunnel{}
	client.mu.Unlock()
	_, _, targetErr := client.DialEgress(ctx, "target-error.test:443")
	if targetErr == nil || !strings.Contains(targetErr.Error(), "target application timed out") {
		t.Fatalf("target error = %v, want target application timeout", targetErr)
	}
	if status := client.Status(); status.ActiveNodeID != "node-a" {
		t.Fatalf("target timeout reselected a live node: %+v", status)
	}

	// A manual endpoint pin must preserve its exclusive diagnostic semantics:
	// even a typed carrier failure cannot move the client to node B.
	client.mu.Lock()
	client.tunnel = &nodeSelectiveTunnel{failedEndpointID: nodeAEgress.ID, healthyEndpointID: nodeBEgress.ID}
	client.mu.Unlock()
	if _, err := client.SelectEgressEndpoint(nodeAEgress.ID); err != nil {
		t.Fatalf("select node A manual endpoint: %v", err)
	}
	if _, _, err := client.DialEgress(ctx, "manual-pin.test:443"); err == nil {
		t.Fatal("manual endpoint dial unexpectedly succeeded")
	}
	if status := client.Status(); status.ActiveNodeID != "node-a" {
		t.Fatalf("manual endpoint failure reselected node: %+v", status)
	}
	if _, err := client.SelectEgressEndpoint("auto"); err != nil {
		t.Fatalf("restore automatic endpoint selection: %v", err)
	}
	// The manual failure correctly quarantined its explicit diagnostic route.
	// Reset the test fixture's quarantine state so the following automatic
	// request can demonstrate a fresh typed provider-stream failure.
	client.mu.Lock()
	client.egressRouteStreams = newRouteStreamRegistry(defaultRouteInterruptAfter, nil)
	client.egressRecovery = nil
	client.initializeAutomaticEgressRouteLocked()
	client.state = statusStateConnected
	client.mu.Unlock()

	// Two concurrent automatic dials after node A loss must coalesce to one
	// replacement session. Neither caller may receive a stale-node error.
	cancelNodeA()
	nodeA.Stop()
	livenessCtx, cancelLiveness := context.WithTimeout(context.Background(), nodeLivenessProbeTimeout)
	livenessErr := client.probeActiveNodeLiveness(livenessCtx, client.activeSessionSnapshot())
	cancelLiveness()
	if !session.IsCarrierFailure(livenessErr) || !errors.Is(livenessErr, ErrNodeLiveness) {
		t.Fatalf("node A liveness error = %v, want typed bounded carrier liveness failure", livenessErr)
	}
	active := client.activeSessionSnapshot()
	beforeNodeBOffers := countTargetNodeOffers(t, controlCarrier, controlEndpoint, "node-b")
	client.recordQuarantinedEgressStreamFailure(
		active.SessionID,
		nodeAEgress,
		session.NewCarrierFailure(errors.New("provider stream closed after node A loss")),
	)
	// The intent loop may start replacement before either foreground dial.
	// Every caller must still coalesce with that single automatic replacement.
	type dialResult struct {
		conn  net.Conn
		route string
		err   error
	}
	results := make(chan dialResult, 2)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()
	for range 2 {
		go func() {
			conn, route, err := client.DialEgress(dialCtx, "example.test:443")
			results <- dialResult{conn: conn, route: route, err: err}
		}()
	}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent DialEgress should reselect node B: %v", result.err)
		}
		if result.route != nodeBEgress.ID {
			t.Fatalf("concurrent egress route = %q, want node B route %q", result.route, nodeBEgress.ID)
		}
		payload := []byte("payload-through-provider-stream-b")
		if _, err := result.conn.Write(payload); err != nil {
			t.Fatalf("same request write through node B stream: %v", err)
		}
		response := make([]byte, len(payload))
		if _, err := io.ReadFull(result.conn, response); err != nil {
			t.Fatalf("same request read through node B stream: %v", err)
		}
		if string(response) != string(payload) {
			t.Fatalf("same request provider-stream payload = %q, want %q", response, payload)
		}
		_ = result.conn.Close()
	}
	if got := countTargetNodeOffers(t, controlCarrier, controlEndpoint, "node-b"); got != beforeNodeBOffers+1 {
		t.Fatalf("node B offers = %d, want one replacement after baseline %d", got, beforeNodeBOffers)
	}
	if status := client.Status(); status.ActiveNodeID != "node-b" || status.State != statusStateConnected {
		t.Fatalf("status after node reselection = %+v, want connected node-b", status)
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

func TestManualEgressSelectionPreservesAutomaticRoute(t *testing.T) {
	automatic := carriers.Endpoint{ID: "automatic-primary", Carrier: carriers.CarrierSSHTCP}
	manual := carriers.Endpoint{ID: "manual-backup", Carrier: carriers.CarrierVKDocs1024}
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:                    "test-node",
			SessionID:                 "manual-selection-session",
			EgressEndpoints:           []carriers.Endpoint{automatic, manual},
			AutomaticEgressEndpointID: automatic.ID,
		},
		tunnel: &singleEndpointSuccessTunnel{successID: manual.ID},
		policy: policy.DefaultAdaptivePolicy(),
	}

	if _, err := control.SelectEgressEndpoint(manual.ID); err != nil {
		t.Fatalf("SelectEgressEndpoint: %v", err)
	}
	conn, route, err := control.DialEgress(context.Background(), "example.com:443")
	if err != nil {
		t.Fatalf("manual dial: %v", err)
	}
	defer conn.Close()
	if route != manual.ID {
		t.Fatalf("manual route = %q, want %q", route, manual.ID)
	}
	status := control.Status()
	if status.SelectedEgressEndpointID != manual.ID {
		t.Fatalf("manual selected route = %q, want %q", status.SelectedEgressEndpointID, manual.ID)
	}
	if status.AutomaticEgressEndpointID != automatic.ID {
		t.Fatalf("automatic route changed to %q, want %q", status.AutomaticEgressEndpointID, automatic.ID)
	}
}

func TestControlPlaneRecoveryProbeFailsBackWithoutTouchingOpenFallback(t *testing.T) {
	now := time.Date(2026, time.August, 1, 20, 0, 0, 0, time.UTC)
	primary := carriers.Endpoint{ID: "dion-primary", Carrier: carriers.CarrierSSHTCP}
	backup := carriers.Endpoint{ID: "backup", Carrier: carriers.CarrierVKDocs1024}
	probeCarrier := &recoveryProbeCarrier{memoryCarrier: newMemoryCarrierWithDescriptor(t, "recovery-primary", carriers.CarrierSSHTCP), results: []bool{true, true}}
	routes := &recoveryRouteTunnel{primaryID: primary.ID, backupID: backup.ID}
	recovery := policy.NewEgressRecoveryTracker(policy.EgressRecoveryConfig{
		FailureThreshold:  1,
		InitialProbeDelay: time.Minute,
		MaxProbeDelay:     4 * time.Minute,
		ProbeSuccesses:    2,
		FailbackCooldown:  30 * time.Second,
		ProbeJitter:       0,
	}, func() time.Time { return now })
	health := router.NewCarrierHealth()
	health.RecordRuntimeFailure(primary.ID)
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:          "test-node",
			SessionID:       "recovery-session",
			EgressEndpoints: []carriers.Endpoint{primary, backup},
		},
		bindings: map[string]policy.CarrierBinding{
			carriers.CarrierSSHTCP: {Carrier: probeCarrier, Endpoint: primary},
		},
		policy:         policy.DefaultAdaptivePolicy(),
		tunnel:         routes,
		egressRecovery: recovery,
		carrierHealth:  health,
	}

	conn, route, err := control.DialEgress(context.Background(), "example.com:443")
	if err != nil {
		t.Fatalf("initial fallback dial: %v", err)
	}
	defer conn.Close()
	if route != backup.ID {
		t.Fatalf("initial route=%q, want fallback %q", route, backup.ID)
	}
	if got := control.Status().AutomaticEgressEndpointID; got != backup.ID {
		t.Fatalf("automatic route=%q, want backup %q", got, backup.ID)
	}

	now = now.Add(time.Minute)
	if !control.recoverOneEgressEndpoint(context.Background()) {
		t.Fatal("first due recovery probe was not run")
	}
	if got := probeCarrier.probeCount(); got != 1 {
		t.Fatalf("probe count=%d, want 1", got)
	}
	if got := routes.dialCount(primary.ID); got != 1 {
		t.Fatalf("recovery probe opened foreground primary dial %d times", got)
	}
	if got := health.Snapshot()[primary.ID].LifecycleState; got != "degraded" {
		t.Fatalf("health after one probe=%q, want degraded until recovery threshold", got)
	}

	if !control.recoverOneEgressEndpoint(context.Background()) {
		t.Fatal("next-tick recovery confirmation probe was not run")
	}
	if recovered := health.Snapshot()[primary.ID]; !recovered.Healthy || recovered.LifecycleState != "constructed" {
		t.Fatalf("health after recovery probes=%+v, want constructed healthy", recovered)
	}
	routes.allowPrimary = true
	now = now.Add(30 * time.Second)
	conn, route, err = control.DialEgress(context.Background(), "example.com:443")
	if err != nil {
		t.Fatalf("failback dial: %v", err)
	}
	defer conn.Close()
	if route != primary.ID {
		t.Fatalf("failback route=%q, want recovered primary %q", route, primary.ID)
	}
	if got := control.Status().AutomaticEgressEndpointID; got != primary.ID {
		t.Fatalf("automatic route=%q, want recovered primary %q", got, primary.ID)
	}
}

func TestLateRecoveryProbeCannotPromoteReplacementSession(t *testing.T) {
	now := time.Date(2026, time.August, 1, 20, 0, 0, 0, time.UTC)
	primary := carriers.Endpoint{ID: "old-primary", Carrier: carriers.CarrierSSHTCP}
	backup := carriers.Endpoint{ID: "old-backup", Carrier: carriers.CarrierVKDocs1024}
	probeCarrier := &blockingRecoveryProbeCarrier{
		memoryCarrier: newMemoryCarrierWithDescriptor(t, "late-recovery", carriers.CarrierSSHTCP),
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	recovery := policy.NewEgressRecoveryTracker(policy.EgressRecoveryConfig{
		FailureThreshold:  1,
		InitialProbeDelay: time.Minute,
		MaxProbeDelay:     time.Minute,
		ProbeSuccesses:    1,
		ProbeJitter:       0,
	}, func() time.Time { return now })
	recovery.RecordDialFailure(policy.EgressEndpointKey(primary))
	now = now.Add(time.Minute)
	control := &ControlPlane{
		cfg: config.Config{Role: config.RoleClient},
		active: &activeSession{
			NodeID:                    "old-node",
			SessionID:                 "old-session",
			EgressEndpoints:           []carriers.Endpoint{primary, backup},
			AutomaticEgressEndpointID: backup.ID,
		},
		bindings: map[string]policy.CarrierBinding{
			carriers.CarrierSSHTCP: {Carrier: probeCarrier, Endpoint: primary},
		},
		policy:         policy.DefaultAdaptivePolicy(),
		egressRecovery: recovery,
	}

	done := make(chan bool, 1)
	go func() { done <- control.recoverOneEgressEndpoint(context.Background()) }()
	<-probeCarrier.started
	control.mu.Lock()
	control.active = &activeSession{
		NodeID:                    "new-node",
		SessionID:                 "new-session",
		EgressEndpoints:           []carriers.Endpoint{backup},
		AutomaticEgressEndpointID: backup.ID,
	}
	control.egressRecovery = policy.NewEgressRecoveryTracker(policy.EgressRecoveryConfig{}, func() time.Time { return now })
	control.mu.Unlock()
	close(probeCarrier.release)
	if ran := <-done; !ran {
		t.Fatal("due recovery probe did not run")
	}
	status := control.Status()
	if status.SessionID != "new-session" || status.AutomaticEgressEndpointID != backup.ID {
		t.Fatalf("late probe mutated replacement session: %+v", status)
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

func TestSafeRecoveryProbeUsesExplicitProviderCapability(t *testing.T) {
	endpoint := carriers.Endpoint{ID: "dion-recovery", Carrier: "dion"}
	adapter := &safeRecoveryProviderAdapter{fakeVideoProvider: &fakeVideoProvider{}}
	wrapped, err := carriers.NewProviderCarrier(adapter, endpoint)
	if err != nil {
		t.Fatalf("NewProviderCarrier: %v", err)
	}
	control := &ControlPlane{bindings: map[string]policy.CarrierBinding{
		endpoint.Carrier: {Carrier: wrapped, Endpoint: endpoint},
	}}

	if err := control.safeRecoveryProbe(context.Background(), endpoint); err != nil {
		t.Fatalf("safeRecoveryProbe: %v", err)
	}
	if adapter.probes != 1 {
		t.Fatalf("provider probes = %d, want 1", adapter.probes)
	}
}

func TestSafeRecoveryProbeRejectsProviderCarrierWithoutExplicitCapability(t *testing.T) {
	endpoint := carriers.Endpoint{ID: "ordinary-provider", Carrier: "ordinary-provider"}
	wrapped, err := carriers.NewProviderCarrier(&fakeVideoProvider{}, endpoint)
	if err != nil {
		t.Fatalf("NewProviderCarrier: %v", err)
	}
	control := &ControlPlane{bindings: map[string]policy.CarrierBinding{
		endpoint.Carrier: {Carrier: wrapped, Endpoint: endpoint},
	}}
	if err := control.safeRecoveryProbe(context.Background(), endpoint); err == nil || !strings.Contains(err.Error(), "no safe autonomous recovery probe") {
		t.Fatalf("safeRecoveryProbe error = %v, want explicit safe-probe rejection", err)
	}
}

// fallbackTestTunnel is a minimal CarrierTunnel mock that fails on one
// carrier and succeeds on another using a net.Pipe.
type fallbackTestTunnel struct {
	failCarrier string
	okEndpoint  carriers.Endpoint
}

type concurrentHealthyTunnel struct {
	started chan struct{}
	release chan struct{}
}

func (t *concurrentHealthyTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }

func (t *concurrentHealthyTunnel) DialContext(ctx context.Context, _ carriers.Endpoint, _ string) (net.Conn, error) {
	t.started <- struct{}{}
	select {
	case <-t.release:
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type safeRecoveryProviderAdapter struct {
	*fakeVideoProvider
	probes int
}

func (a *safeRecoveryProviderAdapter) SafeEgressRecoveryProbe(context.Context) error {
	a.probes++
	return nil
}

type recoveryProbeCarrier struct {
	*memoryCarrier
	mu      sync.Mutex
	results []bool
	probes  int
}

type blockingRecoveryProbeCarrier struct {
	*memoryCarrier
	started chan struct{}
	release chan struct{}
}

func (c *blockingRecoveryProbeCarrier) SafeEgressRecoveryProbe(context.Context, carriers.Endpoint) (carriers.Metrics, error) {
	close(c.started)
	<-c.release
	return carriers.Metrics{Healthy: true}, nil
}

func (c *recoveryProbeCarrier) Probe(context.Context, carriers.Endpoint) (carriers.Metrics, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probes++
	result := len(c.results) == 0 || c.results[0]
	if len(c.results) > 0 {
		c.results = c.results[1:]
	}
	if !result {
		return carriers.Metrics{Healthy: false}, errors.New("injected recovery probe failure")
	}
	return carriers.Metrics{Healthy: true}, nil
}

func (c *recoveryProbeCarrier) SafeEgressRecoveryProbe(ctx context.Context, endpoint carriers.Endpoint) (carriers.Metrics, error) {
	return c.Probe(ctx, endpoint)
}

func (c *recoveryProbeCarrier) probeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probes
}

type recoveryRouteTunnel struct {
	mu           sync.Mutex
	primaryID    string
	backupID     string
	allowPrimary bool
	dials        map[string]int
}

func (t *recoveryRouteTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }

func (t *recoveryRouteTunnel) DialContext(ctx context.Context, endpoint carriers.Endpoint, _ string) (net.Conn, error) {
	t.mu.Lock()
	if t.dials == nil {
		t.dials = make(map[string]int)
	}
	t.dials[endpoint.ID]++
	allowPrimary := t.allowPrimary
	t.mu.Unlock()
	if endpoint.ID == t.primaryID && !allowPrimary {
		return nil, errors.New("injected primary egress failure")
	}
	if endpoint.ID == t.backupID && allowPrimary {
		select {
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

func (t *recoveryRouteTunnel) dialCount(endpointID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dials[endpointID]
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

type nodeSelectiveTunnel struct {
	failedEndpointID  string
	healthyEndpointID string
}

// newProviderStreamLikeMemoryCarrier supplies a credential-free stand-in for
// a realtime/provider egress binding. Its stream-only descriptor ensures the
// generic typed-carrier failover path is tested separately from file.mailbox.
func newProviderStreamLikeMemoryCarrier(storageID, carrierID string) *memoryCarrier {
	descriptor := carriers.Descriptor{
		ID:             carrierID,
		Provider:       "provider-fixture",
		Mode:           carriers.DeliveryStream,
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficStream, fabric.TrafficEgress},
		Capabilities:   []carriers.Capability{carriers.CapStream, carriers.CapDuplex, carriers.CapEphemeral},
		Metrics:        carriers.Metrics{Healthy: true, QuotaRemaining: -1},
	}
	return &memoryCarrier{
		descriptor: descriptor,
		memory:     carriers.NewMemoryCarrier(storageID),
	}
}

type targetApplicationErrorTunnel struct{}

func (targetApplicationErrorTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }

func (targetApplicationErrorTunnel) DialContext(context.Context, carriers.Endpoint, string) (net.Conn, error) {
	return nil, errors.New("target application timed out")
}

func (t *nodeSelectiveTunnel) SupportsEndpoint(carriers.Endpoint) bool { return true }

func (t *nodeSelectiveTunnel) DialContext(_ context.Context, endpoint carriers.Endpoint, _ string) (net.Conn, error) {
	if endpoint.ID == t.failedEndpointID {
		return nil, session.NewCarrierFailure(errors.New("node A carrier is unavailable"))
	}
	if endpoint.ID == t.healthyEndpointID {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			buffer := make([]byte, 4096)
			for {
				n, err := server.Read(buffer)
				if n > 0 {
					if _, writeErr := server.Write(buffer[:n]); writeErr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		return client, nil
	}
	return nil, fmt.Errorf("unexpected endpoint %q", endpoint.ID)
}

func countTargetNodeOffers(t *testing.T, carrier *memoryCarrier, endpoint carriers.Endpoint, nodeID string) int {
	t.Helper()
	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("read offers: %v", err)
	}
	count := 0
	for _, envelope := range read.Envelopes {
		if envelope.PayloadType != session.PayloadSessionOffer {
			continue
		}
		offer, err := session.DecodePayload[session.Offer](envelope.Payload)
		if err != nil {
			t.Fatalf("decode offer: %v", err)
		}
		if offer.TargetNodeID == nodeID {
			count++
		}
	}
	return count
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
