package tunnel

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	utunnel "whitelist-bypass/relay/tunnel"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

const (
	dtConnectTimeout     = 30 * time.Second
	dtConnectRetry       = 250 * time.Millisecond
	dtReadBufSize        = 32768
	dtKeepaliveInterval  = 15 * time.Second
	dtTunnelWaitInterval = 100 * time.Millisecond
)

// DataTunnelProvider is implemented by provider adapters that expose a
// tunnel.DataTunnel (e.g. whitelist-bypass).
type DataTunnelProvider interface {
	DataTunnel() utunnel.DataTunnel
}

// DataTunnelEgress implements session.CarrierTunnel over a DataTunnel.
// Client side: DialContext sends MsgConnect frames and pipes TCP data
// bidirectionally. Node side: ServeEgress sets up the frame handler.
//
// Call DialContext for client-side egress, ServeEgress for node-side.
// The caller must ensure the underlying adapter's Start() has been called
// so the DataTunnel is established.
type DataTunnelEgress struct {
	identity    string
	carrierName string
	epID        string
	adapter     DataTunnelProvider
	proxyURL    string // upstream HTTP proxy URL for node-side egress (empty = direct)

	dt       atomic.Value // utunnel.DataTunnel
	streams  sync.Map     // connID (uint32) -> *dtStream
	targets  sync.Map     // connID (uint32) -> net.Conn
	connects sync.Map     // connID (uint32) -> *dtNodeConnect
	nextID   atomic.Uint32
	onActive func()
	onIdle   func()
	onClosed func()
	active   atomic.Int32
	closed   atomic.Bool
	serveCtx atomic.Value // context.Context (non-nil = node mode)
}

// dtStream represents one TCP stream multiplexed over the DataTunnel.
type dtStream struct {
	local  net.Conn
	remote net.Conn
	ready  chan error
}

// dtNodeConnect retains the result of a node-side CONNECT long enough to make
// client retries idempotent when either the request or its acknowledgement is
// lost by a newly established provider DataChannel.
type dtNodeConnect struct {
	targetAddr string
	done       chan struct{}
	err        error
}

var _ session.CarrierTunnel = (*DataTunnelEgress)(nil)

// NewDataTunnelEgress creates a DataTunnelEgress for the given binding.
func NewDataTunnelEgress(identity string, carrierName string, epID string, adapter DataTunnelProvider) *DataTunnelEgress {
	e := &DataTunnelEgress{
		identity:    identity,
		carrierName: carrierName,
		epID:        epID,
		adapter:     adapter,
	}
	if dt := adapter.DataTunnel(); dt != nil {
		e.dt.Store(dt)
		e.setupFrameHandler()
	}
	return e
}

func (e *DataTunnelEgress) SupportsEndpoint(ep carriers.Endpoint) bool {
	result := ep.Carrier == e.carrierName || ep.ID == e.epID
	log.Printf("[dataegress] SupportsEndpoint carrier=%s epID=%s ep.Carrier=%s ep.ID=%s => %v", e.carrierName, e.epID, ep.Carrier, ep.ID, result)
	return result
}

func (e *DataTunnelEgress) SetOnActive(fn func()) { e.onActive = fn }
func (e *DataTunnelEgress) SetOnIdle(fn func())   { e.onIdle = fn }
func (e *DataTunnelEgress) SetOnClosed(fn func()) { e.onClosed = fn }

// IsAlive returns true if the underlying DataTunnel has not been closed
// and is still available. Used by session liveness monitoring to detect
// dead tunnels (e.g. when the LiveKit signaling WS dies).
func (e *DataTunnelEgress) IsAlive() bool {
	if e.closed.Load() {
		return false
	}
	dt, ok := e.dt.Load().(utunnel.DataTunnel)
	if !ok || dt == nil {
		// Check if adapter has a fresh tunnel
		if fresh := e.adapter.DataTunnel(); fresh != nil {
			e.dt.Store(fresh)
			e.setupFrameHandler()
			return true
		}
		return false
	}
	return true
}

// SetProxyURL configures an upstream HTTP proxy for node-side egress connections.
func (e *DataTunnelEgress) SetProxyURL(proxyURL string) { e.proxyURL = proxyURL }

// dialTarget dials the target address, routing through the upstream HTTP proxy
// (via HTTP CONNECT) when proxyURL is configured.
func (e *DataTunnelEgress) dialTarget(targetAddr string) (net.Conn, error) {
	if e.proxyURL == "" {
		return net.DialTimeout("tcp", targetAddr, 10*time.Second)
	}
	return dialViaHTTPProxy(targetAddr, e.proxyURL, 10*time.Second)
}

