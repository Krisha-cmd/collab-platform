// Command docnode runs the document service.
//
//	go run ./services/docnode/cmd/docnode -port 50051
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	collabv1 "github.com/Krisha-cmd/collab-platform/gen/go/collab/v1"
	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/oplog"
	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/server"
	"github.com/Krisha-cmd/collab-platform/services/docnode/internal/statemachine"
)

func main() {
	port := flag.Int("port", 50051, "port to listen on")
	flag.Parse()

	sm := statemachine.New()
	srv := server.New(oplog.NewLocal(sm), sm) // Milestone 2 swaps NewLocal for a Raft log

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer()
	collabv1.RegisterDocumentServiceServer(g, srv)
	reflection.Register(g) // lets tools like grpcurl list the services

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Print("shutting down")
		g.Stop() // also ends open Subscribe streams
	}()

	log.Printf("document service listening on :%d", *port)
	if err := g.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}