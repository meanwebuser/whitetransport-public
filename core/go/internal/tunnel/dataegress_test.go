package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	utunnel "whitelist-bypass/relay/tunnel"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

// mockDT satisfies utunnel.DataTunnel for testing.
type mockDT struct {
	sendData   func(data []byte)
	setOnData  func(fn func([]byte))
	setOnClose func(fn func())
}

func (m *mockDT) SendData(data []byte) {
	if m.sendData != nil {
		m.sendData(data)
	}
}
func (m *mockDT) SetOnData(fn func([]byte)) {
	if m.setOnData != nil {
		m.setOnData(fn)
	}
}
func (m *mockDT) SetOnClose(fn func()) {
	if m.setOnClose != nil {
		m.setOnClose(fn)
	}
}
func (m *mockDT) Reconfigure(fps, batch int) {}

// mockDataTunnelProvider implements DataTunnelProvider for testing.
type mockDataTunnelProvider struct {
	mu     sync.RWMutex
	tunnel utunnel.DataTunnel
}

func (m *mockDataTunnelProvider) DataTunnel() utunnel.DataTunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tunnel
}

func (m *mockDataTunnelProvider) setTunnel(tunnel utunnel.DataTunnel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tunnel = tunnel
}

// newConnectedDTs creates a pair of mock DataTunnels wired to each other:
// dtA.SendData -> triggers dtB.onData, and vice versa.
func newConnectedDTs() (*mockDT, *mockDT) {
	client, node, _ := newLossyConnectedDTs(0, 0)
	return client, node
}

// lossyFrameLink connects two mock DataTunnels and can discard the first
// frame of a selected type in either direction. Delivery happens outside the
// mutex because a receiver may synchronously send its response back.
type lossyFrameLink struct {
	mu                sync.Mutex
	clientOnData      func([]byte)
	nodeOnData        func([]byte)
	dropClientToNode  byte
	dropNodeToClient  byte
	clientFrameCount  map[byte]int
	nodeFrameCount    map[byte]int
	nodeDropRemaining map[byte]int
}

func newLossyConnectedDTs(dropClientToNode byte, dropNodeToClient byte) (*mockDT, *mockDT, *lossyFrameLink) {
	link := &lossyFrameLink{
		dropClientToNode:  dropClientToNode,
		dropNodeToClient:  dropNodeToClient,
		clientFrameCount:  make(map[byte]int),
		nodeFrameCount:    make(map[byte]int),
		nodeDropRemaining: make(map[byte]int),
	}
	client := &mockDT{
		sendData:   link.sendClientFrame,
		setOnData:  func(fn func([]byte)) { link.setClientOnData(fn) },
		setOnClose: func(func()) {},
	}
	node := &mockDT{
		sendData:   link.sendNodeFrame,
		setOnData:  func(fn func([]byte)) { link.setNodeOnData(fn) },
		setOnClose: func(func()) {},
	}
	return client, node, link
}

func (l *lossyFrameLink) setClientOnData(fn func([]byte)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clientOnData = fn
}

func (l *lossyFrameLink) setNodeOnData(fn func([]byte)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nodeOnData = fn
}

func (l *lossyFrameLink) sendClientFrame(data []byte) {
	l.deliverFrame(data, true)
}

func (l *lossyFrameLink) sendNodeFrame(data []byte) {
	l.deliverFrame(data, false)
}

func (l *lossyFrameLink) deliverFrame(data []byte, clientToNode bool) {
	var msgType byte
	utunnel.DecodeFrames(data, func(_ uint32, decodedType byte, _ []byte) {
		msgType = decodedType
	})

	l.mu.Lock()
	counts := l.nodeFrameCount
	dropType := l.dropNodeToClient
	receiver := l.clientOnData
	if clientToNode {
		counts = l.clientFrameCount
		dropType = l.dropClientToNode
		receiver = l.nodeOnData
	}
	counts[msgType]++
	drop := msgType == dropType && counts[msgType] == 1
	if !clientToNode && l.nodeDropRemaining[msgType] > 0 {
		l.nodeDropRemaining[msgType]--
		drop = true
	}
	l.mu.Unlock()

	if !drop && receiver != nil {
		receiver(data)
	}
}

func (l *lossyFrameLink) dropNextNodeFrame(msgType byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nodeDropRemaining[msgType]++
}

func (l *lossyFrameLink) clientFrames(msgType byte) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.clientFrameCount[msgType]
}

