package punch

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// memSignaling moves offers/answers through channels; answer filtering by
// session id mirrors the fabric signaling contract.
type memSignaling struct {
	offerCh  chan Offer
	answerCh chan Answer
}

func newMemSignaling() *memSignaling {
	return &memSignaling{offerCh: make(chan Offer, 1), answerCh: make(chan Answer, 4)}
}

func (m *memSignaling) SendOffer(ctx context.Context, o Offer) error {
	select {
	case m.offerCh <- o:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *memSignaling) RecvOffer(ctx context.Context) (Offer, error) {
	select {
	case o := <-m.offerCh:
		return o, nil
	case <-ctx.Done():
		return Offer{}, ctx.Err()
	}
}

func (m *memSignaling) SendAnswer(ctx context.Context, a Answer) error {
	select {
	case m.answerCh <- a:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *memSignaling) RecvAnswer(ctx context.Context, sessionID []byte) (Answer, error) {
	for {
		select {
		case a := <-m.answerCh:
			if bytes.Equal(a.SessionID, sessionID) {
				return a, nil
			}
		case <-ctx.Done():
			return Answer{}, ctx.Err()
		}
	}
}

func offlineTestConfig() Config {
	// Empty non-nil STUNServers explicitly disables STUN: loopback host
	// candidates only, no network dependency.
	return Config{
		STUNServers:   []string{},
		PunchTimeout:  5 * time.Second,
		ProbeInterval: 50 * time.Millisecond,
		Keepalive:     time.Second,
		Log:           func(string, ...any) {},
	}
}

func TestPunchLoopbackBidirectionalData(t *testing.T) {
	sig := newMemSignaling()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	answerDone := make(chan *Session, 1)
	answerErr := make(chan error, 1)
	go func() {
		s, err := Respond(ctx, offlineTestConfig(), sig)
		if err != nil {
			answerErr <- err
			return
		}
		answerDone <- s
	}()

	offerSide, err := Initiate(ctx, offlineTestConfig(), sig)
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	defer offerSide.Close()

	select {
	case s := <-answerDone:
		defer s.Close()
		if s.RemoteAddr() == "" {
			t.Fatal("answerer remote not established")
		}
		// Offerer -> answerer.
		if err := offerSide.Send([]byte("ping")); err != nil {
			t.Fatalf("send: %v", err)
		}
		got, err := s.Recv()
		if err != nil {
			t.Fatalf("answerer recv: %v", err)
		}
		if string(got) != "ping" {
			t.Fatalf("answerer got %q", got)
		}
		// Answerer -> offerer.
		if err := s.Send([]byte("pong")); err != nil {
			t.Fatalf("answerer send: %v", err)
		}
		got, err = offerSide.Recv()
		if err != nil {
			t.Fatalf("offerer recv: %v", err)
		}
		if string(got) != "pong" {
			t.Fatalf("offerer got %q", got)
		}
	case err := <-answerErr:
		t.Fatalf("Respond: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("answer side timed out")
	}
}

func TestPunchFailsWithoutCandidates(t *testing.T) {
	sig := newMemSignaling()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		offer, err := sig.RecvOffer(ctx)
		if err != nil {
			return
		}
		// Advertise no candidates: the punch must fail cleanly.
		_ = sig.SendAnswer(ctx, Answer{SessionID: offer.SessionID, CreatedAt: time.Now().UTC()})
	}()

	if _, err := Initiate(ctx, offlineTestConfig(), sig); err == nil {
		t.Fatal("expected punch failure with zero remote candidates")
	}
}

func TestOpenFrameRejectsTampering(t *testing.T) {
	key := bytes.Repeat([]byte{7}, keyLen)
	sessionID := bytes.Repeat([]byte{9}, sessionIDLen)
	dg := frame{FrameType: frameData, SessionID: sessionID, Seq: 42, Payload: []byte("hello")}.seal(key)

	got, err := openFrame(dg, key)
	if err != nil {
		t.Fatalf("open untampered: %v", err)
	}
	if got.Seq != 42 || string(got.Payload) != "hello" {
		t.Fatalf("decoded frame mismatch: %+v", got)
	}

	// Flip one payload byte.
	tampered := append([]byte(nil), dg...)
	tampered[headerLen] ^= 0xFF
	if _, err := openFrame(tampered, key); err == nil {
		t.Fatal("tampered frame accepted")
	}

	// Wrong key.
	wrongKey := bytes.Repeat([]byte{8}, keyLen)
	if _, err := openFrame(dg, wrongKey); err == nil {
		t.Fatal("frame accepted with wrong key")
	}

	// Truncated.
	if _, err := openFrame(dg[:len(dg)-5], key); err == nil {
		t.Fatal("truncated frame accepted")
	}

	// Bad magic.
	badMagic := append([]byte(nil), dg...)
	badMagic[0] = 'X'
	if _, err := openFrame(badMagic, key); err == nil {
		t.Fatal("bad magic accepted")
	}
}

func TestMaxPayloadBound(t *testing.T) {
	if MaxPayload <= 0 || MaxDatagram != 1280 {
		t.Fatalf("unexpected bounds: MaxDatagram=%d MaxPayload=%d", MaxDatagram, MaxPayload)
	}
}
