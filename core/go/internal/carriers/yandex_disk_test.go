package carriers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestYandexDiskCarrierWriteUploadsEnvelopeFile(t *testing.T) {
	fake := &fakeYandexDiskHTTP{}
	carrier, err := NewYandexDiskCarrier(YandexDiskConfig{
		OAuthToken:      "test-token",
		BaseURL:         "https://disk.test/v1/disk",
		HTTPClient:      fake,
		MinSendInterval: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	env := fabric.NewEnvelope("env-1", fabric.TrafficBulk, "bulk.chunk", []byte("payload"))
	err = carrier.Write(context.Background(), Endpoint{ID: "yd", Address: "mailbox/a2b"}, env)
	if err != nil {
		t.Fatal(err)
	}

	if !fake.called("PUT", "/v1/disk/resources") {
		t.Fatal("expected folder creation request")
	}
	if !fake.called("GET", "/v1/disk/resources/upload") {
		t.Fatal("expected upload-link request")
	}
	if got := fake.uploaded; !bytes.Contains(got, []byte(`"payload_type":"bulk.chunk"`)) {
		t.Fatalf("uploaded payload does not contain encoded envelope: %s", string(got))
	}
}

func TestYandexDiskCarrierReadDownloadsAndAdvancesCursor(t *testing.T) {
	raw, err := encodeDocumentEnvelope(fabric.NewEnvelope("env-2", fabric.TrafficBulk, "bulk.chunk", []byte("payload-2")))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeYandexDiskHTTP{downloadBody: raw}
	carrier, err := NewYandexDiskCarrier(YandexDiskConfig{
		OAuthToken:       "test-token",
		BaseURL:          "https://disk.test/v1/disk",
		HTTPClient:       fake,
		CleanupAfterRead: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := carrier.Read(context.Background(), Endpoint{ID: "yd", Address: "a2b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Envelopes) != 1 || string(result.Envelopes[0].Payload) != "payload-2" {
		t.Fatalf("unexpected envelopes: %+v", result.Envelopes)
	}
	if strings.TrimSpace(string(result.Cursor)) == "" {
		t.Fatal("expected non-empty cursor")
	}
	if !fake.called("DELETE", "/v1/disk/resources") {
		t.Fatal("expected cleanup delete request")
	}
}

type fakeYandexDiskHTTP struct {
	uploaded     []byte
	downloadBody []byte
	requests     []string
}

func (f *fakeYandexDiskHTTP) Do(req *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, req.Method+" "+req.URL.Path)
	switch {
	case req.Method == http.MethodPut && strings.Contains(req.URL.Host, "upload.test"):
		body, _ := io.ReadAll(req.Body)
		f.uploaded = body
		return fakeYandexResponse(201, `{}`), nil
	case req.Method == http.MethodGet && req.URL.Path == "/v1/disk/resources/upload":
		return fakeYandexResponse(200, `{"href":"https://upload.test/file"}`), nil
	case req.Method == http.MethodGet && req.URL.Path == "/v1/disk/resources/download":
		return fakeYandexResponse(200, `{"href":"https://download.test/file"}`), nil
	case req.Method == http.MethodGet && strings.Contains(req.URL.Host, "download.test"):
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(f.downloadBody)), Header: http.Header{}}, nil
	case req.Method == http.MethodGet && req.URL.Path == "/v1/disk/resources":
		return fakeYandexResponse(200, `{"_embedded":{"items":[{"name":"1_env.wtmsg","path":"/ytp/a2b/1_env.wtmsg","modified":"2026-06-15T00:00:00Z","type":"file"}]}}`), nil
	case req.Method == http.MethodPut && req.URL.Path == "/v1/disk/resources":
		return fakeYandexResponse(201, `{}`), nil
	case req.Method == http.MethodDelete && req.URL.Path == "/v1/disk/resources":
		return fakeYandexResponse(204, ``), nil
	default:
		return fakeYandexResponse(404, `{"message":"not found"}`), nil
	}
}

func fakeYandexResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func (f *fakeYandexDiskHTTP) called(method string, path string) bool {
	want := method + " " + path
	for _, got := range f.requests {
		if got == want {
			return true
		}
	}
	return false
}
