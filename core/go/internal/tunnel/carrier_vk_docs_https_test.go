package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestVKDocsTunnelHTTPSWithProviderFailures(t *testing.T) {
	const clientID = "fixture-client"
	const nodeID = "fixture-node"
	const sessionID = "fixture-session"
	const wantBody = "VK Documents delivered this HTTPS response"
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vk-docs" || r.Header.Get("X-Session-Test") != sessionID {
			http.Error(w, "unexpected HTTPS request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, wantBody)
	}))
	defer origin.Close()
	address := origin.Listener.Addr().String()
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	mailbox := &vkDocsTestMailbox{documents: make(map[string]vkDocsTestDocument)}
	endpoint := carriers.Endpoint{ID: "vk-docs-egress", Carrier: carriers.CarrierVKDocs1024, Address: "2000000001"}
	newTunnel := func(identity, peer string) (*CarrierTunnel, map[string]policy.CarrierBinding) {
		adapter, err := carriers.NewVKDocsCarrier(carriers.VKDocsConfig{
			Token: identity, BaseURL: "https://vk.test/method", DescriptorID: endpoint.Carrier, HTTPClient: mailbox,
		})
		if err != nil {
			t.Fatal(err)
		}
		bindings := map[string]policy.CarrierBinding{endpoint.Carrier: {Carrier: adapter, Endpoint: endpoint}}
		plane := NewCarrierTunnel(identity, bindings)
		cipher, err := fabric.NewSessionCipher(fabric.DeriveBootstrapSecretKey("vk-docs-local-fixture"))
		if err != nil {
			t.Fatal(err)
		}
		plane.SetSessionCipher(sessionID, cipher)
		plane.SetPacketSession(sessionID, peer, time.Now().Add(time.Minute))
		return plane, bindings
	}
	client, _ := newTunnel(clientID, nodeID)
	node, bindings := newTunnel(nodeID, clientID)
	reply := carriers.NewMemoryCarrier("vk-docs-test-control")
	replyEndpoint := carriers.Endpoint{Address: "memory://vk-docs-test-control"}
	node.SetEgressFailureReply(sessionID, reply, replyEndpoint)
	nodeDone := make(chan struct{})
	go func() {
		defer close(nodeDone)
		_ = node.ServeEgress(ctx, bindings)
	}()
	defer func() {
		cancel()
		<-nodeDone
	}()

	open := func() net.Conn {
		t.Helper()
		conn, err := client.DialContextForSession(ctx, sessionID, endpoint, address)
		if err != nil {
			t.Fatalf("upload stream open: %v", err)
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		return conn
	}
	secure := func(conn net.Conn) *tls.Conn {
		return tls.Client(conn, &tls.Config{ServerName: "127.0.0.1", RootCAs: roots, MinVersion: tls.VersionTLS12})
	}
	func() {
		conn := secure(open())
		defer conn.Close()
		if err := conn.HandshakeContext(ctx); err != nil {
			t.Fatalf("TLS handshake after upload and attachment retries: %v", err)
		}
		for _, fragment := range []string{"GET /vk-docs HTTP/1.1\r\n", "Host: fixture.test\r\n", "X-Session-Test: " + sessionID + "\r\n\r\n"} {
			if _, err := io.WriteString(conn, fragment); err != nil {
				t.Fatalf("write fragmented HTTPS request: %v", err)
			}
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("read HTTPS response: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK || string(body) != wantBody {
			t.Fatalf("HTTPS response: code=%d bytes=%d err=%v", resp.StatusCode, len(body), err)
		}
	}()

	mailbox.mu.Lock()
	uploadFailed, downloadFailed := mailbox.firstStreamID != "", mailbox.downloadFailed
	mailbox.rejectNodeUpload = true
	mailbox.mu.Unlock()
	if !uploadFailed || !downloadFailed {
		t.Fatal("HTTPS succeeded without exercising the initial upload and attachment failures")
	}

	stream := open().(*carrierTunnelConn)
	defer stream.Close()
	notice := make(chan fabric.Envelope, 1)
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				result, _ := reply.Read(ctx, replyEndpoint, "")
				if len(result.Envelopes) > 0 {
					env := result.Envelopes[0]
					client.HandleEgressStreamFailure(env)
					notice <- env
					return
				}
			}
		}
	}()
	if err := secure(stream).HandshakeContext(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("permanent return upload failure must close this TLS stream via control, got %v", err)
	}
	if !errors.Is(stream.FailureCause(), session.ErrEgressReturnCarrier) {
		t.Fatalf("client lost the causal return error: %v", stream.FailureCause())
	}
	select {
	case env := <-notice:
		if env.SessionID != sessionID || env.PayloadType != session.PayloadEgressStreamFailure {
			t.Fatal("failure notice was not scoped to the active session")
		}
	case <-ctx.Done():
		t.Fatal("node failed to issue a control notice")
	}
	node.ClearSessionCipher(sessionID)
	node.mu.Lock()
	exits := len(node.active)
	node.mu.Unlock()
	if exits != 0 {
		t.Fatal("node retained an exit stream after releasing the session")
	}
}

