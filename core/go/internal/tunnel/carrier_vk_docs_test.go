package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

const vkDocsTestTunnelID = "vk-docs-test-stream"

func TestVKDocsClientCombinesFragmentsBeforeOriginDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 15 * time.Second
		ctx, cancel := context.WithTimeout(t.Context(), timeout)
		defer cancel()
		client, server := net.Pipe()
		defer client.Close()
		carrier := delayedVKDocumentCarrier{
			Carrier: newTestMemoryCarrier(t, "vk-docs-client-burst"),
			sent:    make(chan fabric.Envelope, 4),
		}
		binding := policy.CarrierBinding{Carrier: carrier, Endpoint: carriers.Endpoint{Address: "fixture-peer"}}
		tun := NewCarrierTunnel("fixture-client", nil)
		done := make(chan struct{})
		go func() {
			defer close(done)
			tun.writeLoop(ctx, vkDocsTestTunnelID, server, binding)
		}()

		// The first TLS flight already used most of the origin window.
		time.Sleep(9 * time.Second)
		fragments := []string{
			strings.Repeat("a", 80),
			strings.Repeat("b", 46),
			strings.Repeat("c", 160),
		}
		go func() {
			for _, data := range fragments {
				if _, err := io.WriteString(client, data); err != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()

		var readback strings.Builder
		want := strings.Join(fragments, "")
		documents := 0
		for readback.Len() < len(want) {
			select {
			case env := <-carrier.sent:
				if env.PayloadType != tunnelData || env.Sequence != uint64(documents+1) {
					t.Fatal("malformed stream data")
				}
				readback.Write(env.Payload)
				documents++
			case <-ctx.Done():
				t.Fatalf("origin window expired after %d of %d bytes", readback.Len(), len(want))
			}
		}
		if readback.String() != want || documents != 1 {
			t.Fatalf("got %d bytes in %d documents, want one ordered %d-byte document", readback.Len(), documents, len(want))
		}
		_ = client.Close()
		<-done
	})
}

type delayedVKDocumentCarrier struct {
	carriers.Carrier
	sent chan fabric.Envelope
}

func (c delayedVKDocumentCarrier) Write(ctx context.Context, _ carriers.Endpoint, env fabric.Envelope) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2500 * time.Millisecond):
		c.sent <- env
		return nil
	}
}

func TestSessionSealedVKDocsWriteOverlapsStalledUpload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var grants, sent int
		stallCanceled := make(chan struct{})
		doer := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/docs.getMessagesUploadServer":
				grants++
				return vkDocsTestResponse(fmt.Sprintf(`{"response":{"upload_url":"https://vk.test/upload-%d"}}`, grants)), nil
			case "/upload-1":
				<-req.Context().Done()
				close(stallCanceled)
				return nil, req.Context().Err()
			case "/upload-2":
				select {
				case <-time.After(2 * time.Second):
					return vkDocsTestResponse(`{"file":"uploaded-fixture"}`), nil
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			case "/docs.save":
				return vkDocsTestResponse(`{"response":{"doc":{"owner_id":100,"id":200}}}`), nil
			case "/messages.send":
				sent++
				return vkDocsTestResponse(`{"response":123}`), nil
			default:
				return nil, errors.New("unexpected VK fixture request")
			}
		})
		carrier, err := carriers.NewVKDocsCarrier(carriers.VKDocsConfig{Token: "fixture", BaseURL: "https://vk.test", HTTPClient: doer})
		if err != nil {
			t.Fatal(err)
		}
		cipher, err := fabric.NewSessionCipher(fabric.DeriveBootstrapSecretKey("fixture"))
		if err != nil {
			t.Fatal(err)
		}
		tun := NewCarrierTunnel("fixture-node", nil)
		envelope := fabric.NewEnvelope(vkDocsTestTunnelID, fabric.TrafficEgress, tunnelData, []byte("TLS fixture"))
		envelope.SessionID = "fixture-session"
		tun.SetSessionCipher(envelope.SessionID, cipher)
		ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
		defer cancel()
		if err := tun.writeEnvelope(ctx, carrier, carriers.Endpoint{Address: "2000000001"}, envelope); err != nil {
			t.Fatalf("send sealed TLS data within the origin window: %v", err)
		}
		if grants != 2 || sent != 1 {
			t.Fatalf("grants = %d, published messages = %d, want 2 and 1", grants, sent)
		}
		select {
		case <-stallCanceled:
		default:
			t.Fatal("the stalled upload was left running after publishing")
		}
	})
}

