package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	dnsStreamQueryTimeout = 10 * time.Second
	dnsStreamResolver     = "1.1.1.1:53"
)

type dnsStreamResponse struct {
	payload []byte
	source  net.Addr
}

// dnsStreamPacketConn preserves the SOCKS UDP interface expected by tun2socks
// while carrying DNS requests over a carrier-backed TCP stream.
type dnsStreamPacketConn struct {
	ctx       context.Context
	dial      func(context.Context, string) (net.Conn, string, error)
	responses chan dnsStreamResponse
	done      chan struct{}
	closeOnce sync.Once

	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

func newDNSOverStreamPacketConn(ctx context.Context, dial func(context.Context, string) (net.Conn, string, error)) net.PacketConn {
	return &dnsStreamPacketConn{
		ctx:       ctx,
		dial:      dial,
		responses: make(chan dnsStreamResponse, 16),
		done:      make(chan struct{}),
	}
}

func (conn *dnsStreamPacketConn) WriteTo(payload []byte, destination net.Addr) (int, error) {
	_, err := dnsStreamTarget(destination)
	if err != nil {
		return 0, err
	}
	queryCtx, cancel := context.WithTimeout(conn.ctx, dnsStreamQueryTimeout)
	defer cancel()
	if deadline := conn.currentWriteDeadline(); !deadline.IsZero() {
		var deadlineCancel context.CancelFunc
		queryCtx, deadlineCancel = context.WithDeadline(queryCtx, deadline)
		defer deadlineCancel()
	}
	upstream, _, err := conn.dial(queryCtx, dnsStreamResolver)
	if err != nil {
		return 0, err
	}
	defer upstream.Close()
	if deadline, ok := queryCtx.Deadline(); ok {
		_ = upstream.SetDeadline(deadline)
	}
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(payload)))
	frame = append(frame, payload...)
	if _, err := upstream.Write(frame); err != nil {
		return 0, err
	}
	var size [2]byte
	if _, err := io.ReadFull(upstream, size[:]); err != nil {
		return 0, err
	}
	responseSize := int(binary.BigEndian.Uint16(size[:]))
	if responseSize == 0 || responseSize > maxCarrierUDPDatagramSize {
		return 0, fmt.Errorf("DNS-over-stream response size %d is invalid", responseSize)
	}
	response := make([]byte, responseSize)
	if _, err := io.ReadFull(upstream, response); err != nil {
		return 0, err
	}
	select {
	case conn.responses <- dnsStreamResponse{payload: response, source: destination}:
		return len(payload), nil
	case <-conn.done:
		return 0, net.ErrClosed
	case <-queryCtx.Done():
		return 0, queryCtx.Err()
	}
}

func (conn *dnsStreamPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	deadline := conn.currentReadDeadline()
	var timer *time.Timer
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		timer = time.NewTimer(time.Until(deadline))
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case response := <-conn.responses:
		return copy(buffer, response.payload), response.source, nil
	case <-conn.done:
		return 0, nil, net.ErrClosed
	case <-conn.ctx.Done():
		return 0, nil, conn.ctx.Err()
	case <-timeout:
		return 0, nil, dnsStreamTimeoutError{}
	}
}

func (conn *dnsStreamPacketConn) Close() error {
	conn.closeOnce.Do(func() { close(conn.done) })
	return nil
}

func (conn *dnsStreamPacketConn) LocalAddr() net.Addr {
	return packetAddress{network: "udp", address: "dns-over-stream"}
}

func (conn *dnsStreamPacketConn) SetDeadline(deadline time.Time) error {
	conn.mu.Lock()
	conn.readDeadline = deadline
	conn.writeDeadline = deadline
	conn.mu.Unlock()
	return nil
}

func (conn *dnsStreamPacketConn) SetReadDeadline(deadline time.Time) error {
	conn.mu.Lock()
	conn.readDeadline = deadline
	conn.mu.Unlock()
	return nil
}

func (conn *dnsStreamPacketConn) SetWriteDeadline(deadline time.Time) error {
	conn.mu.Lock()
	conn.writeDeadline = deadline
	conn.mu.Unlock()
	return nil
}

func (conn *dnsStreamPacketConn) currentReadDeadline() time.Time {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.readDeadline
}

func (conn *dnsStreamPacketConn) currentWriteDeadline() time.Time {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.writeDeadline
}

func dnsStreamTarget(destination net.Addr) (string, error) {
	if destination == nil {
		return "", errors.New("DNS-over-stream destination is missing")
	}
	target := destination.String()
	_, portText, err := net.SplitHostPort(target)
	if err != nil {
		return "", fmt.Errorf("split DNS-over-stream destination %q: %w", target, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port != 53 {
		return "", fmt.Errorf("DNS-over-stream rejects non-DNS destination %q", target)
	}
	return target, nil
}

type dnsStreamTimeoutError struct{}

func (dnsStreamTimeoutError) Error() string   { return "DNS-over-stream timeout" }
func (dnsStreamTimeoutError) Timeout() bool   { return true }
func (dnsStreamTimeoutError) Temporary() bool { return true }
