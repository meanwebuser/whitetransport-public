package tunnel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	streamPayloadConnect = "stream.connect"
	streamPayloadData    = "stream.data"
	streamPayloadClose   = "stream.close"
)

// EnvelopeStreamConn wraps a carrier's Write/Read into a net.Conn.
// It implements TCP-over-envelope: outgoing bytes are chunked and sent as
// stream.data envelopes; incoming envelopes are buffered for Read().
type EnvelopeStreamConn struct {
	localID   string
	remoteID  string
	carrier   carriers.Carrier
	endpoint  carriers.Endpoint
	writeFunc func(context.Context, carriers.Endpoint, fabric.Envelope) error
	readFunc  func(context.Context, carriers.Endpoint, carriers.Cursor) (carriers.ReadResult, error)
	cursor    carriers.Cursor

	mu       sync.Mutex
	readBuf  []byte
	cond     *sync.Cond
	closed   bool
	closeErr error
}

func NewEnvelopeStreamConn(
	localID string,
	remoteID string,
	carrier carriers.Carrier,
	endpoint carriers.Endpoint,
) *EnvelopeStreamConn {
	c := &EnvelopeStreamConn{
		localID:  localID,
		remoteID: remoteID,
		carrier:  carrier,
		endpoint: endpoint,
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// PushData feeds incoming stream.data envelopes into the read buffer.
// Called by the poll loop when it receives data for this stream.
func (c *EnvelopeStreamConn) PushData(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.readBuf = append(c.readBuf, data...)
	c.cond.Broadcast()
}

// PushClose signals EOF/error to the reader.
func (c *EnvelopeStreamConn) PushClose(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if err != nil {
		c.closeErr = err
	}
	c.cond.Broadcast()
}

func (c *EnvelopeStreamConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for len(c.readBuf) == 0 && !c.closed {
		c.cond.Wait()
	}
	if len(c.readBuf) == 0 {
		if c.closed && c.closeErr != nil {
			return 0, c.closeErr
		}
		return 0, io.EOF
	}
	n := copy(b, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *EnvelopeStreamConn) Write(b []byte) (int, error) {
	if c.closed {
		return 0, fmt.Errorf("stream closed")
	}
	total := 0
	for len(b) > 0 {
		chunkSize := 2048
		if chunkSize > len(b) {
			chunkSize = len(b)
		}
		chunk := b[:chunkSize]
		env := fabric.Envelope{
			Version:      fabric.CurrentVersion,
			ID:           fmt.Sprintf("%s-data-%d", c.localID, time.Now().UnixNano()),
			SessionID:    c.remoteID,
			Source:       c.localID,
			TrafficClass: fabric.TrafficEgress,
			PayloadType:  streamPayloadData,
			CreatedAt:    time.Now().UTC(),
			Payload:      chunk,
		}
		if err := c.carrier.Write(context.Background(), c.endpoint, env); err != nil {
			return total, err
		}
		total += chunkSize
		b = b[chunkSize:]
	}
	return total, nil
}

func (c *EnvelopeStreamConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()

	closeEnv := fabric.Envelope{
		Version:      fabric.CurrentVersion,
		ID:           fmt.Sprintf("%s-close", c.localID),
		SessionID:    c.remoteID,
		Source:       c.localID,
		TrafficClass: fabric.TrafficEgress,
		PayloadType:  streamPayloadClose,
		CreatedAt:    time.Now().UTC(),
	}
	_ = c.carrier.Write(context.Background(), c.endpoint, closeEnv)
	return nil
}

func (c *EnvelopeStreamConn) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (c *EnvelopeStreamConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }
func (c *EnvelopeStreamConn) SetDeadline(t time.Time) error      { return nil }
func (c *EnvelopeStreamConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *EnvelopeStreamConn) SetWriteDeadline(t time.Time) error { return nil }

// StreamSessionManager manages active envelope-stream connections on both
// client and node sides. On the node side it handles stream.connect requests
// by dialing real TCP targets and bridging data.
type StreamSessionManager struct {
	mu        sync.Mutex
	streams   map[string]*EnvelopeStreamConn
	identity  string
}

func NewStreamSessionManager(identity string) *StreamSessionManager {
	return &StreamSessionManager{
		streams:  make(map[string]*EnvelopeStreamConn),
		identity: identity,
	}
}

// HandleEnvelope processes an incoming envelope on the node side.
// stream.connect → dial TCP and start bridging.
// stream.data → push to the matching conn.
// stream.close → close the matching conn.
func (m *StreamSessionManager) HandleEnvelope(ctx context.Context, env fabric.Envelope, carrier carriers.Carrier, endpoint carriers.Endpoint) {
	switch env.PayloadType {
	case streamPayloadConnect:
		targetAddr := string(env.Payload)
		go m.handleConnect(ctx, env, carrier, endpoint, targetAddr)

	case streamPayloadData:
		m.mu.Lock()
		conn, ok := m.streams[env.Source]
		m.mu.Unlock()
		if ok {
			conn.PushData(env.Payload)
		}

	case streamPayloadClose:
		m.mu.Lock()
		conn, ok := m.streams[env.Source]
		m.mu.Unlock()
		if ok {
			conn.PushClose(nil)
		}
	}
}

func (m *StreamSessionManager) handleConnect(ctx context.Context, env fabric.Envelope, carrier carriers.Carrier, endpoint carriers.Endpoint, targetAddr string) {
	connID := env.ID
	remoteID := env.Source

	upstream, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		errEnv := fabric.Envelope{
			Version:      fabric.CurrentVersion,
			ID:           fmt.Sprintf("%s-close", connID),
			SessionID:    remoteID,
			Source:       connID,
			TrafficClass: fabric.TrafficEgress,
			PayloadType:  streamPayloadClose,
			CreatedAt:    time.Now().UTC(),
			Payload:      []byte(err.Error()),
		}
		_ = carrier.Write(ctx, endpoint, errEnv)
		return
	}

	streamConn := NewEnvelopeStreamConn(connID, remoteID, carrier, endpoint)
	m.mu.Lock()
	m.streams[remoteID] = streamConn
	m.mu.Unlock()

	go func() {
		defer func() {
			upstream.Close()
			m.mu.Lock()
			delete(m.streams, remoteID)
			m.mu.Unlock()
		}()

		buf := make([]byte, 2048)
		for {
			n, err := upstream.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				streamConn.PushData(data)
			}
			if err != nil {
				streamConn.PushClose(err)
				return
			}
		}
	}()

	go func() {
		pipeBuf := make([]byte, 2048)
		for {
			n, err := streamConn.Read(pipeBuf)
			if n > 0 {
				_, werr := upstream.Write(pipeBuf[:n])
				if werr != nil {
					upstream.Close()
					return
				}
			}
			if err != nil {
				upstream.Close()
				return
			}
		}
	}()
}

// DialStream dials a TCP target through an envelope carrier.
// Used on the client side: sends stream.connect, waits for data back.
func (m *StreamSessionManager) DialStream(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, targetAddr string) (net.Conn, error) {
	streamID := fmt.Sprintf("%s-stream-%d", m.identity, time.Now().UnixNano())
	conn := NewEnvelopeStreamConn(streamID, streamID, carrier, endpoint)

	m.mu.Lock()
	m.streams[streamID] = conn
	m.mu.Unlock()

	connectEnv := fabric.Envelope{
		Version:      fabric.CurrentVersion,
		ID:           streamID,
		SessionID:    streamID,
		Source:       streamID,
		TrafficClass: fabric.TrafficControl,
		PayloadType:  streamPayloadConnect,
		CreatedAt:    time.Now().UTC(),
		Payload:      []byte(targetAddr),
	}
	if err := carrier.Write(ctx, endpoint, connectEnv); err != nil {
		m.mu.Lock()
		delete(m.streams, streamID)
		m.mu.Unlock()
		return nil, fmt.Errorf("envelope stream connect: %w", err)
	}

	return conn, nil
}

// PollAndDispatch reads envelopes from the carrier and dispatches stream data
// to active connections. Should run in a goroutine.
func (m *StreamSessionManager) PollAndDispatch(ctx context.Context, carrier carriers.Carrier, endpoint carriers.Endpoint, cursor carriers.Cursor) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		result, err := carrier.Read(ctx, endpoint, cursor)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		cursor = result.Cursor
		for _, env := range result.Envelopes {
			m.HandleEnvelope(ctx, env, carrier, endpoint)
		}
	}
}

var _ = binary.BigEndian