func TestVKDocsTunnelResumesAfterHistoryThrottle(t *testing.T) {
	t.Run("VK API rate limit", func(t *testing.T) {
		testVKDocsTunnelResumes(t, func() *http.Response {
			return vkDocsTestResponse(`{"error":{"error_code":9,"retry_after":1}}`)
		})
	})
	t.Run("HTTP 429", func(t *testing.T) {
		testVKDocsTunnelResumes(t, func() *http.Response {
			response := vkDocsTestResponse(`{"detail":"fixture-private-field"}`)
			response.StatusCode = http.StatusTooManyRequests
			response.Header = http.Header{"Retry-After": {"2"}}
			return response
		})
	})
}

func testVKDocsTunnelResumes(t *testing.T, throttleResponse func() *http.Response) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		const unrelated = `{"id":6,"text":"wtbulk1 another-stream","attachments":[{"type":"doc","doc":{"url":"https://vk.test/unrelated"}}]}`
		const first = `{"id":7,"text":"wtbulk1 vk-docs-test-stream","attachments":[{"type":"doc","doc":{"url":"https://vk.test/first"}}]}`
		const second = `{"id":8,"attachments":[{"type":"doc","doc":{"url":"https://vk.test/second"}}]}`
		var historyCalls int
		var unrelatedDownloads int
		doer := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/method/messages.getHistory":
				historyCalls++
				switch historyCalls {
				case 1:
					return vkDocsTestResponse(`{"response":{"items":[` + first + `,` + unrelated + `]}}`), nil
				case 2, 3, 4:
					return throttleResponse(), nil
				default:
					return vkDocsTestResponse(`{"response":{"items":[` + second + `,` + first + `,` + unrelated + `]}}`), nil
				}
			case "/unrelated":
				unrelatedDownloads++
				return nil, fmt.Errorf("unrelated document must not be downloaded")
			case "/first", "/second":
				envelope := fabric.NewEnvelope(vkDocsTestTunnelID, fabric.TrafficEgress, tunnelData, []byte(req.URL.Path))
				envelope.Source = "fixture-node"
				payload, err := json.Marshal(envelope)
				if err != nil {
					return nil, err
				}
				return vkDocsTestResponse(string(payload)), nil
			default:
				return nil, fmt.Errorf("unexpected synthetic request")
			}
		})

		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		watch := &egressResponseWatch{}
		watch.noteRequest(time.Now())
		conn := startVKDocsTestReadLoop(t, ctx, doer, watch)
		for _, want := range []string{"/first", "/second"} {
			buf := make([]byte, len(want))
			if _, err := io.ReadFull(conn, buf); err != nil {
				t.Fatalf("stream closed before delivering the next document: %v", err)
			}
			if string(buf) != want {
				t.Fatal("resumed poll replayed an old document or delivered unexpected bytes")
			}
			if want == "/first" {
				time.Sleep(time.Nanosecond)
				watch.noteRequest(time.Now())
			}
		}
		if cause := conn.FailureCause(); cause != nil {
			t.Fatalf("recovered stream reported a failure: %v", cause)
		}
		if unrelatedDownloads != 0 {
			t.Fatalf("stream downloaded %d unrelated documents", unrelatedDownloads)
		}
	})
}

