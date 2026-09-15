package bridge

import (
	"errors"
	"io"
	"testing"

	"google.golang.org/grpc/metadata"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type noMaintenanceHeaderServer struct {
	pb.UnimplementedContainerServiceServer
	received chan []byte
}

func (s *noMaintenanceHeaderServer) Exec(stream pb.ContainerService_ExecServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	body := append([]byte(nil), first.GetStdinData()...)
	// Old bridges send only ordinary gRPC headers. Even an eager response must
	// not cause the new client to deliver an unacknowledged cleanup script.
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	for {
		input, err := stream.Recv()
		if err != nil {
			s.received <- body
			return nil
		}
		body = append(body, input.GetStdinData()...)
	}
}

func TestQuiescentOldBridgeNeverReceivesCleanupBody(t *testing.T) {
	server := &noMaintenanceHeaderServer{received: make(chan []byte, 1)}
	client := newTestClient(t, server)
	_, err := client.ExecQuiescent(t.Context(), "rm -rf -- /payload/old\n")
	if !errors.Is(err, ErrPayloadWindowClosed) {
		t.Fatalf("old bridge accepted: %v", err)
	}
	if body := <-server.received; len(body) != 0 {
		t.Fatalf("old bridge received cleanup: %q", body)
	}
}

type uncertainMaintenanceServer struct {
	pb.UnimplementedContainerServiceServer
}

func (*uncertainMaintenanceServer) Exec(stream pb.ContainerService_ExecServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.SendHeader(metadata.Pairs("x-memoh-payload-gc-window", "accepted")); err != nil {
		return err
	}
	for {
		if _, err := stream.Recv(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func TestQuiescentDoesNotTreatMissingExitAsSuccess(t *testing.T) {
	client := newTestClient(t, &uncertainMaintenanceServer{})
	if _, err := client.ExecQuiescent(t.Context(), "true\n"); err == nil {
		t.Fatal("EOF without EXIT acknowledged cleanup")
	}
}
