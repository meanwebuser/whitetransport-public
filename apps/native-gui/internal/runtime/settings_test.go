package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadRoutingSettingsSanitizesUnsupportedLegacyMode(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(settingsPath(configDir), []byte(`{"routing_mode":"ru_direct","lan_access":true}`), 0o600); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}

	settings := LoadRoutingSettings(configDir)
	if settings.Mode != "all_proxy" {
		t.Fatalf("mode = %q, want all_proxy", settings.Mode)
	}
	if !settings.LANAccess {
		t.Fatal("LAN access was lost while sanitizing the legacy routing mode")
	}
}

func TestSystemVPNSplitSettingsRoundTripNormalizesDestinations(t *testing.T) {
	configDir := t.TempDir()
	want := SystemVPNSplitSettings{
		Mode:         SystemVPNSplitBypass,
		LANAccess:    true,
		Destinations: []string{"198.51.100.0/24", "2001:db8::/32"},
	}
	if err := SaveSystemVPNSplitSettings(configDir, SystemVPNSplitSettings{
		Mode:         SystemVPNSplitBypass,
		LANAccess:    true,
		Destinations: []string{" 198.51.100.0/24 ", "2001:0db8::/32", "198.51.100.0/24"},
	}); err != nil {
		t.Fatalf("SaveSystemVPNSplitSettings: %v", err)
	}

	got, err := LoadSystemVPNSplitSettings(configDir)
	if err != nil {
		t.Fatalf("LoadSystemVPNSplitSettings: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("split settings = %#v, want %#v", got, want)
	}
	info, err := os.Stat(filepath.Join(configDir, systemVPNSplitSettingsFilename))
	if err != nil {
		t.Fatalf("stat split settings: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("split settings mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSystemVPNSplitSettingsFailClosedValidation(t *testing.T) {
	tests := []struct {
		name     string
		settings SystemVPNSplitSettings
	}{
		{name: "unknown mode", settings: SystemVPNSplitSettings{Mode: "automatic"}},
		{name: "none with destinations", settings: SystemVPNSplitSettings{Mode: SystemVPNSplitNone, Destinations: []string{"198.51.100.0/24"}}},
		{name: "only without destinations", settings: SystemVPNSplitSettings{Mode: SystemVPNSplitOnly}},
		{name: "hostname", settings: SystemVPNSplitSettings{Mode: SystemVPNSplitBypass, Destinations: []string{"example.com"}}},
		{name: "default ipv4 route", settings: SystemVPNSplitSettings{Mode: SystemVPNSplitOnly, Destinations: []string{"0.0.0.0/0"}}},
		{name: "default ipv6 route", settings: SystemVPNSplitSettings{Mode: SystemVPNSplitOnly, Destinations: []string{"::/0"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := SaveSystemVPNSplitSettings(t.TempDir(), test.settings); err == nil {
				t.Fatalf("SaveSystemVPNSplitSettings(%#v) succeeded", test.settings)
			}
		})
	}
}

func TestLoadSystemVPNSplitSettingsDefaultsToFullTunnel(t *testing.T) {
	got, err := LoadSystemVPNSplitSettings(t.TempDir())
	if err != nil {
		t.Fatalf("LoadSystemVPNSplitSettings: %v", err)
	}
	want := SystemVPNSplitSettings{Mode: SystemVPNSplitNone}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default split settings = %#v, want %#v", got, want)
	}
}
