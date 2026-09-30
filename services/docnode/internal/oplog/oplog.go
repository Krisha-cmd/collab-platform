// Package oplog is where batches of operations are ordered before they reach
// the state machine.
//
// Milestone 1 uses LocalLog, which applies each batch immediately on this one
// node. Milestone 2 adds a Raft-backed Log that first replicates the batch to
// a majority of nodes and only then applies it. The gRPC server only sees the
// Log interface, so it does not change between the two.
package oplog

import (
	"context"

	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/statemachine"
)

// Log orders batches and applies them to the state machine.
type Log interface {
	// Propose adds a batch to the log and waits until it has been applied.
	// It returns the state machine's result, or its error (for example a
	// *statemachine.StaleError).
	Propose(ctx context.Context, b statemachine.Batch) (statemachine.Result, error)
}

// LocalLog applies batches directly, in the order Propose is called.
type LocalLog struct {
	sm *statemachine.StateMachine
}

func NewLocal(sm *statemachine.StateMachine) *LocalLog { return &LocalLog{sm: sm} }

func (l *LocalLog) Propose(ctx context.Context, b statemachine.Batch) (statemachine.Result, error) {
	if err := ctx.Err(); err != nil {
		return statemachine.Result{}, err // the caller already gave up
	}
	return l.sm.Apply(b)
}