package runtime

import (
	"strings"
	"testing"
)

func testAuthSessionBroker(t *testing.T) *AuthSessionBroker {
	t.Helper()
	broker, err := NewAuthSessionBroker([]AuthProviderPolicy{{
		Platform:        "wbstream",
		LoginURL:        "https://login.example.test/sign-in",
		AllowedHosts:    []string{"login.example.test", "app.example.test"},
		CompletionHosts: []string{"app.example.test"},
	}})
	if err != nil {
		t.Fatalf("NewAuthSessionBroker: %v", err)
	}
	return broker
}

func TestAuthSessionBrokerStartsAllowlistedProviderSession(t *testing.T) {
	plan, err := testAuthSessionBroker(t).Start("wbstream")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if plan.Platform != "wbstream" || plan.LoginURL != "https://login.example.test/sign-in" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if got, want := strings.Join(plan.AllowedHosts, ","), "app.example.test,login.example.test"; got != want {
		t.Fatalf("allowed hosts = %q, want %q", got, want)
	}
}

func TestAuthSessionBrokerRejectsNavigationOutsideProviderAllowlist(t *testing.T) {
	broker := testAuthSessionBroker(t)
	if _, err := broker.ValidateNavigation("wbstream", "https://evil.example.test/complete"); err == nil {
		t.Fatal("expected navigation outside provider allowlist to fail")
	}
	if _, err := broker.ValidateNavigation("wbstream", "http://login.example.test/sign-in"); err == nil {
		t.Fatal("expected insecure navigation to fail")
	}
}

func TestAuthSessionBrokerCompletesOnlyAtDeclaredCallback(t *testing.T) {
	broker := testAuthSessionBroker(t)
	credential := ClientCredential{Platform: "wbstream", Token: "example-session"}

	if _, _, err := broker.Complete("wbstream", "https://login.example.test/done", credential); err == nil {
		t.Fatal("expected non-callback completion to fail")
	}

	stored, status, err := broker.Complete("wbstream", "https://app.example.test/complete", credential)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if stored.Platform != "wbstream" || stored.Token != "example-session" {
		t.Fatalf("unexpected normalized credential: %+v", stored)
	}
	if status.Platform != "wbstream" || status.State != AuthSessionStateReady {
		t.Fatalf("unexpected status: %+v", status)
	}
	if strings.Contains(status.Message, credential.Token) {
		t.Fatalf("status leaked credential material: %+v", status)
	}
}

func TestAuthSessionBrokerRejectsMismatchedOrEmptyCredential(t *testing.T) {
	broker := testAuthSessionBroker(t)
	callback := "https://app.example.test/complete"

	if _, _, err := broker.Complete("wbstream", callback, ClientCredential{Platform: "dion", Token: "example-session"}); err == nil {
		t.Fatal("expected mismatched platform to fail")
	}
	if _, _, err := broker.Complete("wbstream", callback, ClientCredential{Platform: "wbstream"}); err == nil {
		t.Fatal("expected empty credential to fail")
	}
}
