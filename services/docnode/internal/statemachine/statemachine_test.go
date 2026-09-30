package statemachine

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/text"
)

func op(client string, seq, base uint64, changes ...text.Change) Operation {
	return Operation{ID: OpID{client, seq}, DocID: "doc", BaseRevision: base, Changes: changes}
}

func ins(pos int, s string) text.Change { return text.Change{From: pos, To: pos, Insert: s} }

func mustApply(t *testing.T, sm *StateMachine, ops ...Operation) Result {
	t.Helper()
	res, err := sm.Apply(Batch{DocID: "doc", Ops: ops})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

func wantText(t *testing.T, sm *StateMachine, want string, wantRev uint64) {
	t.Helper()
	got, rev := sm.Snapshot("doc")
	if got != want || rev != wantRev {
		t.Fatalf("snapshot = (%q, rev %d), want (%q, rev %d)", got, rev, want, wantRev)
	}
}

func TestNewDocumentIsEmpty(t *testing.T) {
	wantText(t, New(), "", 0)
}

func TestAcceptsEditAtCurrentRevision(t *testing.T) {
	sm := New()
	res := mustApply(t, sm, op("a", 1, 0, ins(0, "hello")))
	if res != (Result{Revision: 1, Applied: 1}) {
		t.Fatalf("result = %+v", res)
	}
	mustApply(t, sm, op("b", 1, 1, ins(5, " world")))
	wantText(t, sm, "hello world", 2)
}

func TestRejectsStaleEditAndChangesNothing(t *testing.T) {
	sm := New()
	mustApply(t, sm, op("a", 1, 0, ins(0, "hello")))

	// Client b made its edit against revision 0, before a's edit arrived.
	_, err := sm.Apply(Batch{DocID: "doc", Ops: []Operation{op("b", 1, 0, ins(0, "X"))}})
	var stale *StaleError
	if !errors.As(err, &stale) || !errors.Is(err, ErrStale) || stale.Current != 1 {
		t.Fatalf("err = %v, want a StaleError with Current=1", err)
	}
	wantText(t, sm, "hello", 1)

	// b rebases onto revision 1 and resends with the same seq: accepted.
	mustApply(t, sm, op("b", 1, 1, ins(0, "X")))
	wantText(t, sm, "Xhello", 2)
}

func TestBatchOfConsecutiveEdits(t *testing.T) {
	sm := New()
	res := mustApply(t, sm,
		op("a", 1, 0, ins(0, "abc")),
		op("a", 2, 1, ins(3, "def")),
		op("a", 3, 2, text.Change{From: 0, To: 1, Insert: "A"}),
	)
	if res.Revision != 3 || res.Applied != 3 {
		t.Fatalf("result = %+v", res)
	}
	wantText(t, sm, "Abcdef", 3)
}

func TestBatchIsAllOrNothing(t *testing.T) {
	sm := New()
	_, err := sm.Apply(Batch{DocID: "doc", Ops: []Operation{
		op("a", 1, 0, ins(0, "fine")),
		op("a", 2, 1, text.Change{From: 0, To: 99}), // out of range
	}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	wantText(t, sm, "", 0)
	// The first op was not applied, so its seq was not used up either.
	mustApply(t, sm, op("a", 1, 0, ins(0, "fine")))
	wantText(t, sm, "fine", 1)
}

func TestRetriedBatchIsNotAppliedTwice(t *testing.T) {
	sm := New()
	batch := []Operation{op("a", 1, 0, ins(0, "x")), op("a", 2, 1, ins(1, "y"))}
	mustApply(t, sm, batch...)

	// The client timed out and sends the same batch again.
	res := mustApply(t, sm, batch...)
	if res != (Result{Revision: 2, Applied: 0, Duplicates: 2}) {
		t.Fatalf("result = %+v", res)
	}
	wantText(t, sm, "xy", 2)

	// A retry that also carries a new edit: the old ones are skipped, the new one applied.
	res = mustApply(t, sm, append(batch, op("a", 3, 2, ins(2, "z")))...)
	if res != (Result{Revision: 3, Applied: 1, Duplicates: 2}) {
		t.Fatalf("result = %+v", res)
	}
	wantText(t, sm, "xyz", 3)
}

func TestInvalidOperations(t *testing.T) {
	cases := map[string]Batch{
		"no doc id":      {Ops: []Operation{op("a", 1, 0)}},
		"wrong doc":      {DocID: "doc", Ops: []Operation{{ID: OpID{"a", 1}, DocID: "other"}}},
		"no client id":   {DocID: "doc", Ops: []Operation{op("", 1, 0)}},
		"seq zero":       {DocID: "doc", Ops: []Operation{op("a", 0, 0)}},
		"range past end": {DocID: "doc", Ops: []Operation{op("a", 1, 0, text.Change{From: 1, To: 2})}},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New().Apply(b); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestHistory(t *testing.T) {
	sm := New()
	mustApply(t, sm, op("a", 1, 0, ins(0, "a")), op("a", 2, 1, ins(1, "b")))
	h, err := sm.History("doc", 1)
	if err != nil || len(h) != 1 || h[0].Revision != 2 || h[0].Op.ID.Seq != 2 {
		t.Fatalf("History = %+v, %v", h, err)
	}
	if _, err := sm.History("doc", 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future revision: err = %v", err)
	}
}

// randomEdit makes a valid edit against doc (UTF-16).
func randomEdit(r *rand.Rand, doc []uint16) text.Change {
	from := r.Intn(len(doc) + 1)
	to := from
	if r.Intn(3) == 0 && from < len(doc) {
		to = from + r.Intn(min(3, len(doc)-from)+1)
	}
	for from > 0 && from < len(doc) && doc[from] >= 0xDC00 && doc[from] <= 0xDFFF {
		from-- // never start inside an emoji
	}
	for to < len(doc) && to > 0 && doc[to] >= 0xDC00 && doc[to] <= 0xDFFF {
		to++
	}
	if to < from {
		to = from
	}
	words := []string{"a", "bc", "🎉", "é", "नम", " ", "xyz"}
	return text.Change{From: from, To: to, Insert: words[r.Intn(len(words))]}
}

// TestReplicasAgree is the property Raft depends on: two state machines that
// receive the same batches in the same order end in exactly the same state and
// make exactly the same accept/reject decisions, including for stale,
// duplicate and invalid batches.
func TestReplicasAgree(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		r := rand.New(rand.NewSource(seed))
		leader, follower := New(), New()
		seqs := map[string]uint64{}
		outcomes := map[string]int{}
		type view struct {
			doc []uint16 // this client's possibly out-of-date copy
			rev uint64
		}
		views := map[string]*view{}

		for step := 0; step < 300; step++ {
			client := fmt.Sprintf("c%d", r.Intn(4))
			v := views[client]
			if v == nil {
				v = &view{}
				views[client] = v
			}
			if r.Intn(3) == 0 { // sometimes catch up to the latest revision
				s, rev := leader.Snapshot("doc")
				v.doc, v.rev = text.Encode(s), rev
			}
			seqs[client]++
			o := op(client, seqs[client], v.rev, randomEdit(r, v.doc))
			if r.Intn(10) == 0 {
				o.ID.Seq = 1 // an old retry
			}
			b := Batch{DocID: "doc", Ops: []Operation{o}}

			res1, err1 := leader.Apply(b)
			res2, err2 := follower.Apply(b)
			if res1 != res2 || fmt.Sprint(err1) != fmt.Sprint(err2) {
				t.Fatalf("seed %d step %d: replicas disagree: (%+v, %v) vs (%+v, %v)",
					seed, step, res1, err1, res2, err2)
			}
			switch {
			case errors.Is(err1, ErrStale):
				outcomes["stale"]++
			case err1 != nil:
				outcomes["invalid"]++
			case res1.Duplicates > 0:
				outcomes["duplicate"]++
			default:
				outcomes["applied"]++
			}
			if err1 == nil && res1.Applied == 1 {
				s, rev := leader.Snapshot("doc")
				v.doc, v.rev = text.Encode(s), rev
			}
		}

		t1, r1 := leader.Snapshot("doc")
		t2, r2 := follower.Snapshot("doc")
		if t1 != t2 || r1 != r2 {
			t.Fatalf("seed %d: final states differ", seed)
		}
		if outcomes["applied"] == 0 || outcomes["stale"] == 0 || outcomes["duplicate"] == 0 {
			t.Fatalf("seed %d: outcomes %v; the test is not exercising every path", seed, outcomes)
		}

		// Replaying the committed history from scratch also gives the same text.
		history, _ := leader.History("doc", 0)
		var doc []uint16
		for _, c := range history {
			var err error
			if doc, err = text.Apply(doc, c.Op.Changes); err != nil {
				t.Fatalf("seed %d: replay failed at revision %d: %v", seed, c.Revision, err)
			}
		}
		if text.Decode(doc) != t1 {
			t.Fatalf("seed %d: replayed history does not match the document", seed)
		}
	}
}

// TestConcurrentWriters runs many writers at once (use: go test -race).
func TestConcurrentWriters(t *testing.T) {
	sm := New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(w)))
			client := fmt.Sprintf("w%d", w)
			var seq uint64
			for i := 0; i < 200; i++ {
				s, rev := sm.Snapshot("doc")
				seq++
				_, err := sm.Apply(Batch{DocID: "doc", Ops: []Operation{
					op(client, seq, rev, randomEdit(r, text.Encode(s))),
				}})
				switch {
				case err == nil:
					mu.Lock()
					accepted++
					mu.Unlock()
				case errors.Is(err, ErrStale):
					seq-- // not applied: the seq can be reused
				default:
					t.Errorf("unexpected error: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if _, rev := sm.Snapshot("doc"); rev != uint64(accepted) {
		t.Fatalf("revision %d but %d edits were accepted", rev, accepted)
	}
}