func TestDataTunnelEgressRetriesConnectWithoutDuplicateTargetDial(t *testing.T) {
	tests := []struct {
		name                   string
		dropClientToNode       byte
		dropNodeToClient       byte
		wantConnectRetries     int
		closeTargetImmediately bool
	}{
		{
			name:               "first connect frame dropped",
			dropClientToNode:   utunnel.MsgConnect,
			wantConnectRetries: 2,
		},
		{
			name:               "first connect ack dropped",
			dropNodeToClient:   utunnel.MsgConnectOK,
			wantConnectRetries: 2,
		},
		{
			name:                   "first connect ack dropped and target closes before retry",
			dropNodeToClient:       utunnel.MsgConnectOK,
			wantConnectRetries:     2,
			closeTargetImmediately: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("target listen: %v", err)
			}
			defer listener.Close()

			var targetDials atomic.Int32
			accepted := make(chan net.Conn, 2)
			defer func() {
				for {
					select {
					case target := <-accepted:
						_ = target.Close()
					default:
						return
					}
				}
			}()
			go func() {
				for {
					conn, acceptErr := listener.Accept()
					if acceptErr != nil {
						return
					}
					targetDials.Add(1)
					if tt.closeTargetImmediately {
						_ = conn.Close()
					}
					accepted <- conn
				}
			}()

			clientDT, nodeDT, link := newLossyConnectedDTs(tt.dropClientToNode, tt.dropNodeToClient)
			if tt.closeTargetImmediately {
				// The regression isolates CONNECT result retention. A close-frame
				// loss is valid on the same unreliable startup boundary as the ACK.
				link.dropNextNodeFrame(utunnel.MsgClose)
			}
			nodeEgress := NewDataTunnelEgress("node", "wb", "wb-ep", &mockDataTunnelProvider{tunnel: nodeDT})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			// The constructor has registered the frame handler; setting node mode
			// directly keeps this protocol regression independent of goroutine timing.
			nodeEgress.serveCtx.Store(ctx)

			clientEgress := NewDataTunnelEgress("client", "wb", "wb-ep", &mockDataTunnelProvider{tunnel: clientDT})
			endpoint := carriers.Endpoint{ID: "wb-ep", Carrier: "wb"}
			conn, err := clientEgress.DialContext(ctx, endpoint, listener.Addr().String())
			if err != nil {
				t.Fatalf("DialContext after one lost handshake frame: %v", err)
			}
			_ = conn.Close()
			select {
			case target := <-accepted:
				_ = target.Close()
			case <-ctx.Done():
				t.Fatal("target connection was not accepted")
			}

			if got := link.clientFrames(utunnel.MsgConnect); got < tt.wantConnectRetries {
				t.Fatalf("MsgConnect sends = %d, want at least %d", got, tt.wantConnectRetries)
			}
			// Keep observing through another retry window: without node-side
			// deduplication, a lost CONNECT_OK creates a second target socket.
			select {
			case duplicate := <-accepted:
				_ = duplicate.Close()
				t.Fatal("duplicate MsgConnect opened a second target connection")
			case <-time.After(2 * dtConnectRetry):
			}
			if got := targetDials.Load(); got != 1 {
				t.Fatalf("target dials = %d, want exactly 1 for duplicate MsgConnect", got)
			}
		})
	}
}

func TestDataTunnelEgressConnectResultRetentionIsBounded(t *testing.T) {
	egress := &DataTunnelEgress{}
	const connID uint32 = 41
	connect := &dtNodeConnect{targetAddr: "target.invalid:443", done: make(chan struct{})}
	egress.connects.Store(connID, connect)
	egress.retainConnectResult(connID, connect, 10*time.Millisecond)

	if _, ok := egress.connects.Load(connID); !ok {
		t.Fatal("connect result was removed before its retention window")
	}

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-poll.C:
			if _, ok := egress.connects.Load(connID); !ok {
				return
			}
		case <-deadline.C:
			t.Fatal("connect result was not removed after its bounded retention window")
		}
	}
}

