package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedClientIdentityIsPersistedAndExplicitlyProvisionable(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv(managedClientIdentityEnv, "")
	identity, err := loadOrCreateManagedClientIdentity(configDir)
	if err != nil {
		t.Fatalf("default managed identity: %v", err)
	}
	if !strings.HasPrefix(identity, "native-") {
		t.Fatalf("generated identity = %q, want native-*", identity)
	}
	second, err := loadOrCreateManagedClientIdentity(configDir)
	if err != nil || second != identity {
		t.Fatalf("persisted identity = %q err=%v, want %q", second, err, identity)
	}
	info, err := os.Stat(filepath.Join(configDir, managedIdentityFilename))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity file mode = %v err=%v", info.Mode().Perm(), err)
	}

	t.Setenv(managedClientIdentityEnv, "desktop-user-1")
	identity, err = loadOrCreateManagedClientIdentity(t.TempDir())
	if err != nil || identity != "desktop-user-1" {
		t.Fatalf("provisioned identity = %q err=%v", identity, err)
	}

	t.Setenv(managedClientIdentityEnv, "bad identity with spaces")
	if _, err := loadOrCreateManagedClientIdentity(t.TempDir()); err == nil {
		t.Fatal("invalid managed client identity unexpectedly accepted")
	}
}
