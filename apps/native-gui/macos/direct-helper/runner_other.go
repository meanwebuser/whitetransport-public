//go:build !darwin

package main

func platformStart(cfg Config) Result {
	return Result{Command: "start", Error: "direct-utun helper requires macOS"}
}

func platformStop(cfg Config) Result {
	return Result{Command: "stop", Error: "direct-utun helper requires macOS"}
}

func platformStatus(cfg Config) Result {
	return Result{OK: true, Command: "status", Message: "unsupported platform; no helper process running"}
}
