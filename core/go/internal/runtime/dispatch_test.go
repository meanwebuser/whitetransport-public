package runtime

import (
	"context"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

func TestDispatchPayloadMirrorsControlAcrossMailboxBindings(t *testing.T) {
	vk := carriers.NewMemoryCarrier(carriers.CarrierVKMessages)
	ok := carriers.NewMemoryCarrier(carriers.CarrierOKMessages)
	bindings := runtimeBindings(
		runtimeBinding(vk, "vk-control"),
		runtimeBinding(ok, "ok-control"),
	)

	report, err := DispatchPayload(context.Background(), policy.DefaultAdaptivePolicy(), bindings, DispatchRequest{
		ID:          "control-1",
		Traffic:     fabric.TrafficControl,
		PayloadType: "session.state",
		Payload:     []byte("hello"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if report.Plan.Strategy != policy.DeliveryMirrored || report.Result.ImmediateWrites != 2 {
		t.Fatalf("expected mirrored control dispatch, got plan=%+v result=%+v", report.Plan, report.Result)
	}
	assertRuntimePayloads(t, vk, bindings[carriers.CarrierVKMessages].Endpoint, []string{"hello"})
	assertRuntimePayloads(t, ok, bindings[carriers.CarrierOKMessages].Endpoint, []string{"hello"})
}

func TestDispatchPayloadStripesBulkAcrossDocumentBindings(t *testing.T) {
	vkDesc, err := carriers.FindStandardDescriptor(carriers.CarrierVKDocs1024)
	if err != nil {
		t.Fatal(err)
	}
	okDesc, err := carriers.FindStandardDescriptor(carriers.CarrierOKDocs256)
	if err != nil {
		t.Fatal(err)
	}
	vk := carriers.NewMemoryCarrierWithDescriptor(vkDesc)
	ok := carriers.NewMemoryCarrierWithDescriptor(okDesc)
	bindings := runtimeBindings(
		runtimeBinding(vk, "vk-bulk"),
		runtimeBinding(ok, "ok-bulk"),
	)
	payload := patternedPayload(okDesc.Limits.ChunkPayloadBytes + 1024)

	report, err := DispatchPayload(context.Background(), policy.DefaultAdaptivePolicy(), bindings, DispatchRequest{
		ID:          "bulk-1",
		Traffic:     fabric.TrafficBulk,
		PayloadType: "bulk.frame",
		Payload:     payload,
	})
	if err != nil {
		t.Fatal(err)
	}

	if report.Plan.Strategy != policy.DeliveryStriped || report.Result.ImmediateWrites != 2 || len(report.Placements) != 2 {
		t.Fatalf("expected striped bulk dispatch with two chunks, got placements=%+v result=%+v", report.Placements, report.Result)
	}
	if report.Placements[0].CarrierID != carriers.CarrierVKDocs1024 || report.Placements[1].CarrierID != carriers.CarrierOKDocs256 {
		t.Fatalf("expected bulk chunks on vk then ok docs, got %+v", report.Placements)
	}
	assertRuntimePayloadSizes(t, vk, bindings[carriers.CarrierVKDocs1024].Endpoint, []int{okDesc.Limits.ChunkPayloadBytes})
	assertRuntimePayloadSizes(t, ok, bindings[carriers.CarrierOKDocs256].Endpoint, []int{1024})
}

func TestDispatchPayloadKeepsEgressImmediateWriteOnPrimary(t *testing.T) {
	stream := carriers.NewMemoryCarrier(carriers.CarrierWBStreamVP8)
	yandexDesc, err := carriers.FindStandardDescriptor(carriers.CarrierYandexDisk)
	if err != nil {
		t.Fatal(err)
	}
	yandex := carriers.NewMemoryCarrierWithDescriptor(yandexDesc)
	bindings := runtimeBindings(
		runtimeBinding(stream, "stream"),
		runtimeBinding(yandex, "yandex-disk"),
	)

	report, err := DispatchPayload(context.Background(), policy.DefaultAdaptivePolicy(), bindings, DispatchRequest{
		ID:          "egress-1",
		Traffic:     fabric.TrafficEgress,
		PayloadType: "egress.frame",
		Payload:     []byte("request"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if report.Plan.Strategy != policy.DeliveryHedged || report.Result.ImmediateWrites != 1 || len(report.Result.PendingHedges) != 1 {
		t.Fatalf("expected primary egress write with slow hedge candidate, got plan=%+v result=%+v", report.Plan, report.Result)
	}
	assertRuntimePayloads(t, stream, bindings[carriers.CarrierWBStreamVP8].Endpoint, []string{"request"})
	assertRuntimePayloads(t, yandex, bindings[carriers.CarrierYandexDisk].Endpoint, nil)
}

func TestDispatchPayloadValidatesRequestAndBindings(t *testing.T) {
	bindings := runtimeBindings(runtimeBinding(carriers.NewMemoryCarrier(carriers.CarrierVKMessages), "vk-control"))

	if _, err := DispatchPayload(context.Background(), policy.DefaultAdaptivePolicy(), bindings, DispatchRequest{}); err == nil {
		t.Fatal("expected missing request fields error")
	}
	if _, err := DispatchPayload(context.Background(), policy.DefaultAdaptivePolicy(), map[string]policy.CarrierBinding{}, DispatchRequest{
		ID:          "control-1",
		Traffic:     fabric.TrafficControl,
		PayloadType: "session.state",
		Payload:     []byte("hello"),
	}); err == nil {
		t.Fatal("expected missing bindings error")
	}
}

func runtimeBinding(carrier carriers.Carrier, endpointID string) policy.CarrierBinding {
	return policy.CarrierBinding{
		Carrier: carrier,
		Endpoint: carriers.Endpoint{
			ID:      endpointID,
			Carrier: carrier.Descriptor().ID,
			Address: "memory://" + endpointID,
		},
	}
}

func runtimeBindings(bindings ...policy.CarrierBinding) map[string]policy.CarrierBinding {
	out := make(map[string]policy.CarrierBinding, len(bindings))
	for _, binding := range bindings {
		out[binding.Carrier.Descriptor().ID] = binding
	}
	return out
}

func assertRuntimePayloads(t *testing.T, carrier carriers.Carrier, endpoint carriers.Endpoint, expected []string) {
	t.Helper()
	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != len(expected) {
		t.Fatalf("expected %d envelopes, got %d", len(expected), len(read.Envelopes))
	}
	for i, envelope := range read.Envelopes {
		if string(envelope.Payload) != expected[i] {
			t.Fatalf("payload %d: expected %q, got %q", i, expected[i], string(envelope.Payload))
		}
		if envelope.PayloadType == "bulk.frame" || envelope.PayloadType == "session.state" || envelope.PayloadType == "egress.frame" {
			t.Fatalf("payload type was not chunked: %s", envelope.PayloadType)
		}
	}
}

func assertRuntimePayloadSizes(t *testing.T, carrier carriers.Carrier, endpoint carriers.Endpoint, expected []int) {
	t.Helper()
	read, err := carrier.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != len(expected) {
		t.Fatalf("expected %d envelopes, got %d", len(expected), len(read.Envelopes))
	}
	for i, envelope := range read.Envelopes {
		if len(envelope.Payload) != expected[i] {
			t.Fatalf("payload %d: expected %d bytes, got %d", i, expected[i], len(envelope.Payload))
		}
	}
}

func patternedPayload(size int) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	return payload
}
