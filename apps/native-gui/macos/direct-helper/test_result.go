package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func writeTestResult(cfg Config, result Result) error {
	if err := os.MkdirAll(filepath.Dir(cfg.TestResultPath), 0o700); err != nil {
		return fmt.Errorf("create test-result directory: %w", err)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode test result: %w", err)
	}
	return os.WriteFile(cfg.TestResultPath, data, 0o600)
}
