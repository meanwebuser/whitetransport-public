package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/punch"
)

// TCPSignaling moves punch offers/answers over one length-prefixed TCP
// connection: 1 byte kind ('o' offer / 'a' answer), 4-byte big-endian
// length, then an AES-256-GCM sealed fabric envelope carrying the JSON
// payload (the offer contains the punch session key, so the shared token
// seals the channel exactly like the fabric mailbox path). It exists so
// the two-machine canary can signal across the network without a shared
// file.mailbox directory; in the product this role belongs to fabric
// envelopes over a mailbox carrier.
type TCPSignaling struct {
	conn   net.Conn
	cipher *fabric.EnvelopeCipher
	mu     sync.Mutex
}

func newTCPSignaling(conn net.Conn, token string) (*TCPSignaling, error) {
	cipher, err := fabric.NewSessionCipher(fabric.DeriveBootstrapKey(token))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("tcp signal cipher: %w", err)
	}
	return &TCPSignaling{conn: conn, cipher: cipher}, nil
}

func tcpDialSignaling(ctx context.Context, addr, token string) (*TCPSignaling, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcp signal dial %s: %w", addr, err)
	}
	return newTCPSignaling(conn, token)
}

func tcpListenSignaling(ctx context.Context, addr, token string) (*TCPSignaling, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcp signal listen %s: %w", addr, err)
	}
	defer ln.Close()
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan acceptResult, 1)
	go func() {
		conn, err := ln.Accept()
		ch <- acceptResult{conn, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("tcp signal accept: %w", r.err)
		}
		return newTCPSignaling(r.conn, token)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *TCPSignaling) Close() error { return t.conn.Close() }

func randomSignalID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "punch-tcp-" + hex.EncodeToString(b)
}

func (t *TCPSignaling) writeMsg(kind byte, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sealed, err := t.cipher.Seal(fabric.NewEnvelope(randomSignalID(), fabric.TrafficBootstrap, "punch.tcp", body))
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	buf := make([]byte, 5, 5+len(sealed))
	buf[0] = kind
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(sealed)))
	buf = append(buf, sealed...)
	if err := t.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	_, err = t.conn.Write(buf)
	return err
}

// readMsg returns the opened JSON payload of the next message.
func (t *TCPSignaling) readMsg(ctx context.Context) (byte, []byte, error) {
	for {
		if err := t.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return 0, nil, err
		}
		var hdr [5]byte
		if _, err := io.ReadFull(t.conn, hdr[:]); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-ctx.Done():
					return 0, nil, ctx.Err()
				default:
					continue
				}
			}
			return 0, nil, err
		}
		n := binary.BigEndian.Uint32(hdr[1:5])
		if n > 1<<20 {
			return 0, nil, fmt.Errorf("tcp signal: oversized message %d", n)
		}
		sealed := make([]byte, n)
		if _, err := io.ReadFull(t.conn, sealed); err != nil {
			return 0, nil, err
		}
		inner, err := t.cipher.Open(sealed)
		if err != nil {
			return 0, nil, fmt.Errorf("tcp signal: open: %w", err)
		}
		return hdr[0], inner.Payload, nil
	}
}

func (t *TCPSignaling) SendOffer(ctx context.Context, offer punch.Offer) error {
	return t.writeMsg('o', offer)
}

func (t *TCPSignaling) SendAnswer(ctx context.Context, answer punch.Answer) error {
	return t.writeMsg('a', answer)
}

func (t *TCPSignaling) RecvOffer(ctx context.Context) (punch.Offer, error) {
	for {
		kind, body, err := t.readMsg(ctx)
		if err != nil {
			return punch.Offer{}, err
		}
		if kind != 'o' {
			continue
		}
		var offer punch.Offer
		err = json.Unmarshal(body, &offer)
		return offer, err
	}
}

func (t *TCPSignaling) RecvAnswer(ctx context.Context, sessionID []byte) (punch.Answer, error) {
	for {
		kind, body, err := t.readMsg(ctx)
		if err != nil {
			return punch.Answer{}, err
		}
		if kind != 'a' {
			continue
		}
		var answer punch.Answer
		if err := json.Unmarshal(body, &answer); err != nil {
			return punch.Answer{}, err
		}
		if sessionID == nil || string(answer.SessionID) == string(sessionID) {
			return answer, nil
		}
	}
}
