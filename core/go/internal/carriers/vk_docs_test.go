package carriers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

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
	file, err := carrier.uploadDocumentWithRetry(context.Background(), "2000000001", []byte("payload"), "fixture.wtmsg", false)
	if err != nil || file != "new-upload-reference" {
		t.Fatalf("upload after provider rejection = %q, %v", file, err)
	}
}

func TestVKDocsCarrierRenewsStalledAndRejectedUploadLocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const base = "https://vk.test"
		locations := []string{"/stalled", "/rejected", "/working"}
		grants := 0
		stalledUploadExpired := false
		client := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
			status, body := http.StatusOK, ""
			switch req.URL.Path {
			case "/docs.getMessagesUploadServer":
				body = `{"response":{"upload_url":"` + base + locations[grants] + `"}}`
				grants++
			case "/stalled":
				select {
				case <-req.Context().Done():
					stalledUploadExpired = req.Context().Err() == context.DeadlineExceeded
					return nil, req.Context().Err()
				case <-time.After(8 * time.Second):
					return nil, errors.New("upload reached fixture stall limit")
				}
			case "/rejected":
				status = http.StatusMethodNotAllowed
			case "/working":
				body = `{"file":"new-upload-reference"}`
			default:
				t.Fatalf("unexpected path %s", req.URL.Path)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "fixture", BaseURL: base, HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		file, err := carrier.uploadDocumentWithRetry(t.Context(), "2000000001", []byte("payload"), "fixture.wtmsg", true)
		if err != nil || file != "new-upload-reference" || grants != len(locations) || !stalledUploadExpired {
			t.Fatalf("upload = %q, %v, grants=%d, stalled upload timed out=%t", file, err, grants, stalledUploadExpired)
		}
	})
}

func TestVKDocsCarrierPublishesAfterSlowUploadRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const base = "https://vk.test"
		var grants int
		var published bool
		locations := []string{"/gateway-timeout", "/stalled", "/working"}
		client := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
			status, body := http.StatusOK, ""
			switch req.URL.Path {
			case "/docs.getMessagesUploadServer":
				if grants == len(locations) {
					t.Fatal("unexpected extra upload grant")
				}
				body = `{"response":{"upload_url":"` + base + locations[grants] + `"}}`
				grants++
			case "/gateway-timeout":
				time.Sleep(3100 * time.Millisecond)
				status, body = http.StatusGatewayTimeout, "{}"
			case "/stalled":
				<-req.Context().Done()
				return nil, req.Context().Err()
			case "/working":
				select {
				case <-time.After(3 * time.Second):
					body = `{"file":"fresh-upload"}`
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			case "/docs.save":
				body = `{"response":{"doc":{"owner_id":100,"id":200}}}`
			case "/messages.send":
				select {
				case <-time.After(400 * time.Millisecond):
					published = true
					body = `{"response":123}`
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			default:
				t.Fatalf("unexpected path %s", req.URL.Path)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "fixture", BaseURL: base, HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		// The origin's TLS window has already started while its first flight
		// travels back to the client. Publication must fit the time left.
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		time.Sleep(2750 * time.Millisecond)
		envelope := fabric.NewEnvelope("fixture", fabric.TrafficEgress, "tunnel.data", []byte("fixture"))
		err = carrier.Write(ctx, Endpoint{ID: "vk-doc", Address: "2000000001"}, envelope)
		if err != nil || !published || grants != len(locations) {
			t.Fatalf("published within origin window = %t, grants = %d, error = %v", published, grants, err)
		}
	})
}

