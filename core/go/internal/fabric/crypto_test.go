package fabric

import (
	"bytes"
	"testing"
	"time"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key, err := GenerateSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	env := NewEnvelope("test-1", TrafficControl, "session.offer", []byte(`{"node_id":"example-exit-node"}`))
	env.Source = "client-1"
	env.Destination = "example-exit-node"
	env.TTL = 30 * time.Second

	ciphertext, err := cipher.Seal(env)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	opened, err := cipher.Open(ciphertext)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if opened.ID != env.ID {
		t.Errorf("id mismatch: got %q, want %q", opened.ID, env.ID)
	}
	if opened.Source != env.Source {
		t.Errorf("source mismatch: got %q, want %q", opened.Source, env.Source)
	}
	if opened.TrafficClass != env.TrafficClass {
		t.Errorf("traffic class mismatch: got %q, want %q", opened.TrafficClass, env.TrafficClass)
	}
	if !bytes.Equal(opened.Payload, env.Payload) {
		t.Errorf("payload mismatch: got %q, want %q", opened.Payload, env.Payload)
	}
}

func TestTamperRejection(t *testing.T) {
	key, err := GenerateSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	env := NewEnvelope("test-2", TrafficStream, "tunnel.data", []byte("secret-payload"))
	ciphertext, err := cipher.Seal(env)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with ciphertext.
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0xFF

	if _, err := cipher.Open(tampered); err == nil {
		t.Fatal("expected decryption error for tampered ciphertext")
	}
}

func TestWrongKeyRejection(t *testing.T) {
	key1, _ := GenerateSessionKey()
	key2, _ := GenerateSessionKey()
	cipher1, _ := NewSessionCipher(key1)
	cipher2, _ := NewSessionCipher(key2)

	env := NewEnvelope("test-3", TrafficControl, "session.offer", []byte("data"))
	ciphertext, _ := cipher1.Seal(env)

	if _, err := cipher2.Open(ciphertext); err == nil {
		t.Fatal("expected decryption error for wrong key")
	}
}

func TestDeriveBootstrapKeyDeterministic(t *testing.T) {
	token := "vk-token-abc123"
	key1 := DeriveBootstrapKey(token)
	key2 := DeriveBootstrapKey(token)
	if key1 != key2 {
		t.Error("DeriveBootstrapKey should be deterministic for the same token")
	}

	key3 := DeriveBootstrapKey("different-token")
	if key1 == key3 {
		t.Error("DeriveBootstrapKey should produce different keys for different tokens")
	}
}

func TestDeriveBootstrapSecretKeyIsDomainSeparated(t *testing.T) {
	secret := "shared-bootstrap-secret"
	key1 := DeriveBootstrapSecretKey(secret)
	key2 := DeriveBootstrapSecretKey(secret)
	if key1 != key2 {
		t.Fatal("DeriveBootstrapSecretKey should be deterministic")
	}
	if key1 == DeriveBootstrapKey(secret) {
		t.Fatal("dedicated bootstrap secret must not reuse the provider-token derivation domain")
	}
}

func TestGenerateSessionKeyUniqueness(t *testing.T) {
	key1, err := GenerateSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	key2, err := GenerateSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	if key1 == key2 {
		t.Error("GenerateSessionKey should produce unique keys")
	}
}

func TestCiphertextTooShort(t *testing.T) {
	key, _ := GenerateSessionKey()
	cipher, _ := NewSessionCipher(key)

	if _, err := cipher.Open([]byte("short")); err == nil {
		t.Fatal("expected error for short ciphertext")
	}
}
