//go:build integration

package tests

import (
	"testing"

	testutils "github.com/meanwebuser/whitetransport/core/tests/internal"
)

const secretsDir = "secrets/production"

func repoRoot(t *testing.T) string {
	return testutils.RepoRoot(t)
}

func requireFile(t *testing.T, path string) string {
	return testutils.RequireFile(t, path)
}

func loadVKToken(t *testing.T) string {
	return testutils.LoadVKToken(t)
}

func loadOKToken(t *testing.T) string {
	return testutils.LoadOKToken(t)
}

func loadDIONAccessToken(t *testing.T) string {
	return testutils.LoadDIONAccessToken(t)
}
