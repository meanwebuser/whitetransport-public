package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

type carrierMailboxEntry struct {
	ID        string
	Envelope  fabric.Envelope
	Timestamp time.Time
}

type carrierMailbox struct {
	mu      sync.Mutex
	inbox   []carrierMailboxEntry
	outbox  []carrierMailboxEntry
	nextSeq uint64
}

func newCarrierMailbox() *carrierMailbox {
	return &carrierMailbox{}
}

func (m *carrierMailbox) addInbound(env fabric.Envelope) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSeq++
	m.inbox = append(m.inbox, carrierMailboxEntry{
		ID:        env.ID,
		Envelope:  env,
		Timestamp: time.Now().UTC(),
	})
}

func (m *carrierMailbox) drainInbound() []fabric.Envelope {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inbox) == 0 {
		return nil
	}
	out := make([]fabric.Envelope, len(m.inbox))
	for i, e := range m.inbox {
		out[i] = e.Envelope
	}
	m.inbox = m.inbox[:0]
	return out
}

func (m *carrierMailbox) addOutbound(env fabric.Envelope) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outbox = append(m.outbox, carrierMailboxEntry{
		ID:        env.ID,
		Envelope:  env,
		Timestamp: time.Now().UTC(),
	})
}

func (m *carrierMailbox) readOutbound(cursor int) ([]fabric.Envelope, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := cursor
	if idx < 0 || idx > len(m.outbox) {
		idx = len(m.outbox)
	}
	out := make([]fabric.Envelope, 0)
	for i := idx; i < len(m.outbox); i++ {
		out = append(out, m.outbox[i].Envelope)
	}
	nextCursor := len(m.outbox)
	return out, nextCursor
}

func (m *carrierMailbox) deleteOutbound(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.outbox {
		if e.ID == id {
			m.outbox = append(m.outbox[:i], m.outbox[i+1:]...)
			return
		}
	}
}

func (m *carrierMailbox) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/carrier/envelope" && r.Method == http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
				return
			}
			var env fabric.Envelope
			if err := json.Unmarshal(body, &env); err != nil {
				http.Error(w, "parse envelope: "+err.Error(), http.StatusBadRequest)
				return
			}
			m.addInbound(env)
			w.WriteHeader(http.StatusCreated)

		case path == "/carrier/envelopes" && r.Method == http.MethodGet:
			cursorStr := r.URL.Query().Get("cursor")
			cursor, _ := strconv.Atoi(cursorStr)
			envs, nextCursor := m.readOutbound(cursor)
			resp := map[string]any{
				"envelopes": envs,
				"cursor":    strconv.Itoa(nextCursor),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case path == "/carrier/inbox" && r.Method == http.MethodGet:
			envs := m.drainInbound()
			if envs == nil {
				envs = []fabric.Envelope{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"envelopes": envs})

		default:
			if r.Method == http.MethodDelete {
				id := path[strings.LastIndex(path, "/")+1:]
				if id != "" {
					m.deleteOutbound(id)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
}
