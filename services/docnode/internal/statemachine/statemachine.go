// Package statemachine holds every document and applies batches of operations
// to them.
//
// Apply is deterministic: given the same batches in the same order, every
// copy of the state machine ends up with exactly the same text, revisions and
// accept/reject decisions. It never reads the clock, generates random numbers
// or talks to the network. That is what lets Raft (Milestone 2) keep several
// copies in sync by feeding each one the same log.
//
// Conflicts are resolved by the clients: an operation is accepted only if it
// was made against the document's current revision. Anything older is rejected
// with a StaleError, and the client rebases its edit onto the newer revisions
// (CodeMirror's collab package does this) and sends it again.
package statemachine

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/text"
)

// OpID identifies one operation, so a retried operation is applied only once.
// Each editor session picks a random ClientID and numbers its operations
// 1, 2, 3, ... in the order it creates them.
type OpID struct {
	ClientID string
	Seq      uint64
}

// Operation is one edit by one user.
type Operation struct {
	ID           OpID
	DocID        string
	BaseRevision uint64 // the revision the edit was made against
	Changes      []text.Change
	UserID       string
}

// Batch is a group of operations on one document, applied all-or-nothing.
// It is one entry in the log. Operation i must be based on the revision
// produced by operation i-1, so a client can send several pending edits at once.
type Batch struct {
	DocID string
	Ops   []Operation
}

// Committed is an operation that has been accepted, with the revision it produced.
type Committed struct {
	Revision uint64
	Op       Operation
}

// Result describes what Apply did with an accepted batch.
type Result struct {
	Revision   uint64 // the document's revision after the batch
	Applied    int    // operations applied now
	Duplicates int    // operations skipped because they were applied before
}

// ErrStale matches (with errors.Is) any StaleError.
var ErrStale = errors.New("stale base revision")

// StaleError means an operation was made against an older revision.
// The client should catch up to Current, rebase its edit and send it again.
type StaleError struct {
	BaseRevision uint64
	Current      uint64
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("operation is based on revision %d but the document is at revision %d",
		e.BaseRevision, e.Current)
}

func (e *StaleError) Is(target error) bool { return target == ErrStale }

// ErrInvalid matches (with errors.Is) any error caused by a malformed batch.
var ErrInvalid = errors.New("invalid operation")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// StateMachine is safe for concurrent use.
type StateMachine struct {
	mu   sync.RWMutex
	docs map[string]*document
}

type document struct {
	text    []uint16
	history []Operation       // history[i] produced revision i+1
	lastSeq map[string]uint64 // highest applied Seq for each ClientID
	subs    map[*Subscription]struct{}
}

func (d *document) revision() uint64 { return uint64(len(d.history)) }

// New returns a state machine with no documents. Any document ID can be used;
// a document that has never been edited is empty, at revision 0.
func New() *StateMachine {
	return &StateMachine{docs: make(map[string]*document)}
}

func (sm *StateMachine) doc(id string) *document {
	d, ok := sm.docs[id]
	if !ok {
		d = &document{lastSeq: make(map[string]uint64), subs: make(map[*Subscription]struct{})}
		sm.docs[id] = d
	}
	return d
}

// Apply applies a batch. It either applies every new operation in it, or
// changes nothing and returns an error (a *StaleError, or one matching ErrInvalid).
func (sm *StateMachine) Apply(b Batch) (Result, error) {
	if b.DocID == "" {
		return Result{}, invalid("doc_id is empty")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	d := sm.doc(b.DocID)

	// First pass: check everything and build the new text, without touching d.
	revision := d.revision()
	newText := d.text
	seen := make(map[string]uint64) // Seq updates made by this batch so far
	var accepted []Operation
	duplicates := 0

	for i, op := range b.Ops {
		if op.DocID != b.DocID {
			return Result{}, invalid("operation %d is for document %q, not %q", i, op.DocID, b.DocID)
		}
		if op.ID.ClientID == "" || op.ID.Seq == 0 {
			return Result{}, invalid("operation %d needs a client_id and a seq above 0", i)
		}
		last, ok := seen[op.ID.ClientID]
		if !ok {
			last = d.lastSeq[op.ID.ClientID]
		}
		if op.ID.Seq <= last {
			duplicates++ // a retry of something already applied: skip it
			continue
		}
		if op.BaseRevision != revision {
			return Result{}, &StaleError{BaseRevision: op.BaseRevision, Current: revision}
		}
		next, err := text.Apply(newText, op.Changes)
		if err != nil {
			return Result{}, invalid("operation %d: %v", i, err)
		}
		newText = next
		seen[op.ID.ClientID] = op.ID.Seq
		accepted = append(accepted, op)
		revision++
	}

	// Second pass: everything checked out, so commit.
	d.text = newText
	for client, seq := range seen {
		d.lastSeq[client] = seq
	}
	for _, op := range accepted {
		d.history = append(d.history, op)
		d.publish(Committed{Revision: d.revision(), Op: op})
	}
	return Result{Revision: d.revision(), Applied: len(accepted), Duplicates: duplicates}, nil
}

// Snapshot returns a document's current text and revision.
func (sm *StateMachine) Snapshot(docID string) (string, uint64) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	d, ok := sm.docs[docID]
	if !ok {
		return "", 0
	}
	return text.Decode(d.text), d.revision()
}

// History returns the committed operations after fromRevision.
func (sm *StateMachine) History(docID string, fromRevision uint64) ([]Committed, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	d, ok := sm.docs[docID]
	if !ok {
		d = &document{}
	}
	return d.since(fromRevision)
}

func (d *document) since(from uint64) ([]Committed, error) {
	if from > d.revision() {
		return nil, invalid("revision %d is in the future (document is at %d)", from, d.revision())
	}
	out := make([]Committed, 0, d.revision()-from)
	for i := from; i < d.revision(); i++ {
		out = append(out, Committed{Revision: i + 1, Op: d.history[i]})
	}
	return out, nil
}