// DialContext sends a MsgConnect frame over the DataTunnel and returns a
// net.Conn that pipes TCP data bidirectionally through the tunnel.
func (e *DataTunnelEgress) DialContext(ctx context.Context, ep carriers.Endpoint, targetAddr string) (net.Conn, error) {
	log.Printf("[dataegress] DialContext carrier=%s epID=%s target=%s", e.carrierName, e.epID, targetAddr)
	dt := e.getOrWaitTunnel(ctx)
	if dt == nil {
		log.Printf("[dataegress] DialContext FAILED: tunnel not available carrier=%s", e.carrierName)
		return nil, fmt.Errorf("data-tunnel egress: tunnel not available")
	}
	log.Printf("[dataegress] DialContext got DataTunnel carrier=%s", e.carrierName)

	connID := e.nextID.Add(1)
	local, remote := net.Pipe()

	stream := &dtStream{
		local:  local,
		remote: remote,
		ready:  make(chan error, 1),
	}
	e.streams.Store(connID, stream)

	frame := utunnel.EncodeFrame(connID, utunnel.MsgConnect, []byte(targetAddr))
	dt.SendData(frame)
	retryTicker := time.NewTicker(dtConnectRetry)
	defer retryTicker.Stop()
	connectTimer := time.NewTimer(dtConnectTimeout)
	defer connectTimer.Stop()

	for {
		select {
		case err := <-stream.ready:
			if err != nil {
				e.streams.Delete(connID)
				_ = local.Close()
				_ = remote.Close()
				return nil, err
			}
			log.Printf("[dataegress] DialContext connected connID=%d target=%s", connID, targetAddr)
			e.active.Add(1)
			e.fireOnActive()
			pipeTunnel := e.currentDT()
			if pipeTunnel == nil {
				pipeTunnel = dt
			}
			go e.pipeToTunnel(connID, remote, pipeTunnel)
			return local, nil
		case <-retryTicker.C:
			// A provider may report its DataChannel ready just before the remote
			// message handler is attached. Retrying the same connID lets the node
			// deduplicate the request and resend its acknowledgement safely.
			if current := e.currentDT(); current != nil {
				current.SendData(frame)
			}
		case <-ctx.Done():
			e.streams.Delete(connID)
			_ = local.Close()
			_ = remote.Close()
			return nil, ctx.Err()
		case <-connectTimer.C:
			e.streams.Delete(connID)
			_ = local.Close()
			_ = remote.Close()
			return nil, fmt.Errorf("data-tunnel egress: connect timeout for %s", targetAddr)
		}
	}
}

// pipeToTunnel reads from the pipe remote end and sends MsgData frames
// over the DataTunnel (client -> node direction).
func (e *DataTunnelEgress) pipeToTunnel(connID uint32, remote net.Conn, dt utunnel.DataTunnel) {
	defer func() {
		_ = remote.Close()
	}()
	log.Printf("[dataegress] pipeToTunnel started connID=%d", connID)
	buf := make([]byte, dtReadBufSize)
	for {
		n, err := remote.Read(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				frame := utunnel.EncodeFrame(connID, utunnel.MsgClose, nil)
				dt.SendData(frame)
			}
			e.cleanupStream(connID)
			return
		}
		if n == 0 {
			continue
		}
		chunk := make([]byte, n)
		copy(chunk, buf[:n])
		log.Printf("[dataegress] pipeToTunnel send connID=%d bytes=%d", connID, len(chunk))
		frame := utunnel.EncodeFrame(connID, utunnel.MsgData, chunk)
		dt.SendData(frame)
	}
}

// ServeEgress starts the node-side egress handler. Call in a goroutine
// when the daemon role is node. Blocks until ctx is cancelled.
// It continuously monitors the adapter for tunnel changes (e.g. when a
// new WBStream session replaces a stale one) and re-registers the frame
// handler accordingly.
func (e *DataTunnelEgress) ServeEgress(ctx context.Context, bindings map[string]policy.CarrierBinding) error {
	e.serveCtx.Store(ctx)
	e.setupFrameHandler()
	dt := e.getOrWaitTunnel(ctx)
	if dt == nil {
		return fmt.Errorf("data-tunnel egress: serve called but no tunnel available")
	}
	e.setupFrameHandler()

	// Watch for tunnel changes: if a new WBStream session creates a new
	// DataTunnel (e.g. stale session offer consumed the first OnConnected),
	// we must re-register the frame handler on the fresh tunnel.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if fresh := e.adapter.DataTunnel(); fresh != nil {
				if cached, _ := e.dt.Load().(utunnel.DataTunnel); cached != fresh {
					log.Printf("[dataegress] ServeEgress: tunnel changed, refreshing carrier=%s", e.carrierName)
					e.dt.Store(fresh)
					e.setupFrameHandler()
				}
			}
		}
	}
}

