package policy

import (
	"context"
	"testing"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestDispatchScheduledPayloadWritesMirrorsImmediately(t *testing.T) {
	primary := carriers.NewMemoryCarrier("vk.messages")
	mirror := carriers.NewMemoryCarrier("ok.messages")
	bindings := map[string]CarrierBinding{
		"vk.messages": binding(primary, "primary"),
		"ok.messages": binding(mirror, "mirror"),
	}
	base := fabric.NewEnvelope("control-1", fabric.TrafficControl, "session.state", []byte("hello world"))
	placements := []ChunkPlacement{
		{Index: 0, Offset: 0, Size: 5, CarrierID: "vk.messages", MirrorCarrierIDs: []string{"ok.messages"}},
	}

	result, err := DispatchScheduledPayload(context.Background(), base, placements, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if result.ImmediateWrites != 2 || len(result.PendingHedges) != 0 {
		t.Fatalf("unexpected dispatch result: %+v", result)
	}
	assertPayloads(t, primary, bindings["vk.messages"].Endpoint, []string{"hello"})
	assertPayloads(t, mirror, bindings["ok.messages"].Endpoint, []string{"hello"})
}

func TestDispatchScheduledPayloadReturnsHedgesWithoutImmediateWrite(t *testing.T) {
	primary := carriers.NewMemoryCarrier("vk.messages")
	hedge := carriers.NewMemoryCarrier("ok.messages")
	bindings := map[string]CarrierBinding{
		"vk.messages": binding(primary, "primary"),
		"ok.messages": binding(hedge, "hedge"),
	}
	base := fabric.NewEnvelope("admin-1", fabric.TrafficAdmin, "admin.command", []byte("payload"))
	placements := []ChunkPlacement{
		{Index: 0, Offset: 0, Size: 7, CarrierID: "vk.messages", HedgeCarrierIDs: []string{"ok.messages"}, HedgeAfter: time.Second},
	}

	result, err := DispatchScheduledPayload(context.Background(), base, placements, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if result.ImmediateWrites != 1 || len(result.PendingHedges) != 1 {
		t.Fatalf("unexpected dispatch result: %+v", result)
	}
	assertPayloads(t, primary, bindings["vk.messages"].Endpoint, []string{"payload"})
	assertPayloads(t, hedge, bindings["ok.messages"].Endpoint, nil)
	if result.PendingHedges[0].CarrierID != "ok.messages" || result.PendingHedges[0].Envelope.PayloadType != "admin.command.chunk" {
		t.Fatalf("bad pending hedge: %+v", result.PendingHedges[0])
	}
}

func TestDispatchScheduledPayloadStripesAcrossBindings(t *testing.T) {
	first := carriers.NewMemoryCarrier("vk.docs.1024")
	second := carriers.NewMemoryCarrier("ok.docs.256")
	bindings := map[string]CarrierBinding{
		"vk.docs.1024": binding(first, "first"),
		"ok.docs.256":  binding(second, "second"),
	}
	base := fabric.NewEnvelope("bulk-1", fabric.TrafficBulk, "bulk.frame", []byte("abcdefghijkl"))
	placements := []ChunkPlacement{
		{Index: 0, Offset: 0, Size: 4, CarrierID: "vk.docs.1024"},
		{Index: 1, Offset: 4, Size: 4, CarrierID: "ok.docs.256"},
		{Index: 2, Offset: 8, Size: 4, CarrierID: "vk.docs.1024"},
	}

	result, err := DispatchScheduledPayload(context.Background(), base, placements, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if result.ImmediateWrites != 3 {
		t.Fatalf("expected three writes, got %+v", result)
	}
	assertPayloads(t, first, bindings["vk.docs.1024"].Endpoint, []string{"abcd", "ijkl"})
	assertPayloads(t, second, bindings["ok.docs.256"].Endpoint, []string{"efgh"})
}

func TestDispatchScheduledPayloadRejectsMissingBinding(t *testing.T) {
	base := fabric.NewEnvelope("bulk-1", fabric.TrafficBulk, "bulk.frame", []byte("abc"))
	placements := []ChunkPlacement{{Index: 0, Offset: 0, Size: 3, CarrierID: "missing"}}

	if _, err := DispatchScheduledPayload(context.Background(), base, placements, map[string]CarrierBinding{}); err == nil {
		t.Fatal("expected missing binding error")
	}
}

func binding(carrier carriers.Carrier, endpointID string) CarrierBinding {
	return CarrierBinding{
		Carrier: carrier,
		Endpoint: carriers.Endpoint{
			ID:      endpointID,
			Carrier: carrier.Descriptor().ID,
			Address: "memory://" + endpointID,
		},
	}
}

func assertPayloads(t *testing.T, carrier carriers.Carrier, endpoint carriers.Endpoint, expected []string) {
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
		if envelope.PayloadType == "bulk.frame" || envelope.PayloadType == "session.state" {
			t.Fatalf("payload type was not chunked: %s", envelope.PayloadType)
		}
	}
}

func TestWritePlacementCompoundKeyResolvesBinding(t *testing.T) {
	carrier := carriers.NewMemoryCarrier("vk.messages")
	bindings := map[string]CarrierBinding{
		"vk.messages:discovery": {
			Carrier:  carrier,
			Endpoint: carriers.Endpoint{ID: "vk.messages:discovery", Carrier: "vk.messages", Address: "peer-1"},
			Role:     "discovery",
		},
	}

	// writePlacement with plain "vk.messages" should resolve to the compound key.
	ctx := context.Background()
	env := fabric.NewEnvelope("test-1", fabric.TrafficControl, "test", []byte("data"))
	if err := writePlacement(ctx, "vk.messages", env, bindings); err != nil {
		t.Fatalf("writePlacement with plain carrier ID failed: %v", err)
	}

	// Verify the write went to the correct endpoint.
	read, err := carrier.Read(ctx, bindings["vk.messages:discovery"].Endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 1 || string(read.Envelopes[0].Payload) != "data" {
		t.Fatalf("unexpected read: %+v", read.Envelopes)
	}
}

func TestWritePlacementExplicitBindingKey(t *testing.T) {
	carrier := carriers.NewMemoryCarrier("vk.messages")
	bindings := map[string]CarrierBinding{
		"vk.messages:discovery": {
			Carrier:  carrier,
			Endpoint: carriers.Endpoint{ID: "vk.messages:discovery", Carrier: "vk.messages", Address: "peer-1"},
			Role:     "discovery",
		},
		"vk.messages:node-client": {
			Carrier:  carrier,
			Endpoint: carriers.Endpoint{ID: "vk.messages:node-client", Carrier: "vk.messages", Address: "peer-2"},
			Role:     "node-client",
		},
	}

	ctx := context.Background()
	env := fabric.NewEnvelope("test-2", fabric.TrafficControl, "test", []byte("targeted"))
	// Write using explicit compound key.
	if err := writePlacement(ctx, "vk.messages:node-client", env, bindings); err != nil {
		t.Fatalf("writePlacement with explicit binding key failed: %v", err)
	}

	read, err := carrier.Read(ctx, bindings["vk.messages:node-client"].Endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Envelopes) != 1 || string(read.Envelopes[0].Payload) != "targeted" {
		t.Fatalf("unexpected read from node-client endpoint: %+v", read.Envelopes)
	}
}

func TestDispatchWithBindingKeyPlacement(t *testing.T) {
	carrier := carriers.NewMemoryCarrier("vk.messages")
	bindings := map[string]CarrierBinding{
		"vk.messages:discovery": {
			Carrier:  carrier,
			Endpoint: carriers.Endpoint{ID: "vk.messages:discovery", Carrier: "vk.messages", Address: "peer-1"},
			Role:     "discovery",
		},
	}
	base := fabric.NewEnvelope("ctrl-1", fabric.TrafficControl, "session.state", []byte("hello"))
	placements := []ChunkPlacement{
		{Index: 0, Offset: 0, Size: 5, CarrierID: "vk.messages", BindingKey: "vk.messages:discovery"},
	}

	result, err := DispatchScheduledPayload(context.Background(), base, placements, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if result.ImmediateWrites != 1 {
		t.Fatalf("expected 1 write, got %+v", result)
	}
}
