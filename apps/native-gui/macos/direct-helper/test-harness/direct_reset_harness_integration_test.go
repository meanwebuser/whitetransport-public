package main

import (
	"bytes"
	"testing"
	"time"
)

func TestInstrumentedSOCKSMapsNumericTestNetTargetToNonceHTTP(t *testing.T) {
	nonce := "local-only-direct-reset-nonce"
	localHTTP, closeHTTP, err := startNonceHTTP(nonce)
	if err != nil {
		t.Fatalf("start nonce HTTP: %v", err)
	}
	defer closeHTTP()
	socks, err := startInstrumentedSOCKS(localHTTP)
	if err != nil {
		t.Fatalf("start instrumented SOCKS: %v", err)
	}
	defer socks.Close()

	response, err := dialSOCKSAndReadHTTP(socks.Addr(), testNetTarget, []byte("GET /nonce HTTP/1.1\r\nHost: 198.51.100.77\r\nConnection: close\r\n\r\n"), 5*time.Second)
	if err != nil {
		t.Fatalf("SOCKS mapped HTTP request: %v", err)
	}
	if !bytes.Contains(response, []byte(nonce)) {
		t.Fatalf("mapped response did not contain nonce %q: %q", nonce, response)
	}
	greeting, connect, atyp, target, code := socks.trace.snapshot()
	if !greeting || !connect || atyp != "ipv4" || target != testNetTarget || code != 0 {
		t.Fatalf("unexpected SOCKS trace greeting=%v connect=%v atyp=%q target=%q code=%d", greeting, connect, atyp, target, code)
	}
}

func TestRunRequiresExplicitMacRootAcceptance(t *testing.T) {
	result := Run(Options{})
	if result.Status != "not-run" || result.Exit != 2 || result.ProductionCredentialsUsed || result.InternetRequired {
		t.Fatalf("unsafe default run contract changed: %#v", result)
	}
}
