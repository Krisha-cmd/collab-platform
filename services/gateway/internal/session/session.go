// Package session handles one browser's WebSocket connection to one document.
//
// For each connection it:
//   - sends the document snapshot, then every committed operation after it
//     (from the document service's Subscribe stream);
//   - forwards the browser's edits to the document service, in order;
//   - forwards LLM requests to the Assist service, streaming replies back;
//   - reports the user to the presence hub and relays presence updates.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"sync"
	"time"

	"github.com/coder/websocket"
	rpccode "google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	collabv1 "github.com/Krisha-cmd/collab-platform/gen/go/collab/v1"
	"github.com/Krisha-cmd/collab-platform/services/gateway/internal/presence"
)

const (
	maxMessageBytes = 1 << 20 // largest message accepted from a browser
	outboxSize      = 256     // messages queued for a browser before we wait
	writeTimeout    = 10 * time.Second
	submitTimeout   = 10 * time.Second
	assistTimeout   = 60 * time.Second
)

// Deps are the things a session talks to.
type Deps struct {
	Docs   collabv1.DocumentServiceClient
	Assist collabv1.AssistServiceClient
	Hub    *presence.Hub
	Now    func() time.Time // replaceable in tests
}

type session struct {
	ctx    context.Context // lives as long as the connection
	deps   Deps
	conn   *websocket.Conn
	docID  string
	id     string
	name   string
	outbox chan *collabv1.ServerMessage
	cancel context.CancelFunc

	mu      sync.Mutex
	streams map[string]context.CancelFunc // running streaming requests by request_id
}

// Serve runs a session until the browser disconnects or ctx is cancelled.
func Serve(ctx context.Context, conn *websocket.Conn, deps Deps, docID, name string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn.SetReadLimit(maxMessageBytes)

	s := &session{
		ctx: ctx, deps: deps, conn: conn, docID: docID, id: newID(), name: name,
		outbox: make(chan *collabv1.ServerMessage, outboxSize), cancel: cancel,
		streams: make(map[string]context.CancelFunc),
	}
	go s.writeLoop(ctx)

	if err := s.join(ctx); err != nil {
		s.sendError("", err)
		time.Sleep(100 * time.Millisecond) // let the error reach the browser
		conn.Close(websocket.StatusTryAgainLater, "document service unavailable")
		return
	}
	s.readLoop(ctx)
	conn.Close(websocket.StatusNormalClosure, "")
}

// join sends the snapshot, starts forwarding operations and enters presence.
func (s *session) join(ctx context.Context) error {
	snap, err := s.deps.Docs.GetSnapshot(ctx, &collabv1.GetSnapshotRequest{DocId: s.docID})
	if err != nil {
		return err
	}
	// Subscribing from the snapshot's revision means nothing committed after
	// the snapshot can be missed.
	stream, err := s.deps.Docs.Subscribe(ctx, &collabv1.SubscribeRequest{
		DocId: s.docID, FromRevision: snap.GetSnapshot().GetRevision(),
	})
	if err != nil {
		return err
	}
	s.send(&collabv1.ServerMessage{Kind: &collabv1.ServerMessage_Joined{Joined: &collabv1.JoinedEvent{
		SessionId: s.id, Name: s.name, Snapshot: snap.GetSnapshot(),
	}}})
	go s.forwardOperations(ctx, stream)

	leave := s.deps.Hub.Join(s.docID, presence.Participant{SessionID: s.id, Name: s.name}, s.onPresence)
	context.AfterFunc(ctx, leave)
	return nil
}

func (s *session) forwardOperations(ctx context.Context, stream collabv1.DocumentService_SubscribeClient) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			if ctx.Err() == nil {
				// Lost the document service. The browser must reconnect and
				// reload, because it may have missed operations.
				log.Printf("session %s: subscription ended: %v", s.id, err)
				s.sendError("", status.Error(codes.Unavailable, "lost connection to the document service"))
				time.Sleep(100 * time.Millisecond)
				s.cancel()
			}
			return
		}
		s.send(&collabv1.ServerMessage{Kind: &collabv1.ServerMessage_Operation{Operation: msg}})
	}
}

