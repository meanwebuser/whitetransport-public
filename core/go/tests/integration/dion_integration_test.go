//go:build integration

package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	dionlib "whitelist-bypass/relay/dion"
	"whitelist-bypass/relay/tunnel"
)

func TestIntegrationDIONGuestAuthAndRoomCreate(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, err := dionlib.NewSession(nil)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	info, err := sess.RegisterGuest()
	if err != nil {
		t.Logf("RegisterGuest failed (may be rate-limited): %v", err)
		t.Skip("skip: DION guest auth not available")
	}
	t.Logf("guest auth: user_id=%s name=%s", info.User.ID, info.User.Name)

	event, err := sess.CreateRoom()
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	t.Logf("created room: slug=%s id=%s", event.Slug, event.ID)

	if event.Slug == "" {
		t.Fatal("room slug is empty")
	}

	obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(event.Slug))
	if err != nil {
		t.Fatalf("NewTunnelObfuscator: %v", err)
	}

	call := dionlib.NewCall(dionlib.CallConfig{
		Auth:        sess,
		Event:       event,
		Obfuscator:  obf,
		DisplayName: "IntegrationTest",
		LogFn:       t.Logf,
		Role:        dionlib.RoleCreator,
	})
	defer call.Close()

	tunCh := make(chan tunnel.DataTunnel, 1)
	call.OnConnected = func(tun tunnel.DataTunnel) {
		tunCh <- tun
	}

	if err := call.Start(); err != nil {
		t.Fatalf("Call.Start: %v", err)
	}

	select {
	case tun := <-tunCh:
		t.Logf("DION tunnel connected: %T", tun)
		tun.SendData([]byte("dion-integration-ping"))
		t.Log("sent test payload through DION tunnel")
	case <-time.After(25 * time.Second):
		t.Fatal("DION tunnel connect timeout (25s)")
	case <-ctx.Done():
		t.Fatal("context cancelled")
	}
}

func TestIntegrationDIONAuthenticatedCreateRoom(t *testing.T) {
	if os.Getenv("WT_INTEGRATION") == "" {
		t.Skip("skip: WT_INTEGRATION not set")
	}
	accessToken := loadDIONAccessToken(t)
	cookiesFile := filepath.Join(repoRoot(t), "secrets/production", "dion/dion-cookies.json")
	requireFile(t, cookiesFile)

	sess, err := dionlib.NewSession(nil)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.AccessToken = accessToken

	if err := sess.LoadCookiesFromFile(cookiesFile); err != nil {
		t.Fatalf("LoadCookiesFromFile: %v", err)
	}
	if err := sess.EnsureValidToken(); err != nil {
		t.Fatalf("EnsureValidToken: %v", err)
	}
	t.Logf("authenticated: user_id=%s", sess.UserID)

	event, err := sess.CreateRoom()
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	t.Logf("authenticated room: slug=%s", event.Slug)
	if event.Slug == "" {
		t.Fatal("authenticated room slug is empty")
	}
}
