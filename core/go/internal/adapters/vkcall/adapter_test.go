package vkcall

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"whitelist-bypass/relay/tunnel"
	upstream "whitelist-bypass/relay/vkcall"

	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

func TestCreatorStartsTunnelFromFreshVKCall(t *testing.T) {
	p := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"cookie": "test-cookie", "peer_id": "2000000001"},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{"vp8_fps": 4, "vp8_batch": 2, "tunnel_mode": "video"},
	})
	client := &fakeCallClient{create: &upstream.CallInfo{
		JoinLink:   "https://vk.ru/call/created",
		OKJoinLink: "ok-created",
		SessionKey: "session", ApplicationKey: "app", APIBaseURL: "https://call.example/fb.do", AnonymToken: "anon",
	}}
	joiner := &fakeJoiner{tunnel: &fakeTunnel{}, paramsReady: make(chan string, 1)}
	p.callClientFactory = func(sessionConfig) (callClient, error) { return client, nil }
	p.joinerFactory = func(onConnected func(tunnel.DataTunnel)) vkJoiner { joiner.onConnected = onConnected; return joiner }

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	addr, err := p.CreateAndStartEgress(ctx)
	if err != nil {
		t.Fatalf("CreateAndStartEgress: %v", err)
	}
	if addr != "https://vk.ru/call/created" {
		t.Fatalf("address = %q", addr)
	}
	if client.createCookie != "test-cookie" || client.createPeerID != "2000000001" {
		t.Fatalf("create args = cookie=%q peer=%q", client.createCookie, client.createPeerID)
	}
	var encoded string
	select {
	case encoded = <-joiner.paramsReady:
	case <-time.After(time.Second):
		t.Fatal("joiner did not receive parameters")
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(encoded), &params); err != nil {
		t.Fatalf("joiner params: %v", err)
	}
	if params["joinLink"] != "ok-created" || params["vp8Fps"] != float64(4) || params["vp8Batch"] != float64(2) {
		t.Fatalf("joiner params = %#v", params)
	}
	if p.DataTunnel() == nil {
		t.Fatal("DataTunnel is nil after connection")
	}
}

func TestCreatorJoinsConfiguredExistingVKCall(t *testing.T) {
	p := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{
			"cookie":    "test-cookie",
			"join_link": "https://vk.ru/call/existing",
		},
	})
	client := &fakeCallClient{join: &upstream.CallInfo{
		JoinLink: "https://vk.ru/call/existing", OKJoinLink: "ok-existing",
		SessionKey: "session", ApplicationKey: "app", APIBaseURL: "https://call.example/fb.do", AnonymToken: "anon",
	}}
	joiner := &fakeJoiner{tunnel: &fakeTunnel{}}
	p.callClientFactory = func(sessionConfig) (callClient, error) { return client, nil }
	p.joinerFactory = func(onConnected func(tunnel.DataTunnel)) vkJoiner { joiner.onConnected = onConnected; return joiner }

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	addr, err := p.CreateAndStartEgress(ctx)
	if err != nil {
		t.Fatalf("CreateAndStartEgress: %v", err)
	}
	if addr != "https://vk.ru/call/existing" {
		t.Fatalf("address = %q", addr)
	}
	if client.joinCookie != "test-cookie" || client.joinLink != addr {
		t.Fatalf("join args = cookie=%q link=%q", client.joinCookie, client.joinLink)
	}
	if client.createPeerID != "" {
		t.Fatalf("unexpected fresh-call request for peer %q", client.createPeerID)
	}
}

func TestCreatorPublishesExistingLinkBeforePeerConnects(t *testing.T) {
	p := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{
			"cookie":    "test-cookie",
			"join_link": "https://vk.ru/call/existing",
		},
	})
	p.callClientFactory = func(sessionConfig) (callClient, error) {
		return &fakeCallClient{join: &upstream.CallInfo{
			JoinLink: "https://vk.ru/call/existing", OKJoinLink: "ok-existing",
			SessionKey: "session", ApplicationKey: "app", APIBaseURL: "https://call.example/fb.do", AnonymToken: "anon",
		}}, nil
	}
	joiner := &fakeJoiner{}
	p.joinerFactory = func(onConnected func(tunnel.DataTunnel)) vkJoiner {
		joiner.onConnected = onConnected
		return joiner
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	started := time.Now()
	addr, err := p.CreateAndStartEgress(ctx)
	if err != nil {
		t.Fatalf("CreateAndStartEgress: %v", err)
	}
	if addr != "https://vk.ru/call/existing" {
		t.Fatalf("address = %q", addr)
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("CreateAndStartEgress waited %s for a peer tunnel", elapsed)
	}
	if p.DataTunnel() != nil {
		t.Fatal("node should publish the link before a peer connects")
	}
}