func TestVKDocsTunnelBoundsHistoryThrottle(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pendingFor   time.Duration
		blockOnCall  int
		cancelInRead bool
	}{
		{name: "idle with repeated throttle"},
		{name: "pending response bounds first poll retry", pendingFor: 20 * time.Second, blockOnCall: 2},
		{name: "pending response bounds in-flight recovery poll", pendingFor: 20 * time.Second, blockOnCall: 4},
		{name: "parent cancellation stops in-flight poll", blockOnCall: 4, cancelInRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				started := time.Now()
				watch := &egressResponseWatch{}
				deadline := started.Add(egressReadInterval + mailboxEgressResponseLivenessTimeout)
				if tc.pendingFor > 0 {
					requestedAt := started.Add(-tc.pendingFor)
					watch.noteRequest(requestedAt)
					deadline = requestedAt.Add(mailboxEgressResponseLivenessTimeout)
				}
				var calls int
				doer := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
					calls++
					if calls == tc.blockOnCall {
						if tc.cancelInRead {
							cancel()
						}
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					return vkDocsTestResponse(`{"error":{"error_code":9,"retry_after":1,"error_msg":"private-fixture-field"}}`), nil
				})
				conn := startVKDocsTestReadLoop(t, ctx, doer, watch)
				if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
					t.Fatalf("stream close: want EOF, got %v", err)
				}
				if calls < max(tc.blockOnCall, 2) {
					t.Fatal("stream closed before attempting recovery")
				}
				cause := conn.FailureCause()
				if tc.cancelInRead {
					if ctx.Err() != context.Canceled || cause != nil {
						t.Fatalf("parent cancellation reported a carrier failure: %v", cause)
					}
					return
				}
				if ctx.Err() != nil || !time.Now().Equal(deadline) {
					t.Fatalf("recovery did not end at its own deadline: elapsed %s, want %s", time.Since(started), deadline.Sub(started))
				}
				var throttle *carriers.VKHistoryThrottleError
				if !errors.As(cause, &throttle) || throttle.Code != 9 || strings.Contains(cause.Error(), "private-fixture-field") {
					t.Fatal("recovery expiry must report a typed, redacted VK history throttle")
				}
			})
		})
	}
}

func TestVKDocsTunnelCloseCancelsInFlightPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		stopped := make(chan struct{})
		doer := vkDocsTestHTTPDoer(func(req *http.Request) (*http.Response, error) {
			close(entered)
			<-req.Context().Done()
			close(stopped)
			return nil, req.Context().Err()
		})
		conn := startVKDocsTestReadLoop(t, t.Context(), doer, &egressResponseWatch{})
		<-entered
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Fatal("explicit close left the VK history poll running")
		}
		if cause := conn.FailureCause(); cause != nil {
			t.Fatalf("explicit close reported a carrier failure: %v", cause)
		}
	})
}

func TestVKDocsTunnelClosesOnPermanentHistoryErrorDuringRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		var calls int
		doer := vkDocsTestHTTPDoer(func(*http.Request) (*http.Response, error) {
			calls++
			if calls <= 3 {
				return vkDocsTestResponse(`{"error":{"error_code":9,"retry_after":1}}`), nil
			}
			return vkDocsTestResponse(`{"error":{"error_code":5,"error_msg":"private-fixture-field"}}`), nil
		})
		conn := startVKDocsTestReadLoop(t, ctx, doer, &egressResponseWatch{})
		if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("stream close: want EOF, got %v", err)
		}
		var throttle *carriers.VKHistoryThrottleError
		cause := conn.FailureCause()
		if calls != 4 || cause == nil || errors.As(cause, &throttle) ||
			!strings.Contains(cause.Error(), "error_code=5") || strings.Contains(cause.Error(), "private-fixture-field") {
			t.Fatal("permanent VK history error must immediately fail the recovering stream")
		}
	})
}

type vkDocsTestHTTPDoer func(*http.Request) (*http.Response, error)

func (f vkDocsTestHTTPDoer) Do(req *http.Request) (*http.Response, error) { return f(req) }

func vkDocsTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