func TestDataTunnelEgressDialContextWithEchoServer(t *testing.T) {
	clientDT, nodeDT := newConnectedDTs()

	clientAdapter := &mockDataTunnelProvider{tunnel: clientDT}
	nodeAdapter := &mockDataTunnelProvider{tunnel: nodeDT}

	// Echo server
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
			go func(c net.Conn) {
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						c.Close()
						return
					}
					c.Write(buf[:n])
				}
			}(conn)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Node-side egress handler
	nodeEgress := NewDataTunnelEgress("test-node", "test-dt", "test-dt-ep", nodeAdapter)
	go func() {
		_ = nodeEgress.ServeEgress(ctx, nil)
	}()

	time.Sleep(50 * time.Millisecond)

	// Client dials through DataTunnelEgress
	clientEgress := NewDataTunnelEgress("test-client", "test-dt", "test-dt-ep", clientAdapter)
	ep := carriers.Endpoint{ID: "test-dt", Carrier: "test-dt"}

	conn, err := clientEgress.DialContext(ctx, ep, echoAddr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	// Send data
	_, err = conn.Write([]byte("hello dataegress"))
	if err != nil {
		t.Fatalf("client write: %v", err)
	}

	// Read echo
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}

	got := strings.TrimSpace(string(buf[:n]))
	if got != "hello dataegress" {
		t.Fatalf("expected echo 'hello dataegress', got %q", got)
	}
}

func TestDataTunnelEgressSupportsEndpoint(t *testing.T) {
	adapter := &mockDataTunnelProvider{tunnel: &mockDT{}}
	e := NewDataTunnelEgress("test", "white-dt", "white-ep-id", adapter)

	if !e.SupportsEndpoint(carriers.Endpoint{Carrier: "white-dt"}) {
		t.Fatal("should support by Carrier")
	}
	if !e.SupportsEndpoint(carriers.Endpoint{ID: "white-ep-id"}) {
		t.Fatal("should support by ID")
	}
	if e.SupportsEndpoint(carriers.Endpoint{Carrier: "other"}) {
		t.Fatal("should not support other")
	}
}

func TestDataTunnelEgressConnectTimeout(t *testing.T) {
	// Use a tunnel that never responds to MsgConnect.
	dt := &mockDT{
		sendData:   func(data []byte) {},
		setOnData:  func(fn func([]byte)) {},
		setOnClose: func(fn func()) {},
	}
	adapter := &mockDataTunnelProvider{tunnel: dt}
	e := NewDataTunnelEgress("test", "test-dt", "test-dt", adapter)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ep := carriers.Endpoint{ID: "test-dt", Carrier: "test-dt"}
	_, err := e.DialContext(ctx, ep, "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected timeout/error from unreachable tunnel")
	}
}

// ── Reconnect-aware adapter mock ──────────────────────────────────────────

// mockAdapterWithSetOnData implements both DataTunnelProvider and
// DataTunnelAdapter (SetOnData), exactly like the real WBStream adapter.
type mockAdapterWithSetOnData struct {
	mu        sync.Mutex
	tunnel    utunnel.DataTunnel
	onDataFn  func([]byte)
	onCloseFn func()
}

func (m *mockAdapterWithSetOnData) DataTunnel() utunnel.DataTunnel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tunnel
}

func (m *mockAdapterWithSetOnData) SetOnData(fn func([]byte)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onDataFn = fn
	if m.tunnel != nil {
		m.tunnel.SetOnData(fn)
	}
}

func (m *mockAdapterWithSetOnData) SetOnClose(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onCloseFn = fn
	if m.tunnel != nil {
		m.tunnel.SetOnClose(fn)
	}
}

func (m *mockAdapterWithSetOnData) hasOnData() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onDataFn != nil
}

// simulateReconnect swaps the adapter's tunnel (like WBStream OnConnected).
func (m *mockAdapterWithSetOnData) simulateReconnect(newDT utunnel.DataTunnel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tunnel = newDT
	if m.onDataFn != nil {
		newDT.SetOnData(m.onDataFn)
	}
	if m.onCloseFn != nil {
		newDT.SetOnClose(m.onCloseFn)
	}
}

func (m *mockAdapterWithSetOnData) triggerClose() {
	m.mu.Lock()
	fn := m.onCloseFn
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func TestDataTunnelEgressFiresClosedHookFromAdapter(t *testing.T) {
	adapter := &mockAdapterWithSetOnData{tunnel: &mockDT{}}
	egress := NewDataTunnelEgress("node", "wb", "wb-ep", adapter)
	closed := make(chan struct{}, 1)
	egress.SetOnClosed(func() {
		closed <- struct{}{}
	})

	egress.setupFrameHandler()
	adapter.triggerClose()

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("DataTunnel close did not fire closed hook")
	}
}

