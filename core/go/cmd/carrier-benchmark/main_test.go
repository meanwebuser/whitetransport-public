package main

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

func TestBuildReportCoversCanonicalInventory(t *testing.T) {
	report := buildReport()
	inventory := carriers.CapabilityInventory()
	if report.Mode != "canonical-catalog-model" || len(report.Results) != len(inventory) {
		t.Fatalf("report must cover canonical inventory: mode=%q rows=%d inventory=%d", report.Mode, len(report.Results), len(inventory))
	}

	byID := make(map[string]benchmarkRow, len(report.Results))
	for _, row := range report.Results {
		byID[row.ID] = row
	}
	for _, expected := range inventory {
		row, ok := byID[expected.ID]
		if !ok || row.Availability != expected.Availability || row.Source != expected.Source {
			t.Fatalf("inventory row %q missing or changed: %#v", expected.ID, row)
		}
	}
}

func TestBuildReportUsesNullForRowsWithoutDescriptorMetrics(t *testing.T) {
	rows := make(map[string]benchmarkRow)
	for _, row := range buildReport().Results {
		rows[row.ID] = row
	}
	for _, id := range []string{"telegram.messages", "memory.provider", "wbstream"} {
		row := rows[id]
		if row.ModeledLatencyMs != nil || row.ModeledThroughputBytesPerSec != nil || row.ModeledDailyCapacityBytes != nil {
			t.Fatalf("%s must remain unavailable without an exact standard descriptor: %#v", id, row)
		}
	}
	if got := rows[carriers.CarrierVKMessages]; got.ModeledLatencyMs == nil || got.ModeledThroughputBytesPerSec == nil || got.ModeledDailyCapacityBytes == nil {
		t.Fatalf("descriptor-backed row must expose catalog metrics: %#v", got)
	}
}
