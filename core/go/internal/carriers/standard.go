package carriers

import (
	"fmt"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

const (
	CarrierVKMessages = "vk.messages"
	CarrierOKMessages = "ok.messages"
	// Deprecated: CarrierWBStreamVP8 is retained for descriptor catalog compatibility.
	// Use the adapter registry with "wbstream", "telemost", or "dion" instead.
	CarrierWBStreamVP8  = "wbstream.vp8"
	CarrierVKDocs256    = "vk.docs.256"
	CarrierVKDocs1024   = "vk.docs.1024"
	CarrierVKPhotos     = "vk.photos"
	CarrierOKDocs256    = "ok.docs.256"
	CarrierOKPhotos     = "ok.photos"
	CarrierYandexDisk   = "yandex.disk.files"
	CarrierSSHTCP       = "ssh.tcp"
	CarrierSSHFabric    = "ssh.fabric"
	CarrierSingBoxVLESS = "singbox.vless"
	CarrierDIONCall     = "dion.call"
)

// StandardDescriptors returns the measured carrier profiles shared by clients
// and nodes. These are catalog facts, not credentials or live endpoints.
//
// Native carriers (created directly in bindings.go, no Provider bridge):
//
//	vk.messages, ok.messages, vk.docs.*, ok.docs.*, vk.photos, ok.photos,
//	yandex.disk.files, ssh.tcp, singbox.vless
//
// Bridged carriers (created via ProviderCarrier + adapter registry):
//
//	wbstream.vp8, telemost, dion.call
func StandardDescriptors() []Descriptor {
	return []Descriptor{
		{
			ID:             CarrierVKMessages,
			Provider:       "vk",
			Mode:           DeliveryMailbox,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficEgress},
			Capabilities:   []Capability{CapRendezvous, CapMailbox, CapRetained, CapRetrospective, CapMutable, CapList, CapPoll, CapEdit, CapDelete, CapDurable, CapOrdered},
			Limits:         Limits{MaxPayloadBytes: 4096, ChunkPayloadBytes: 3072, SendsPerMinute: 120, PollsPerMinute: 120, DailyBytes: 1_000_000_000},
			Metrics:        Metrics{Healthy: true, Latency: 200 * time.Millisecond, BandwidthBPS: 12 * 1024, Reliability: 0.95, QuotaRemaining: 1_000_000_000},
			Notes:          "VK messaging API; encrypted bootstrap/control/log/egress carrier. Supports TCP-over-envelope tunneling.",
		},
		{
			ID:             CarrierOKMessages,
			Provider:       "ok",
			Mode:           DeliveryMailbox,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficEgress},
			Capabilities:   []Capability{CapRendezvous, CapMailbox, CapRetained, CapRetrospective, CapMutable, CapList, CapPoll, CapDelete, CapDurable, CapOrdered},
			Limits:         Limits{MaxPayloadBytes: 4096, ChunkPayloadBytes: 3072, SendsPerMinute: 90, PollsPerMinute: 90, DailyBytes: 844_000_000},
			Metrics:        Metrics{Healthy: true, Latency: 250 * time.Millisecond, BandwidthBPS: 10 * 1024, Reliability: 0.93, QuotaRemaining: 844_000_000},
			Notes:          "OK messaging API; encrypted mailbox for mirrored/hedged control and egress. Supports TCP-over-envelope tunneling.",
		},
		{
			ID:             CarrierWBStreamVP8,
			Provider:       "wbstream",
			Mode:           DeliveryStream,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficStream, fabric.TrafficEgress},
			Capabilities:   []Capability{CapRendezvous, CapStream, CapDuplex, CapDatagram, CapEphemeral},
			Limits:         Limits{MaxPayloadBytes: 32768, ChunkPayloadBytes: 1200, SendsPerMinute: 0, PollsPerMinute: 0},
			Metrics:        Metrics{Healthy: true, BandwidthBPS: 7811 * 1024, Reliability: 0.85, QuotaRemaining: -1},
			Notes:          "Realtime primary carrier over WBStream DataChannel; measured faster than VP8 video frames on the same room.",
		},
		{
			ID:             CarrierVKDocs256,
			Provider:       "vk",
			Mode:           DeliveryBulk,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBulk, fabric.TrafficRepair},
			Capabilities:   []Capability{CapBulk, CapRetained, CapList, CapPoll, CapDelete, CapDurable},
			Limits:         Limits{MaxPayloadBytes: 192 * 1024, ChunkPayloadBytes: 192 * 1024, SendsPerMinute: 180, PollsPerMinute: 180, DailyBytes: 49_000_000_000},
			Metrics:        Metrics{Healthy: true, BandwidthBPS: 576 * 1024, Reliability: 0.92, QuotaRemaining: 49_000_000_000},
			Notes:          "VK document 256x256 PNG payload from YTP budget; reliable high-throughput fallback/repair path.",
		},
		{
			ID:             CarrierVKDocs1024,
			Provider:       "vk",
			Mode:           DeliveryBulk,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBulk, fabric.TrafficRepair},
			Capabilities:   []Capability{CapBulk, CapRetained, CapList, CapPoll, CapDelete, CapDurable},
			Limits:         Limits{MaxPayloadBytes: 3 * 1024 * 1024, ChunkPayloadBytes: 3 * 1024 * 1024, SendsPerMinute: 180, PollsPerMinute: 180, DailyBytes: 798_000_000_000},
			Metrics:        Metrics{Healthy: true, BandwidthBPS: 9200 * 1024, Reliability: 0.92, QuotaRemaining: 798_000_000_000},
			Notes:          "VK document 1024x1024 PNG payload; fastest measured YTP provider, requires larger memory and token budget.",
		},
		{
			ID:             CarrierVKPhotos,
			Provider:       "vk",
			Mode:           DeliveryBulk,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficRepair},
			Capabilities:   []Capability{CapBulk, CapRetained, CapList, CapPoll, CapDurable},
			Limits:         Limits{MaxPayloadBytes: 4096, ChunkPayloadBytes: 3072, SendsPerMinute: 180, PollsPerMinute: 180},
			Metrics:        Metrics{Healthy: true, BandwidthBPS: 12 * 1024, Reliability: 0.88, QuotaRemaining: -1},
			Notes:          "VK photo is retained as a low-throughput repair carrier because VK re-encodes images.",
		},
		{
			ID:             CarrierOKDocs256,
			Provider:       "ok",
			Mode:           DeliveryBulk,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBulk, fabric.TrafficRepair},
			Capabilities:   []Capability{CapBulk, CapRetained, CapList, CapPoll, CapDelete, CapDurable},
			Limits:         Limits{MaxPayloadBytes: 192 * 1024, ChunkPayloadBytes: 192 * 1024, SendsPerMinute: 150, PollsPerMinute: 150, DailyBytes: 40_000_000_000},
			Metrics:        Metrics{Healthy: true, BandwidthBPS: 480 * 1024, Reliability: 0.90, QuotaRemaining: 40_000_000_000},
			Notes:          "OK document upload fallback using the measured YTP 256x256 PNG budget.",
		},
		{
			ID:             CarrierOKPhotos,
			Provider:       "ok",
			Mode:           DeliveryBulk,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBulk, fabric.TrafficRepair},
			Capabilities:   []Capability{CapBulk, CapRetained, CapList, CapPoll, CapDurable},
			Limits:         Limits{MaxPayloadBytes: 192 * 1024, ChunkPayloadBytes: 192 * 1024, SendsPerMinute: 150, PollsPerMinute: 150},
			Metrics:        Metrics{Healthy: true, BandwidthBPS: 480 * 1024, Reliability: 0.88, QuotaRemaining: -1},
			Notes:          "OK photo PNG payload candidate; keep behind docs until runtime smoke tests confirm preservation.",
		},
		{
			ID:             CarrierYandexDisk,
			Provider:       "yandex",
			Mode:           DeliveryBulk,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBulk, fabric.TrafficRepair, fabric.TrafficEgress},
			Capabilities:   []Capability{CapBulk, CapRetained, CapRetrospective, CapMutable, CapList, CapPoll, CapOverwrite, CapDelete, CapDurable},
			Limits:         Limits{MaxPayloadBytes: 1 << 20, ChunkPayloadBytes: 1 << 20, SendsPerMinute: 120, PollsPerMinute: 120, DailyBytes: 50_000_000_000},
			Metrics:        Metrics{Healthy: true, Latency: 3 * time.Second, BandwidthBPS: 350 * 1024, Reliability: 0.90, QuotaRemaining: 50_000_000_000},
			Notes:          "Yandex Disk file mailbox; high-payload durable carrier for bulk egress, repair, retry, and background drain.",
		},
		{
			ID:             CarrierSSHTCP,
			Provider:       "ssh",
			Mode:           DeliveryStream,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficEgress},
			Capabilities:   []Capability{CapStream, CapDuplex, CapEphemeral, CapOrdered},
			Limits:         Limits{MaxPayloadBytes: 32768, ChunkPayloadBytes: 8192},
			Metrics:        Metrics{Healthy: true, Latency: 150 * time.Millisecond, BandwidthBPS: 2 * 1024 * 1024, Reliability: 0.90, CostPerMB: 0.01, QuotaRemaining: -1},
			Notes:          "SSH direct-tcpip outbound; NekoBox-style profile for fast egress over a reachable SSH server.",
		},
		{
			ID:             CarrierSSHFabric,
			Provider:       "ssh",
			Mode:           DeliveryStream,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog, fabric.TrafficStream, fabric.TrafficEgress, fabric.TrafficRepair},
			Capabilities:   []Capability{CapRendezvous, CapMailbox, CapRetained, CapStream, CapDuplex, CapOrdered},
			Limits:         Limits{MaxPayloadBytes: 32768, ChunkPayloadBytes: 8192},
			Metrics:        Metrics{Healthy: true, Latency: 150 * time.Millisecond, BandwidthBPS: 2 * 1024 * 1024, Reliability: 0.90, CostPerMB: 0.01, QuotaRemaining: -1},
			Notes:          "Persistent pinned SSH session carrying retained WhiteTransport control envelopes and direct-tcpip egress streams.",
		},
		{
			ID:             CarrierSingBoxVLESS,
			Provider:       "sing-box",
			Mode:           DeliveryStream,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficEgress},
			Capabilities:   []Capability{CapStream, CapDuplex, CapEphemeral, CapOrdered},
			Limits:         Limits{MaxPayloadBytes: 32768, ChunkPayloadBytes: 16384},
			Metrics:        Metrics{Healthy: true, Latency: 80 * time.Millisecond, BandwidthBPS: 16 * 1024 * 1024, Reliability: 0.92, CostPerMB: 0.02, QuotaRemaining: -1},
			Notes:          "sing-box VLESS outbound; fast realtime egress and control/log relay through a compatible Xray/sing-box server.",
		},
	}
}

