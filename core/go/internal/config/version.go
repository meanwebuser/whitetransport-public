package config

import (
	"fmt"
	"regexp"
)

// Version is auto-generated from git commit count.
// Do not edit manually.
const Version = "0.1.271"

var productVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// CompatibilityLine returns the major/minor interoperability line for one
// strict product version. It rejects development and malformed versions.
func CompatibilityLine(version string) (string, error) {
	parts := productVersionPattern.FindStringSubmatch(version)
	if parts == nil {
		return "", fmt.Errorf("invalid product version %q: expected MAJOR.MINOR.PATCH", version)
	}
	return parts[1] + "." + parts[2], nil
}
