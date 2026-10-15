//go:build !windows

package agentprocess

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

type stopTestServer struct {
	pb.UnimplementedContainerServiceServer
	reached chan struct{}
	release chan struct{}
	drop    bool
}

func (s *stopTestServer) Exec(stream pb.ContainerService_ExecServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_STDOUT, Data: []byte("ready")}); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if msg.Terminate {
			close(s.reached)
			if s.drop {
				return nil
			}
			select {
			case <-s.release:
				return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT, ExitCode: 137})
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}
	}
}

func stopTestClient(t *testing.T, service pb.ContainerServiceServer) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///stop-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

func startStopTestProcess(t *testing.T, client *bridge.Client) *Process {
	t.Helper()
	proc, err := Start(t.Context(), client, "printf ready; sleep 30", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Close() })
	ready := make([]byte, 5)
	if _, err := io.ReadFull(proc, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("process did not become ready: %q %v", ready, err)
	}
	go func() { _, _ = io.Copy(io.Discard, proc) }()
	return proc
}

func TestStopWaitsForExitAcknowledgement(t *testing.T) {
	server := &stopTestServer{reached: make(chan struct{}), release: make(chan struct{})}
	proc := startStopTestProcess(t, stopTestClient(t, server))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- proc.Stop(ctx) }()
	select {
	case <-server.reached:
	case err := <-result:
		t.Fatalf("stop returned before termination request and EXIT: %v", err)
	case <-ctx.Done():
		t.Fatal("termination request never reached bridge")
	}
	select {
	case err := <-result:
		t.Fatalf("stop returned before EXIT: %v", err)
	default:
	}
	close(server.release)
	if err := <-result; err != nil {
		t.Fatalf("confirmed process stop failed: %v", err)
	}
}

func TestStopDoesNotConfuseTransportLossOrTimeoutWithExit(t *testing.T) {
	for _, drop := range []bool{false, true} {
		server := &stopTestServer{reached: make(chan struct{}), release: make(chan struct{}), drop: drop}
		proc := startStopTestProcess(t, stopTestClient(t, server))
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		err := proc.Stop(ctx)
		cancel()
		if !errors.Is(err, ErrTerminationUnconfirmed) {
			t.Fatalf("unconfirmed process stop was treated as confirmed (disconnect=%v): %v", drop, err)
		}
	}
}

func TestStoppingChildProcessLeavesParentAndSiblingRunning(t *testing.T) {
	client := stopTestClient(t, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: t.TempDir(), AllowHostAbsolute: true}))
	parent := startStopTestProcess(t, client)
	childA := startStopTestProcess(t, client)
	childB := startStopTestProcess(t, client)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := childA.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, other := range []*Process{parent, childB} {
		select {
		case <-other.Done():
			t.Fatal("stopping child stopped parent or sibling")
		default:
		}
	}
	next := startStopTestProcess(t, client)
	if err := next.Stop(ctx); err != nil {
		t.Fatalf("next child could not execute and stop: %v", err)
	}
}