// FindStandardDescriptor returns one standard carrier descriptor by id.
func FindStandardDescriptor(id string) (Descriptor, error) {
	for _, desc := range StandardDescriptors() {
		if desc.ID == id {
			return desc, nil
		}
	}
	// Local file-backed mailbox is a test/dev carrier kept out of the public
	// standard catalog but still resolvable for config validation.
	if id == CarrierFileMailbox {
		return Descriptor{
			ID:             CarrierFileMailbox,
			Provider:       "local",
			Mode:           DeliveryMailbox,
			TrafficClasses: []fabric.TrafficClass{fabric.TrafficBootstrap, fabric.TrafficControl, fabric.TrafficAdmin, fabric.TrafficHealth, fabric.TrafficLog},
			Capabilities:   []Capability{CapRendezvous, CapMailbox, CapRetained},
			Limits:         Limits{MaxPayloadBytes: 1 << 20, ChunkPayloadBytes: 1 << 20, SendsPerMinute: 6000, PollsPerMinute: 6000},
			Metrics:        Metrics{Healthy: true, Latency: time.Millisecond, BandwidthBPS: 100 * 1024 * 1024, Reliability: 1.0, QuotaRemaining: -1},
			Notes:          "Local file-backed mailbox for deterministic cross-process control testing; not for production.",
		}, nil
	}
	return Descriptor{}, fmt.Errorf("unknown standard carrier %q", id)
}
