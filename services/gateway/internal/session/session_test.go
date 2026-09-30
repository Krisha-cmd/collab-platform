package session

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"

	collabv1 "github.com/Krisha-cmd/collab-platform/gen/go/collab/v1"
	"github.com/Krisha-cmd/collab-platform/services/gateway/internal/presence"
)

// fakeDocs is a tiny in-memory document service: it accepts an edit only at
// the current revision, like the real one, and broadcasts accepted edits.
type fakeDocs struct {
	collabv1.UnimplementedDocumentServiceServer
	mu   sync.Mutex
	ops  []*collabv1.Operation
	subs []chan *collabv1.SubscribeResponse
	last *collabv1.SubmitOperationsRequest // the most recent request, as received
}

func (f *fakeDocs) GetSnapshot(ctx context.Context, r *collabv1.GetSnapshotRequest) (*collabv1.GetSnapshotResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &collabv1.GetSnapshotResponse{Snapshot: &collabv1.DocumentSnapshot{
		DocId: r.GetDocId(), Revision: uint64(len(f.ops)), Text: "hello",
	}}, nil
}

func (f *fakeDocs) SubmitOperations(ctx context.Context, r *collabv1.SubmitOperationsRequest) (*collabv1.SubmitOperationsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = r
	for _, op := range r.GetOperations() {
		if op.GetBaseRevision() != uint64(len(f.ops)) {
			return nil, status.Error(codes.Aborted, "stale")
		}
		f.ops = append(f.ops, op)
		for _, ch := range f.subs {
			ch <- &collabv1.SubscribeResponse{Revision: uint64(len(f.ops)), Operation: op}
		}
	}
	return &collabv1.SubmitOperationsResponse{Revision: uint64(len(f.ops)), Applied: uint32(len(r.GetOperations()))}, nil
}

func (f *fakeDocs) Subscribe(r *collabv1.SubscribeRequest, stream collabv1.DocumentService_SubscribeServer) error {
	ch := make(chan *collabv1.SubscribeResponse, 100)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case m := <-ch:
			if err := stream.Send(m); err != nil {
				return err
			}
		}
	}
}

// fakeAssist streams words slowly so cancellation can be tested.
type fakeAssist struct {
	collabv1.UnimplementedAssistServiceServer
	cancelled chan struct{}
}

func (f *fakeAssist) Autocomplete(r *collabv1.AutocompleteRequest, stream collabv1.AssistService_AutocompleteServer) error {
	for i, w := range []string{" one", " two", " three", " four", " five"} {
		if i == 4 { // the last word only comes after a long pause
			select {
			case <-stream.Context().Done():
				close(f.cancelled)
				return stream.Context().Err()
			case <-time.After(5 * time.Second):
			}
		}
		if err := stream.Send(&collabv1.AutocompleteResponse{RequestId: r.GetContext().GetRequestId(), Delta: w}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeAssist) CheckGrammar(ctx context.Context, r *collabv1.CheckGrammarRequest) (*collabv1.CheckGrammarResponse, error) {
	return nil, status.Error(codes.ResourceExhausted, "rate limited")
}

type env struct {
	url    string
	docs   *fakeDocs
	assist *fakeAssist
}

func setup(t *testing.T) *env {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	docs := &fakeDocs{}
	assist := &fakeAssist{cancelled: make(chan struct{})}
	g := grpc.NewServer()
	collabv1.RegisterDocumentServiceServer(g, docs)
	collabv1.RegisterAssistServiceServer(g, assist)
	go g.Serve(lis)
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	deps := Deps{
		Docs:   collabv1.NewDocumentServiceClient(conn),
		Assist: collabv1.NewAssistServiceClient(conn),
		Hub:    presence.NewHub(),
		Now:    func() time.Time { return time.UnixMilli(1_700_000_000_000) },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		Serve(r.Context(), c, deps, r.URL.Query().Get("doc"), r.URL.Query().Get("name"))
	}))
	t.Cleanup(srv.Close)
	return &env{url: "ws" + strings.TrimPrefix(srv.URL, "http"), docs: docs, assist: assist}
}

type client struct {
	t    *testing.T
	conn *websocket.Conn
}

func (e *env) connect(t *testing.T, name string) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, e.url+"?doc=doc1&name="+name, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return &client{t: t, conn: c}
}

func (c *client) send(msg string) {
	c.t.Helper()
	if err := c.conn.Write(context.Background(), websocket.MessageText, []byte(msg)); err != nil {
		c.t.Fatal(err)
	}
}

// next returns the next message for which keep returns true, skipping others.
func (c *client) next(keep func(*collabv1.ServerMessage) bool) *collabv1.ServerMessage {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		msg := &collabv1.ServerMessage{}
		if err := protojson.Unmarshal(data, msg); err != nil {
			c.t.Fatalf("bad JSON from gateway: %v: %s", err, data)
		}
		if keep(msg) {
			return msg
		}
	}
}

func isPresenceWith(n int) func(*collabv1.ServerMessage) bool {
	return func(m *collabv1.ServerMessage) bool { return len(m.GetPresence().GetParticipants()) == n }
}

