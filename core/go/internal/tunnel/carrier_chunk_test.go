package tunnel

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

func TestEgressWriteChunkLimitLeavesMailboxEncodingHeadroom(t *testing.T) {
	carrier, err := carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := egressWriteChunkLimit(carrier, false), 2048; got != want {
		t.Fatalf("VK mailbox chunk limit = %d, want %d", got, want)
	}
	if got, want := egressWriteChunkLimit(carrier, true), 1024; got != want {
		t.Fatalf("session-sealed VK mailbox chunk limit = %d, want %d", got, want)
	}
}

func TestVKMessagesExitFitsSessionSealedFrames(t *testing.T) {
	var sent atomic.Int32
	carrier, err := carriers.NewVKMessagesCarrier(carriers.VKMessagesConfig{
		Token: "fixture", BaseURL: "https://vk.test", HTTPClient: vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
			if err := req.ParseForm(); err != nil {
				return nil, err
			}
			sent.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"response":31}`))}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	tun := NewCarrierTunnel("fixture-exit-canary", nil)
	peer := "wt-example-client"
	sessionID := peer + "-1234567890123456789"
	streamID := "tun-" + sessionID
	cipher, err := fabric.NewSessionCipher(fabric.DeriveBootstrapSecretKey("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	tun.SetSessionCipher(sessionID, cipher)
	origin, target := net.Pipe()
	defer origin.Close()
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: carriers.Endpoint{Address: "42"}}
	failed := make(chan bool, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tun.tunnelReader(t.Context(), streamID, sessionID, target, binding, peer, func(carrierFailed bool) { failed <- carrierFailed })
	}()
	_ = origin.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := origin.Write(bytes.Repeat([]byte{0x6e}, 2795)); err != nil {
		t.Fatal(err)
	}
	_ = origin.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("exit did not finish writing the fixture response")
	}
	if <-failed || sent.Load() < 2 {
		t.Fatalf("session-sealed return bytes exceeded VK message framing, sent=%d", sent.Load())
	}
}
