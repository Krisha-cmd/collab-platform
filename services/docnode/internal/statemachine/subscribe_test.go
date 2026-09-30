package statemachine

import (
	"errors"
	"testing"
)

func TestSubscriptionGetsBacklogThenLiveOperations(t *testing.T) {
	sm := New()
	mustApply(t, sm, op("a", 1, 0, ins(0, "a")), op("a", 2, 1, ins(1, "b")))

	sub, err := sm.Subscribe("doc", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if len(sub.Backlog) != 1 || sub.Backlog[0].Revision != 2 {
		t.Fatalf("backlog = %+v", sub.Backlog)
	}

	mustApply(t, sm, op("b", 1, 2, ins(2, "c")))
	got := <-sub.C
	if got.Revision != 3 || got.Op.ID.ClientID != "b" {
		t.Fatalf("live op = %+v", got)
	}
}

func TestRejectedBatchesAreNotPublished(t *testing.T) {
	sm := New()
	sub, _ := sm.Subscribe("doc", 0, 10)
	defer sub.Close()
	_, _ = sm.Apply(Batch{DocID: "doc", Ops: []Operation{op("a", 1, 7, ins(0, "x"))}}) // stale
	select {
	case c := <-sub.C:
		t.Fatalf("got %+v from a rejected batch", c)
	default:
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	sm := New()
	sub, _ := sm.Subscribe("doc", 0, 1) // room for one queued operation
	mustApply(t, sm, op("a", 1, 0, ins(0, "1")))
	mustApply(t, sm, op("a", 2, 1, ins(0, "2"))) // buffer full: dropped

	<-sub.C // the one that fit
	if _, open := <-sub.C; open {
		t.Fatal("channel should be closed")
	}
	if !errors.Is(sub.Err(), ErrTooSlow) {
		t.Fatalf("Err = %v, want ErrTooSlow", sub.Err())
	}
	// Writers were never blocked.
	wantText(t, sm, "21", 2)
}

func TestCloseEndsSubscription(t *testing.T) {
	sm := New()
	sub, _ := sm.Subscribe("doc", 0, 1)
	sub.Close()
	sub.Close() // safe twice
	if _, open := <-sub.C; open || sub.Err() != nil {
		t.Fatal("expected a cleanly closed channel")
	}
	mustApply(t, sm, op("a", 1, 0, ins(0, "x"))) // no panic after close
}

func TestSubscribeFromFutureRevisionFails(t *testing.T) {
	if _, err := New().Subscribe("doc", 3, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}