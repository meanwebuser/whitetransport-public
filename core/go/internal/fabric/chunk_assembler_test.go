package fabric

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestChunkAssemblerReassemblesCompletePayload(t *testing.T) {
	// Simulate 3 chunks from a chunked payload.
	basePayload := []byte("abcdefghij")
	chunks := []Envelope{
		{
			Version: 1, ID: "bulk-1.0", TrafficClass: TrafficBulk,
			PayloadType: "bulk.frame.chunk", Sequence: 0,
			ChunkIndex: 0, ChunkTotal: 3,
			CreatedAt: time.Now(), Payload: basePayload[0:4],
		},
		{
			Version: 1, ID: "bulk-1.1", TrafficClass: TrafficBulk,
			PayloadType: "bulk.frame.chunk", Sequence: 1,
			ChunkIndex: 1, ChunkTotal: 3,
			CreatedAt: time.Now(), Payload: basePayload[4:7],
		},
		{
			Version: 1, ID: "bulk-1.2", TrafficClass: TrafficBulk,
			PayloadType: "bulk.frame.chunk", Sequence: 2,
			ChunkIndex: 2, ChunkTotal: 3,
			CreatedAt: time.Now(), Payload: basePayload[7:10],
		},
	}

	asm := NewChunkAssembler()
	for _, chunk := range chunks {
		base := asm.Add(chunk)
		if base != "bulk-1" {
			t.Fatalf("expected base ID bulk-1, got %s", base)
		}
	}

	if !asm.IsComplete("bulk-1") {
		t.Fatal("expected group to be complete")
	}

	reassembled, err := asm.Assemble("bulk-1")
	if err != nil {
		t.Fatal(err)
	}
	if reassembled.ID != "bulk-1" {
		t.Fatalf("expected ID bulk-1, got %s", reassembled.ID)
	}
	if reassembled.PayloadType != "bulk.frame" {
		t.Fatalf("expected payload type bulk.frame, got %s", reassembled.PayloadType)
	}
	if !bytes.Equal(reassembled.Payload, basePayload) {
		t.Fatalf("expected payload %q, got %q", basePayload, reassembled.Payload)
	}
	if reassembled.ChunkIndex != 0 || reassembled.ChunkTotal != 0 {
		t.Fatalf("reassembled envelope should not have chunk metadata: %+v", reassembled)
	}
}

func TestChunkAssemblerReportsIncompleteGroup(t *testing.T) {
	asm := NewChunkAssembler()
	asm.Add(Envelope{
		Version: 1, ID: "msg.0", TrafficClass: TrafficControl,
		PayloadType: "control.frame.chunk",
		ChunkIndex: 0, ChunkTotal: 3,
		Payload: []byte("a"),
	})
	asm.Add(Envelope{
		Version: 1, ID: "msg.1", TrafficClass: TrafficControl,
		PayloadType: "control.frame.chunk",
		ChunkIndex: 1, ChunkTotal: 3,
		Payload: []byte("b"),
	})

	if asm.IsComplete("msg") {
		t.Fatal("group should be incomplete with 2/3 chunks")
	}

	have, total := asm.GroupSize("msg")
	if have != 2 || total != 3 {
		t.Fatalf("expected 2/3, got %d/%d", have, total)
	}

	if _, err := asm.Assemble("msg"); err == nil {
		t.Fatal("expected error assembling incomplete group")
	}
}

func TestChunkAssemblerHandlesOutOfOrderChunks(t *testing.T) {
	asm := NewChunkAssembler()
	// Add chunks in reverse order.
	for i := 2; i >= 0; i-- {
		asm.Add(Envelope{
			Version: 1, ID: fmt.Sprintf("data.%d", i),
			TrafficClass: TrafficBulk, PayloadType: "bulk.frame.chunk",
			ChunkIndex: i, ChunkTotal: 3,
			Payload: []byte{byte('a' + i)},
		})
	}

	reassembled, err := asm.Assemble("data")
	if err != nil {
		t.Fatal(err)
	}
	if string(reassembled.Payload) != "abc" {
		t.Fatalf("expected abc, got %s", string(reassembled.Payload))
	}
}

func TestChunkAssemblerIgnoresStandaloneEnvelopes(t *testing.T) {
	asm := NewChunkAssembler()
	base := asm.Add(Envelope{
		Version: 1, ID: "standalone", TrafficClass: TrafficControl,
		PayloadType: "session.offer", Payload: []byte("hello"),
	})
	if base != "standalone" {
		t.Fatalf("expected standalone ID returned as-is, got %s", base)
	}
	if len(asm.PendingGroups()) != 0 {
		t.Fatalf("standalone should not create a group: %v", asm.PendingGroups())
	}
}

func TestChunkAssemblerDeduplicatesChunks(t *testing.T) {
	asm := NewChunkAssembler()
	chunk := Envelope{
		Version: 1, ID: "dup.0", TrafficClass: TrafficBulk,
		PayloadType: "bulk.frame.chunk",
		ChunkIndex: 0, ChunkTotal: 1,
		Payload: []byte("once"),
	}
	asm.Add(chunk)
	asm.Add(chunk) // duplicate

	have, _ := asm.GroupSize("dup")
	if have != 1 {
		t.Fatalf("expected 1 chunk after dedup, got %d", have)
	}
}

func TestChunkAssemblerPendingGroups(t *testing.T) {
	asm := NewChunkAssembler()
	asm.Add(Envelope{
		Version: 1, ID: "b.0", TrafficClass: TrafficBulk,
		PayloadType: "x.chunk", ChunkIndex: 0, ChunkTotal: 2, Payload: []byte("1"),
	})
	asm.Add(Envelope{
		Version: 1, ID: "a.0", TrafficClass: TrafficBulk,
		PayloadType: "x.chunk", ChunkIndex: 0, ChunkTotal: 1, Payload: []byte("2"),
	})

	pending := asm.PendingGroups()
	if len(pending) != 2 || pending[0] != "a" || pending[1] != "b" {
		t.Fatalf("expected sorted pending [a b], got %v", pending)
	}

	// Assemble complete group "a" -> should be removed.
	if _, err := asm.Assemble("a"); err != nil {
		t.Fatal(err)
	}
	pending = asm.PendingGroups()
	if len(pending) != 1 || pending[0] != "b" {
		t.Fatalf("expected [b] after assembling a, got %v", pending)
	}
}

func TestEnvelopeIsChunkAndBaseID(t *testing.T) {
	standalone := Envelope{Version: 1, ID: "msg-1"}
	if standalone.IsChunk() {
		t.Fatal("standalone should not be a chunk")
	}
	if standalone.BaseID() != "msg-1" {
		t.Fatalf("standalone BaseID should be msg-1, got %s", standalone.BaseID())
	}

	chunk := Envelope{Version: 1, ID: "msg-1.2", ChunkIndex: 2, ChunkTotal: 5}
	if !chunk.IsChunk() {
		t.Fatal("should be a chunk")
	}
	if chunk.BaseID() != "msg-1" {
		t.Fatalf("chunk BaseID should be msg-1, got %s", chunk.BaseID())
	}
}
