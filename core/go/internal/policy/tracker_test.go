package policy

import (
	"testing"
	"time"
)

func TestDeliveryTrackerTracksCompletionByAck(t *testing.T) {
	tracker := NewDeliveryTracker()
	sentAt := time.Unix(100, 0)
	tracker.Register([]ChunkPlacement{
		{Index: 0, Offset: 0, Size: 4, CarrierID: "vk.docs.1024"},
		{Index: 1, Offset: 4, Size: 4, CarrierID: "ok.docs.256"},
	}, sentAt)

	if tracker.Complete() {
		t.Fatal("new delivery should not be complete")
	}
	if !tracker.Ack(0, sentAt.Add(time.Second)) {
		t.Fatal("expected ack for registered chunk")
	}
	if tracker.Complete() {
		t.Fatal("one missing chunk should keep delivery incomplete")
	}
	if !tracker.Ack(1, sentAt.Add(2*time.Second)) {
		t.Fatal("expected ack for second registered chunk")
	}
	if !tracker.Complete() {
		t.Fatal("all chunks acked should complete delivery")
	}
}

func TestDeliveryTrackerReturnsDueHedgesOnlyAfterDeadline(t *testing.T) {
	tracker := NewDeliveryTracker()
	sentAt := time.Unix(100, 0)
	tracker.Register([]ChunkPlacement{
		{Index: 0, Offset: 0, Size: 4, CarrierID: "vk.messages", HedgeCarrierIDs: []string{"ok.messages"}, HedgeAfter: 750 * time.Millisecond},
		{Index: 1, Offset: 4, Size: 4, CarrierID: "vk.messages", HedgeCarrierIDs: []string{"ok.messages"}, HedgeAfter: 750 * time.Millisecond},
	}, sentAt)
	tracker.Ack(0, sentAt.Add(100*time.Millisecond))

	if due := tracker.DueHedges(sentAt.Add(500 * time.Millisecond)); len(due) != 0 {
		t.Fatalf("no hedges should be due before deadline, got %+v", due)
	}
	due := tracker.DueHedges(sentAt.Add(time.Second))
	if len(due) != 1 || due[0].Index != 1 {
		t.Fatalf("expected only unacked chunk 1 due, got %+v", due)
	}
}

func TestDeliveryTrackerBuildsRepairPlacementsForMissingChunks(t *testing.T) {
	tracker := NewDeliveryTracker()
	sentAt := time.Unix(100, 0)
	tracker.Register([]ChunkPlacement{
		{Index: 0, Offset: 0, Size: 4, CarrierID: "vk.docs.1024", MirrorCarrierIDs: []string{"ok.messages"}},
		{Index: 1, Offset: 4, Size: 4, CarrierID: "vk.docs.256"},
		{Index: 2, Offset: 8, Size: 4, CarrierID: "ok.docs.256", HedgeCarrierIDs: []string{"vk.photos"}, HedgeAfter: time.Second},
	}, sentAt)
	tracker.Ack(1, sentAt.Add(time.Second))

	repair := tracker.RepairPlacements([]string{"ok.docs.256", "vk.photos"})
	if len(repair) != 2 {
		t.Fatalf("expected two repair placements, got %+v", repair)
	}
	if repair[0].Index != 0 || repair[0].CarrierID != "ok.docs.256" {
		t.Fatalf("bad first repair placement: %+v", repair[0])
	}
	if repair[1].Index != 2 || repair[1].CarrierID != "vk.photos" {
		t.Fatalf("bad second repair placement: %+v", repair[1])
	}
	if len(repair[0].MirrorCarrierIDs) != 0 || len(repair[1].HedgeCarrierIDs) != 0 {
		t.Fatalf("repair placements should not inherit mirror/hedge metadata: %+v", repair)
	}
}

func TestDeliveryTrackerRejectsUnknownAck(t *testing.T) {
	tracker := NewDeliveryTracker()
	if tracker.Ack(42, time.Now()) {
		t.Fatal("unknown ack should return false")
	}
}