func startVKDocsTestReadLoop(t *testing.T, ctx context.Context, doer vkDocsTestHTTPDoer, watch *egressResponseWatch) *carrierTunnelConn {
	t.Helper()
	carrier, err := carriers.NewVKDocsCarrier(carriers.VKDocsConfig{
		Token: "fixture", BaseURL: "https://vk.test/method", HTTPClient: doer,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: carriers.Endpoint{Address: "2000000001"}}
	client, server := net.Pipe()
	readCtx, stopReader := context.WithCancel(ctx)
	conn := &carrierTunnelConn{Conn: client, failure: &carrierFailureState{}, stopReader: stopReader}
	t.Cleanup(func() { _ = conn.Close() })
	tun := NewCarrierTunnel("fixture-client", nil)
	go tun.readLoopWithFailure(readCtx, vkDocsTestTunnelID, server, binding, conn.failure, watch)
	return conn
}

func TestVKDocsHTTPSExitWaitsForFirstData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	proxyTarget := make(chan string, 1)
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		in := bufio.NewReader(conn)
		request, err := http.ReadRequest(in)
		if err != nil {
			serverDone <- err
			return
		}
		proxyTarget <- request.Method + " " + request.Host
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			serverDone <- err
			return
		}
		buf := make([]byte, len("first bytes"))
		if _, err := io.ReadFull(in, buf); err != nil {
			serverDone <- err
			return
		}
		if string(buf) != "first bytes" {
			serverDone <- errors.New("unexpected first bytes")
			return
		}
		serverDone <- nil
	}()

	carrier := newTestMemoryCarrier(t, "vk-docs-first-data")
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: carriers.Endpoint{Address: "mem://first-data"}}
	node := NewCarrierTunnel("node", nil)
	node.SetProxyURL("http://" + listener.Addr().String())
	t.Cleanup(node.CloseAll)
	node.SetPacketSession("session", "client", time.Now().Add(time.Minute))
	node.handleOpen(t.Context(), binding, "first-stream", "session", "target.invalid:443", "client")
	select {
	case <-proxyTarget:
		t.Fatal("opened the target before receiving any stream data")
	case err := <-serverDone:
		t.Fatalf("proxy failed before receiving stream data: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	node.handleData("first-stream", []byte("first bytes"))
	select {
	case got := <-proxyTarget:
		if got != "CONNECT target.invalid:443" {
			t.Fatalf("unexpected proxy target: %s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first stream data did not open the proxy")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first stream data did not reach the proxy")
	}
}

func TestVKDocsHTTPSExitSessionReleaseClosesOwnedStreams(t *testing.T) {
	carrier := newTestMemoryCarrier(t, "vk-docs-release")
	binding := policy.CarrierBinding{Carrier: carrier, Endpoint: carriers.Endpoint{Address: "mem://release"}}
	node := NewCarrierTunnel("node", nil)
	t.Cleanup(node.CloseAll)
	expires := time.Now().Add(time.Minute)
	node.SetPacketSession("closing-session", "client", expires)
	node.SetPacketSession("other-session", "other-client", expires)
	node.handleOpen(t.Context(), binding, "closing-stream", "closing-session", "target.invalid:443", "client")
	node.handleOpen(t.Context(), binding, "other-stream", "other-session", "target.invalid:443", "other-client")
	target, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	if err := peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	streamCtx, cancelStream := context.WithCancel(t.Context())
	t.Cleanup(cancelStream)
	node.mu.Lock()
	node.active["open-stream"] = &tunnelConn{sessionID: "closing-session", targetConn: target, cancel: cancelStream}
	node.mu.Unlock()

	node.ClearSessionCipher("closing-session")

	if streamCtx.Err() != context.Canceled {
		t.Error("ended session still owns a running stream")
	}
	var data [1]byte
	if _, err := peer.Read(data[:]); !errors.Is(err, io.EOF) {
		t.Errorf("released stream target read = %v, want EOF", err)
	}
	node.handleOpen(t.Context(), binding, "late-stream", "closing-session", "target.invalid:443", "client")
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.active["closing-stream"] != nil {
		t.Fatal("ended session still has a pending dial")
	}
	if node.active["open-stream"] != nil || node.active["late-stream"] != nil {
		t.Fatal("ended session still has an open or newly registered stream")
	}
	if node.active["other-stream"] == nil {
		t.Fatal("release canceled a different session")
	}
}

func TestCarrierSessionReleaseUnblocksTargetWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		node := NewCarrierTunnel("node", nil)
		target, peer := net.Pipe()
		defer peer.Close()
		_, cancel := context.WithCancel(t.Context())
		node.active["stream"] = &tunnelConn{sessionID: "session", targetConn: target, cancel: cancel}
		done := make(chan struct{})
		go func() {
			node.handleData("stream", []byte("fixture"))
			close(done)
		}()
		synctest.Wait()

		node.ClearSessionCipher("session")

		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("target writer remained blocked after session release")
		}
	})
}
