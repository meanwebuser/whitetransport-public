package punch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

func TestFabricSignalingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	shared, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: dir})
	if err != nil {
		t.Fatalf("filemailbox: %v", err)
	}
	offerSide, err := NewFabricSignaling(shared, "pair-token", "nodeA", "nodeB", nil)
	if err != nil {
		t.Fatalf("offer signaling: %v", err)
	}
	answerSide, err := NewFabricSignaling(shared, "pair-token", "nodeB", "nodeA", nil)
	if err != nil {
		t.Fatalf("answer signaling: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	want := Offer{
		SessionID:  []byte("0123456789abcdef"),
		Key:        []byte("k"),
		Candidates: []Candidate{{Addr: "192.0.2.10:40000", Typ: "srflx"}},
		CreatedAt:  time.Now().UTC(),
	}

	gotOffer := make(chan Offer, 1)
	go func() {
		o, err := answerSide.RecvOffer(ctx)
		if err == nil {
			gotOffer <- o
		}
	}()
	if err := offerSide.SendOffer(ctx, want); err != nil {
		t.Fatalf("send offer: %v", err)
	}
	select {
	case o := <-gotOffer:
		if string(o.SessionID) != string(want.SessionID) || len(o.Candidates) != 1 || o.Candidates[0].Addr != want.Candidates[0].Addr {
			t.Fatalf("offer round-trip mismatch: %+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("offer round-trip timed out")
	}

	answer := Answer{
		SessionID:  want.SessionID,
		Candidates: []Candidate{{Addr: "192.0.2.20:40001", Typ: "host"}},
		CreatedAt:  time.Now().UTC(),
	}
	if err := answerSide.SendAnswer(ctx, answer); err != nil {
		t.Fatalf("send answer: %v", err)
	}
	gotAnswer, err := offerSide.RecvAnswer(ctx, want.SessionID)
	if err != nil {
		t.Fatalf("recv answer: %v", err)
	}
	if gotAnswer.Candidates[0].Addr != answer.Candidates[0].Addr {
		t.Fatalf("answer round-trip mismatch: %+v", gotAnswer)
	}
}

func TestFabricSignalingSealedAtRest(t *testing.T) {
	dir := t.TempDir()
	shared, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: dir})
	if err != nil {
		t.Fatalf("filemailbox: %v", err)
	}
	offerSide, err := NewFabricSignaling(shared, "secret-token", "nodeA", "nodeB", nil)
	if err != nil {
		t.Fatalf("offer signaling: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	marker := "punch-canary-plaintext-marker"
	offer := Offer{
		SessionID:  []byte("0123456789abcdef"),
		Key:        []byte("k"),
		Candidates: []Candidate{{Addr: marker + ":40000", Typ: "host"}},
		CreatedAt:  time.Now().UTC(),
	}
	if err := offerSide.SendOffer(ctx, offer); err != nil {
		t.Fatalf("send offer: %v", err)
	}

	// Walk every mailbox file: the marker must exist only as AES-GCM
	// ciphertext, never as plaintext.
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("mailbox files missing: %v %v", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(raw), marker) {
			t.Fatalf("%s carries plaintext marker; envelope not sealed", f)
		}
		if !strings.Contains(string(raw), PayloadTypeOffer) {
			t.Fatalf("%s missing expected payload type", f)
		}
	}
}

func TestFabricSignalingWrongTokenCannotOpen(t *testing.T) {
	dir := t.TempDir()
	shared, err := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: dir})
	if err != nil {
		t.Fatalf("filemailbox: %v", err)
	}
	sender, err := NewFabricSignaling(shared, "token-one", "nodeA", "nodeB", nil)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	// Different peer mailbox so the sealed envelope lands where the
	// receiver reads: receiver is nodeB with the wrong token.
	receiver, err := NewFabricSignaling(shared, "token-two", "nodeB", "nodeA", nil)
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = sender.SendOffer(ctx, Offer{
		SessionID: []byte("0123456789abcdef"),
		Key:       []byte("k"),
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := receiver.RecvOffer(ctx); err == nil {
		t.Fatal("receiver with wrong token accepted a sealed offer")
	}
}