// onPresence is called by the hub with its lock held, so it must not block.
// Presence is lossy by design: if the outbox is full, this update is skipped.
func (s *session) onPresence(ps []presence.Participant) {
	ev := &collabv1.PresenceEvent{}
	for _, p := range ps {
		part := &collabv1.Participant{
			SessionId: p.SessionID, Name: p.Name, Color: p.Color, LastEditUnixMs: p.LastEditUnixMs,
		}
		if p.Cursor != nil {
			part.Cursor = &collabv1.CursorUpdate{Revision: p.Cursor.Revision, Anchor: p.Cursor.Anchor, Head: p.Cursor.Head}
		}
		ev.Participants = append(ev.Participants, part)
	}
	select {
	case s.outbox <- &collabv1.ServerMessage{Kind: &collabv1.ServerMessage_Presence{Presence: ev}}:
	default:
	}
}

func (s *session) readLoop(ctx context.Context) {
	unmarshal := protojson.UnmarshalOptions{DiscardUnknown: true}
	for {
		_, data, err := s.conn.Read(ctx)
		if err != nil {
			return // browser closed the connection, or ctx was cancelled
		}
		msg := &collabv1.ClientMessage{}
		if err := unmarshal.Unmarshal(data, msg); err != nil {
			s.sendError("", status.Errorf(codes.InvalidArgument, "could not parse message: %v", err))
			continue
		}
		s.handle(ctx, msg)
	}
}

func (s *session) handle(ctx context.Context, msg *collabv1.ClientMessage) {
	id := msg.GetRequestId()
	switch k := msg.GetKind().(type) {
	case *collabv1.ClientMessage_Submit:
		// Handled here, not in a goroutine, so one browser's edits reach the
		// document service in the order they were sent.
		s.submit(ctx, id, k.Submit)
	case *collabv1.ClientMessage_Cursor:
		c := k.Cursor
		s.deps.Hub.UpdateCursor(s.docID, s.id, presence.Cursor{Revision: c.GetRevision(), Anchor: c.GetAnchor(), Head: c.GetHead()})
	case *collabv1.ClientMessage_CheckGrammar:
		go s.unary(ctx, id, func(ctx context.Context) (*collabv1.ServerMessage, error) {
			r, err := s.deps.Assist.CheckGrammar(ctx, k.CheckGrammar)
			return &collabv1.ServerMessage{Kind: &collabv1.ServerMessage_Grammar{Grammar: r}}, err
		})
	case *collabv1.ClientMessage_Enhance:
		go s.unary(ctx, id, func(ctx context.Context) (*collabv1.ServerMessage, error) {
			r, err := s.deps.Assist.Enhance(ctx, k.Enhance)
			return &collabv1.ServerMessage{Kind: &collabv1.ServerMessage_Enhanced{Enhanced: r}}, err
		})
	case *collabv1.ClientMessage_Autocomplete:
		go s.streaming(ctx, id, func(ctx context.Context) (func() (*collabv1.ServerMessage, error), error) {
			st, err := s.deps.Assist.Autocomplete(ctx, k.Autocomplete)
			return func() (*collabv1.ServerMessage, error) {
				r, err := st.Recv()
				return &collabv1.ServerMessage{Kind: &collabv1.ServerMessage_Autocomplete{Autocomplete: r}}, err
			}, err
		})
	case *collabv1.ClientMessage_Summarize:
		req := k.Summarize
		if req.GetDocument() == nil { // summarize the current document by default
			snap, err := s.deps.Docs.GetSnapshot(ctx, &collabv1.GetSnapshotRequest{DocId: s.docID})
			if err != nil {
				s.sendError(id, err)
				return
			}
			req.Document = snap.GetSnapshot()
		}
		go s.streaming(ctx, id, func(ctx context.Context) (func() (*collabv1.ServerMessage, error), error) {
			st, err := s.deps.Assist.Summarize(ctx, req)
			return func() (*collabv1.ServerMessage, error) {
				r, err := st.Recv()
				return &collabv1.ServerMessage{Kind: &collabv1.ServerMessage_Summary{Summary: r}}, err
			}, err
		})
	case *collabv1.ClientMessage_Cancel:
		s.mu.Lock()
		if stop, ok := s.streams[k.Cancel.GetTargetRequestId()]; ok {
			stop()
		}
		s.mu.Unlock()
	default:
		s.sendError(id, status.Error(codes.InvalidArgument, "unknown message kind"))
	}
}

