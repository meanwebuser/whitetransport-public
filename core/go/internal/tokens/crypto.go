package tokens

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// encryptedPrefix is prepended to encrypted values to distinguish them
	// from plaintext. Format: enc:v1:<base64(nonce || ciphertext || tag)>
	encryptedPrefix = "enc:v1:"

	// argon2 parameters for passphrase-based key derivation
	argon2Time    = 3
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

// IsEncrypted returns true if the value is encrypted with the enc:v1: prefix.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, encryptedPrefix)
}

// EncryptToken encrypts a plaintext token value using AES-256-GCM.
// Returns the encoded string in format: enc:v1:<base64(nonce || ciphertext || tag)>.
func EncryptToken(plaintext string, masterKey [32]byte) (string, error) {
	block, err := aes.NewCipher(masterKey[:])
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	// Seal appends ciphertext to nonce: result = nonce || ciphertext || tag
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	encoded := base64.StdEncoding.EncodeToString(sealed)
	return encryptedPrefix + encoded, nil
}

// DecryptToken decrypts a value in enc:v1: format using AES-256-GCM.
func DecryptToken(encoded string, masterKey [32]byte) (string, error) {
	if !IsEncrypted(encoded) {
		return encoded, nil // plaintext, return as-is
	}

	raw := strings.TrimPrefix(encoded, encryptedPrefix)
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(masterKey[:])
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// LoadMasterKey loads the 32-byte master key from environment or file.
//
// Priority:
//  1. WT_MASTER_KEY env var (hex-encoded 64 chars)
//  2. File at path WT_MASTER_KEY_FILE (raw 32 bytes or hex-encoded)
//  3. Argon2id derivation from WT_MASTER_PASSPHRASE
//
// Returns an error if no key source is available.
func LoadMasterKey() ([32]byte, error) {
	var key [32]byte

	// Option 1: hex-encoded env var
	if hexKey := os.Getenv("WT_MASTER_KEY"); hexKey != "" {
		decoded, err := hex.DecodeString(hexKey)
		if err != nil {
			return key, fmt.Errorf("WT_MASTER_KEY hex decode: %w", err)
		}
		if len(decoded) != 32 {
			return key, fmt.Errorf("WT_MASTER_KEY must be 32 bytes (64 hex chars), got %d", len(decoded))
		}
		copy(key[:], decoded)
		return key, nil
	}

	// Option 2: file
	if keyFile := os.Getenv("WT_MASTER_KEY_FILE"); keyFile != "" {
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return key, fmt.Errorf("read key file: %w", err)
		}
		data = []byte(strings.TrimSpace(string(data)))
		if len(data) == 64 {
			// hex-encoded
			decoded, err := hex.DecodeString(string(data))
			if err != nil {
				return key, fmt.Errorf("key file hex decode: %w", err)
			}
			copy(key[:], decoded)
			return key, nil
		}
		if len(data) == 32 {
			copy(key[:], data)
			return key, nil
		}
		return key, fmt.Errorf("key file must be 32 raw bytes or 64 hex chars, got %d bytes", len(data))
	}

	// Option 3: passphrase via Argon2id (with optional salt file)
	if passphrase := os.Getenv("WT_MASTER_PASSPHRASE"); passphrase != "" {
		var salt []byte
		if saltFile := os.Getenv("WT_MASTER_KEY_SALT_FILE"); saltFile != "" {
			data, err := os.ReadFile(saltFile)
			if err != nil {
				return key, fmt.Errorf("read salt file: %w", err)
			}
			data = []byte(strings.TrimSpace(string(data)))
			if len(data) == argon2SaltLen*2 {
				// hex-encoded
				decoded, err := hex.DecodeString(string(data))
				if err != nil {
					return key, fmt.Errorf("salt file hex decode: %w", err)
				}
				salt = decoded
			} else if len(data) == argon2SaltLen {
				salt = data
			} else {
				return key, fmt.Errorf("salt file must be %d raw bytes or %d hex chars, got %d", argon2SaltLen, argon2SaltLen*2, len(data))
			}
		} else {
			// Fallback: deterministic salt from passphrase (for backward compatibility).
			// WARNING: This is cryptographically weak. Use WT_MASTER_KEY_SALT_FILE in production.
			salt = argon2.IDKey([]byte(passphrase), []byte("whitetransport-token-v1"), 1, argon2Memory, argon2Threads, argon2SaltLen)
		}
		derived := argon2.IDKey([]byte(passphrase), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
		copy(key[:], derived)
		return key, nil
	}

	return key, errors.New("no master key source: set WT_MASTER_KEY, WT_MASTER_KEY_FILE, or WT_MASTER_PASSPHRASE")
}

// DecryptTokenValue decrypts a token value if it is encrypted. If the value
// is plaintext, it is returned as-is. This is the main entry point for
// config loading.
func DecryptTokenValue(value string, masterKey *[32]byte) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	if masterKey == nil {
		return "", errors.New("token is encrypted but no master key available")
	}
	return DecryptToken(value, *masterKey)
}

// DecryptTokenParts decrypts all values in a parts map. Plaintext values
// are left as-is.
func DecryptTokenParts(parts map[string]string, masterKey *[32]byte) (map[string]string, error) {
	if len(parts) == 0 {
		return parts, nil
	}
	out := make(map[string]string, len(parts))
	for k, v := range parts {
		decrypted, err := DecryptTokenValue(v, masterKey)
		if err != nil {
			return nil, fmt.Errorf("part %q: %w", k, err)
		}
		out[k] = decrypted
	}
	return out, nil
}
