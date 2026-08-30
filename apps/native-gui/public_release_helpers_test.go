package main

import (
	"context"
	"path/filepath"
	"testing"

	guiruntime "github.com/meanwebuser/whitetransport/apps/native-gui/internal/runtime"
)

func newTestLogSink(t *testing.T) *guiruntime.LogSink {
	t.Helper()
	logSink, err := guiruntime.NewLogSink(filepath.Join(t.TempDir(), "WhiteTransport.log"))
	if err != nil {
		t.Fatalf("NewLogSink: %v", err)
	}
	return logSink
}

type stubRuntimeService struct {
	status            guiruntime.DesktopStatus
	servers           []guiruntime.ServerSummary
	connected         guiruntime.DesktopStatus
	disconnected      guiruntime.DesktopStatus
	telemetry         guiruntime.DesktopTelemetry
	diagnostics       guiruntime.DiagnosticResult
	connectedServerID string
	disconnectedCalls int
	connectErr        error
	operationOrder    *[]string
	serversByCall     [][]guiruntime.ServerSummary
	listServersCalls  int
}

func (s *stubRuntimeService) Status(context.Context) (guiruntime.DesktopStatus, error) {
	return s.status, nil
}

func (s *stubRuntimeService) ListServers(context.Context) ([]guiruntime.ServerSummary, error) {
	s.listServersCalls++
	if len(s.serversByCall) > 0 {
		index := s.listServersCalls - 1
		if index >= len(s.serversByCall) {
			index = len(s.serversByCall) - 1
		}
		return append([]guiruntime.ServerSummary(nil), s.serversByCall[index]...), nil
	}
	if s.servers == nil {
		return []guiruntime.ServerSummary{{ID: "public-release-fixture", Available: true}}, nil
	}
	return append([]guiruntime.ServerSummary(nil), s.servers...), nil
}

func (s *stubRuntimeService) Connect(_ context.Context, serverID string) (guiruntime.DesktopStatus, error) {
	s.connectedServerID = serverID
	if s.operationOrder != nil {
		*s.operationOrder = append(*s.operationOrder, "transport-connect")
	}
	return s.connected, s.connectErr
}

func (s *stubRuntimeService) Disconnect(context.Context) (guiruntime.DesktopStatus, error) {
	s.disconnectedCalls++
	if s.operationOrder != nil {
		*s.operationOrder = append(*s.operationOrder, "transport-disconnect")
	}
	return s.disconnected, nil
}

func (s *stubRuntimeService) Telemetry(context.Context) (guiruntime.DesktopTelemetry, error) {
	return s.telemetry, nil
}

func (s *stubRuntimeService) RunDiagnostics(context.Context) guiruntime.DiagnosticResult {
	return s.diagnostics
}
