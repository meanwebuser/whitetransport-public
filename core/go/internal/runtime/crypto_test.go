package runtime

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/config"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/session"
)

func TestExtractBootstrapTokenFromVKMessages(t *testing.T) {
	t.Setenv("WT_CRYPTO_TEST_TOKEN", "my-secret-token")
	cfg := config.Config{
		CarrierConfigs: []config.CarrierConfig{
			{
				ID: "vk.messages",
				VKMessages: &config.VKMessagesConfig{
					TokenEnv: "WT_CRYPTO_TEST_TOKEN",
				},
			},
		},
	}
	token := extractBootstrapToken(cfg)
	if token != "my-secret-token" {
		t.Fatalf("expected my-secret-token, got %q", token)
	}
}

func TestExtractBootstrapTokenEmpty(t *testing.T) {
	cfg := config.Config{}
	token := extractBootstrapToken(cfg)
	if token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}
}

func TestSessionKeyEncryptDecryptRoundTrip(t *testing.T) {
	key := fabric.DeriveBootstrapKey("test-bootstrap-token")
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	sessionKey, err := fabric.GenerateSessionKey()
	if err != nil {
		t.Fatal(err)
	}

	encrypted, err := session.EncryptSessionKey(cipher, sessionKey[:])
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := session.DecryptSessionKey(cipher, encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if len(decrypted) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(decrypted))
	}

	for i := range sessionKey {
		if sessionKey[i] != decrypted[i] {
			t.Fatal("decrypted session key does not match original")
		}
	}
}

func TestNilCipherPassthrough(t *testing.T) {
	// When cipher is nil, encrypt/decrypt should pass through raw bytes.
	sessionKey, _ := fabric.GenerateSessionKey()
	result, err := session.EncryptSessionKey(nil, sessionKey[:])
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 32 {
		t.Fatalf("expected passthrough, got %d bytes", len(result))
	}

	result2, err := session.DecryptSessionKey(nil, result)
	if err != nil {
		t.Fatal(err)
	}
	if len(result2) != 32 {
		t.Fatalf("expected passthrough, got %d bytes", len(result2))
	}
}

func TestOfferSessionKeyRejectsMismatchedV2BootstrapSecret(t *testing.T) {
	wrongKey := fabric.DeriveBootstrapSecretKey("wrong-secret")
	wrongCipher, err := fabric.NewSessionCipher(wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := fabric.GenerateSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := session.EncryptSessionKey(wrongCipher, sessionKey[:])
	if err != nil {
		t.Fatal(err)
	}

	control := &ControlPlane{bootstrapSecretCipher: mustSessionCipher(t, "node-secret")}
	_, encryptedDelivery, decryptErr := control.offerSessionKey(session.Offer{SessionKey: encrypted, Metadata: map[string]string{bootstrapKeyMetadata: bootstrapKeyV2}})
	if decryptErr == nil {
		t.Fatal("mismatched v2 bootstrap secret must fail closed")
	}
	if encryptedDelivery {
		t.Fatal("mismatched v2 bootstrap secret must not activate encrypted delivery")
	}
}

func TestOfferSessionKeyLegacyWithoutBootstrapCipherRemainsCompatible(t *testing.T) {
	_, encryptedDelivery, err := (&ControlPlane{}).offerSessionKey(session.Offer{SessionKey: []byte("legacy-session-key")})
	if err != nil {
		t.Fatalf("legacy offer should remain compatible: %v", err)
	}
	if encryptedDelivery {
		t.Fatal("legacy offer without a bootstrap cipher must not claim encrypted delivery")
	}
}

func mustSessionCipher(t *testing.T, secret string) *fabric.EnvelopeCipher {
	t.Helper()
	key := fabric.DeriveBootstrapSecretKey(secret)
	cipher, err := fabric.NewSessionCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}
