package config

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// ValidChannelRoles enumerates the allowed role values for VK/OK channels.
var ValidChannelRoles = map[string]bool{
	"discovery":   true,
	"node-client": true,
	"logs":        true,
	"admin":       true,
	"flex":        true,
}

// ChannelBindingsEnabled reports whether the WT_CHANNEL_BINDINGS feature
// flag is active. When off, the runtime creates exactly one binding per
// carrier config (legacy behavior). When on, VK/OK configs with Channels
// produce one binding per role.
func ChannelBindingsEnabled() bool {
	return channelBindingsEnabled(runtime.GOOS, os.Getenv("WT_CHANNEL_BINDINGS"))
}

func channelBindingsEnabled(goos string, value string) bool {
	if value != "" {
		return value == "1"
	}
	// Android gomobile cannot inherit the desktop shell's environment. Its
	// embedded runtime config is intentionally role-aware, so default to the
	// binding-aware path there while preserving desktop legacy behavior.
	return goos == "android"
}

// ValidateVKChannels checks that a set of VK channel configs is internally
// consistent: non-empty PeerID, valid Role, no duplicate roles.
func ValidateVKChannels(channels []VKChannelConfig) error {
	seen := make(map[string]bool, len(channels))
	for i, ch := range channels {
		if strings.TrimSpace(ch.PeerID) == "" {
			return fmt.Errorf("vk channel[%d]: peer_id is required", i)
		}
		if !ValidChannelRoles[ch.Role] {
			return fmt.Errorf("vk channel[%d]: invalid role %q (valid: discovery, node-client, logs, admin, flex)", i, ch.Role)
		}
		if seen[ch.Role] {
			return fmt.Errorf("vk channel[%d]: duplicate role %q", i, ch.Role)
		}
		seen[ch.Role] = true
	}
	return nil
}

// ValidateOKChannels checks that a set of OK channel configs is internally
// consistent: non-empty ChatID, valid Role, no duplicate roles.
func ValidateOKChannels(channels []OKChannelConfig) error {
	seen := make(map[string]bool, len(channels))
	for i, ch := range channels {
		if strings.TrimSpace(ch.ChatID) == "" {
			return fmt.Errorf("ok channel[%d]: chat_id is required", i)
		}
		if !ValidChannelRoles[ch.Role] {
			return fmt.Errorf("ok channel[%d]: invalid role %q (valid: discovery, node-client, logs, admin, flex)", i, ch.Role)
		}
		if seen[ch.Role] {
			return fmt.Errorf("ok channel[%d]: duplicate role %q", i, ch.Role)
		}
		seen[ch.Role] = true
	}
	return nil
}
