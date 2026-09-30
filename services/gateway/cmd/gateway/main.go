// Command gateway is the WebSocket server browsers connect to.
//
//	go run ./services/gateway/cmd/gateway
//
// Browsers connect to ws://localhost:8080/ws?doc=<doc_id>&name=<display name>.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	collabv1 "github.com/Krisha-cmd/collab-platform/gen/go/collab/v1"
	"github.com/Krisha-cmd/collab-platform/services/gateway/internal/presence"
	"github.com/Krisha-cmd/collab-platform/services/gateway/internal/session"
)

var docIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func main() {
	addr := flag.String("addr", ":8080", "address to listen on for browsers")
	docsAddr := flag.String("docs", "localhost:50051", "document service address")
	assistAddr := flag.String("assist", "localhost:50061", "Assist (LLM) service address")
	origins := flag.String("origins", "localhost:*,127.0.0.1:*",
		"comma-separated host patterns of web pages allowed to connect")
	flag.Parse()

	// grpc.NewClient does not connect yet, so the gateway starts even if the
	// other services are not running; requests fail with UNAVAILABLE until they are.
	docsConn, err := grpc.NewClient(*docsAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("document service client: %v", err)
	}
	defer docsConn.Close()
	assistConn, err := grpc.NewClient(*assistAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("assist service client: %v", err)
	}
	defer assistConn.Close()

	deps := session.Deps{
		Docs:   collabv1.NewDocumentServiceClient(docsConn),
		Assist: collabv1.NewAssistServiceClient(assistConn),
		Hub:    presence.NewHub(),
		Now:    time.Now,
	}
	originPatterns := strings.Split(*origins, ",")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		docID := r.URL.Query().Get("doc")
		if !docIDPattern.MatchString(docID) {
			http.Error(w, "doc must be 1-100 letters, digits, '-' or '_'", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originPatterns})
		if err != nil {
			return // Accept already wrote an error response
		}
		name := displayName(r.URL.Query().Get("name"))
		log.Printf("%s joined %s", name, docID)
		session.Serve(r.Context(), conn, deps, docID, name)
		log.Printf("%s left %s", name, docID)
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv := &http.Server{
		Addr:    *addr,
		Handler: mux,
		// Every request's context is cancelled on Ctrl+C, which closes open WebSockets.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("gateway listening on %s (documents: %s, assist: %s)", *addr, *docsAddr, *assistAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

// displayName cleans up the name a browser asks for.
func displayName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) {
		return "Anonymous"
	}
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}