func TestJoinerUsesAddressAndForwardsData(t *testing.T) {
	p := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"cookie": "test-cookie"},
		Endpoints:   map[string]string{},
		Settings:    map[string]any{},
	})
	client := &fakeCallClient{join: &upstream.CallInfo{
		OKJoinLink: "ok-joined", SessionKey: "session", ApplicationKey: "app", APIBaseURL: "https://call.example/fb.do", AnonymToken: "anon",
	}}
	dt := &fakeTunnel{}
	joiner := &fakeJoiner{tunnel: dt}
	p.callClientFactory = func(sessionConfig) (callClient, error) { return client, nil }
	p.joinerFactory = func(onConnected func(tunnel.DataTunnel)) vkJoiner { joiner.onConnected = onConnected; return joiner }

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.StartEgressAddr(ctx, "https://vk.ru/call/join-token"); err != nil {
		t.Fatalf("StartEgressAddr: %v", err)
	}
	if client.joinLink != "https://vk.ru/call/join-token" || client.joinCookie != "test-cookie" {
		t.Fatalf("join args = link=%q cookie=%q", client.joinLink, client.joinCookie)
	}
	payload := []byte("vkcall-data")
	if err := p.Send(ctx, payload); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if string(dt.sent) != string(payload) {
		t.Fatalf("sent = %q", dt.sent)
	}
	dt.onData([]byte("reply"))
	got, err := p.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if string(got) != "reply" {
		t.Fatalf("received = %q", got)
	}
}

func TestCreatorRejectsMissingCallInfo(t *testing.T) {
	p := configuredProvider(t, provider.ProviderConfig{
		Credentials: map[string]string{"cookie": "test-cookie", "peer_id": "2000000001"},
	})
	p.callClientFactory = func(sessionConfig) (callClient, error) { return &fakeCallClient{}, nil }

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := p.CreateAndStartEgress(ctx); err == nil {
		t.Fatal("CreateAndStartEgress accepted missing call info")
	}
}

func configuredProvider(t *testing.T, cfg provider.ProviderConfig) *Provider {
	t.Helper()
	p := &Provider{}
	if err := p.Configure(cfg); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return p
}

type fakeCallClient struct {
	join, create               *upstream.CallInfo
	joinCookie, joinLink       string
	createCookie, createPeerID string
}

func (f *fakeCallClient) JoinExisting(_ context.Context, cookie, link string) (*upstream.CallInfo, error) {
	f.joinCookie, f.joinLink = cookie, link
	return f.join, nil
}

func (f *fakeCallClient) CreateAndJoin(_ context.Context, cookie, peerID string) (*upstream.CallInfo, error) {
	f.createCookie, f.createPeerID = cookie, peerID
	return f.create, nil
}

type fakeJoiner struct {
	onConnected func(tunnel.DataTunnel)
	tunnel      tunnel.DataTunnel
	params      string
	paramsReady chan string
	closed      bool
}

func (f *fakeJoiner) RunWithParams(params string) {
	f.params = params
	if f.onConnected != nil {
		f.onConnected(f.tunnel)
	}
	if f.paramsReady != nil {
		f.paramsReady <- params
	}
}

func (f *fakeJoiner) Close() { f.closed = true }

type fakeTunnel struct {
	sent   []byte
	onData func([]byte)
}

func (f *fakeTunnel) SendData(data []byte)            { f.sent = append([]byte(nil), data...) }
func (f *fakeTunnel) SetOnData(callback func([]byte)) { f.onData = callback }
func (f *fakeTunnel) SetOnClose(func())               {}
func (f *fakeTunnel) Reconfigure(int, int)            {}
