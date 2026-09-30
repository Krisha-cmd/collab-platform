package statemachine

import (
	"errors"
	"sync"
)

// ErrTooSlow is reported by a Subscription that was dropped because its
// reader fell too far behind. The reader should subscribe again from the last
// revision it received.
var ErrTooSlow = errors.New("subscriber fell too far behind")

// Subscription delivers a document's committed operations in order.
//
// Subscriptions are not part of the replicated state; they only observe it.
type Subscription struct {
	// Backlog holds the operations committed between the requested revision
	// and the moment of subscribing. Read it first, then read C.
	Backlog []Committed
	// C receives every operation committed after subscribing. It is closed
	// when the subscription ends; Err then says why.
	C <-chan Committed

	ch     chan Committed
	sm     *StateMachine
	docID  string
	mu     sync.Mutex
	err    error
	closed bool
}

// Subscribe starts delivering a document's operations after fromRevision.
// buffer is how many operations may queue up before the subscriber is
// considered too slow and dropped.
func (sm *StateMachine) Subscribe(docID string, fromRevision uint64, buffer int) (*Subscription, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	d := sm.doc(docID)

	// Taking the backlog and registering happen under the same lock, so no
	// operation can slip in between them and be missed.
	backlog, err := d.since(fromRevision)
	if err != nil {
		return nil, err
	}
	ch := make(chan Committed, buffer)
	s := &Subscription{Backlog: backlog, C: ch, ch: ch, sm: sm, docID: docID}
	d.subs[s] = struct{}{}
	return s, nil
}

// Close ends the subscription. It is safe to call more than once.
func (s *Subscription) Close() {
	s.sm.mu.Lock()
	defer s.sm.mu.Unlock()
	if d, ok := s.sm.docs[s.docID]; ok {
		delete(d.subs, s)
	}
	s.end(nil)
}

// Err returns why the subscription ended: nil if Close was called, ErrTooSlow
// if the reader fell behind.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Subscription) end(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.err = err
		close(s.ch)
	}
}

// publish is called with sm.mu held. It never blocks: a subscriber whose
// buffer is full is dropped instead of slowing down every other writer.
func (d *document) publish(c Committed) {
	for s := range d.subs {
		select {
		case s.ch <- c:
		default:
			delete(d.subs, s)
			s.end(ErrTooSlow)
		}
	}
}