// DataTunnelAdapter is an optional interface that adapters can implement to
// receive SetOnData calls that persist across tunnel reconnections.
type DataTunnelAdapter interface {
	SetOnData(fn func([]byte))
}

// DataTunnelCloseAdapter is implemented by adapters that preserve close
// callbacks across DataTunnel reconnects.
type DataTunnelCloseAdapter interface {
	SetOnClose(fn func())
}

// setupFrameHandler sets the SetOnData callback via the adapter (preferred)
// so it persists when the adapter creates a new DataTunnel on reconnection.
// Falls back to setting directly on the current tunnel if the adapter does
// not implement DataTunnelAdapter.
func (e *DataTunnelEgress) setupFrameHandler() {
	frameHandler := func(data []byte) {
		utunnel.DecodeFrames(data, func(connID uint32, msgType byte, payload []byte) {
			e.dispatchFrame(connID, msgType, payload)
		})
	}
	closeHandler := func() {
		log.Printf("[dataegress] DataTunnel closed carrier=%s", e.carrierName)
		e.closed.Store(true)
		e.fireOnClosed()
	}
	registeredData := false
	if a, ok := e.adapter.(DataTunnelAdapter); ok {
		a.SetOnData(frameHandler)
		log.Printf("[dataegress] setupFrameHandler: registered via adapter.SetOnData (persists across reconnects)")
		registeredData = true
	}
	registeredClose := false
	if a, ok := e.adapter.(DataTunnelCloseAdapter); ok {
		a.SetOnClose(closeHandler)
		registeredClose = true
	}
	dt, ok := e.dt.Load().(utunnel.DataTunnel)
	if !ok || dt == nil {
		return
	}
	if !registeredData {
		dt.SetOnData(frameHandler)
	}
	if !registeredClose {
		dt.SetOnClose(closeHandler)
	}
}

func (e *DataTunnelEgress) dispatchFrame(connID uint32, msgType byte, payload []byte) {
	switch msgType {
	case utunnel.MsgConnect:
		e.handleConnect(connID, payload)
	case utunnel.MsgConnectOK:
		e.handleConnectOK(connID)
	case utunnel.MsgConnectErr:
		e.handleConnectErr(connID, payload)
	case utunnel.MsgData:
		e.handleData(connID, payload)
	case utunnel.MsgClose:
		e.handleClose(connID)
	}
}

// handleConnect is the node-side handler for incoming MsgConnect frames.
func (e *DataTunnelEgress) handleConnect(connID uint32, payload []byte) {
	ctx, _ := e.serveCtx.Load().(context.Context)
	if ctx == nil {
		log.Printf("[dataegress] MsgConnect dropped: not in node mode id=%d", connID)
		return
	}
	targetAddr := string(payload)
	candidate := &dtNodeConnect{targetAddr: targetAddr, done: make(chan struct{})}
	loaded, duplicate := e.connects.LoadOrStore(connID, candidate)
	connect := loaded.(*dtNodeConnect)
	if duplicate {
		if connect.targetAddr != targetAddr {
			e.sendConnectResult(connID, fmt.Errorf("duplicate connection ID for a different target"))
			return
		}
		select {
		case <-connect.done:
			e.sendConnectResult(connID, connect.err)
		default:
			log.Printf("[dataegress] duplicate MsgConnect pending id=%d", connID)
		}
		return
	}

	log.Printf("[dataegress] node connect id=%d target=%s proxy=%s", connID, targetAddr, e.proxyURL)

	target, err := e.dialTarget(targetAddr)
	connect.err = err
	close(connect.done)
	e.retainConnectResult(connID, connect, dtConnectTimeout)
	if err != nil {
		log.Printf("[dataegress] node connect failed id=%d target=%s err=%v", connID, targetAddr, err)
		e.sendConnectResult(connID, err)
		return
	}

	e.targets.Store(connID, target)
	e.sendConnectResult(connID, nil)

	go e.nodeTargetReader(ctx, connID, target)
}

