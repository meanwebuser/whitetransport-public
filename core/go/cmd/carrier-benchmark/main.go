// carrier-benchmark emits a safe, deterministic projection of the canonical
// carrier catalog. It does not construct carriers or contact providers.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

const day = 24 * time.Hour

type benchmarkRow struct {
	ID                           string                     `json:"id"`
	Availability                 carriers.AvailabilityClass `json:"availability"`
	Source                       string                     `json:"source"`
	RuntimeID                    string                     `json:"runtimeId"`
	Construction                 string                     `json:"construction"`
	CatalogIDs                   []string                   `json:"catalogIds"`
	TrafficClasses               []string                   `json:"trafficClasses"`
	Capabilities                 []string                   `json:"capabilities"`
	ModeledLatencyMs             *int64                     `json:"modeledLatencyMs"`
	ModeledThroughputBytesPerSec *int64                     `json:"modeledThroughputBytesPerSec"`
	ModeledDailyCapacityBytes    *int64                     `json:"modeledDailyCapacityBytes"`
	DailyCapacitySource          string                     `json:"dailyCapacitySource"`
}

type report struct {
	SchemaVersion int            `json:"schemaVersion"`
	Mode          string         `json:"mode"`
	Methodology   map[string]any `json:"methodology"`
	ProofBoundary map[string]any `json:"proofBoundary"`
	Results       []benchmarkRow `json:"results"`
}

func main() {
	if err := json.NewEncoder(os.Stdout).Encode(buildReport()); err != nil {
		fmt.Fprintf(os.Stderr, "encode carrier benchmark: %v\n", err)
		os.Exit(1)
	}
}

func buildReport() report {
	descriptors := make(map[string]carriers.Descriptor, len(carriers.StandardDescriptors()))
	for _, descriptor := range carriers.StandardDescriptors() {
		descriptors[descriptor.ID] = descriptor
	}

	rows := make([]benchmarkRow, 0, len(carriers.CapabilityInventory()))
	for _, inventory := range carriers.CapabilityInventory() {
		row := benchmarkRow{
			ID: inventory.ID, Availability: inventory.Availability, Source: inventory.Source,
			RuntimeID: inventory.RuntimeID, Construction: inventory.Construction,
			CatalogIDs: inventory.CatalogIDs, TrafficClasses: inventory.TrafficClasses,
			Capabilities: inventory.Capabilities,
		}
		if descriptor, ok := descriptors[inventory.ID]; ok {
			applyDescriptorMetrics(&row, descriptor)
		}
		rows = append(rows, row)
	}

	return report{
		SchemaVersion: 1,
		Mode:          "canonical-catalog-model",
		Methodology: map[string]any{
			"source":        "carriers.CapabilityInventory() joined by exact ID to carriers.StandardDescriptors()",
			"latency":       "descriptor Metrics.Latency when present; otherwise null",
			"throughput":    "descriptor Metrics.BandwidthBPS when present; otherwise null",
			"dailyCapacity": "descriptor Limits.DailyBytes when present; otherwise descriptor bandwidth multiplied by 24 hours; otherwise null",
		},
		ProofBoundary: map[string]any{
			"measured":      []string{"local command execution", "canonical Go catalog constants"},
			"modeled":       []string{"latency", "throughput", "daily capacity"},
			"notMeasured":   []string{"provider API latency", "provider quota", "network bandwidth", "remote delivery", "end-to-end tunnel throughput"},
			"networkAccess": false,
		},
		Results: rows,
	}
}

func applyDescriptorMetrics(row *benchmarkRow, descriptor carriers.Descriptor) {
	if descriptor.Metrics.Latency > 0 {
		value := descriptor.Metrics.Latency.Milliseconds()
		row.ModeledLatencyMs = &value
	}
	if descriptor.Metrics.BandwidthBPS > 0 {
		value := descriptor.Metrics.BandwidthBPS
		row.ModeledThroughputBytesPerSec = &value
	}
	if descriptor.Limits.DailyBytes > 0 {
		value := descriptor.Limits.DailyBytes
		row.ModeledDailyCapacityBytes = &value
		row.DailyCapacitySource = "limits.dailyBytes"
		return
	}
	if descriptor.Metrics.BandwidthBPS > 0 {
		value := descriptor.Metrics.BandwidthBPS * int64(day/time.Second)
		row.ModeledDailyCapacityBytes = &value
		row.DailyCapacitySource = "metrics.bandwidthBPS*86400"
	}
}
