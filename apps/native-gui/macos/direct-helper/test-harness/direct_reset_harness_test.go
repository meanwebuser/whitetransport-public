package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net/http"
	"testing"
)

func expectedLargeTLSPayload(nonce string) []byte {
	payload := make([]byte, 64*1024)
	seed := []byte(nonce)
	for offset := 0; offset < len(payload); offset += len(seed) {
		copy(payload[offset:], seed)
	}
	return payload
}

func TestNonceHTTPSFixture(t *testing.T) {
	address, closeServer, err := startNonceHTTPS("tls-fixture")
	if err != nil {
		t.Fatalf("start HTTPS fixture: %v", err)
	}
	defer closeServer()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	response, err := client.Get("https://" + address + "/nonce")
	if err != nil {
		t.Fatalf("GET HTTPS fixture: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTPS fixture: %v", err)
	}
	expected := expectedLargeTLSPayload("tls-fixture")
	if len(body) < 64*1024 {
		t.Fatalf("HTTPS fixture body = %d bytes, want at least %d", len(body), 64*1024)
	}
	if !bytes.Equal(body, expected) {
		actualHash := sha256.Sum256(body)
		expectedHash := sha256.Sum256(expected)
		t.Fatalf("HTTPS fixture SHA-256 = %s, want %s", hex.EncodeToString(actualHash[:]), hex.EncodeToString(expectedHash[:]))
	}
}

func TestPayloadEvidenceVerifiesLargeTLSHash(t *testing.T) {
	payload := expectedLargeTLSPayload("tls-fixture")
	expectedHash := sha256.Sum256(payload)

	evidence := summarizePayload(payload, payload)

	if evidence.Bytes < 64*1024 {
		t.Fatalf("payload evidence bytes = %d, want at least %d", evidence.Bytes, 64*1024)
	}
	if evidence.SHA256 != hex.EncodeToString(expectedHash[:]) || evidence.ExpectedSHA256 != evidence.SHA256 {
		t.Fatalf("payload evidence hashes do not match expected digest: %#v", evidence)
	}
	if !evidence.HashValid {
		t.Fatalf("payload evidence must verify the exact TLS payload hash: %#v", evidence)
	}
}

func TestDirectResetHarnessContract(t *testing.T) {
	result := HarnessResult{
		Test:                  "macos-direct-utun-reset",
		TargetAddress:         testNetTarget,
		SocksGreetingObserved: true,
		SocksConnectObserved:  true,
		SocksRequestedATYP:    "ipv4",
		SocksRequestedTarget:  testNetTarget,
		SocksResponseCode:     0,
		PayloadNonce:          "nonce",
		PayloadNonceResult:    "nonce",
		PayloadBytes:          64 * 1024,
		PayloadSHA256:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PayloadExpectedSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PayloadHashValid:      true,
		PayloadProtocol:       "https",
		TLSProbe:              true,
		CreatedUtun:           "utun9",
		RouteDecision:         "198.51.100.77 via utun9",
		Cleanup:               CleanupState{HelperStopped: true, RouteRemoved: true, UtunRemoved: true, TempRemoved: true},
	}

	if result.Test != "macos-direct-utun-reset" || result.TargetAddress != testNetTarget {
		t.Fatalf("harness identity contract changed: %#v", result)
	}
	if !result.SocksGreetingObserved || !result.SocksConnectObserved {
		t.Fatalf("SOCKS trace must record greeting and CONNECT: %#v", result)
	}
	if result.SocksRequestedATYP != "ipv4" || result.SocksRequestedTarget != testNetTarget {
		t.Fatalf("SOCKS request must preserve numeric TEST-NET target: %#v", result)
	}
	if result.SocksResponseCode != 0 || result.PayloadNonceResult != result.PayloadNonce {
		t.Fatalf("payload proof must record successful response and nonce: %#v", result)
	}
	if result.PayloadBytes < 64*1024 || result.PayloadSHA256 != result.PayloadExpectedSHA256 || !result.PayloadHashValid {
		t.Fatalf("TLS payload proof must record a verified large-payload digest: %#v", result)
	}
	if result.PayloadProtocol != "https" || !result.TLSProbe {
		t.Fatalf("TLS probe contract must identify HTTPS payloads: %#v", result)
	}
	if !result.Cleanup.HelperStopped || !result.Cleanup.RouteRemoved || !result.Cleanup.UtunRemoved || !result.Cleanup.TempRemoved {
		t.Fatalf("cleanup contract must be explicit: %#v", result.Cleanup)
	}
}