func (e *DataTunnelEgress) retainConnectResult(connID uint32, connect *dtNodeConnect, retention time.Duration) {
	// Socket lifetime and handshake lifetime are intentionally independent. If
	// the target closes after CONNECT_OK is lost, retries must still receive the
	// original result instead of opening a second target connection.
	time.AfterFunc(retention, func() {
		e.connects.CompareAndDelete(connID, connect)
	})
}

func (e *DataTunnelEgress) sendConnectResult(connID uint32, err error) {
	dt := e.currentDT()
	if dt == nil {
		return
	}
	if err != nil {
		dt.SendData(utunnel.EncodeFrame(connID, utunnel.MsgConnectErr, []byte(err.Error())))
		return
	}
	dt.SendData(utunnel.EncodeFrame(connID, utunnel.MsgConnectOK, nil))
}

func (e *DataTunnelEgress) nodeTargetReader(ctx context.Context, connID uint32, target net.Conn) {
	defer e.cleanupTarget(connID)
	buf := make([]byte, dtReadBufSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := target.SetReadDeadline(time.Now().Add(dtKeepaliveInterval)); err != nil {
			return
		}
		n, err := target.Read(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				dt := e.currentDT()
				if dt != nil {
					frame := utunnel.EncodeFrame(connID, utunnel.MsgClose, nil)
					dt.SendData(frame)
				}
			}
			return
		}
		if n == 0 {
			continue
		}

		chunk := make([]byte, n)
		copy(chunk, buf[:n])
		dt := e.currentDT()
		if dt != nil {
			frame := utunnel.EncodeFrame(connID, utunnel.MsgData, chunk)
			dt.SendData(frame)
		}
	}
}

func (e *DataTunnelEgress) handleConnectOK(connID uint32) {
	log.Printf("[dataegress] handleConnectOK connID=%d", connID)
	v, ok := e.streams.Load(connID)
	if !ok {
		return
	}
	stream := v.(*dtStream)
	select {
	case stream.ready <- nil:
	default:
	}
}

func (e *DataTunnelEgress) handleConnectErr(connID uint32, payload []byte) {
	v, ok := e.streams.Load(connID)
	if !ok {
		return
	}
	stream := v.(*dtStream)
	err := fmt.Errorf("data-tunnel egress: connect rejected: %s", string(payload))
	select {
	case stream.ready <- err:
	default:
	}
}

func (e *DataTunnelEgress) handleData(connID uint32, payload []byte) {
	log.Printf("[dataegress] handleData connID=%d bytes=%d", connID, len(payload))
	if v, ok := e.streams.Load(connID); ok {
		stream := v.(*dtStream)
		if _, err := stream.remote.Write(payload); err != nil {
			log.Printf("[dataegress] handleData write remote failed connID=%d err=%v", connID, err)
			e.cleanupStream(connID)
		} else {
			log.Printf("[dataegress] handleData wrote remote connID=%d bytes=%d", connID, len(payload))
		}
		return
	}

	if v, ok := e.targets.Load(connID); ok {
		target := v.(net.Conn)
		if _, err := target.Write(payload); err != nil {
			log.Printf("[dataegress] handleData write target failed connID=%d err=%v", connID, err)
			e.cleanupTarget(connID)
		}
	}
}

func (e *DataTunnelEgress) handleClose(connID uint32) {
	e.cleanupStream(connID)
	e.cleanupTarget(connID)
}

func (e *DataTunnelEgress) cleanupStream(connID uint32) {
	v, ok := e.streams.LoadAndDelete(connID)
	if !ok {
		return
	}
	stream := v.(*dtStream)
	_ = stream.local.Close()
	_ = stream.remote.Close()
	if e.active.Add(-1) <= 0 {
		e.fireOnIdle()
	}
}

func (e *DataTunnelEgress) cleanupTarget(connID uint32) {
	v, ok := e.targets.LoadAndDelete(connID)
	if !ok {
		return
	}
	target := v.(net.Conn)
	_ = target.Close()
}

func (e *DataTunnelEgress) fireOnActive() {
	if fn := e.onActive; fn != nil {
		go fn()
	}
}

func (e *DataTunnelEgress) fireOnIdle() {
	if fn := e.onIdle; fn != nil {
		go fn()
	}
}

func (e *DataTunnelEgress) fireOnClosed() {
	if fn := e.onClosed; fn != nil {
		go fn()
	}
}

