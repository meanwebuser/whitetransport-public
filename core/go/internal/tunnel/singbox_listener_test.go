package tunnel

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

func TestManagedSingBoxOwnedListenerIgnoresOccupiedPort(t *testing.T) {
	binary := os.Getenv("WT_SINGBOX_BINARY")
	if binary == "" {
		t.Skip("set WT_SINGBOX_BINARY for real owned-listener proof")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{
		Server: "127.0.0.1", ServerPort: 1, UUID: "11111111-1111-4111-8111-111111111111",
		Network: "tcp", BinaryPath: binary, ConfigDir: t.TempDir(), LocalListen: occupied.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, address, err := (singBoxCommandRunner{}).Start(ctx, carrier, carriers.Endpoint{ID: "owned-port"}, occupied.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	if address == occupied.Addr().String() {
		t.Fatal("runner accepted the unrelated occupied listener")
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if response[0] != 5 || response[1] != 0 {
		t.Fatalf("not owned SOCKS handshake: %v", response)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if probe, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		probe.Close()
		t.Fatal("owned listener survived Close")
	}
}

func TestAnnouncedSingBoxInbound(t *testing.T) {
	for _, address := range []string{"127.0.0.1:1234", "[::1]:4321"} {
		line := "+0300 2026-09-05 05:21:53 \x1b[36mINFO\x1b[0m inbound/mixed[wt-mixed-in]: tcp server started at " + address
		if got := announcedSingBoxInbound(line); got != address {
			t.Fatalf("bound address %q = %q", address, got)
		}
	}
	for _, line := range []string{
		"INFO outbound/mixed[wt-mixed-in]: tcp server started at 127.0.0.1:12",
		"INFO inbound/mixed[foreign]: tcp server started at 127.0.0.1:12",
		"INFO inbound/mixed[wt-mixed-in]: tcp server started at 127.0.0.1:0",
		"INFO inbound/mixed[wt-mixed-in]: tcp server started at 192.0.2.1:12",
		"unrelated INFO inbound/mixed[wt-mixed-in]: tcp server started at 127.0.0.1:12",
	} {
		if got := announcedSingBoxInbound(line); got != "" {
			t.Fatalf("accepted unrelated or unbound log %q", line)
		}
	}
}

func TestSingBoxRunnerEarlyExitAndCancellationCleanup(t *testing.T) {
	for _, mode := range []string{"exit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			binary := os.Getenv("WT_SINGBOX_BINARY")
			if binary == "" {
				t.Skip("set WT_SINGBOX_BINARY for real startup cancellation")
			}
			if mode == "exit" {
				binary = filepath.Join(t.TempDir(), "early-exit")
				if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'FATAL sidecar fixture stopped' >&2\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			directory := t.TempDir()
			carrier, err := carriers.NewSingBoxVLESSCarrier(carriers.SingBoxVLESSConfig{Server: "127.0.0.1", ServerPort: 1, UUID: "11111111-1111-4111-8111-111111111111", Network: "tcp", BinaryPath: binary, ConfigDir: directory})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			process, address, err := (singBoxCommandRunner{}).Start(ctx, carrier, carriers.Endpoint{ID: "failed-start"}, "127.0.0.1:0")
			if err == nil {
				process.Close()
				t.Fatalf("failed startup returned owned endpoint %q", address)
			}
			if mode == "exit" && !strings.Contains(err.Error(), "FATAL sidecar fixture stopped") {
				t.Fatalf("lost exit diagnostics: %v", err)
			}
			entries, readErr := os.ReadDir(directory)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("failed startup left generated config: %v, %v", entries, readErr)
			}
		})
	}
}