func TestJoinEditAndPresence(t *testing.T) {
	e := setup(t)
	asha := e.connect(t, "Asha")
	joined := asha.next(func(m *collabv1.ServerMessage) bool { return m.GetJoined() != nil }).GetJoined()
	if joined.GetName() != "Asha" || joined.GetSnapshot().GetText() != "hello" || joined.GetSessionId() == "" {
		t.Fatalf("joined = %v", joined)
	}

	bala := e.connect(t, "Bala")
	bala.next(func(m *collabv1.ServerMessage) bool { return m.GetJoined() != nil })
	asha.next(isPresenceWith(2)) // Asha sees Bala arrive

	// Asha edits. Her op claims to be for another document and another user;
	// the gateway must correct both.
	asha.send(`{"requestId": "r1", "submit": {"operations": [{
		"opId": {"clientId": "c-asha", "seq": "1"}, "docId": "other", "userId": "mallory",
		"baseRevision": "0", "changes": [{"range": {"start": 5, "end": 5}, "insert": "!"}]}]}}`)
	reply := asha.next(func(m *collabv1.ServerMessage) bool { return m.GetRequestId() == "r1" })
	if reply.GetSubmitted().GetRevision() != 1 {
		t.Fatalf("submit reply = %v", reply)
	}
	e.docs.mu.Lock()
	got := e.docs.last
	e.docs.mu.Unlock()
	if got.GetDocId() != "doc1" || got.GetOperations()[0].GetDocId() != "doc1" || got.GetOperations()[0].GetUserId() != "Asha" {
		t.Fatalf("gateway did not correct doc/user: %v", got)
	}

	// Bala receives the committed edit.
	op := bala.next(func(m *collabv1.ServerMessage) bool { return m.GetOperation() != nil }).GetOperation()
	if op.GetRevision() != 1 || op.GetOperation().GetOpId().GetClientId() != "c-asha" {
		t.Fatalf("operation = %v", op)
	}

	// Bala moves his cursor; Asha sees it and sees that she edited recently.
	bala.send(`{"cursor": {"revision": "1", "anchor": 2, "head": 4}}`)
	p := asha.next(func(m *collabv1.ServerMessage) bool {
		for _, part := range m.GetPresence().GetParticipants() {
			if part.GetName() == "Bala" && part.GetCursor().GetHead() == 4 {
				return true
			}
		}
		return false
	}).GetPresence()
	for _, part := range p.GetParticipants() {
		if part.GetName() == "Asha" && part.GetLastEditUnixMs() != 1_700_000_000_000 {
			t.Fatalf("Asha's last edit not recorded: %v", part)
		}
	}

	// Bala leaves; Asha sees one participant again.
	bala.conn.Close(websocket.StatusNormalClosure, "")
	asha.next(isPresenceWith(1))
}

func TestStaleEditReturnsAborted(t *testing.T) {
	e := setup(t)
	c := e.connect(t, "Asha")
	c.send(`{"requestId": "r1", "submit": {"operations": [{"opId": {"clientId": "c", "seq": "1"}, "baseRevision": "7"}]}}`)
	msg := c.next(func(m *collabv1.ServerMessage) bool { return m.GetRequestId() == "r1" })
	if msg.GetError().GetCode() != "ABORTED" {
		t.Fatalf("got %v, want an ABORTED error", msg)
	}
}

func TestAssistErrorsAreForwarded(t *testing.T) {
	e := setup(t)
	c := e.connect(t, "Asha")
	c.send(`{"requestId": "g1", "checkGrammar": {"context": {"text": "hi"}}}`)
	msg := c.next(func(m *collabv1.ServerMessage) bool { return m.GetRequestId() == "g1" })
	if msg.GetError().GetCode() != "RESOURCE_EXHAUSTED" {
		t.Fatalf("got %v", msg)
	}
}

func TestAutocompleteStreamsAndCanBeCancelled(t *testing.T) {
	e := setup(t)
	c := e.connect(t, "Asha")
	c.send(`{"requestId": "a1", "autocomplete": {"context": {"requestId": "a1", "before": "count:"}}}`)

	var words []string
	for len(words) < 4 {
		m := c.next(func(m *collabv1.ServerMessage) bool { return m.GetRequestId() == "a1" })
		words = append(words, m.GetAutocomplete().GetDelta())
	}
	if strings.Join(words, "") != " one two three four" {
		t.Fatalf("streamed %q", words)
	}

	// The user keeps typing, so the browser cancels the suggestion.
	c.send(`{"cancel": {"targetRequestId": "a1"}}`)
	end := c.next(func(m *collabv1.ServerMessage) bool { return m.GetRequestId() == "a1" })
	if end.GetStreamEnd() == nil {
		t.Fatalf("expected stream_end, got %v", end)
	}
	select {
	case <-e.assist.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the Assist call was not cancelled")
	}
}

func TestBadJSONGetsAnError(t *testing.T) {
	e := setup(t)
	c := e.connect(t, "Asha")
	c.send(`not json`)
	msg := c.next(func(m *collabv1.ServerMessage) bool { return m.GetError() != nil })
	if msg.GetError().GetCode() != "INVALID_ARGUMENT" {
		t.Fatalf("got %v", msg)
	}
}