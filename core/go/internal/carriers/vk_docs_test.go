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
