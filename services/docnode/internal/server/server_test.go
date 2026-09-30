package server

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	collabv1 "github.com/Krisha-cmd/collab-platform/gen/go/collab/v1"
	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/oplog"
	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/statemachine"
)

// startServer runs the real gRPC server over an in-memory connection.
func startServer(t *testing.T) collabv1.DocumentServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	sm := statemachine.New()
	g := grpc.NewServer()
	collabv1.RegisterDocumentServiceServer(g, New(oplog.NewLocal(sm), sm))
	go g.Serve(lis)
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return collabv1.NewDocumentServiceClient(conn)
}

func insertOp(client string, seq, base uint64, pos uint32, s string) *collabv1.Operation {
	return &collabv1.Operation{
		OpId:         &collabv1.OpId{ClientId: client, Seq: seq},
		DocId:        "doc",
		BaseRevision: base,
		Changes: []*collabv1.TextChange{
			{Range: &collabv1.TextRange{Start: pos, End: pos}, Insert: s},
		},
	}
}

func submit(c collabv1.DocumentServiceClient, ops ...*collabv1.Operation) (*collabv1.SubmitOperationsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.SubmitOperations(ctx, &collabv1.SubmitOperationsRequest{DocId: "doc", Operations: ops})
}

func TestEditSnapshotAndConflict(t *testing.T) {
	c := startServer(t)
	ctx := context.Background()

	res, err := submit(c, insertOp("alice", 1, 0, 0, "hello"))
	if err != nil || res.GetRevision() != 1 || res.GetApplied() != 1 {
		t.Fatalf("submit = %v, %v", res, err)
	}

	// Bob edited revision 0 too, so his edit is stale.
	_, err = submit(c, insertOp("bob", 1, 0, 0, "X"))
	if status.Code(err) != codes.Aborted {
		t.Fatalf("stale edit: got %v, want ABORTED", err)
	}

	// Bob rebases onto revision 1 and resends.
	if _, err := submit(c, insertOp("bob", 1, 1, 5, "!")); err != nil {
		t.Fatal(err)
	}

	snap, err := c.GetSnapshot(ctx, &collabv1.GetSnapshotRequest{DocId: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	if s := snap.GetSnapshot(); s.GetText() != "hello!" || s.GetRevision() != 2 {
		t.Fatalf("snapshot = %v", s)
	}
}

func TestRetryIsReportedAsDuplicate(t *testing.T) {
	c := startServer(t)
	op := insertOp("alice", 1, 0, 0, "x")
	if _, err := submit(c, op); err != nil {
		t.Fatal(err)
	}
	res, err := submit(c, op)
	if err != nil || res.GetApplied() != 0 || res.GetDuplicates() != 1 || res.GetRevision() != 1 {
		t.Fatalf("retry = %v, %v", res, err)
	}
}

func TestInvalidEditIsRejected(t *testing.T) {
	c := startServer(t)
	_, err := submit(c, insertOp("alice", 1, 0, 50, "x")) // past the end of an empty doc
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want INVALID_ARGUMENT", err)
	}
}

func TestSubscribeStreamsHistoryThenLiveEdits(t *testing.T) {
	c := startServer(t)
	if _, err := submit(c, insertOp("alice", 1, 0, 0, "a")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := c.Subscribe(ctx, &collabv1.SubscribeRequest{DocId: "doc", FromRevision: 0})
	if err != nil {
		t.Fatal(err)
	}

	first, err := stream.Recv() // from history
	if err != nil || first.GetRevision() != 1 || first.GetOperation().GetOpId().GetClientId() != "alice" {
		t.Fatalf("first = %v, %v", first, err)
	}

	if _, err := submit(c, insertOp("bob", 1, 1, 1, "b")); err != nil {
		t.Fatal(err)
	}
	second, err := stream.Recv() // live
	if err != nil || second.GetRevision() != 2 || second.GetOperation().GetChanges()[0].GetInsert() != "b" {
		t.Fatalf("second = %v, %v", second, err)
	}
	if second.GetOperation().GetBaseRevision() != 1 {
		t.Fatalf("base_revision should be revision-1, got %d", second.GetOperation().GetBaseRevision())
	}
}