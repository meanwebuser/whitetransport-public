package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestCarrierPacketEgressThroughMemoryCarrier(t *testing.T) {
	carrier := newPacketRecordingCarrier(t, "packet-e2e")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-e2e"}
	bindings := map[string]policy.CarrierBinding{carriers.CarrierVKDocs1024: {Carrier: carrier, Endpoint: ep}}
	cipher := newPacketTestCipher(t)
	expiresAt := time.Now().Add(time.Minute)

	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("UDP echo listen: %v", err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, readErr := echo.ReadFrom(buf)
			if readErr != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	node := NewCarrierTunnel("packet-node", bindings)
	node.SetCipher(cipher)
	node.SetPacketSession("session-packet-test", "packet-client", expiresAt)
	go func() { _ = node.ServeEgress(ctx, bindings) }()
	time.Sleep(50 * time.Millisecond)

	client := NewCarrierTunnel("packet-client", bindings)
	client.SetCipher(cipher)
	client.SetPacketSession("session-packet-test", "packet-node", expiresAt)
	packetConn, err := client.OpenPacketConn(ctx, ep, session.PacketMetadata{
		FlowID:    "packet-flow-test",
		SessionID: "session-packet-test",
		PeerID:    "packet-node",
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("OpenPacketConn: %v", err)
	}
	defer packetConn.Close()
	if _, err := packetConn.WriteTo([]byte("udp-carrier-echo"), echo.LocalAddr()); err != nil {
		t.Fatalf("packet WriteTo: %v", err)
	}
	if err := packetConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("packet SetReadDeadline: %v", err)
	}
	buffer := make([]byte, 2048)
	n, source, err := packetConn.ReadFrom(buffer)
	if err != nil {
		t.Fatalf("packet ReadFrom: %v", err)
	}
	if string(buffer[:n]) != "udp-carrier-echo" {
		t.Fatalf("packet echo = %q", buffer[:n])
	}
	if source.String() != echo.LocalAddr().String() {
		t.Fatalf("packet source = %q, want %q", source, echo.LocalAddr())
	}
	for _, envelope := range carrier.snapshotWrites() {
		if envelope.PayloadType != "encrypted" {
			t.Fatalf("packet carrier write downgraded to plaintext type=%q", envelope.PayloadType)
		}
	}
}

func TestCarrierPacketAssociationsShareOneEndpointReader(t *testing.T) {
	carrier, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir(), AllowEgress: true})
	if err != nil {
		t.Fatalf("new physical mailbox carrier: %v", err)
	}
	baseEndpoint := carriers.Endpoint{ID: "packet-shared-base", Carrier: carriers.CarrierFileMailbox, Address: "packet-shared-physical"}
	aliasEndpoint := carriers.Endpoint{ID: "packet-shared-alias", Carrier: "session.packet.alias", Address: baseEndpoint.Address}
	baseBinding := policy.CarrierBinding{Carrier: carrier, Endpoint: baseEndpoint}
	aliasBinding := policy.CarrierBinding{Carrier: carrier, Endpoint: aliasEndpoint}
	bindings := map[string]policy.CarrierBinding{carriers.CarrierFileMailbox: baseBinding}
	cipher := newPacketTestCipher(t)
	expiresAt := time.Now().Add(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	node := NewCarrierTunnel("packet-node", bindings)
	node.SetSessionBinding(aliasEndpoint, aliasBinding)
	node.SetCipher(cipher)
	node.SetPacketSession("shared-reader-session", "packet-client", expiresAt)
	go func() { _ = node.ServeEgress(ctx, bindings) }()

	client := NewCarrierTunnel("packet-client", bindings)
	client.SetSessionBinding(aliasEndpoint, aliasBinding)
	client.SetCipher(cipher)
	client.SetPacketSession("shared-reader-session", "packet-node", expiresAt)
	first, err := client.OpenPacketConn(ctx, baseEndpoint, session.PacketMetadata{FlowID: "shared-reader-first", SessionID: "shared-reader-session", PeerID: "packet-node"})
	if err != nil {
		t.Fatalf("open first packet association: %v", err)
	}
	second, err := client.OpenPacketConn(ctx, aliasEndpoint, session.PacketMetadata{FlowID: "shared-reader-second", SessionID: "shared-reader-session", PeerID: "packet-node"})
	if err != nil {
		_ = first.Close()
		t.Fatalf("open second packet association: %v", err)
	}

	client.mu.Lock()
	readerCount := len(client.packetReaders)
	client.mu.Unlock()
	if readerCount != 1 {
		t.Fatalf("packet readers = %d, want one shared reader for the endpoint", readerCount)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first packet association: %v", err)
	}
	client.mu.Lock()
	readerCount = len(client.packetReaders)
	client.mu.Unlock()
	if readerCount != 1 {
		t.Fatalf("packet readers after first close = %d, want shared reader retained", readerCount)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second packet association: %v", err)
	}
	eventuallyPacket(t, time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.packetReaders) == 0
	})
}