func TestDataTunnelEgressServeEgressRegistersHandlerBeforeTunnelExists(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	adapter := &mockAdapterWithSetOnData{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	nodeEgress := NewDataTunnelEgress("node", "wb", "wb-ep", adapter)
	go func() { _ = nodeEgress.ServeEgress(ctx, nil) }()

	deadline := time.Now().Add(time.Second)
	for !adapter.hasOnData() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !adapter.hasOnData() {
		t.Fatal("ServeEgress did not register frame handler before tunnel appeared")
	}

	clientDT, nodeDT := newConnectedDTs()
	responseCh := make(chan byte, 1)
	clientDT.SetOnData(func(data []byte) {
		utunnel.DecodeFrames(data, func(_ uint32, msgType byte, _ []byte) {
			responseCh <- msgType
		})
	})
	adapter.simulateReconnect(nodeDT)

	clientDT.SendData(utunnel.EncodeFrame(1, utunnel.MsgConnect, []byte(echoLn.Addr().String())))

	select {
	case msgType := <-responseCh:
		if msgType != utunnel.MsgConnectOK {
			t.Fatalf("response msgType = %d, want MsgConnectOK", msgType)
		}
	case <-ctx.Done():
		t.Fatal("first MsgConnect after delayed tunnel did not reach node handler")
	}
}

// TestDataTunnelEgress_FrameHandlerPersistsAcrossReconnect verifies that
// setupFrameHandler stores the callback via adapter.SetOnData so it survives
// tunnel reconnections (Bug #3).
func TestDataTunnelEgress_FrameHandlerPersistsAcrossReconnect(t *testing.T) {
	// Track how many times SetOnData was called on each tunnel.
	var dt1OnDataCalled, dt2OnDataCalled atomic.Int32
	dt1 := &mockDT{
		sendData:   func(data []byte) {},
		setOnData:  func(fn func([]byte)) { dt1OnDataCalled.Add(1) },
		setOnClose: func(fn func()) {},
	}
	dt2 := &mockDT{
		sendData:   func(data []byte) {},
		setOnData:  func(fn func([]byte)) { dt2OnDataCalled.Add(1) },
		setOnClose: func(fn func()) {},
	}

	adapter := &mockAdapterWithSetOnData{tunnel: dt1}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewDataTunnelEgress("node", "wb", "wb-ep", adapter)
	go func() { _ = e.ServeEgress(ctx, nil) }()
	time.Sleep(50 * time.Millisecond)

	// setupFrameHandler was called by NewDataTunnelEgress → adapter.SetOnData
	if dt1OnDataCalled.Load() == 0 {
		t.Fatal("expected SetOnData on dt1 after creation")
	}

	// Simulate reconnect: adapter gets a new tunnel.
	adapter.simulateReconnect(dt2)

	// Trigger getOrWaitTunnel which should detect the new tunnel.
	_ = e.getOrWaitTunnel(ctx)

	// dt2 should have received SetOnData via adapter.onDataFn reapplication.
	if dt2OnDataCalled.Load() == 0 {
		t.Fatal("expected SetOnData on dt2 after reconnect — frame handler did not persist")
	}
}

// TestDataTunnelEgress_CurrentDT_ReturnsFreshAfterReconnect verifies that
// currentDT() returns the new tunnel after the adapter reconnected.
func TestDataTunnelEgress_CurrentDT_ReturnsFreshAfterReconnect(t *testing.T) {
	dt1 := &mockDT{
		sendData: func(data []byte) {}, setOnData: func(fn func([]byte)) {}, setOnClose: func(fn func()) {},
	}
	dt2 := &mockDT{
		sendData: func(data []byte) {}, setOnData: func(fn func([]byte)) {}, setOnClose: func(fn func()) {},
	}

	adapter := &mockAdapterWithSetOnData{tunnel: dt1}
	e := NewDataTunnelEgress("node", "wb", "wb-ep", adapter)

	// currentDT should return dt1.
	got := e.currentDT()
	if got != dt1 {
		t.Fatal("currentDT should return dt1")
	}

	// Simulate reconnect.
	adapter.simulateReconnect(dt2)

	// currentDT should now return dt2.
	got = e.currentDT()
	if got != dt2 {
		t.Fatal("currentDT should return dt2 after reconnect")
	}
}

// TestDataTunnelEgress_DialTarget_ViaHTTPProxy verifies that when proxyURL
// is configured, dialTarget routes the connection through an HTTP CONNECT
// proxy (Bug #2).
func TestDataTunnelEgress_DialTarget_ViaHTTPProxy(t *testing.T) {
	// Start a simple echo target.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoLn.Close()
	echoAddr := echoLn.Addr().String()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 256)
				n, err := c.Read(buf)
				if err == nil {
					c.Write(buf[:n])
				}
				c.Close()
			}(c)
		}
	}()

	// Start a minimal HTTP CONNECT proxy.
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	defer proxyLn.Close()
	proxyAddr := proxyLn.Addr().String()
	var proxyUsed atomic.Bool
	go func() {
		for {
			c, err := proxyLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != "CONNECT" {
					return
				}
				proxyUsed.Store(true)
				// Dial the real target.
				target, err := net.Dial("tcp", req.Host)
				if err != nil {
					fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer target.Close()
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\n\r\n")
				// Bidirectional copy.
				done := make(chan struct{}, 2)
				go func() { io.Copy(target, br); done <- struct{}{} }()
				go func() { io.Copy(c, target); done <- struct{}{} }()
				<-done
			}(c)
		}
	}()

	e := &DataTunnelEgress{
		proxyURL: "http://" + proxyAddr,
	}

	conn, err := e.dialTarget(echoAddr)
	if err != nil {
		t.Fatalf("dialTarget via proxy: %v", err)
	}
	defer conn.Close()

	// Write and read echo through the proxy.
	_, err = conn.Write([]byte("proxy-test"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "proxy-test" {
		t.Fatalf("expected 'proxy-test', got %q", string(buf[:n]))
	}
	if !proxyUsed.Load() {
		t.Fatal("proxy was not used — connection went direct")
	}
}

// TestDataTunnelEgress_EchoAfterReconnect is the key integration test:
// it verifies that after a simulated WBStream reconnect (new tunnel),
// a client can still DialContext through the node and get an echo response.
func TestDataTunnelEgress_EchoAfterReconnect(t *testing.T) {
	// Echo target.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoLn.Close()
	echoAddr := echoLn.Addr().String()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						c.Close()
						return
					}
					c.Write(buf[:n])
				}
			}(c)
		}
	}()

	// Phase 1: create initial connected pair.
	clientDT1, nodeDT1 := newConnectedDTs()
	clientAdapter := &mockDataTunnelProvider{tunnel: clientDT1}

	// Node uses reconnect-aware adapter.
	nodeAdapter := &mockAdapterWithSetOnData{tunnel: nodeDT1}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nodeEgress := NewDataTunnelEgress("node", "wb", "wb-ep", nodeAdapter)
	go func() { _ = nodeEgress.ServeEgress(ctx, nil) }()
	time.Sleep(50 * time.Millisecond)

	clientEgress := NewDataTunnelEgress("client", "wb", "wb-ep", clientAdapter)
	ep := carriers.Endpoint{ID: "wb", Carrier: "wb"}

	// First connection should work.
	conn1, err := clientEgress.DialContext(ctx, ep, echoAddr)
	if err != nil {
		t.Fatalf("DialContext #1: %v", err)
	}
	conn1.Write([]byte("first"))
	buf := make([]byte, 64)
	conn1.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn1.Read(buf)
	if err != nil {
		t.Fatalf("Read #1: %v", err)
	}
	if string(buf[:n]) != "first" {
		t.Fatalf("expected 'first', got %q", string(buf[:n]))
	}
	conn1.Close()

	// Phase 2: simulate reconnect — new DT pair, swap on node side.
	clientDT2, nodeDT2 := newConnectedDTs()
	// Client adapter gets new tunnel.
	clientAdapter.setTunnel(clientDT2)
	// Node adapter gets new tunnel (simulates OnConnected).
	nodeAdapter.simulateReconnect(nodeDT2)

	// Client must also refresh its cached tunnel.
	_ = clientEgress.getOrWaitTunnel(ctx)

	time.Sleep(50 * time.Millisecond) // let frame handler settle

	// Second connection after reconnect should also work.
	conn2, err := clientEgress.DialContext(ctx, ep, echoAddr)
	if err != nil {
		t.Fatalf("DialContext #2 after reconnect: %v", err)
	}
	conn2.Write([]byte("second"))
	conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err = conn2.Read(buf)
	if err != nil {
		t.Fatalf("Read #2: %v", err)
	}
	if string(buf[:n]) != "second" {
		t.Fatalf("expected 'second', got %q", string(buf[:n]))
	}
	conn2.Close()
}
