//go:build !darwin

package main

import "fmt"

func roomAuthHelperPath() (string, error) {
	return "", fmt.Errorf("room auth helper is currently available only on macOS")
}
