package carriers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestVKDocsCarrierWritesEnvelopeThroughDocumentAttachment(t *testing.T) {
	var uploadedEnvelope fabric.Envelope
	var seenSendForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/docs.getMessagesUploadServer":
			_, _ = w.Write([]byte(`{"response":{"upload_url":"` + serverURL(r) + `/upload"}}`))
		case "/upload":
			envelope, err := readUploadedEnvelope(r)
			if err != nil {
				t.Fatal(err)
			}
			uploadedEnvelope = envelope
			_, _ = w.Write([]byte(`{"file":"upload-file-token"}`))
		case "/docs.save":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostForm.Get("file") != "upload-file-token" {
				t.Fatalf("bad save form: %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"response":{"doc":{"owner_id":100,"id":200}}}`))
		case "/messages.send":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			seenSendForm = r.PostForm
			_, _ = w.Write([]byte(`{"response":123}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "token", BaseURL: server.URL, DescriptorID: CarrierVKDocs1024})
	if err != nil {
		t.Fatal(err)
	}
	envelope := fabric.NewEnvelope("bulk-1.0", fabric.TrafficBulk, "bulk.frame.chunk", []byte("payload bytes"))

	if err := carrier.Write(context.Background(), Endpoint{ID: "vk-doc", Address: "2000000001"}, envelope); err != nil {
		t.Fatal(err)
	}

	if uploadedEnvelope.ID != envelope.ID || string(uploadedEnvelope.Payload) != "payload bytes" {
		t.Fatalf("bad uploaded envelope: %+v", uploadedEnvelope)
	}
	if seenSendForm.Get("peer_id") != "2000000001" || seenSendForm.Get("attachment") != "doc100_200" {
		t.Fatalf("bad send form: %v", seenSendForm)
	}
	if !strings.Contains(seenSendForm.Get("message"), envelope.ID) {
		t.Fatalf("send message should include envelope id, got %q", seenSendForm.Get("message"))
	}
}

func TestVKDocsCarrierRenewsFailedUploadLocation(t *testing.T) {
	grants := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/docs.getMessagesUploadServer":
			grants++
			location := "/first"
			if grants == 2 {
				location = "/second"
			}
			_, _ = w.Write([]byte(`{"response":{"upload_url":"` + serverURL(r) + location + `"}}`))
		case "/first":
			_, _ = w.Write([]byte(`{"error":1,"error_descr":"temporary upload failure"}`))
		case "/second":
			_, _ = w.Write([]byte(`{"file":"new-upload-reference"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "fixture", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	file, err := carrier.uploadDocumentWithRetry(context.Background(), "2000000001", []byte("payload"), "fixture.wtmsg")
	if err != nil || file != "new-upload-reference" {
		t.Fatalf("upload after provider rejection = %q, %v", file, err)
	}
}

func TestVKDocsCarrierReadsDocumentAttachmentsInAscendingMessageOrder(t *testing.T) {
	first := fabric.NewEnvelope("bulk-1.0", fabric.TrafficBulk, "bulk.frame.chunk", []byte("one"))
	second := fabric.NewEnvelope("bulk-1.1", fabric.TrafficBulk, "bulk.frame.chunk", []byte("two"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/messages.getHistory":
			payload := map[string]any{
				"response": map[string]any{
					"items": []map[string]any{
						{"id": 12, "attachments": []map[string]any{{"type": "doc", "doc": map[string]string{"url": serverURL(r) + "/doc2"}}}},
						{"id": 11, "text": "ignore me"},
						{"id": 10, "attachments": []map[string]any{{"type": "doc", "doc": map[string]string{"url": serverURL(r) + "/doc1"}}}},
						{"id": 9, "attachments": []map[string]any{{"type": "doc", "doc": map[string]string{"url": serverURL(r) + "/old"}}}},
					},
				},
			}
			if err := json.NewEncoder(w).Encode(payload); err != nil {
				t.Fatal(err)
			}
		case "/doc1":
			writeEnvelope(t, w, first)
		case "/doc2":
			writeEnvelope(t, w, second)
		case "/old":
			writeEnvelope(t, w, first)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	read, err := carrier.Read(context.Background(), Endpoint{ID: "vk-doc", Address: "2000000001"}, Cursor("9"))
	if err != nil {
		t.Fatal(err)
	}

	if read.Cursor != "12" {
		t.Fatalf("expected cursor 12, got %s", read.Cursor)
	}
	if len(read.Envelopes) != 2 {
		t.Fatalf("expected two envelopes, got %d", len(read.Envelopes))
	}
	if read.Envelopes[0].ID != "bulk-1.0" || read.Envelopes[1].ID != "bulk-1.1" {
		t.Fatalf("expected ascending message order, got %+v", read.Envelopes)
	}
}

func TestVKDocsCarrierRetriesUnreadAttachmentBeforeAdvancingCursor(t *testing.T) {
	open := fabric.NewEnvelope("tunnel", fabric.TrafficEgress, "tunnel.open", []byte(`{"target_addr":"origin.test:443"}`))
	first := fabric.NewEnvelope(open.ID, fabric.TrafficEgress, "tunnel.data", []byte{0x16, 0x03, 0x03, 0x00})
	first.Sequence = 1
	second := fabric.NewEnvelope(open.ID, fabric.TrafficEgress, "tunnel.data", []byte{0x04, 0x02, 0x00, 0x00, 0x00})
	second.Sequence = 2

	var firstDataReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/messages.getHistory":
			items := []map[string]any{}
			for _, doc := range []struct {
				id   int
				path string
			}{{12, "/second"}, {11, "/first"}, {10, "/open"}} {
				items = append(items, map[string]any{
					"id":   doc.id,
					"text": "wtbulk1 tunnel",
					"attachments": []map[string]any{{
						"type": "doc",
						"doc":  map[string]string{"url": serverURL(r) + doc.path},
					}},
				})
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"items": items}}); err != nil {
				t.Error(err)
			}
		case "/open":
			writeEnvelope(t, w, open)
		case "/first":
			if firstDataReads.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			writeEnvelope(t, w, first)
		case "/second":
			writeEnvelope(t, w, second)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "fixture", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{ID: "vk-doc", Address: "2000000001"}
	read, err := carrier.Read(context.Background(), endpoint, "9")
	if err != nil {
		t.Fatal(err)
	}
	if read.Cursor != "10" || len(read.Envelopes) != 1 || read.Envelopes[0].PayloadType != open.PayloadType {
		t.Errorf("first poll should deliver only the open: cursor=%s envelopes=%d", read.Cursor, len(read.Envelopes))
	}
	read, err = carrier.Read(context.Background(), endpoint, read.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if read.Cursor != "12" || len(read.Envelopes) != 2 {
		t.Fatalf("second poll should deliver both data frames: cursor=%s envelopes=%d", read.Cursor, len(read.Envelopes))
	}
	for i, want := range []fabric.Envelope{first, second} {
		got := read.Envelopes[i]
		if got.PayloadType != want.PayloadType || got.Sequence != want.Sequence || !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("frame %d: got type=%s sequence=%d payload=%x", i, got.PayloadType, got.Sequence, got.Payload)
		}
	}
}

func TestVKDocsCarrierReportsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"error_code":5,"error_msg":"auth failed"}}`))
	}))
	defer server.Close()
	carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	err = carrier.Write(context.Background(), Endpoint{ID: "vk-doc", Address: "2000000001"}, fabric.NewEnvelope("env", fabric.TrafficBulk, "x", nil))
	if err == nil || !strings.Contains(err.Error(), "error_code") {
		t.Fatalf("expected VK error, got %v", err)
	}
}

func TestVKDocsCarrierRejectsOversizedEnvelopeBeforeProviderCalls(t *testing.T) {
	carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "token", BaseURL: "http://127.0.0.1:1", DescriptorID: CarrierVKDocs256})
	if err != nil {
		t.Fatal(err)
	}
	envelope := fabric.NewEnvelope("too-large", fabric.TrafficBulk, "bulk.frame.chunk", bytes.Repeat([]byte("x"), carrier.Descriptor().Limits.MaxPayloadBytes+1))

	err = carrier.Write(context.Background(), Endpoint{ID: "vk-doc", Address: "2000000001"}, envelope)
	if err == nil || !strings.Contains(err.Error(), "exceeds carrier limit") {
		t.Fatalf("expected local size limit error, got %v", err)
	}
}

func TestVKDocsCarrierRequiresTokenPeerIDAndVKDocsDescriptor(t *testing.T) {
	if _, err := NewVKDocsCarrier(VKDocsConfig{}); err == nil {
		t.Fatal("expected missing token error")
	}
	if _, err := NewVKDocsCarrier(VKDocsConfig{Token: "token", DescriptorID: CarrierOKDocs256}); err == nil {
		t.Fatal("expected invalid descriptor error")
	}
	carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := carrier.Write(context.Background(), Endpoint{}, fabric.NewEnvelope("env", fabric.TrafficBulk, "x", nil)); err == nil {
		t.Fatal("expected missing peer id error")
	}
}

func readUploadedEnvelope(r *http.Request) (fabric.Envelope, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return fabric.Envelope{}, err
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fabric.Envelope{}, err
		}
		if part.FormName() != "file" {
			continue
		}
		raw, err := io.ReadAll(part)
		if err != nil {
			return fabric.Envelope{}, err
		}
		return decodeDocumentEnvelope(raw)
	}
	return fabric.Envelope{}, errors.New("multipart file part not found")
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, envelope fabric.Envelope) {
	t.Helper()
	raw, err := encodeDocumentEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