// currentDT returns the freshest DataTunnel available: it checks the adapter
// first (which may have reconnected) and refreshes the cache if the tunnel
// changed. This is used by node-side response paths (handleConnect,
// nodeTargetReader) that must always send on the live tunnel.
func (e *DataTunnelEgress) currentDT() utunnel.DataTunnel {
	if fresh := e.adapter.DataTunnel(); fresh != nil {
		if cached, _ := e.dt.Load().(utunnel.DataTunnel); cached != fresh {
			e.dt.Store(fresh)
		}
		return fresh
	}
	dt, _ := e.dt.Load().(utunnel.DataTunnel)
	return dt
}

// getOrWaitTunnel waits for the DataTunnel to become available.
// If the adapter's DataTunnel changes (e.g. new WBStream room after reconnect),
// the cached reference is updated and the frame handler is re-installed.
func (e *DataTunnelEgress) getOrWaitTunnel(ctx context.Context) utunnel.DataTunnel {
	if dt, ok := e.dt.Load().(utunnel.DataTunnel); ok && dt != nil {
		// Always check if adapter now has a different tunnel.
		if fresh := e.adapter.DataTunnel(); fresh != nil && fresh != dt {
			log.Printf("[dataegress] getOrWaitTunnel: DataTunnel changed, refreshing carrier=%s", e.carrierName)
			e.dt.Store(fresh)
			e.setupFrameHandler()
			return fresh
		}
		log.Printf("[dataegress] getOrWaitTunnel: cached DataTunnel carrier=%s", e.carrierName)
		return dt
	}
	log.Printf("[dataegress] getOrWaitTunnel: waiting for DataTunnel carrier=%s", e.carrierName)
	ticker := time.NewTicker(dtTunnelWaitInterval)
	defer ticker.Stop()
	for {
		dt := e.adapter.DataTunnel()
		if dt != nil {
			log.Printf("[dataegress] getOrWaitTunnel: got DataTunnel from adapter carrier=%s", e.carrierName)
			e.dt.Store(dt)
			e.setupFrameHandler()
			return dt
		}
		select {
		case <-ctx.Done():
			log.Printf("[dataegress] getOrWaitTunnel: context done carrier=%s", e.carrierName)
			return nil
		case <-ticker.C:
			log.Printf("[dataegress] getOrWaitTunnel: still waiting carrier=%s", e.carrierName)
		}
	}
}

// getDataTunnelAdapter extracts a DataTunnelProvider from the first matching
// binding. Returns the adapter, carrier name, endpoint ID, and whether found.
func getDataTunnelAdapter(bindings map[string]policy.CarrierBinding) (DataTunnelProvider, string, string, bool) {
	for carrierName, binding := range bindings {
		if pc, ok := binding.Carrier.(*carriers.ProviderCarrier); ok {
			if dtp, ok := pc.GetProvider().(DataTunnelProvider); ok {
				return dtp, carrierName, binding.Endpoint.ID, true
			}
		}
	}
	return nil, "", "", false
}

// HasDataTunnelBinding checks whether any binding provides a DataTunnel.
func HasDataTunnelBinding(bindings map[string]policy.CarrierBinding) bool {
	_, _, _, ok := getDataTunnelAdapter(bindings)
	return ok
}

// dialViaHTTPProxy connects to targetAddr through an HTTP CONNECT proxy.
func dialViaHTTPProxy(targetAddr, proxyURL string, timeout time.Duration) (net.Conn, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}
	proxyAddr := u.Host
	if u.Port() == "" {
		proxyAddr = u.Hostname() + ":8080"
	}

	conn, err := net.DialTimeout("tcp", proxyAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxyAddr, err)
	}

	connectReq := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: targetAddr},
		Host:   targetAddr,
		Header: make(http.Header),
	}
	if u.User != nil {
		connectReq.Header.Set("Proxy-Authorization", "Basic "+basicAuth(u.User))
	}
	if err := connectReq.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("CONNECT proxy returned %d", resp.StatusCode)
	}
	return conn, nil
}

func basicAuth(u *url.Userinfo) string {
	pw, _ := u.Password()
	return base64.StdEncoding.EncodeToString([]byte(u.Username() + ":" + pw))
}

var _ interface {
	ServeEgress(context.Context, map[string]policy.CarrierBinding) error
	SetOnActive(func())
	SetOnIdle(func())
} = (*DataTunnelEgress)(nil)