func (s *session) submit(ctx context.Context, id string, req *collabv1.SubmitOperationsRequest) {
	// The gateway decides which document and user an edit belongs to; the
	// browser cannot claim to be someone else or write to another document.
	req.DocId = s.docID
	for _, op := range req.GetOperations() {
		op.DocId = s.docID
		op.UserId = s.name
	}
	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	res, err := s.deps.Docs.SubmitOperations(ctx, req)
	if err != nil {
		s.sendError(id, err)
		return
	}
	if res.GetApplied() > 0 {
		s.deps.Hub.MarkEdited(s.docID, s.id, s.deps.Now())
	}
	s.send(&collabv1.ServerMessage{RequestId: id, Kind: &collabv1.ServerMessage_Submitted{Submitted: res}})
}

func (s *session) unary(ctx context.Context, id string, call func(context.Context) (*collabv1.ServerMessage, error)) {
	ctx, cancel := context.WithTimeout(ctx, assistTimeout)
	defer cancel()
	msg, err := call(ctx)
	if err != nil {
		s.sendError(id, err)
		return
	}
	msg.RequestId = id
	s.send(msg)
}

// streaming runs a streaming Assist call, forwarding each piece, then StreamEnd.
func (s *session) streaming(ctx context.Context, id string,
	open func(context.Context) (func() (*collabv1.ServerMessage, error), error)) {
	ctx, cancel := context.WithTimeout(ctx, assistTimeout)
	defer cancel()
	s.mu.Lock()
	s.streams[id] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.streams, id)
		s.mu.Unlock()
	}()

	next, err := open(ctx)
	for err == nil {
		var msg *collabv1.ServerMessage
		if msg, err = next(); err == nil {
			msg.RequestId = id
			s.send(msg)
		}
	}
	if !errors.Is(err, io.EOF) {
		if status.Code(err) == codes.Canceled {
			err = nil // the browser asked us to stop; still end the stream cleanly
		} else {
			s.sendError(id, err)
			return
		}
	}
	s.send(&collabv1.ServerMessage{RequestId: id, Kind: &collabv1.ServerMessage_StreamEnd{StreamEnd: &collabv1.StreamEnd{}}})
}

// send queues a message for the browser, waiting if the queue is full.
// It gives up once the connection is closing.
func (s *session) send(msg *collabv1.ServerMessage) {
	select {
	case s.outbox <- msg:
	case <-s.ctx.Done():
	}
}

func (s *session) sendError(id string, err error) {
	st := status.Convert(err)
	s.send(&collabv1.ServerMessage{RequestId: id, Kind: &collabv1.ServerMessage_Error{Error: &collabv1.ErrorEvent{
		Code:    rpccode.Code(st.Code()).String(), // e.g. "ABORTED"
		Message: st.Message(),
	}}})
}

func (s *session) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-s.outbox:
			data, err := protojson.Marshal(msg)
			if err != nil {
				log.Printf("session %s: encode: %v", s.id, err)
				continue
			}
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err = s.conn.Write(wctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				s.cancel() // the browser is gone or too slow: end the session
				return
			}
		}
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}