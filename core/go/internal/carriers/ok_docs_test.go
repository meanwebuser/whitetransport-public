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

func TestOKDocsCarrierWritesEnvelopeThroughDocumentAttachment(t *testing.T) {
	var uploadedEnvelope fabric.Envelope
	var seenMethods []string
	var seenSendQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fb.do":
			method := r.URL.Query().Get("method")
			seenMethods = append(seenMethods, method)
			switch method {
			case "docs.getUploadUrl":
				if r.URL.Query().Get("group_id") != "123" {
					t.Fatalf("missing group_id: %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"upload_url":"` + serverURL(r) + `/upload"}`))
			case "docs.commit":
				if r.URL.Query().Get("doc_id") != "doc-upload-id" || r.URL.Query().Get("token") != "doc-token" {
					t.Fatalf("bad commit query: %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"id":"committed-doc-id"}`))
			case "messages.send":
				seenSendQuery = r.URL.Query()
				_, _ = w.Write([]byte(`{"success":true}`))
			default:
				t.Fatalf("unexpected method %s", method)
			}
		case "/upload":
			envelope, err := readOKUploadedEnvelope(r)
			if err != nil {
				t.Fatal(err)
			}
			uploadedEnvelope = envelope
			_, _ = w.Write([]byte(`{"docs":[{"id":"doc-upload-id","token":"doc-token"}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	carrier, err := NewOKDocsCarrier(OKDocsConfig{
		AccessToken:      "token",
		ApplicationKey:   "app-key",
		SessionSecretKey: "session-secret",
		BaseURL:          server.URL + "/fb.do",
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := fabric.NewEnvelope("bulk-1.0", fabric.TrafficBulk, "bulk.frame.chunk", []byte("payload bytes"))

	if err := carrier.Write(context.Background(), Endpoint{ID: "ok-doc", Address: "chat:C123"}, envelope); err != nil {
		t.Fatal(err)
	}

	if strings.Join(seenMethods, ",") != "docs.getUploadUrl,docs.commit,messages.send" {
		t.Fatalf("unexpected OK method sequence: %v", seenMethods)
	}
	if uploadedEnvelope.ID != envelope.ID || string(uploadedEnvelope.Payload) != "payload bytes" {
		t.Fatalf("bad uploaded envelope: %+v", uploadedEnvelope)
	}
	if seenSendQuery.Get("chat") != "chat:C123" || seenSendQuery.Get("recipient_id") != "C123" {
		t.Fatalf("bad send query: %s", seenSendQuery.Encode())
	}
	if !strings.Contains(seenSendQuery.Get("attachment"), "committed-doc-id") {
		t.Fatalf("send attachment should include doc id, got %q", seenSendQuery.Get("attachment"))
	}
}

func TestOKDocsCarrierReadsDocumentAttachmentsInAscendingMessageOrder(t *testing.T) {
	first := fabric.NewEnvelope("bulk-1.0", fabric.TrafficBulk, "bulk.frame.chunk", []byte("one"))
	second := fabric.NewEnvelope("bulk-1.1", fabric.TrafficBulk, "bulk.frame.chunk", []byte("two"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fb.do":
			if r.URL.Query().Get("method") != "messages.getHistory" {
				t.Fatalf("unexpected method %s", r.URL.Query().Get("method"))
			}
			payload := map[string]any{
				"messages": []map[string]any{
					{"messageId": 12, "attachment": map[string]any{"type": "doc", "doc": map[string]string{"url": serverURL(r) + "/doc2"}}},
					{"messageId": 11, "text": "ignore me"},
					{"messageId": 10, "attachment": map[string]any{"type": "doc", "doc": map[string]string{"url": serverURL(r) + "/doc1"}}},
					{"messageId": 9, "attachment": map[string]any{"type": "doc", "doc": map[string]string{"url": serverURL(r) + "/old"}}},
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
	carrier, err := NewOKDocsCarrier(OKDocsConfig{
		AccessToken:      "token",
		ApplicationKey:   "app-key",
		SessionSecretKey: "session-secret",
		BaseURL:          server.URL + "/fb.do",
	})
	if err != nil {
		t.Fatal(err)
	}

	read, err := carrier.Read(context.Background(), Endpoint{ID: "ok-doc", Address: "chat:C123"}, Cursor("9"))
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

func TestOKDocsCarrierReportsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error_code":401,"error_msg":"auth failed"}`))
	}))
	defer server.Close()
	carrier, err := NewOKDocsCarrier(OKDocsConfig{
		AccessToken:      "token",
		ApplicationKey:   "app-key",
		SessionSecretKey: "session-secret",
		BaseURL:          server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = carrier.Write(context.Background(), Endpoint{ID: "ok-doc", Address: "chat:C123"}, fabric.NewEnvelope("env", fabric.TrafficBulk, "x", nil))
	if err == nil || !strings.Contains(err.Error(), "error_code") {
		t.Fatalf("expected OK error, got %v", err)
	}
}

func TestOKDocsCarrierRejectsOversizedEnvelopeBeforeProviderCalls(t *testing.T) {
	carrier, err := NewOKDocsCarrier(OKDocsConfig{
		AccessToken:      "token",
		ApplicationKey:   "app-key",
		SessionSecretKey: "session-secret",
		BaseURL:          "http://127.0.0.1:1/fb.do",
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := fabric.NewEnvelope("too-large", fabric.TrafficBulk, "bulk.frame.chunk", bytes.Repeat([]byte("x"), carrier.Descriptor().Limits.MaxPayloadBytes+1))

	err = carrier.Write(context.Background(), Endpoint{ID: "ok-doc", Address: "chat:C123"}, envelope)
	if err == nil || !strings.Contains(err.Error(), "exceeds carrier limit") {
		t.Fatalf("expected local size limit error, got %v", err)
	}
}

func TestOKDocsCarrierRequiresSecretsChatAndDescriptor(t *testing.T) {
	if _, err := NewOKDocsCarrier(OKDocsConfig{}); err == nil {
		t.Fatal("expected missing access token error")
	}
	if _, err := NewOKDocsCarrier(OKDocsConfig{AccessToken: "token"}); err == nil {
		t.Fatal("expected missing application key error")
	}
	if _, err := NewOKDocsCarrier(OKDocsConfig{AccessToken: "token", ApplicationKey: "app-key"}); err == nil {
		t.Fatal("expected missing session secret error")
	}
	if _, err := NewOKDocsCarrier(OKDocsConfig{AccessToken: "token", ApplicationKey: "app-key", SessionSecretKey: "secret", DescriptorID: CarrierOKPhotos}); err == nil {
		t.Fatal("expected invalid descriptor error")
	}
	carrier, err := NewOKDocsCarrier(OKDocsConfig{AccessToken: "token", ApplicationKey: "app-key", SessionSecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := carrier.Write(context.Background(), Endpoint{}, fabric.NewEnvelope("env", fabric.TrafficBulk, "x", nil)); err == nil {
		t.Fatal("expected missing chat error")
	}
}

func TestOKSignatureMatchesSortedFBParams(t *testing.T) {
	values := map[string]string{
		"method":          "messages.send",
		"application_key": "app-key",
		"format":          "json",
		"access_token":    "token",
		"message":         "hello",
	}

	if got := okSignature(values, "secret"); got != "21eb13bea13a263e1643b69769dc23ae" {
		t.Fatalf("unexpected signature %s", got)
	}
}

func readOKUploadedEnvelope(r *http.Request) (fabric.Envelope, error) {
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
		if part.FormName() != "file1" {
			continue
		}
		raw, err := io.ReadAll(part)
		if err != nil {
			return fabric.Envelope{}, err
		}
		return decodeDocumentEnvelope(raw)
	}
	return fabric.Envelope{}, errors.New("multipart file1 part not found")
}
