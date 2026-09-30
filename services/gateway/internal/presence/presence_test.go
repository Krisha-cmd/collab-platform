package presence

import (
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu   sync.Mutex
	last []Participant
	n    int
}

func (r *recorder) notify(ps []Participant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last, r.n = ps, r.n+1
}

func TestJoinLeaveAndColours(t *testing.T) {
	h := NewHub()
	var a, b recorder
	leaveA := h.Join("doc", Participant{SessionID: "s1", Name: "Asha"}, a.notify)
	leaveB := h.Join("doc", Participant{SessionID: "s2", Name: "Bala"}, b.notify)

	if len(a.last) != 2 || len(b.last) != 2 {
		t.Fatalf("both should see 2 participants: %v / %v", a.last, b.last)
	}
	if a.last[0].Color == a.last[1].Color {
		t.Fatal("participants should get different colours")
	}

	leaveB()
	leaveB() // safe twice
	if len(a.last) != 1 || a.last[0].Name != "Asha" {
		t.Fatalf("after leave: %v", a.last)
	}
	leaveA()
	if got := h.Participants("doc"); len(got) != 0 {
		t.Fatalf("room should be empty: %v", got)
	}
}

func TestCursorAndEditUpdatesAreBroadcast(t *testing.T) {
	h := NewHub()
	var a, b recorder
	h.Join("doc", Participant{SessionID: "s1", Name: "Asha"}, a.notify)
	h.Join("doc", Participant{SessionID: "s2", Name: "Bala"}, b.notify)

	h.UpdateCursor("doc", "s2", Cursor{Revision: 3, Anchor: 4, Head: 9})
	bala := a.last[1]
	if bala.Cursor == nil || bala.Cursor.Head != 9 {
		t.Fatalf("Asha should see Bala's cursor: %+v", bala)
	}

	now := time.UnixMilli(1_700_000_000_000)
	h.MarkEdited("doc", "s1", now)
	if b.last[0].LastEditUnixMs != now.UnixMilli() {
		t.Fatalf("Bala should see Asha's last edit: %+v", b.last[0])
	}
}

func TestRoomsAreSeparate(t *testing.T) {
	h := NewHub()
	var a, b recorder
	h.Join("doc1", Participant{SessionID: "s1", Name: "Asha"}, a.notify)
	h.Join("doc2", Participant{SessionID: "s2", Name: "Bala"}, b.notify)
	if len(a.last) != 1 || len(b.last) != 1 {
		t.Fatal("participants of different documents must not see each other")
	}
	h.UpdateCursor("doc1", "s2", Cursor{}) // wrong room: ignored
	if a.n != 1 {
		t.Fatal("an update for another room should not notify")
	}
}