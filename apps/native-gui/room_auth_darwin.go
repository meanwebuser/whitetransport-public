//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func roomAuthHelperPath() (string, error) {
	if override := os.Getenv("WT_ROOM_AUTH_HELPER"); override != "" {
		return override, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate room auth helper: %w", err)
	}
	helper := filepath.Join(filepath.Dir(executable), "..", "Resources", "RoomAuthHelper")
	if _, err := os.Stat(helper); err != nil {
		return "", fmt.Errorf("room auth helper is unavailable: %w", err)
	}
	return helper, nil
}
