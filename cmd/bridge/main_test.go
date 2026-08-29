package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type blockingExecBridgeServer struct {
	pb.UnimplementedContainerServiceServer
	started chan struct{}
	release chan struct{}
}

func (s *blockingExecBridgeServer) Exec(pb.ContainerService_ExecServer) error {
	close(s.started)
	// Deliberately ignore stream cancellation. Positive-timeout bridge Exec
	// handlers can do the same while their child process is still running.
	<-s.release
	return nil
}

func TestBridgeGRPCShutdownStopsActiveStreams(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	service := &blockingExecBridgeServer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(service.release)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, service)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		server.Stop()
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	streamCtx, streamCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer streamCancel()
	stream, err := pb.NewContainerServiceClient(conn).Exec(streamCtx)
	if err != nil {
		server.Stop()
		t.Fatalf("open exec stream: %v", err)
	}
	if err := stream.Send(&pb.ExecInput{Command: "sleep forever"}); err != nil {
		server.Stop()
		t.Fatalf("start exec stream: %v", err)
	}
	select {
	case <-service.started:
	case <-time.After(time.Second):
		server.Stop()
		t.Fatal("exec stream did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	shutdownDone := make(chan struct{})
	go func() {
		stopBridgeGRPCServer(ctx, server)
		close(shutdownDone)
	}()
	cancel()

	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		server.Stop()
		t.Fatal("bridge shutdown did not finish")
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("active exec stream survived forced bridge shutdown")
	}
	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("gRPC Serve did not return after shutdown")
	}
}
