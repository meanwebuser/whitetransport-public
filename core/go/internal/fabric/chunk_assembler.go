package fabric

import (
	"fmt"
	"sort"
)

// ChunkAssembler collects chunk envelopes and reassembles complete payloads
// once all chunks for a group have arrived. It is safe for concurrent use
// patterns only when guarded externally; the struct itself is not mutex-protected.
type ChunkAssembler struct {
	// groups maps base ID -> collected chunk envelopes.
	groups map[string][]Envelope
}

// NewChunkAssembler returns an empty assembler ready to accept chunks.
func NewChunkAssembler() *ChunkAssembler {
	return &ChunkAssembler{groups: make(map[string][]Envelope)}
}

// Add inserts an envelope into the assembler. Standalone (non-chunk) envelopes
// are silently ignored; pass them through directly to the consumer.
// Returns the base ID of the group this chunk belongs to.
func (a *ChunkAssembler) Add(env Envelope) string {
	if !env.IsChunk() {
		return env.ID
	}
	base := env.BaseID()
	a.groups[base] = appendUnique(a.groups[base], env)
	return base
}

// IsComplete returns true when all chunks for a given base ID have been received.
func (a *ChunkAssembler) IsComplete(baseID string) bool {
	chunks, ok := a.groups[baseID]
	if !ok || len(chunks) == 0 {
		return false
	}
	return len(chunks) == chunks[0].ChunkTotal
}

// Assemble reassembles the original payload from collected chunks and removes
// the group from the assembler. Returns an error if chunks are missing or
// inconsistent.
func (a *ChunkAssembler) Assemble(baseID string) (Envelope, error) {
	chunks, ok := a.groups[baseID]
	if !ok || len(chunks) == 0 {
		return Envelope{}, fmt.Errorf("no chunks for group %q", baseID)
	}

	total := chunks[0].ChunkTotal
	if total <= 0 {
		return Envelope{}, fmt.Errorf("chunk_total must be > 0, got %d", total)
	}
	if len(chunks) != total {
		return Envelope{}, fmt.Errorf(
			"group %q: have %d/%d chunks", baseID, len(chunks), total,
		)
	}

	// Sort by chunk index to guarantee correct ordering regardless of arrival order.
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})

	// Validate indices are contiguous [0, total).
	for i, chunk := range chunks {
		if chunk.ChunkIndex != i {
			return Envelope{}, fmt.Errorf(
				"group %q: missing chunk at index %d (got %d)", baseID, i, chunk.ChunkIndex,
			)
		}
		if chunk.ChunkTotal != total {
			return Envelope{}, fmt.Errorf(
				"group %q: chunk %d reports chunk_total=%d, expected %d",
				baseID, i, chunk.ChunkTotal, total,
			)
		}
	}

	// Concatenate payloads.
	var payload []byte
	for _, chunk := range chunks {
		payload = append(payload, chunk.Payload...)
	}

	// Reconstruct the original envelope from the first chunk's metadata.
	original := chunks[0]
	original.ID = baseID
	// Strip the ".chunk" suffix that chunkEnvelope adds.
	if len(original.PayloadType) > 6 && original.PayloadType[len(original.PayloadType)-6:] == ".chunk" {
		original.PayloadType = original.PayloadType[:len(original.PayloadType)-6]
	}
	original.ChunkIndex = 0
	original.ChunkTotal = 0
	original.Payload = payload

	delete(a.groups, baseID)
	return original, nil
}

// PendingGroups returns the base IDs of all incomplete groups currently held.
func (a *ChunkAssembler) PendingGroups() []string {
	ids := make([]string, 0, len(a.groups))
	for id := range a.groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// GroupSize returns the number of chunks collected for a group and the
// expected total (0 if the group is unknown).
func (a *ChunkAssembler) GroupSize(baseID string) (have, total int) {
	chunks, ok := a.groups[baseID]
	if !ok {
		return 0, 0
	}
	if len(chunks) > 0 {
		total = chunks[0].ChunkTotal
	}
	return len(chunks), total
}

// appendUnique adds env to the slice only if no chunk with the same
// ChunkIndex is already present.
func appendUnique(chunks []Envelope, env Envelope) []Envelope {
	for _, existing := range chunks {
		if existing.ChunkIndex == env.ChunkIndex {
			return chunks // duplicate, ignore
		}
	}
	return append(chunks, env)
}
