package policy

import "time"

// ChunkState is the ACK/repair state for one scheduled chunk.
type ChunkState struct {
	Placement ChunkPlacement
	SentAt    time.Time
	AckedAt   time.Time
}

// DeliveryTracker tracks scheduled chunks until ACK or repair. It is deliberately
// carrier-neutral; callers decide how ACKs are received over control channels.
type DeliveryTracker struct {
	chunks map[int]ChunkState
}

// NewDeliveryTracker creates an empty delivery tracker.
func NewDeliveryTracker() *DeliveryTracker {
	return &DeliveryTracker{chunks: map[int]ChunkState{}}
}

// Register records scheduled chunks as sent at one time.
func (t *DeliveryTracker) Register(placements []ChunkPlacement, sentAt time.Time) {
	for _, placement := range placements {
		t.chunks[placement.Index] = ChunkState{Placement: placement, SentAt: sentAt}
	}
}

// Ack marks a chunk sequence as acknowledged.
func (t *DeliveryTracker) Ack(index int, ackedAt time.Time) bool {
	state, ok := t.chunks[index]
	if !ok {
		return false
	}
	state.AckedAt = ackedAt
	t.chunks[index] = state
	return true
}

// Complete returns true when every registered chunk has been acknowledged.
func (t *DeliveryTracker) Complete() bool {
	if len(t.chunks) == 0 {
		return false
	}
	for _, state := range t.chunks {
		if state.AckedAt.IsZero() {
			return false
		}
	}
	return true
}

// DueHedges returns hedged placements whose primary chunk has not been ACKed by
// its hedge deadline.
func (t *DeliveryTracker) DueHedges(now time.Time) []ChunkPlacement {
	due := make([]ChunkPlacement, 0)
	for _, state := range t.orderedStates() {
		if state.AckedAt.IsZero() && len(state.Placement.HedgeCarrierIDs) > 0 && !state.SentAt.IsZero() && !now.Before(state.SentAt.Add(state.Placement.HedgeAfter)) {
			due = append(due, state.Placement)
		}
	}
	return due
}

// Missing returns all unacknowledged placements in index order.
func (t *DeliveryTracker) Missing() []ChunkPlacement {
	missing := make([]ChunkPlacement, 0)
	for _, state := range t.orderedStates() {
		if state.AckedAt.IsZero() {
			missing = append(missing, state.Placement)
		}
	}
	return missing
}

// RepairPlacements returns missing chunks assigned round-robin to repair
// carriers. The returned placements keep the original offset/size/index.
func (t *DeliveryTracker) RepairPlacements(repairCarrierIDs []string) []ChunkPlacement {
	if len(repairCarrierIDs) == 0 {
		return nil
	}
	missing := t.Missing()
	out := make([]ChunkPlacement, 0, len(missing))
	for i, placement := range missing {
		repair := placement
		repair.CarrierID = repairCarrierIDs[i%len(repairCarrierIDs)]
		repair.MirrorCarrierIDs = nil
		repair.HedgeCarrierIDs = nil
		repair.HedgeAfter = 0
		out = append(out, repair)
	}
	return out
}

func (t *DeliveryTracker) orderedStates() []ChunkState {
	states := make([]ChunkState, 0, len(t.chunks))
	for _, state := range t.chunks {
		states = append(states, state)
	}
	for i := 1; i < len(states); i++ {
		current := states[i]
		j := i - 1
		for j >= 0 && states[j].Placement.Index > current.Placement.Index {
			states[j+1] = states[j]
			j--
		}
		states[j+1] = current
	}
	return states
}
