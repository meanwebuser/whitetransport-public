package carriers

import (
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestStandardDescriptorsExposeNextCarrierSet(t *testing.T) {
	descriptors := StandardDescriptors()
	byID := map[string]Descriptor{}
	for _, desc := range descriptors {
		byID[desc.ID] = desc
	}

	expected := []string{
		CarrierVKMessages,
		CarrierOKMessages,
		CarrierWBStreamVP8,
		CarrierVKDocs256,
		CarrierVKDocs1024,
		CarrierVKPhotos,
		CarrierOKDocs256,
		CarrierOKPhotos,
		CarrierYandexDisk,
		CarrierSSHTCP,
		CarrierSSHFabric,
		CarrierSingBoxVLESS,
		CarrierHysteria2,
		CarrierTailscale,
		CarrierDNSTXT,
	}
	for _, id := range expected {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing standard carrier descriptor %s", id)
		}
	}

	vkMessages := byID[CarrierVKMessages]
	if vkMessages.Mode != DeliveryMailbox || !hasTrafficClass(vkMessages, fabric.TrafficBootstrap) || !hasTrafficClass(vkMessages, fabric.TrafficControl) {
		t.Fatalf("vk.messages must be bootstrap/control mailbox, got %+v", vkMessages)
	}
	if vkMessages.Limits.MaxPayloadBytes != 4096 {
		t.Fatalf("vk.messages max payload changed: %d", vkMessages.Limits.MaxPayloadBytes)
	}
	if !hasCapability(vkMessages, CapStream) || !hasCapability(vkMessages, CapDuplex) {
		t.Fatalf("vk.messages must expose its bidirectional envelope egress capability, got %+v", vkMessages)
	}
	okMessages := byID[CarrierOKMessages]
	if okMessages.Mode != DeliveryMailbox || !hasTrafficClass(okMessages, fabric.TrafficControl) {
		t.Fatalf("ok.messages must be control mailbox mirror candidate, got %+v", okMessages)
	}

	wbVP8 := byID[CarrierWBStreamVP8]
	if wbVP8.Mode != DeliveryStream || !hasTrafficClass(wbVP8, fabric.TrafficStream) || !hasTrafficClass(wbVP8, fabric.TrafficEgress) {
		t.Fatalf("wbstream.vp8 must be stream carrier, got %+v", wbVP8)
	}
	if wbVP8.Limits.ChunkPayloadBytes != 1200 {
		t.Fatalf("wbstream.vp8 chunk budget should stay MTU-like, got %d", wbVP8.Limits.ChunkPayloadBytes)
	}

	vkDocs1024 := byID[CarrierVKDocs1024]
	if vkDocs1024.Metrics.BandwidthBPS <= byID[CarrierVKDocs256].Metrics.BandwidthBPS {
		t.Fatalf("vk.docs.1024 should remain higher throughput than vk.docs.256")
	}
	yandexDisk := byID[CarrierYandexDisk]
	if yandexDisk.Mode != DeliveryBulk || !hasTrafficClass(yandexDisk, fabric.TrafficEgress) || yandexDisk.Limits.MaxPayloadBytes < 1<<20 {
		t.Fatalf("yandex.disk.files must be a large durable egress-capable carrier, got %+v", yandexDisk)
	}
	sshTCP := byID[CarrierSSHTCP]
	if sshTCP.Mode != DeliveryStream || !hasTrafficClass(sshTCP, fabric.TrafficEgress) || !hasCapability(sshTCP, CapDuplex) {
		t.Fatalf("ssh.tcp must be a duplex egress carrier, got %+v", sshTCP)
	}
	sshFabric := byID[CarrierSSHFabric]
	if sshFabric.Mode != DeliveryStream || !hasTrafficClass(sshFabric, fabric.TrafficControl) || !hasTrafficClass(sshFabric, fabric.TrafficStream) || !hasTrafficClass(sshFabric, fabric.TrafficEgress) {
		t.Fatalf("ssh.fabric must carry control and stream egress, got %+v", sshFabric)
	}
	for _, capability := range []Capability{CapRendezvous, CapMailbox, CapRetained, CapStream, CapDuplex, CapOrdered} {
		if !hasCapability(sshFabric, capability) {
			t.Fatalf("ssh.fabric missing capability %s: %+v", capability, sshFabric)
		}
	}
	singBoxVLESS := byID[CarrierSingBoxVLESS]
	if singBoxVLESS.Mode != DeliveryStream || !hasTrafficClass(singBoxVLESS, fabric.TrafficEgress) || !hasCapability(singBoxVLESS, CapDuplex) {
		t.Fatalf("singbox.vless must be a duplex egress carrier, got %+v", singBoxVLESS)
	}
	hysteria2 := byID[CarrierHysteria2]
	if hysteria2.Mode != DeliveryStream || !hasTrafficClass(hysteria2, fabric.TrafficEgress) || !hasCapability(hysteria2, CapTCP) || !hasCapability(hysteria2, CapAuthenticated) {
		t.Fatalf("hysteria2 must be an authenticated TCP egress carrier, got %+v", hysteria2)
	}
	tailscale := byID[CarrierTailscale]
	if tailscale.Mode != DeliveryStream || !hasTrafficClass(tailscale, fabric.TrafficEgress) || !hasCapability(tailscale, CapTCP) || !hasCapability(tailscale, CapAuthenticated) {
		t.Fatalf("tailscale must be an authenticated TCP egress carrier, got %+v", tailscale)
	}
	dnsTXT := byID[CarrierDNSTXT]
	if dnsTXT.Mode != DeliveryMailbox || !hasTrafficClass(dnsTXT, fabric.TrafficBootstrap) || !hasTrafficClass(dnsTXT, fabric.TrafficControl) || !hasTrafficClass(dnsTXT, fabric.TrafficEgress) {
		t.Fatalf("dns.txt must be a bootstrap/control mailbox with last-resort egress, got %+v", dnsTXT)
	}
	if dnsTXT.Limits.MaxPayloadBytes != 4096 {
		t.Fatalf("dns.txt max payload changed: %d", dnsTXT.Limits.MaxPayloadBytes)
	}
}

func TestFindStandardDescriptorRejectsUnknownID(t *testing.T) {
	if _, err := FindStandardDescriptor("unknown.carrier"); err == nil {
		t.Fatal("expected unknown carrier error")
	}
}

func TestFindStandardDescriptorProvidesEvidenceState(t *testing.T) {
	for _, descriptor := range StandardDescriptors() {
		resolved, err := FindStandardDescriptor(descriptor.ID)
		if err != nil {
			t.Fatalf("resolve %s: %v", descriptor.ID, err)
		}
		if resolved.Evidence == nil {
			t.Fatalf("descriptor %s has no mutable evidence state", descriptor.ID)
		}
	}
}

func hasTrafficClass(desc Descriptor, traffic fabric.TrafficClass) bool {
	for _, candidate := range desc.TrafficClasses {
		if candidate == traffic {
			return true
		}
	}
	return false
}

func hasCapability(desc Descriptor, capability Capability) bool {
	for _, candidate := range desc.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}