func TestServeEgressSharesPhysicalAliasReaderAndHonorsPollBudget(t *testing.T) {
	carrier := newPollCountingCarrier()
	baseEndpoint := carriers.Endpoint{ID: "provider-static", Carrier: "counting.provider", Address: "mailbox-42"}
	aliasEndpoint := carriers.Endpoint{ID: "provider-session-alias", Carrier: "session.alias", Address: baseEndpoint.Address}
	baseBinding := policy.CarrierBinding{Carrier: carrier, Endpoint: baseEndpoint}
	aliasBinding := policy.CarrierBinding{Carrier: carrier, Endpoint: aliasEndpoint}
	tunnel := NewCarrierTunnel("packet-node", map[string]policy.CarrierBinding{"counting.provider": baseBinding})
	tunnel.SetSessionBinding(aliasEndpoint, aliasBinding)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- tunnel.ServeEgress(ctx, map[string]policy.CarrierBinding{"counting.provider": baseBinding})
	}()
	eventuallyPacket(t, time.Second, func() bool { return carrier.readCount.Load() > 0 })
	time.Sleep(350 * time.Millisecond)
	if reads := carrier.readCount.Load(); reads != 1 {
		t.Fatalf("physical provider reads = %d in one-second budget window, want exactly one shared poll", reads)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeEgress did not stop after context cancellation")
	}
}

