package runtime

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	settingsFilename               = "settings.json"
	defaultRoutingMode             = "all_proxy"
	systemVPNSplitSettingsFilename = "system-vpn-settings.json"
)

// SystemVPNSplitMode is the native host route policy persisted for the next
// daemon profile. Android package split remains owned by VpnService.
type SystemVPNSplitMode string

const (
	SystemVPNSplitNone   SystemVPNSplitMode = "none"
	SystemVPNSplitBypass SystemVPNSplitMode = "bypass"
	SystemVPNSplitOnly   SystemVPNSplitMode = "only"
)

// SystemVPNSplitSettings contains only explicit, normalized destination
// routes. Hostnames are rejected because Network Extension applies IP routes.
type SystemVPNSplitSettings struct {
	Mode         SystemVPNSplitMode `json:"mode"`
	LANAccess    bool               `json:"lan_access"`
	Destinations []string           `json:"destinations,omitempty"`
}

type settingsFile struct {
	RoutingMode string `json:"routing_mode,omitempty"`
	LANAccess   bool   `json:"lan_access,omitempty"`
}

func settingsPath(configDir string) string {
	return filepath.Join(configDir, settingsFilename)
}

func LoadRoutingSettings(configDir string) RoutingSettingsResult {
	path := settingsPath(configDir)
	var s settingsFile
	data, err := os.ReadFile(path)
	if err != nil {
		return RoutingSettingsResult{Mode: defaultRoutingMode}
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return RoutingSettingsResult{Mode: defaultRoutingMode}
	}
	mode, err := NormalizeRoutingMode(s.RoutingMode)
	if err != nil {
		mode = defaultRoutingMode
	}
	return RoutingSettingsResult{Mode: mode, LANAccess: s.LANAccess}
}

type RoutingSettingsResult struct {
	Mode      string
	LANAccess bool
}

func SaveRoutingSettings(configDir string, mode string, lanAccess bool) error {
	normalizedMode, err := NormalizeRoutingMode(mode)
	if err != nil {
		return err
	}
	s := settingsFile{RoutingMode: normalizedMode, LANAccess: lanAccess}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(configDir), data, 0o600)
}

// NormalizeRoutingMode accepts only the routing mode currently implemented by
// the native runtime. Split routing belongs to a future host-owned system VPN.
func NormalizeRoutingMode(mode string) (string, error) {
	normalized := strings.TrimSpace(mode)
	if normalized == "" || normalized == defaultRoutingMode {
		return defaultRoutingMode, nil
	}
	return "", fmt.Errorf("routing mode %q is unsupported: native/system VPN owns split routing", normalized)
}

// LoadSystemVPNSplitSettings reads the fail-closed host route policy. A missing
// file means full tunnel; malformed existing settings are returned as errors.
func LoadSystemVPNSplitSettings(configDir string) (SystemVPNSplitSettings, error) {
	path := filepath.Join(configDir, systemVPNSplitSettingsFilename)
	payload, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return SystemVPNSplitSettings{Mode: SystemVPNSplitNone}, nil
	}
	if err != nil {
		return SystemVPNSplitSettings{}, fmt.Errorf("read system VPN split settings: %w", err)
	}
	var settings SystemVPNSplitSettings
	if err := json.Unmarshal(payload, &settings); err != nil {
		return SystemVPNSplitSettings{}, fmt.Errorf("parse system VPN split settings: %w", err)
	}
	return normalizeSystemVPNSplitSettings(settings)
}

// SaveSystemVPNSplitSettings validates and atomically writes a route policy
// with owner-only permissions.
func SaveSystemVPNSplitSettings(configDir string, settings SystemVPNSplitSettings) error {
	normalized, err := normalizeSystemVPNSplitSettings(settings)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create system VPN settings directory: %w", err)
	}
	payload, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("encode system VPN split settings: %w", err)
	}
	temporary, err := os.CreateTemp(configDir, ".system-vpn-settings-*")
	if err != nil {
		return fmt.Errorf("create temporary system VPN split settings: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary system VPN split settings: %w", err)
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary system VPN split settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary system VPN split settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary system VPN split settings: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(configDir, systemVPNSplitSettingsFilename)); err != nil {
		return fmt.Errorf("replace system VPN split settings: %w", err)
	}
	return nil
}

func normalizeSystemVPNSplitSettings(settings SystemVPNSplitSettings) (SystemVPNSplitSettings, error) {
	mode := SystemVPNSplitMode(strings.TrimSpace(string(settings.Mode)))
	if mode == "" {
		mode = SystemVPNSplitNone
	}
	if mode != SystemVPNSplitNone && mode != SystemVPNSplitBypass && mode != SystemVPNSplitOnly {
		return SystemVPNSplitSettings{}, fmt.Errorf("system VPN split mode %q is unsupported", mode)
	}
	destinations := make([]string, 0, len(settings.Destinations))
	seen := make(map[string]struct{}, len(settings.Destinations))
	for _, raw := range settings.Destinations {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return SystemVPNSplitSettings{}, fmt.Errorf("system VPN destination %q is not a CIDR: %w", raw, err)
		}
		prefix = prefix.Masked()
		if prefix.Bits() == 0 {
			return SystemVPNSplitSettings{}, fmt.Errorf("system VPN destination %q is a default route", raw)
		}
		value := prefix.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		destinations = append(destinations, value)
	}
	sort.Strings(destinations)
	if mode == SystemVPNSplitNone && len(destinations) != 0 {
		return SystemVPNSplitSettings{}, fmt.Errorf("full system VPN mode cannot contain destination routes")
	}
	if mode == SystemVPNSplitOnly && len(destinations) == 0 {
		return SystemVPNSplitSettings{}, fmt.Errorf("only system VPN mode requires destination routes")
	}
	if mode == SystemVPNSplitBypass && len(destinations) == 0 && !settings.LANAccess {
		return SystemVPNSplitSettings{}, fmt.Errorf("bypass system VPN mode requires destination routes or LAN access")
	}
	return SystemVPNSplitSettings{Mode: mode, LANAccess: settings.LANAccess, Destinations: destinations}, nil
}