func TestVKDocsCarrierOverlapsStalledUploads(t *testing.T) {
	cases := []struct {
		name           string
		uploadDelays   []time.Duration
		canceled       int32
		repeatGrant    bool
		rateLimitFirst bool
	}{
		{
			name: "two deadlines", uploadDelays: []time.Duration{8 * time.Second, 8 * time.Second, 4 * time.Second},
		},
		{
			name: "cancel losing upload", uploadDelays: []time.Duration{5 * time.Second, 400 * time.Millisecond},
			canceled: 1,
		},
		{
			name: "rate limit cancels overlap", uploadDelays: []time.Duration{3500 * time.Millisecond, 8 * time.Second, 400 * time.Millisecond},
			canceled: 1, rateLimitFirst: true,
		},
		{
			name: "provider repeats a grant", uploadDelays: []time.Duration{8 * time.Second, 8 * time.Second, 400 * time.Millisecond},
			repeatGrant: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const base = "https://vk.test"
				locations := []string{"/first", "/second", "/third"}
				delays := make(map[string]time.Duration, len(tc.uploadDelays))
				for i, delay := range tc.uploadDelays {
					delays[locations[i]] = delay
				}
				var grants, saves, sends, activeUploads, canceled, firstUploads atomic.Int32
				var writeStarted time.Time
				client := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
					var body string
					status := http.StatusOK
					header := make(http.Header)
					switch req.URL.Path {
					case "/docs.getMessagesUploadServer":
						attempt := int(grants.Add(1))
						if attempt > len(tc.uploadDelays) {
							return nil, errors.New("fixture received an unexpected upload grant request")
						}
						if tc.rateLimitFirst && attempt == 3 && time.Since(writeStarted) < tc.uploadDelays[0]+4*time.Second {
							t.Error("third grant was requested before Retry-After elapsed")
						}
						location := locations[attempt-1]
						if tc.repeatGrant && attempt == 2 {
							location = locations[0]
						}
						body = `{"response":{"upload_url":"` + base + location + `"}}`
					case "/docs.save":
						saves.Add(1)
						body = `{"response":{"doc":{"owner_id":100,"id":200}}}`
					case "/messages.send":
						sends.Add(1)
						body = `{"response":123}`
					default:
						delay, ok := delays[req.URL.Path]
						if !ok {
							return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
						}
						if req.URL.Path == locations[0] && firstUploads.Add(1) > 1 {
							t.Error("started a second POST with the same active grant")
						}
						if activeUploads.Add(1) > 2 {
							t.Error("more than two uploads active")
						}
						defer activeUploads.Add(-1)
						select {
						case <-time.After(delay):
							body = `{"file":"uploaded-fixture"}`
							if tc.rateLimitFirst && req.URL.Path == locations[0] {
								status, body = http.StatusTooManyRequests, "{}"
								header.Set("Retry-After", "4")
							}
						case <-req.Context().Done():
							if errors.Is(req.Context().Err(), context.Canceled) {
								canceled.Add(1)
							}
							return nil, req.Context().Err()
						}
					}
					return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "fixture", BaseURL: base, HTTPClient: client})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				// The first TLS response from the origin took about 3 s
				// to reach the client in the failed VPN2 run.
				time.Sleep(2900 * time.Millisecond)
				envelope := fabric.NewEnvelope("fixture", fabric.TrafficEgress, "tunnel.data", []byte("fixture"))
				writeStarted = time.Now()
				err = carrier.Write(ctx, Endpoint{Address: "2000000001"}, envelope)
				if err != nil {
					t.Fatalf("publish within origin window: %v", err)
				}
				synctest.Wait()
				if got := grants.Load(); got != int32(len(tc.uploadDelays)) {
					t.Errorf("upload grants = %d, want %d", got, len(tc.uploadDelays))
				}
				if saves.Load() != 1 || sends.Load() != 1 {
					t.Errorf("docs.save = %d, messages.send = %d, want one each", saves.Load(), sends.Load())
				}
				if got := canceled.Load(); got != tc.canceled || activeUploads.Load() != 0 {
					t.Errorf("canceled uploads = %d, want %d. Active = %d", got, tc.canceled, activeUploads.Load())
				}
			})
		})
	}
}