func TestCarrierPacketEgressRequiresEncryptedBoundSession(t *testing.T) {
	carrier := newTestMemoryCarrier(t, "packet-no-session")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-no-session"}
	tunnel := NewCarrierTunnel("packet-client", map[string]policy.CarrierBinding{
		carriers.CarrierVKDocs1024: {Carrier: carrier, Endpoint: ep},
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := tunnel.OpenPacketConn(ctx, ep, session.PacketMetadata{FlowID: "unbound", SessionID: "missing", PeerID: "packet-node"}); err == nil {
		t.Fatal("OpenPacketConn succeeded without an encrypted bound session")
	}
}

func TestCarrierPacketSessionCloseIsIdempotentAndClosesBothSides(t *testing.T) {
	carrier := newPacketRecordingCarrier(t, "packet-close")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-close"}
	bindings := map[string]policy.CarrierBinding{carriers.CarrierVKDocs1024: {Carrier: carrier, Endpoint: ep}}
	cipher := newPacketTestCipher(t)
	expiresAt := time.Now().Add(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node := NewCarrierTunnel("packet-node", bindings)
	node.SetCipher(cipher)
	node.SetPacketSession("packet-session", "packet-client", expiresAt)
	go func() { _ = node.ServeEgress(ctx, bindings) }()
	client := NewCarrierTunnel("packet-client", bindings)
	client.SetCipher(cipher)
	client.SetPacketSession("packet-session", "packet-node", expiresAt)
	conn, err := client.OpenPacketConn(ctx, ep, session.PacketMetadata{FlowID: "packet-close-flow", SessionID: "packet-session", PeerID: "packet-node", ExpiresAt: expiresAt})
	if err != nil {
		t.Fatalf("OpenPacketConn: %v", err)
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = conn.Close() }()
		go func() { defer wg.Done(); client.ClosePacketSession("packet-session") }()
	}
	wg.Wait()
	client.ClearCipher()
	if _, err := conn.WriteTo([]byte("must-not-leak"), mustPacketUDPAddr(t, "127.0.0.1:9")); err == nil {
		t.Fatal("closed packet association accepted a post-session write")
	}
	eventuallyPacket(t, 2*time.Second, func() bool {
		node.mu.Lock()
		defer node.mu.Unlock()
		return len(node.activePackets) == 0
	})
	for _, envelope := range carrier.snapshotWrites() {
		if envelope.PayloadType != "encrypted" {
			t.Fatalf("session close emitted plaintext packet envelope type=%q", envelope.PayloadType)
		}
	}
}

func TestCarrierPacketRejectsPlaintextWrongPeerAndReplayedOpen(t *testing.T) {
	carrier := newPacketRecordingCarrier(t, "packet-attacks")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-attacks"}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	cipher := newPacketTestCipher(t)
	node := NewCarrierTunnel("packet-node", map[string]policy.CarrierBinding{carriers.CarrierVKDocs1024: binding})
	node.SetCipher(cipher)
	node.SetPacketSession("packet-session", "packet-client", time.Now().Add(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	openPayload, err := json.Marshal(packetOpenPayload{FlowID: "attack-flow", SessionID: "packet-session"})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	open := fabric.NewEnvelope("attack-flow", fabric.TrafficEgress, packetOpen, openPayload)
	open.Source = "packet-client"
	open.Destination = "packet-node"
	open.SessionID = "packet-session"
	open.Sequence = 1
	node.HandleEgressEnvelope(ctx, binding, open)
	if count := nodePacketCount(node); count != 0 {
		t.Fatalf("plaintext packet open was accepted: active=%d", count)
	}

	node.HandleEgressEnvelope(ctx, binding, sealPacketTestEnvelope(t, cipher, open))
	if count := nodePacketCount(node); count != 1 {
		t.Fatalf("valid encrypted packet open active=%d, want 1", count)
	}
	closeEnvelope := fabric.NewEnvelope("attack-flow", fabric.TrafficEgress, packetClose, nil)
	closeEnvelope.Source = "packet-client"
	closeEnvelope.Destination = "packet-node"
	closeEnvelope.SessionID = "packet-session"
	closeEnvelope.Sequence = 2
	node.HandleEgressEnvelope(ctx, binding, sealPacketTestEnvelope(t, cipher, closeEnvelope))
	if count := nodePacketCount(node); count != 0 {
		t.Fatalf("valid close active=%d, want 0", count)
	}
	node.HandleEgressEnvelope(ctx, binding, sealPacketTestEnvelope(t, cipher, open))
	if count := nodePacketCount(node); count != 0 {
		t.Fatalf("replayed packet open recreated association: active=%d", count)
	}

	wrongPeer := open
	wrongPeer.ID = "wrong-peer-flow"
	wrongPeer.Source = "attacker"
	wrongPeer.Sequence = 1
	wrongPayload, _ := json.Marshal(packetOpenPayload{FlowID: wrongPeer.ID, SessionID: wrongPeer.SessionID})
	wrongPeer.Payload = wrongPayload
	node.HandleEgressEnvelope(ctx, binding, sealPacketTestEnvelope(t, cipher, wrongPeer))
	if count := nodePacketCount(node); count != 0 {
		t.Fatalf("wrong-peer packet open was accepted: active=%d", count)
	}
}

func TestCarrierPacketRejectsOversizedDatagramAndUpstreamProxyBypass(t *testing.T) {
	carrier := newTestMemoryCarrier(t, "packet-policy")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-policy"}
	bindings := map[string]policy.CarrierBinding{carriers.CarrierVKDocs1024: {Carrier: carrier, Endpoint: ep}}
	cipher := newPacketTestCipher(t)
	expiresAt := time.Now().Add(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node := NewCarrierTunnel("packet-node", bindings)
	node.SetProxyURL("http://127.0.0.1:3128")
	node.SetCipher(cipher)
	node.SetPacketSession("packet-session", "packet-client", expiresAt)
	go func() { _ = node.ServeEgress(ctx, bindings) }()
	client := NewCarrierTunnel("packet-client", bindings)
	client.SetCipher(cipher)
	client.SetPacketSession("packet-session", "packet-node", expiresAt)
	_, err := client.OpenPacketConn(ctx, ep, session.PacketMetadata{FlowID: "proxy-policy", SessionID: "packet-session", PeerID: "packet-node", ExpiresAt: expiresAt})
	if err == nil || !strings.Contains(err.Error(), "upstream proxy") {
		t.Fatalf("packet open with upstream proxy err=%v, want explicit unsupported error", err)
	}

	node.SetProxyURL("")
	conn, err := client.OpenPacketConn(ctx, ep, session.PacketMetadata{FlowID: "size-policy", SessionID: "packet-session", PeerID: "packet-node", ExpiresAt: expiresAt})
	if err != nil {
		t.Fatalf("OpenPacketConn after clearing proxy: %v", err)
	}
	defer conn.Close()
	if _, err := conn.WriteTo(make([]byte, 4097), mustPacketUDPAddr(t, "127.0.0.1:9")); err == nil {
		t.Fatal("oversized carrier datagram was accepted")
	}
}

func TestCarrierPacketCapabilityExcludesStreamOnlyOutbounds(t *testing.T) {
	packetDescriptor, err := carriers.FindStandardDescriptor(carriers.CarrierWBStreamVP8)
	if err != nil {
		t.Fatalf("WBStream descriptor: %v", err)
	}
	packetCarrier := newTestMemoryCarrier(t, "packet-capability")
	packetCarrier.descriptor = packetDescriptor
	packetEndpoint := carriers.Endpoint{ID: carriers.CarrierWBStreamVP8, Carrier: carriers.CarrierWBStreamVP8, Address: "mem://packet-capability"}
	packetTunnel := NewCarrierTunnel("packet-client", map[string]policy.CarrierBinding{
		carriers.CarrierWBStreamVP8: {Carrier: packetCarrier, Endpoint: packetEndpoint},
	})
	if !packetTunnel.SupportsPacketEndpoint(packetEndpoint) {
		t.Fatal("WBStream datagram-capable endpoint was rejected")
	}

	streamDescriptor, err := carriers.FindStandardDescriptor(carriers.CarrierSingBoxVLESS)
	if err != nil {
		t.Fatalf("sing-box descriptor: %v", err)
	}
	streamCarrier := newTestMemoryCarrier(t, "stream-only-capability")
	streamCarrier.descriptor = streamDescriptor
	streamEndpoint := carriers.Endpoint{ID: carriers.CarrierSingBoxVLESS, Carrier: carriers.CarrierSingBoxVLESS, Address: "vless://stream-only"}
	streamTunnel := NewCarrierTunnel("packet-client", map[string]policy.CarrierBinding{
		carriers.CarrierSingBoxVLESS: {Carrier: streamCarrier, Endpoint: streamEndpoint},
	})
	if !streamTunnel.SupportsEndpoint(streamEndpoint) {
		t.Fatal("stream endpoint should remain TCP-capable")
	}
	if streamTunnel.SupportsPacketEndpoint(streamEndpoint) {
		t.Fatal("stream-only endpoint was incorrectly advertised as packet-capable")
	}

	controlOnlyCarrier, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("control-only file mailbox: %v", err)
	}
	controlEndpoint := carriers.Endpoint{ID: "local.control", Carrier: carriers.CarrierFileMailbox, Address: "control"}
	controlBinding := policy.CarrierBinding{Carrier: controlOnlyCarrier, Endpoint: controlEndpoint}
	controlTunnel := NewCarrierTunnel("packet-client", map[string]policy.CarrierBinding{"local.control": controlBinding})
	controlTunnel.SetSessionBinding(controlEndpoint, controlBinding)
	if controlTunnel.SupportsPacketEndpoint(controlEndpoint) {
		t.Fatal("control-only file mailbox was incorrectly advertised as packet-capable")
	}
	egressCarrier, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: t.TempDir(), AllowEgress: true})
	if err != nil {
		t.Fatalf("egress file mailbox: %v", err)
	}
	egressEndpoint := carriers.Endpoint{ID: "local.egress", Carrier: carriers.CarrierFileMailbox, Address: "egress"}
	egressBinding := policy.CarrierBinding{Carrier: egressCarrier, Endpoint: egressEndpoint}
	egressTunnel := NewCarrierTunnel("packet-client", map[string]policy.CarrierBinding{"local.egress": egressBinding})
	egressTunnel.SetSessionBinding(egressEndpoint, egressBinding)
	if !egressTunnel.SupportsPacketEndpoint(egressEndpoint) {
		t.Fatal("explicit egress file mailbox was rejected as packet-capable")
	}
}

func TestCarrierPacketSessionExpiryAndAssociationQuota(t *testing.T) {
	carrier := newTestMemoryCarrier(t, "packet-limits")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-limits"}
	bindings := map[string]policy.CarrierBinding{carriers.CarrierVKDocs1024: {Carrier: carrier, Endpoint: ep}}
	tunnel := NewCarrierTunnel("packet-client", bindings)
	tunnel.SetCipher(newPacketTestCipher(t))
	tunnel.SetPacketSession("expired-session", "packet-node", time.Now().Add(-time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := tunnel.OpenPacketConn(ctx, ep, session.PacketMetadata{FlowID: "expired-flow", SessionID: "expired-session", PeerID: "packet-node"}); err == nil {
		t.Fatal("expired packet session accepted an association")
	}
	if !packetSessionExpired(time.Time{}) {
		t.Fatal("zero packet-session expiry was treated as unbounded")
	}
	tunnel.SetPacketSession("bounded-default-session", "packet-node", time.Time{})
	tunnel.mu.Lock()
	boundedExpiry := tunnel.packetSessions["bounded-default-session"].expiresAt
	tunnel.mu.Unlock()
	if boundedExpiry.IsZero() || time.Until(boundedExpiry) <= 0 || time.Until(boundedExpiry) > packetDefaultSessionTTL {
		t.Fatalf("default packet-session expiry = %s, want a bounded future deadline", boundedExpiry)
	}

	tunnel.SetPacketSession("quota-session", "packet-node", time.Now().Add(time.Minute))
	tunnel.mu.Lock()
	for index := range packetMaxAssociationsPerSession {
		flowID := fmt.Sprintf("quota-%d", index)
		tunnel.clientPackets[flowID] = &carrierPacketConn{metadata: session.PacketMetadata{SessionID: "quota-session"}}
	}
	tunnel.mu.Unlock()
	if _, err := tunnel.OpenPacketConn(ctx, ep, session.PacketMetadata{FlowID: "quota-overflow", SessionID: "quota-session", PeerID: "packet-node"}); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("association quota err=%v, want explicit quota failure", err)
	}
}

func TestCarrierPacketRejectsInvalidSourceMetadata(t *testing.T) {
	carrier := newPacketRecordingCarrier(t, "packet-source-validation")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-source-validation"}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	cipher := newPacketTestCipher(t)
	node := NewCarrierTunnel("packet-node", map[string]policy.CarrierBinding{carriers.CarrierVKDocs1024: binding})
	node.SetCipher(cipher)
	node.SetPacketSession("packet-source-session", "packet-client", time.Now().Add(time.Minute))
	payload, err := json.Marshal(packetOpenPayload{FlowID: "invalid-source-flow", SessionID: "packet-source-session", SourceAddr: "not-a-socket-address"})
	if err != nil {
		t.Fatalf("marshal invalid source open: %v", err)
	}
	open := fabric.NewEnvelope("invalid-source-flow", fabric.TrafficEgress, packetOpen, payload)
	open.Source = "packet-client"
	open.Destination = "packet-node"
	open.SessionID = "packet-source-session"
	open.Sequence = 1
	node.HandleEgressEnvelope(context.Background(), binding, sealPacketTestEnvelope(t, cipher, open))
	if count := nodePacketCount(node); count != 0 {
		t.Fatalf("invalid source metadata opened %d packet association(s)", count)
	}
}

func TestCarrierPacketCountsOnlyExitSideDomainResolution(t *testing.T) {
	carrier := newPacketRecordingCarrier(t, "packet-exit-resolver")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-exit-resolver"}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: ep}
	cipher := newPacketTestCipher(t)
	node := NewCarrierTunnel("packet-node", map[string]policy.CarrierBinding{carriers.CarrierVKDocs1024: binding})
	node.SetCipher(cipher)
	node.SetPacketSession("packet-resolver-session", "packet-client", time.Now().Add(time.Minute))
	defer node.ClosePacketSession("packet-resolver-session")
	var resolverCalls atomic.Int32
	node.packetResolver = func(_ context.Context, address string) (*net.UDPAddr, error) {
		resolverCalls.Add(1)
		if address != "exit-only.invalid:5353" {
			return nil, fmt.Errorf("unexpected resolver address %q", address)
		}
		return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5353}, nil
	}

	openPayload, _ := json.Marshal(packetOpenPayload{FlowID: "resolver-flow", SessionID: "packet-resolver-session"})
	open := fabric.NewEnvelope("resolver-flow", fabric.TrafficEgress, packetOpen, openPayload)
	open.Source = "packet-client"
	open.Destination = "packet-node"
	open.SessionID = "packet-resolver-session"
	open.Sequence = 1
	node.HandleEgressEnvelope(context.Background(), binding, sealPacketTestEnvelope(t, cipher, open))
	if count := nodePacketCount(node); count != 1 {
		t.Fatalf("resolver packet open active=%d, want 1", count)
	}
	dataPayload, _ := json.Marshal(packetDataPayload{DestinationAddr: "exit-only.invalid:5353", SourceAddr: "127.0.0.1:49152", Payload: []byte("resolver-proof")})
	data := fabric.NewEnvelope("resolver-flow", fabric.TrafficEgress, packetData, dataPayload)
	data.Source = "packet-client"
	data.Destination = "packet-node"
	data.SessionID = "packet-resolver-session"
	data.Sequence = 2
	node.HandleEgressEnvelope(context.Background(), binding, sealPacketTestEnvelope(t, cipher, data))
	if calls := resolverCalls.Load(); calls != 1 {
		t.Fatalf("exit resolver calls = %d, want 1", calls)
	}
	node.mu.Lock()
	exitCount := node.packetExitDomainResolutions
	node.mu.Unlock()
	if exitCount != 1 {
		t.Fatalf("exit domain resolution counter = %d, want 1", exitCount)
	}
}

func TestCarrierPacketEgressSupportsIPv6ExitSocket(t *testing.T) {
	echo, err := net.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer echo.Close()
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, addr, readErr := echo.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			_, _ = echo.WriteTo(buffer[:n], addr)
		}
	}()

	carrier := newTestMemoryCarrier(t, "packet-ipv6")
	ep := carriers.Endpoint{ID: carriers.CarrierVKDocs1024, Carrier: carriers.CarrierVKDocs1024, Address: "mem://packet-ipv6"}
	bindings := map[string]policy.CarrierBinding{carriers.CarrierVKDocs1024: {Carrier: carrier, Endpoint: ep}}
	cipher := newPacketTestCipher(t)
	expiresAt := time.Now().Add(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	node := NewCarrierTunnel("packet-node", bindings)
	node.SetCipher(cipher)
	node.SetPacketSession("ipv6-session", "packet-client", expiresAt)
	go func() { _ = node.ServeEgress(ctx, bindings) }()
	client := NewCarrierTunnel("packet-client", bindings)
	client.SetCipher(cipher)
	client.SetPacketSession("ipv6-session", "packet-node", expiresAt)
	conn, err := client.OpenPacketConn(ctx, ep, session.PacketMetadata{FlowID: "ipv6-flow", SessionID: "ipv6-session", PeerID: "packet-node"})
	if err != nil {
		t.Fatalf("OpenPacketConn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.WriteTo([]byte("ipv6-packet-echo"), echo.LocalAddr()); err != nil {
		t.Fatalf("IPv6 packet WriteTo: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("IPv6 packet deadline: %v", err)
	}
	buffer := make([]byte, 2048)
	n, source, err := conn.ReadFrom(buffer)
	if err != nil {
		t.Fatalf("IPv6 packet ReadFrom: %v", err)
	}
	if string(buffer[:n]) != "ipv6-packet-echo" || source.String() != echo.LocalAddr().String() {
		t.Fatalf("IPv6 packet response source=%s payload=%q", source, buffer[:n])
	}
}

func newPacketTestCipher(t *testing.T) *fabric.EnvelopeCipher {
	t.Helper()
	var key [32]byte
	copy(key[:], []byte("packet-test-session-key-material-32"))
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatalf("new packet cipher: %v", err)
	}
	return cipher
}

func sealPacketTestEnvelope(t *testing.T, cipher *fabric.EnvelopeCipher, envelope fabric.Envelope) fabric.Envelope {
	t.Helper()
	sealed, err := cipher.Seal(envelope)
	if err != nil {
		t.Fatalf("seal packet envelope: %v", err)
	}
	return fabric.Envelope{Version: 1, ID: envelope.ID, PayloadType: "encrypted", Source: envelope.Source, Destination: envelope.Destination, Sequence: envelope.Sequence, CreatedAt: envelope.CreatedAt, Payload: sealed}
}

func nodePacketCount(tunnel *CarrierTunnel) int {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	return len(tunnel.activePackets)
}

func eventuallyPacket(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("packet condition did not become true before timeout")
}

func mustPacketUDPAddr(t *testing.T, address string) net.Addr {
	t.Helper()
	resolved, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		t.Fatalf("resolve UDP address %q: %v", address, err)
	}
	return resolved
}

type packetRecordingCarrier struct {
	*testMemoryCarrier
	mu     sync.Mutex
	writes []fabric.Envelope
}

type pollCountingCarrier struct {
	readCount atomic.Int32
}

func newPollCountingCarrier() *pollCountingCarrier { return &pollCountingCarrier{} }

func (c *pollCountingCarrier) Descriptor() carriers.Descriptor {
	return carriers.Descriptor{
		ID:             "counting.provider",
		Provider:       "counting",
		Mode:           carriers.DeliveryMailbox,
		TrafficClasses: []fabric.TrafficClass{fabric.TrafficEgress},
		Capabilities:   []carriers.Capability{carriers.CapMailbox, carriers.CapDatagram},
		Limits:         carriers.Limits{MaxPayloadBytes: 1 << 20, PollsPerMinute: 60},
	}
}

func (c *pollCountingCarrier) Write(context.Context, carriers.Endpoint, fabric.Envelope) error {
	return nil
}

func (c *pollCountingCarrier) Read(context.Context, carriers.Endpoint, carriers.Cursor) (carriers.ReadResult, error) {
	c.readCount.Add(1)
	return carriers.ReadResult{}, nil
}

func (c *pollCountingCarrier) Probe(context.Context, carriers.Endpoint) (carriers.Metrics, error) {
	return carriers.Metrics{Healthy: true}, nil
}

func (c *pollCountingCarrier) DeleteMessage(context.Context, carriers.Endpoint, string) error {
	return nil
}

func newPacketRecordingCarrier(t *testing.T, id string) *packetRecordingCarrier {
	t.Helper()
	return &packetRecordingCarrier{testMemoryCarrier: newTestMemoryCarrier(t, id)}
}

func (c *packetRecordingCarrier) Write(ctx context.Context, endpoint carriers.Endpoint, envelope fabric.Envelope) error {
	c.mu.Lock()
	c.writes = append(c.writes, envelope)
	c.mu.Unlock()
	return c.testMemoryCarrier.Write(ctx, endpoint, envelope)
}

func (c *packetRecordingCarrier) snapshotWrites() []fabric.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]fabric.Envelope(nil), c.writes...)
}
