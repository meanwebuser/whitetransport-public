package carriers

import (
	"bytes"
	"context"
	"fmt"
	mathrand "math/rand"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func newTestRand() *mathrand.Rand { return mathrand.New(mathrand.NewSource(1)) }

type fakeDNSExchange struct {
	server *DNSTXTServer
}

func (f *fakeDNSExchange) Exchange(_ context.Context, query []byte) ([]byte, error) {
	response := f.server.HandleQuery(query)
	if response == nil {
		return nil, fmt.Errorf("fake exchange: malformed query")
	}
	return response, nil
}

func newTestDNSTXTCarrier(t *testing.T, exchange DNSTXTExchanger) *DNSTXTCarrier {
	t.Helper()
	carrier, err := NewDNSTXTCarrier(DNSTXTConfig{
		Zone:             "wt.test",
		Exchange:         exchange,
		MaxUpChunkBytes:  64,
		MinQueryInterval: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return carrier
}

func TestDNSCodecQueryResponseRoundTrip(t *testing.T) {
	query, err := dnsBuildTXTQuery(0x1234, "box.h.wt.test", true)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewDNSTXTServer(DNSTXTServerConfig{Zone: "wt.test"})
	if err != nil {
		t.Fatal(err)
	}
	responseBytes := server.HandleQuery(query)
	if responseBytes == nil {
		t.Fatal("server returned no response")
	}
	response, err := dnsParseResponse(responseBytes)
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != 0x1234 {
		t.Fatalf("response id mismatch: %#x", response.ID)
	}
	if response.RCode != dnsRcodeOK {
		t.Fatalf("expected NOERROR, got rcode %d", response.RCode)
	}
	if len(response.Answers) != 1 || !strings.HasPrefix(firstString(response.Answers[0]), dnsTXTHelloPrefix) {
		t.Fatalf("expected hello TXT answer, got %+v", response.Answers)
	}
}

func TestDNSB32ChunkRoundTripWithRandomCase(t *testing.T) {
	raw := []byte("the quick brown fox jumps over the lazy dog 0123456789")
	encoded := dnsEncodeUpChunk(raw)
	randomized := dnsRandomCase(encoded, newTestRand())
	if randomized == encoded {
		t.Fatal("expected case randomization to change the encoded chunk")
	}
	decoded, err := dnsDecodeUpChunk(randomized)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("round trip mismatch: %q != %q", decoded, raw)
	}
}

func TestDNSTXTServerUploadDedupPollFetchAck(t *testing.T) {
	server, err := NewDNSTXTServer(DNSTXTServerConfig{Zone: "wt.test"})
	if err != nil {
		t.Fatal(err)
	}
	rng := newTestRand()
	message := []byte(encodeMailboxText(t, fabric.NewEnvelope("env-dedup", fabric.TrafficControl, "control.ping", []byte("payload"))))
	total := (len(message) + 31) / 32
	if total < 2 {
		t.Fatalf("test needs a multi-chunk message, got %d bytes", len(message))
	}
	for seq := 0; seq < total; seq++ {
		end := (seq + 1) * 32
		if end > len(message) {
			end = len(message)
		}
		encoded := dnsRandomCase(dnsEncodeUpChunk(message[seq*32:end]), rng)
		labels := strings.Join(dnsSplitB32Labels(encoded), ".")
		name := fmt.Sprintf("%s.abcd1234.%d.%d.box.u.wt.test", labels, seq, total)
		query, err := dnsBuildTXTQuery(uint16(seq), name, true)
		if err != nil {
			t.Fatal(err)
		}
		response, err := dnsParseResponse(server.HandleQuery(query))
		if err != nil {
			t.Fatal(err)
		}
		if response.RCode != dnsRcodeOK || len(response.Answers) == 0 {
			t.Fatalf("upload chunk %d rejected: %+v", seq, response)
		}
	}
	// Re-uploading the complete message must answer "dup" without queueing twice.
	encoded := dnsRandomCase(dnsEncodeUpChunk(message[:32]), rng)
	query, _ := dnsBuildTXTQuery(1, fmt.Sprintf("%s.abcd1234.0.%d.box.u.wt.test", strings.Join(dnsSplitB32Labels(encoded), "."), total), true)
	response, _ := dnsParseResponse(server.HandleQuery(query))
	if got := firstString(response.Answers[0]); got != "dup" && got != "ok" {
		t.Fatalf("duplicate chunk upload answered %q", got)
	}

	poll, _ := dnsBuildTXTQuery(2, "box.p.wt.test", true)
	response, err = dnsParseResponse(server.HandleQuery(poll))
	if err != nil {
		t.Fatal(err)
	}
	tokens := strings.Fields(firstString(response.Answers[0]))
	if len(tokens) < 3 || tokens[0] != dnsTXTHelloPrefix {
		t.Fatalf("unexpected poll answer %q", firstString(response.Answers[0]))
	}
	// Poll reports downstream chunks (170-byte splits), not the upstream
	// chunk count the writer used.
	wantEntry := fmt.Sprintf("abcd1234:%d", (len(message)+dnsTXTDownChunkBytes-1)/dnsTXTDownChunkBytes)
	if tokens[2] != wantEntry {
		t.Fatalf("poll entry mismatch: %q != %q", tokens[2], wantEntry)
	}

	fetch, _ := dnsBuildTXTQuery(3, fmt.Sprintf("0.abcd1234.box.g.wt.test"), true)
	response, err = dnsParseResponse(server.HandleQuery(fetch))
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := dnsDecodeDownChunk(firstString(response.Answers[0]))
	if err != nil {
		t.Fatal(err)
	}
	wantPrefixLen := dnsTXTDownChunkBytes
	if len(message) < wantPrefixLen {
		wantPrefixLen = len(message)
	}
	if !bytes.Equal(chunk, message[:wantPrefixLen]) {
		t.Fatalf("first down chunk mismatch: %q", chunk)
	}

	ack, _ := dnsBuildTXTQuery(4, "abcd1234.box.a.wt.test", true)
	response, err = dnsParseResponse(server.HandleQuery(ack))
	if err != nil || response.RCode != dnsRcodeOK {
		t.Fatalf("ack failed: %+v err=%v", response, err)
	}

	poll, _ = dnsBuildTXTQuery(5, "box.p.wt.test", true)
	response, _ = dnsParseResponse(server.HandleQuery(poll))
	if got := firstString(response.Answers[0]); got != dnsTXTHelloPrefix+" 0" {
		t.Fatalf("queue should be empty after ack, got %q", got)
	}
}

func TestDNSTXTCarrierRoundTripThroughFakeResolver(t *testing.T) {
	server, err := NewDNSTXTServer(DNSTXTServerConfig{Zone: "wt.test"})
	if err != nil {
		t.Fatal(err)
	}
	writer := newTestDNSTXTCarrier(t, &fakeDNSExchange{server: server})
	reader := newTestDNSTXTCarrier(t, &fakeDNSExchange{server: server})

	payload := bytes.Repeat([]byte("dns-txt-roundtrip-"), 40) // ~720 bytes, multi-chunk
	env := fabric.NewEnvelope("env-rt", fabric.TrafficControl, "control.payload", payload)
	if err := writer.Write(context.Background(), Endpoint{ID: "dns", Address: "node-b"}, env); err != nil {
		t.Fatal(err)
	}

	result, err := reader.Read(context.Background(), Endpoint{ID: "dns", Address: "node-b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Envelopes) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(result.Envelopes))
	}
	got := result.Envelopes[0]
	if got.ID != env.ID || got.TrafficClass != env.TrafficClass || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("envelope mismatch: id=%s class=%s payloadLen=%d", got.ID, got.TrafficClass, len(got.Payload))
	}

	again, err := reader.Read(context.Background(), Endpoint{ID: "dns", Address: "node-b"}, result.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Envelopes) != 0 {
		t.Fatalf("expected empty second read, got %d envelopes", len(again.Envelopes))
	}

	metrics, err := reader.Probe(context.Background(), Endpoint{ID: "dns", Address: "node-b"})
	if err != nil || !metrics.Healthy {
		t.Fatalf("probe failed: %+v err=%v", metrics, err)
	}
}

func TestDNSTXTCarrierSkipsPoisonPill(t *testing.T) {
	server, err := NewDNSTXTServer(DNSTXTServerConfig{Zone: "wt.test"})
	if err != nil {
		t.Fatal(err)
	}
	reader := newTestDNSTXTCarrier(t, &fakeDNSExchange{server: server})

	// Manually queue a message body that will never decode as an envelope.
	uploadRawText(t, server, "abcd1234", "box", []byte("}]not-an-envelope{"), 32)

	result, err := reader.Read(context.Background(), Endpoint{ID: "dns", Address: "box"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Envelopes) != 0 {
		t.Fatalf("poison pill should be skipped, got %d envelopes", len(result.Envelopes))
	}
	again, err := reader.Read(context.Background(), Endpoint{ID: "dns", Address: "box"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Envelopes) != 0 {
		t.Fatalf("poison pill should be acked away, got %d envelopes", len(again.Envelopes))
	}
}

// uploadRawText pushes an arbitrary byte payload through the raw upload wire
// format so tests can queue bodies the client encoder would never produce.
func uploadRawText(t *testing.T, server *DNSTXTServer, msgid, mailbox string, message []byte, chunkSize int) {
	t.Helper()
	rng := newTestRand()
	total := (len(message) + chunkSize - 1) / chunkSize
	for seq := 0; seq < total; seq++ {
		end := (seq + 1) * chunkSize
		if end > len(message) {
			end = len(message)
		}
		encoded := dnsRandomCase(dnsEncodeUpChunk(message[seq*chunkSize:end]), rng)
		labels := strings.Join(dnsSplitB32Labels(encoded), ".")
		name := fmt.Sprintf("%s.%s.%d.%d.%s.u.wt.test", labels, msgid, seq, total, mailbox)
		query, err := dnsBuildTXTQuery(uint16(seq), name, true)
		if err != nil {
			t.Fatal(err)
		}
		if response := server.HandleQuery(query); response == nil {
			t.Fatalf("upload of raw chunk %d rejected", seq)
		}
	}
}

func TestDNSTXTCarrierUDPLoopback(t *testing.T) {
	server, err := NewDNSTXTServer(DNSTXTServerConfig{Zone: "wt.test"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.ServeUDP(ctx, conn) }()

	carrier, err := NewDNSTXTCarrier(DNSTXTConfig{
		Zone:             "wt.test",
		DirectAddr:       conn.LocalAddr().String(),
		MinQueryInterval: 0,
		QueryTimeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	env := fabric.NewEnvelope("env-udp", fabric.TrafficHealth, "health.beat", []byte("udp-loopback-payload"))
	if err := carrier.Write(ctx, Endpoint{ID: "dns", Address: "box-1"}, env); err != nil {
		t.Fatal(err)
	}
	result, err := carrier.Read(ctx, Endpoint{ID: "dns", Address: "box-1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Envelopes) != 1 || result.Envelopes[0].ID != "env-udp" {
		t.Fatalf("udp loopback read mismatch: %+v", result.Envelopes)
	}
	metrics, err := carrier.Probe(ctx, Endpoint{ID: "dns", Address: "box-1"})
	if err != nil || !metrics.Healthy {
		t.Fatalf("udp probe failed: %+v err=%v", metrics, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeUDP did not stop after context cancel")
	}
}

func encodeMailboxText(t *testing.T, env fabric.Envelope) string {
	t.Helper()
	text, err := encodeMailboxEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	return text
}
