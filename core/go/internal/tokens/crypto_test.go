package tokens

import (
	"bytes"
	"encoding/hex"
	"os"
	"testing"
)

func testMasterKey() [32]byte {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testMasterKey()

	tests := []struct {
		name      string
		plaintext string
	}{
		{"short", "hello"},
		{"vk token", "<synthetic-vk-token-fixture>"},
		{"jwt", "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpYXQiOjE3ODAwODczMTV9"},
		{"empty", ""},
		{"unicode", "Привет мир 世界"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encrypted, err := EncryptToken(tt.plaintext, key)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}

			if !IsEncrypted(encrypted) {
				t.Error("encrypted value should have enc:v1: prefix")
			}

			decrypted, err := DecryptToken(encrypted, key)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}

			if decrypted != tt.plaintext {
				t.Errorf("decrypted = %q, want %q", decrypted, tt.plaintext)
			}
		})
	}
}

func TestEncryptDifferentCiphertexts(t *testing.T) {
	key := testMasterKey()

	// Same plaintext should produce different ciphertexts (random nonce)
	enc1, _ := EncryptToken("hello", key)
	enc2, _ := EncryptToken("hello", key)

	if enc1 == enc2 {
		t.Error("two encryptions of same plaintext should differ (random nonce)")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key1 := testMasterKey()
	var key2 [32]byte
	for i := range key2 {
		key2[i] = byte(255 - i)
	}

	encrypted, _ := EncryptToken("secret", key1)

	_, err := DecryptToken(encrypted, key2)
	if err == nil {
		t.Error("decrypting with wrong key should fail")
	}
}

func TestDecryptPlaintextPassthrough(t *testing.T) {
	key := testMasterKey()

	// Non-encrypted value should pass through unchanged
	decrypted, err := DecryptToken("plaintext-value", key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decrypted != "plaintext-value" {
		t.Errorf("expected passthrough, got %q", decrypted)
	}
}

func TestIsEncrypted(t *testing.T) {
	if IsEncrypted("vk1.a.token") {
		t.Error("plaintext should not be encrypted")
	}
	if !IsEncrypted("enc:v1:base64data") {
		t.Error("enc:v1: prefix should be detected")
	}
}

func TestDecryptTokenValue(t *testing.T) {
	key := testMasterKey()

	// Plaintext passthrough with nil key
	dec, err := DecryptTokenValue("plaintext", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec != "plaintext" {
		t.Errorf("expected plaintext passthrough, got %q", dec)
	}

	// Encrypted without key → error
	enc, _ := EncryptToken("secret", key)
	_, err = DecryptTokenValue(enc, nil)
	if err == nil {
		t.Error("expected error when decrypting without key")
	}

	// Encrypted with key → success
	dec, err = DecryptTokenValue(enc, &key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec != "secret" {
		t.Errorf("expected 'secret', got %q", dec)
	}
}

func TestDecryptTokenParts(t *testing.T) {
	key := testMasterKey()

	enc1, _ := EncryptToken("access-token-value", key)
	enc2, _ := EncryptToken("app-key-value", key)

	parts := map[string]string{
		"access_token": enc1,
		"app_key":      enc2,
		"plain_file":   "/path/to/file", // not encrypted
	}

	decrypted, err := DecryptTokenParts(parts, &key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if decrypted["access_token"] != "access-token-value" {
		t.Errorf("access_token = %q", decrypted["access_token"])
	}
	if decrypted["app_key"] != "app-key-value" {
		t.Errorf("app_key = %q", decrypted["app_key"])
	}
	if decrypted["plain_file"] != "/path/to/file" {
		t.Errorf("plain_file = %q", decrypted["plain_file"])
	}
}

func TestDecryptTokenPartsNil(t *testing.T) {
	parts, err := DecryptTokenParts(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parts != nil {
		t.Error("nil parts should return nil")
	}
}

func TestLoadMasterKeyFromEnv(t *testing.T) {
	// Generate a valid hex key
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 10)
	}
	hexKey := hex.EncodeToString(keyBytes)

	t.Setenv("WT_MASTER_KEY", hexKey)
	t.Setenv("WT_MASTER_KEY_FILE", "")
	t.Setenv("WT_MASTER_PASSPHRASE", "")

	key, err := LoadMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(key[:], keyBytes) {
		t.Error("loaded key does not match env var")
	}
}

func TestLoadMasterKeyFromFile(t *testing.T) {
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 20)
	}

	// Write hex-encoded key to temp file
	tmpFile, err := os.CreateTemp("", "wt-test-key-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(hex.EncodeToString(keyBytes))
	tmpFile.Close()

	t.Setenv("WT_MASTER_KEY", "")
	t.Setenv("WT_MASTER_KEY_FILE", tmpFile.Name())
	t.Setenv("WT_MASTER_PASSPHRASE", "")

	key, err := LoadMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(key[:], keyBytes) {
		t.Error("loaded key does not match file content")
	}
}

func TestLoadMasterKeyFromPassphrase(t *testing.T) {
	t.Setenv("WT_MASTER_KEY", "")
	t.Setenv("WT_MASTER_KEY_FILE", "")
	t.Setenv("WT_MASTER_PASSPHRASE", "test-passphrase-123")

	key1, err := LoadMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Same passphrase should produce same key (deterministic)
	key2, err := LoadMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(key1[:], key2[:]) {
		t.Error("same passphrase should produce same key")
	}

	// Different passphrase should produce different key
	t.Setenv("WT_MASTER_PASSPHRASE", "different-passphrase")
	key3, err := LoadMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bytes.Equal(key1[:], key3[:]) {
		t.Error("different passphrase should produce different key")
	}
}

func TestLoadMasterKeyNoSource(t *testing.T) {
	t.Setenv("WT_MASTER_KEY", "")
	t.Setenv("WT_MASTER_KEY_FILE", "")
	t.Setenv("WT_MASTER_PASSPHRASE", "")

	_, err := LoadMasterKey()
	if err == nil {
		t.Error("expected error when no key source is available")
	}
}

func TestLoadMasterKeyBadHex(t *testing.T) {
	t.Setenv("WT_MASTER_KEY", "not-hex")

	_, err := LoadMasterKey()
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestLoadMasterKeyWrongLength(t *testing.T) {
	t.Setenv("WT_MASTER_KEY", hex.EncodeToString([]byte("too-short")))

	_, err := LoadMasterKey()
	if err == nil {
		t.Error("expected error for wrong key length")
	}
}