type vkDocsTestDocument struct {
	body []byte
	env  fabric.Envelope
}

// vkDocsTestMailbox implements the provider boundary, including a separate
// grant, multipart upload, save, publish and readback for every document.
type vkDocsTestMailbox struct {
	mu               sync.Mutex
	grant            int
	documents        map[string]vkDocsTestDocument
	messages         []json.RawMessage
	firstStreamID    string
	downloadFailed   bool
	rejectNodeUpload bool
}

func (m *vkDocsTestMailbox) Do(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := req.URL.Path
	switch {
	case path == "/method/docs.getMessagesUploadServer":
		m.grant++
		return vkDocsTestResponse(fmt.Sprintf(`{"response":{"upload_url":"https://vk.test/upload/%d"}}`, m.grant)), nil
	case strings.HasPrefix(path, "/upload/"):
		id := strings.TrimPrefix(path, "/upload/")
		multipart, err := req.MultipartReader()
		if err != nil {
			return nil, err
		}
		file, err := multipart.NextPart()
		if err != nil {
			return nil, err
		}
		defer file.Close()
		var document vkDocsTestDocument
		document.body, err = io.ReadAll(file)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(document.body, &document.env); err != nil {
			return nil, err
		}
		env := document.env
		if env.PayloadType != "encrypted" || env.TrafficClass != fabric.TrafficEgress {
			return nil, errors.New("provider saw an unsealed or misclassified stream document")
		}
		if env.Source == "fixture-client" && env.Sequence == 1 && m.firstStreamID == "" {
			m.firstStreamID = env.ID
			resp := vkDocsTestResponse(`{}`)
			resp.StatusCode = http.StatusGatewayTimeout
			return resp, nil
		}
		if env.Source == "fixture-node" && env.Sequence > 0 && m.rejectNodeUpload && env.ID != m.firstStreamID {
			resp := vkDocsTestResponse(`{}`)
			resp.StatusCode = http.StatusForbidden
			return resp, nil
		}
		m.documents[id] = document
		return vkDocsTestResponse(fmt.Sprintf(`{"file":%q}`, id)), nil
	case path == "/method/docs.save":
		if err := req.ParseForm(); err != nil {
			return nil, err
		}
		id, err := strconv.Atoi(req.PostForm.Get("file"))
		if err != nil || m.documents[strconv.Itoa(id)].body == nil {
			return nil, errors.New("saving an upload that did not complete")
		}
		return vkDocsTestResponse(fmt.Sprintf(`{"response":{"doc":{"owner_id":1,"id":%d}}}`, id)), nil
	case path == "/method/messages.send":
		if err := req.ParseForm(); err != nil {
			return nil, err
		}
		id := strings.TrimPrefix(req.PostForm.Get("attachment"), "doc1_")
		if m.documents[id].body == nil || req.PostForm.Get("peer_id") != "2000000001" {
			return nil, errors.New("publishing outside the configured mailbox or without a document")
		}
		messageID := len(m.messages) + 1
		m.messages = append(m.messages, json.RawMessage(fmt.Sprintf(`{"id":%d,"text":%q,"attachments":[{"type":"doc","doc":{"url":"https://vk.test/document/%s"}}]}`, messageID, req.PostForm.Get("message"), id)))
		return vkDocsTestResponse(fmt.Sprintf(`{"response":%d}`, messageID)), nil
	case path == "/method/messages.getHistory":
		if m.messages == nil {
			return vkDocsTestResponse(`{"response":{"items":[]}}`), nil
		}
		history, err := json.Marshal(m.messages)
		if err != nil {
			return nil, err
		}
		return vkDocsTestResponse(`{"response":{"items":` + string(history) + `}}`), nil
	case strings.HasPrefix(path, "/document/"):
		doc := m.documents[strings.TrimPrefix(path, "/document/")]
		if doc.env.Source == "fixture-client" && doc.env.Sequence == 1 && doc.env.ID == m.firstStreamID && !m.downloadFailed {
			m.downloadFailed = true
			resp := vkDocsTestResponse(`{}`)
			resp.StatusCode = http.StatusServiceUnavailable
			return resp, nil
		}
		return vkDocsTestResponse(string(doc.body)), nil
	default:
		return nil, fmt.Errorf("unexpected fixture HTTP path %s", path)
	}
}
