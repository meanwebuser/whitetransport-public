package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	managedIdentityFilename  = "client-id"
	managedClientIdentityEnv = "WT_NATIVE_GUI_CLIENT_ID"
)

var managedIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// loadOrCreateManagedClientIdentity returns an explicitly provisioned client
// identity or creates one stable local identity for managed desktop installs.
func loadOrCreateManagedClientIdentity(configDir string) (string, error) {
	if configured := strings.TrimSpace(os.Getenv(managedClientIdentityEnv)); configured != "" {
		if err := validateManagedClientIdentity(configured); err != nil {
			return "", fmt.Errorf("%s: %w", managedClientIdentityEnv, err)
		}
		return configured, nil
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", fmt.Errorf("create managed identity directory: %w", err)
	}
	path := filepath.Join(configDir, managedIdentityFilename)
	if identity, err := readManagedClientIdentity(path); err == nil {
		return identity, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	identity, err := newManagedClientIdentity()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return readManagedClientIdentity(path)
	}
	if err != nil {
		return "", fmt.Errorf("create managed client identity: %w", err)
	}
	if _, err := file.WriteString(identity + "\n"); err != nil {
		file.Close()
		return "", fmt.Errorf("write managed client identity: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", fmt.Errorf("sync managed client identity: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close managed client identity: %w", err)
	}
	return identity, nil
}

func readManagedClientIdentity(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(raw))
	if err := validateManagedClientIdentity(identity); err != nil {
		return "", fmt.Errorf("invalid persisted managed client identity: %w", err)
	}
	return identity, nil
}

func newManagedClientIdentity() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate managed client identity: %w", err)
	}
	return "native-" + hex.EncodeToString(random), nil
}

func validateManagedClientIdentity(identity string) error {
	if !managedIdentityPattern.MatchString(identity) {
		return fmt.Errorf("identity must match %s", managedIdentityPattern.String())
	}
	return nil
}