func TestVKDocsCarrierConcurrentWritesShareActiveGrants(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		firstPost := make(chan struct{})
		releaseFirst := make(chan struct{})
		var grants, sharedPosts, freshPosts, saves, sends atomic.Int32
		doer := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Path {
			case "/docs.getMessagesUploadServer":
				location := "/shared"
				if grants.Add(1) == 3 {
					location = "/fresh"
				}
				body = `{"response":{"upload_url":"https://vk.test` + location + `"}}`
			case "/shared":
				if sharedPosts.Add(1) == 1 {
					close(firstPost)
					select {
					case <-releaseFirst:
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				}
				body = `{"file":"shared-fixture"}`
			case "/fresh":
				freshPosts.Add(1)
				body = `{"file":"fresh-fixture"}`
			case "/docs.save":
				id := saves.Add(1)
				body = fmt.Sprintf(`{"response":{"doc":{"owner_id":100,"id":%d}}}`, id)
			case "/messages.send":
				body = fmt.Sprintf(`{"response":%d}`, sends.Add(1))
			default:
				return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "fixture", BaseURL: "https://vk.test", HTTPClient: doer})
		if err != nil {
			t.Fatal(err)
		}
		write := func(id string) error {
			env := fabric.NewEnvelope(id, fabric.TrafficEgress, "tunnel.data", []byte(id))
			return carrier.Write(ctx, Endpoint{Address: "2000000001"}, env)
		}
		firstDone := make(chan error, 1)
		go func() { firstDone <- write("first") }()
		select {
		case <-firstPost:
		case <-ctx.Done():
			t.Fatal("first POST did not start")
		}
		if err := write("second"); err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
		if got := sharedPosts.Load(); got != 1 || freshPosts.Load() != 1 {
			t.Fatalf("while the first POST was held: shared POSTs=%d fresh POSTs=%d, want one each", got, freshPosts.Load())
		}
		close(releaseFirst)
		if err := <-firstDone; err != nil {
			t.Fatalf("first write: %v", err)
		}
		if saves.Load() != 2 || sends.Load() != 2 {
			t.Fatalf("two writes must each save and publish once: saves=%d sends=%d", saves.Load(), sends.Load())
		}
		if err := write("reuse"); err != nil {
			t.Fatalf("reuse released grant: %v", err)
		}
		if grants.Load() != 4 || sharedPosts.Load() != 2 || sends.Load() != 3 {
			t.Fatalf("released grant was not reused immediately: grants=%d shared POSTs=%d sends=%d", grants.Load(), sharedPosts.Load(), sends.Load())
		}
	})
}

type vkDocsTestHTTPDoer func(*http.Request) (*http.Response, error)

func (f vkDocsTestHTTPDoer) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestVKDocsStreamSkipsKnownOwnAttachmentBeforeReadingPeer(t *testing.T) {
	const base = "https://vk.test"
	const history = `{"response":{"items":[
		{"id":22,"text":"wtbulk1 fixture-stream","attachments":[{"type":"doc","doc":{"url":"https://vk.test/peer-document"}}]},
		{"id":21,"text":"wtbulk1 fixture-stream","attachments":[{"type":"doc","doc":{"url":"https://vk.test/own-document"}}]}
	]}}`
	request := fabric.NewEnvelope("fixture-stream", fabric.TrafficEgress, "tunnel.data", []byte("request"))
	response := fabric.NewEnvelope(request.ID, fabric.TrafficEgress, "tunnel.data", []byte("response"))
	responseBytes, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var ownDownloads int
	doer := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		switch req.URL.Path {
		case "/docs.getMessagesUploadServer":
			body = `{"response":{"upload_url":"https://vk.test/upload"}}`
		case "/upload":
			body = `{"file":"fixture-upload"}`
		case "/docs.save":
			body = `{"response":{"doc":{"owner_id":1,"id":2}}}`
		case "/messages.send":
			body = `{"response":21}`
		case "/messages.getHistory":
			body = history
		case "/own-document":
			ownDownloads++
			status = http.StatusServiceUnavailable
		case "/peer-document":
			body = string(responseBytes)
		default:
			return nil, fmt.Errorf("unexpected fixture path %s", req.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	carrier, err := NewVKDocsCarrier(VKDocsConfig{Token: "fixture", BaseURL: base, HTTPClient: doer})
	if err != nil {
		t.Fatal(err)
	}
	mailbox := Endpoint{Address: "2000000001"}
	if err := carrier.Write(t.Context(), mailbox, request); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{request.ID, ""} {
		got, err := carrier.Read(WithVKDocsEnvelopeFilter(t.Context(), filter), mailbox, "20")
		if err != nil || got.Cursor != "22" || len(got.Envelopes) != 1 || string(got.Envelopes[0].Payload) != "response" || ownDownloads != 0 {
			t.Fatalf("peer response was held behind the local document: cursor=%v downloads=%d err=%v", got.Cursor, ownDownloads, err)
		}
	}
	otherMailbox := Endpoint{Address: "2000000002"}
	other, err := carrier.Read(WithVKDocsEnvelopeFilter(t.Context(), request.ID), otherMailbox, "20")
	if err != nil || other.Cursor != "20" || ownDownloads != 1 {
		t.Fatalf("message from another mailbox was skipped: cursor=%v downloads=%d err=%v", other.Cursor, ownDownloads, err)
	}
	generic, err := carrier.Read(t.Context(), mailbox, "20")
	if err != nil || generic.Cursor != "20" || ownDownloads != 2 {
		t.Fatalf("ordinary carrier read changed: cursor=%v downloads=%d err=%v", generic.Cursor, ownDownloads, err)
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
