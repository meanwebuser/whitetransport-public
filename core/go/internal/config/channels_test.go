package config

import (
	"os"
	"testing"
)

func TestChannelBindingsEnabled(t *testing.T) {
	// Save and restore.
	prev, hadPrev := os.LookupEnv("WT_CHANNEL_BINDINGS")
	defer func() {
		if hadPrev {
			os.Setenv("WT_CHANNEL_BINDINGS", prev)
		} else {
			os.Unsetenv("WT_CHANNEL_BINDINGS")
		}
	}()

	os.Unsetenv("WT_CHANNEL_BINDINGS")
	if ChannelBindingsEnabled() {
		t.Error("expected ChannelBindingsEnabled() = false when env unset")
	}

	os.Setenv("WT_CHANNEL_BINDINGS", "0")
	if ChannelBindingsEnabled() {
		t.Error("expected ChannelBindingsEnabled() = false when env=0")
	}

	os.Setenv("WT_CHANNEL_BINDINGS", "1")
	if !ChannelBindingsEnabled() {
		t.Error("expected ChannelBindingsEnabled() = true when env=1")
	}
}

func TestAndroidChannelBindingsDefaultToEnabled(t *testing.T) {
	if !channelBindingsEnabled("android", "") {
		t.Fatal("Android runtime must enable configured channel bindings by default")
	}
	if channelBindingsEnabled("android", "0") {
		t.Fatal("explicit WT_CHANNEL_BINDINGS=0 must still disable Android channel bindings")
	}
	if channelBindingsEnabled("linux", "") {
		t.Fatal("desktop legacy runtime must keep channel bindings opt-in")
	}
}

func TestValidateVKChannels(t *testing.T) {
	// Valid set.
	if err := ValidateVKChannels([]VKChannelConfig{
		{PeerID: "111", Role: "discovery"},
		{PeerID: "222", Role: "node-client"},
		{PeerID: "333", Role: "logs"},
		{PeerID: "444", Role: "admin"},
	}); err != nil {
		t.Errorf("valid channels rejected: %v", err)
	}

	// Empty peer_id.
	if err := ValidateVKChannels([]VKChannelConfig{
		{PeerID: "", Role: "discovery"},
	}); err == nil {
		t.Error("expected error for empty peer_id")
	}

	// Invalid role.
	if err := ValidateVKChannels([]VKChannelConfig{
		{PeerID: "111", Role: "unknown"},
	}); err == nil {
		t.Error("expected error for invalid role")
	}

	// Duplicate role.
	if err := ValidateVKChannels([]VKChannelConfig{
		{PeerID: "111", Role: "discovery"},
		{PeerID: "222", Role: "discovery"},
	}); err == nil {
		t.Error("expected error for duplicate role")
	}

	// Empty slice is valid (no channels configured).
	if err := ValidateVKChannels(nil); err != nil {
		t.Errorf("nil channels rejected: %v", err)
	}
}

func TestValidateOKChannels(t *testing.T) {
	// Valid set.
	if err := ValidateOKChannels([]OKChannelConfig{
		{ChatID: "chat1", Role: "discovery"},
		{ChatID: "chat2", Role: "node-client"},
	}); err != nil {
		t.Errorf("valid channels rejected: %v", err)
	}

	// Empty chat_id.
	if err := ValidateOKChannels([]OKChannelConfig{
		{ChatID: "", Role: "discovery"},
	}); err == nil {
		t.Error("expected error for empty chat_id")
	}

	// Invalid role.
	if err := ValidateOKChannels([]OKChannelConfig{
		{ChatID: "chat1", Role: "badrole"},
	}); err == nil {
		t.Error("expected error for invalid role")
	}

	// Duplicate role.
	if err := ValidateOKChannels([]OKChannelConfig{
		{ChatID: "chat1", Role: "admin"},
		{ChatID: "chat2", Role: "admin"},
	}); err == nil {
		t.Error("expected error for duplicate role")
	}
}
