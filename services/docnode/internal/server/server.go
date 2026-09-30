// Package server exposes the document state machine over gRPC.
//
// It only translates: protobuf messages in, state machine calls, protobuf
// messages and gRPC status codes out. All decisions are made by the state
// machine, and all writes go through the oplog.Log.
package server

import (
	"context"
	"errors"
	"log"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	collabv1 "github.com/Krisha-cmd/collab-platform/gen/go/collab/v1"
	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/oplog"
	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/statemachine"
	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/text"
)

// SubscribeBuffer is how many operations may queue up for one subscriber
// before it is considered too slow and disconnected.
const SubscribeBuffer = 1024

// MaxOpsPerRequest limits the size of one SubmitOperations batch.
const MaxOpsPerRequest = 1000

type Server struct {
	collabv1.UnimplementedDocumentServiceServer
	log oplog.Log
	sm  *statemachine.StateMachine
}

// New returns a server that writes through log and reads from sm.
func New(log oplog.Log, sm *statemachine.StateMachine) *Server {
	return &Server{log: log, sm: sm}
}

func (s *Server) GetSnapshot(ctx context.Context, req *collabv1.GetSnapshotRequest) (*collabv1.GetSnapshotResponse, error) {
	if req.GetDocId() == "" {
		return nil, status.Error(codes.InvalidArgument, "doc_id is required")
	}
	txt, rev := s.sm.Snapshot(req.GetDocId())
	return &collabv1.GetSnapshotResponse{
		Snapshot: &collabv1.DocumentSnapshot{DocId: req.GetDocId(), Revision: rev, Text: txt},
	}, nil
}

func (s *Server) SubmitOperations(ctx context.Context, req *collabv1.SubmitOperationsRequest) (*collabv1.SubmitOperationsResponse, error) {
	if len(req.GetOperations()) > MaxOpsPerRequest {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d operations per request", MaxOpsPerRequest)
	}
	batch := statemachine.Batch{DocID: req.GetDocId()}
	for _, op := range req.GetOperations() {
		batch.Ops = append(batch.Ops, operationFromProto(op))
	}

	res, err := s.log.Propose(ctx, batch)
	if err != nil {
		return nil, toStatus(err)
	}
	return &collabv1.SubmitOperationsResponse{
		Revision:   res.Revision,
		Applied:    uint32(res.Applied),
		Duplicates: uint32(res.Duplicates),
	}, nil
}

func (s *Server) Subscribe(req *collabv1.SubscribeRequest, stream collabv1.DocumentService_SubscribeServer) error {
	if req.GetDocId() == "" {
		return status.Error(codes.InvalidArgument, "doc_id is required")
	}
	sub, err := s.sm.Subscribe(req.GetDocId(), req.GetFromRevision(), SubscribeBuffer)
	if err != nil {
		return toStatus(err)
	}
	defer sub.Close()

	send := func(c statemachine.Committed) error {
		return stream.Send(&collabv1.SubscribeResponse{
			Revision:  c.Revision,
			Operation: operationToProto(c.Op),
		})
	}
	for _, c := range sub.Backlog {
		if err := send(c); err != nil {
			return err
		}
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil // the client went away
		case c, ok := <-sub.C:
			if !ok {
				if errors.Is(sub.Err(), statemachine.ErrTooSlow) {
					return status.Error(codes.Unavailable,
						"subscriber fell behind; subscribe again from the last revision received")
				}
				return nil
			}
			if err := send(c); err != nil {
				return err
			}
		}
	}
}

// toStatus turns state machine errors into gRPC status codes.
func toStatus(err error) error {
	switch {
	case errors.Is(err, statemachine.ErrStale):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, statemachine.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		log.Printf("internal error: %v", err)
		return status.Error(codes.Internal, "internal error")
	}
}

func operationFromProto(p *collabv1.Operation) statemachine.Operation {
	op := statemachine.Operation{
		ID:           statemachine.OpID{ClientID: p.GetOpId().GetClientId(), Seq: p.GetOpId().GetSeq()},
		DocID:        p.GetDocId(),
		BaseRevision: p.GetBaseRevision(),
		UserID:       p.GetUserId(),
	}
	for _, c := range p.GetChanges() {
		op.Changes = append(op.Changes, text.Change{
			From:   int(c.GetRange().GetStart()),
			To:     int(c.GetRange().GetEnd()),
			Insert: c.GetInsert(),
		})
	}
	return op
}

func operationToProto(op statemachine.Operation) *collabv1.Operation {
	p := &collabv1.Operation{
		OpId:         &collabv1.OpId{ClientId: op.ID.ClientID, Seq: op.ID.Seq},
		DocId:        op.DocID,
		BaseRevision: op.BaseRevision,
		UserId:       op.UserID,
	}
	for _, c := range op.Changes {
		p.Changes = append(p.Changes, &collabv1.TextChange{
			Range:  &collabv1.TextRange{Start: uint32(c.From), End: uint32(c.To)},
			Insert: c.Insert,
		})
	}
	return p
}