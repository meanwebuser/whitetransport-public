package fabric

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// EnvelopeCipher wraps AES-256-GCM for envelope-level encryption.
type EnvelopeCipher struct {
	aead cipher.AEAD
}

// NewSessionCipher creates an AES-256-GCM cipher from a 32-byte key.
func NewSessionCipher(key [32]byte) (*EnvelopeCipher, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &EnvelopeCipher{aead: aead}, nil
}

// Seal marshals the envelope to JSON and encrypts it with AES-256-GCM.
// The output format is: nonce || ciphertext.
func (ec *EnvelopeCipher) Seal(envelope Envelope) ([]byte, error) {
	return ec.SealWithAAD(envelope, nil)
}

// SealWithAAD encrypts an envelope while authenticating carrier-visible
// routing metadata that must remain outside the ciphertext.
func (ec *EnvelopeCipher) SealWithAAD(envelope Envelope, aad []byte) ([]byte, error) {
	plaintext, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	nonce := make([]byte, ec.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := ec.aead.Seal(nonce, nonce, plaintext, aad)
	return ciphertext, nil
}

// Open decrypts ciphertext (nonce || encrypted) and unmarshals the envelope.
func (ec *EnvelopeCipher) Open(ciphertext []byte) (Envelope, error) {
	return ec.OpenWithAAD(ciphertext, nil)
}

// OpenWithAAD decrypts an envelope only when the authenticated carrier-visible
// metadata exactly matches the bytes used by SealWithAAD.
func (ec *EnvelopeCipher) OpenWithAAD(ciphertext, aad []byte) (Envelope, error) {
	nonceSize := ec.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return Envelope{}, fmt.Errorf("ciphertext too short: %d < %d", len(ciphertext), nonceSize)
	}
	nonce, encrypted := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := ec.aead.Open(nil, nonce, encrypted, aad)
	if err != nil {
		return Envelope{}, fmt.Errorf("decrypt: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		return Envelope{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return env, nil
}

// DeriveBootstrapKey derives a 32-byte AES key from a carrier token using
// HKDF-SHA256. This is deterministic: the same token always produces the
// same key, allowing both client and node to derive it independently.
func DeriveBootstrapKey(carrierToken string) [32]byte {
	var key [32]byte
	r := hkdf.New(sha256.New, []byte(carrierToken), []byte("whitetransport-bootstrap"), []byte("carrier-key-v1"))
	if _, err := io.ReadFull(r, key[:]); err != nil {
		// HKDF with SHA256 should never fail for 32 bytes.
		panic(fmt.Sprintf("hkdf derive: %v", err))
	}
	return key
}

// DeriveBootstrapSecretKey derives the v2 bootstrap key from a dedicated
// WhiteTransport secret. Its HKDF info is distinct from DeriveBootstrapKey so
// a provider credential can never be confused with the bootstrap secret.
func DeriveBootstrapSecretKey(secret string) [32]byte {
	var key [32]byte
	r := hkdf.New(sha256.New, []byte(secret), []byte("whitetransport-bootstrap"), []byte("bootstrap-secret-v2"))
	if _, err := io.ReadFull(r, key[:]); err != nil {
		panic(fmt.Sprintf("hkdf derive: %v", err))
	}
	return key
}

// GenerateSessionKey generates a cryptographically random 32-byte session key.
func GenerateSessionKey() ([32]byte, error) {
	var key [32]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return key, fmt.Errorf("generate session key: %w", err)
	}
	return key, nil
}
