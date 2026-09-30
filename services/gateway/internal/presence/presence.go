// Package presence tracks who is connected to each document and where their
// cursors are.
//
// Presence lives only in the gateway's memory. It is not replicated or stored:
// if the gateway restarts, browsers reconnect and announce themselves again.
// It is also allowed to be lossy: every update carries the full participant
// list, so a missed update is corrected by the next one.
package presence

import (
	"sort"
	"sync"
	"time"
)

// Palette holds the colours given to participants, in order.
var Palette = []string{
	"#e8590c", "#1c7ed6", "#2f9e44", "#ae3ec9", "#f08c00", "#0c8599", "#e03131", "#5f3dc4",
}

type Cursor struct {
	Revision uint64
	Anchor   uint32
	Head     uint32
}

type Participant struct {
	SessionID      string
	Name           string
	Color          string
	Cursor         *Cursor // nil until the first cursor update
	LastEditUnixMs int64   // 0 if the participant has not edited yet
}

// Notify receives the full, current participant list. It is called with the
// hub's lock held, so it must not block or call back into the Hub.
type Notify func([]Participant)

type member struct {
	p      Participant
	notify Notify
}

// Hub is safe for concurrent use.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]map[string]*member // doc ID -> session ID -> member
}

func NewHub() *Hub { return &Hub{rooms: make(map[string]map[string]*member)} }

// Join adds a participant to a document's room and tells everyone, including
// the new participant. The Color field is filled in by the hub. Call the
// returned function when the participant disconnects.
func (h *Hub) Join(docID string, p Participant, notify Notify) (leave func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room := h.rooms[docID]
	if room == nil {
		room = make(map[string]*member)
		h.rooms[docID] = room
	}
	p.Color = freeColor(room)
	room[p.SessionID] = &member{p: p, notify: notify}
	h.broadcast(room)

	var once sync.Once
	return func() { once.Do(func() { h.leave(docID, p.SessionID) }) }
}

func (h *Hub) leave(docID, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room := h.rooms[docID]
	delete(room, sessionID)
	if len(room) == 0 {
		delete(h.rooms, docID)
		return
	}
	h.broadcast(room)
}

// UpdateCursor records where a participant's cursor is.
func (h *Hub) UpdateCursor(docID, sessionID string, c Cursor) {
	h.update(docID, sessionID, func(p *Participant) { p.Cursor = &c })
}

// MarkEdited records that a participant just made an edit.
func (h *Hub) MarkEdited(docID, sessionID string, at time.Time) {
	h.update(docID, sessionID, func(p *Participant) { p.LastEditUnixMs = at.UnixMilli() })
}

// Participants returns a document's current participants, sorted by name.
func (h *Hub) Participants(docID string) []Participant {
	h.mu.Lock()
	defer h.mu.Unlock()
	return list(h.rooms[docID])
}

func (h *Hub) update(docID, sessionID string, change func(*Participant)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room := h.rooms[docID]
	m, ok := room[sessionID]
	if !ok {
		return
	}
	change(&m.p)
	h.broadcast(room)
}

func (h *Hub) broadcast(room map[string]*member) {
	ps := list(room)
	for _, m := range room {
		m.notify(ps)
	}
}

func list(room map[string]*member) []Participant {
	ps := make([]Participant, 0, len(room))
	for _, m := range room {
		p := m.p
		if p.Cursor != nil {
			c := *p.Cursor
			p.Cursor = &c // copy, so callers never share memory with the hub
		}
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Name != ps[j].Name {
			return ps[i].Name < ps[j].Name
		}
		return ps[i].SessionID < ps[j].SessionID
	})
	return ps
}

// freeColor picks the first palette colour nobody in the room is using.
func freeColor(room map[string]*member) string {
	used := make(map[string]bool)
	for _, m := range room {
		used[m.p.Color] = true
	}
	for _, c := range Palette {
		if !used[c] {
			return c
		}
	}
	return Palette[len(room)%len(Palette)]
}