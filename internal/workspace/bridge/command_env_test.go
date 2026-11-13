package bridge

import (
	"context"
	"slices"
	"testing"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// envCaptureTestServer records the environment of each command it is asked
// to run and exits it at once.
type envCaptureTestServer struct {
	pb.UnimplementedContainerServiceServer
	inputs chan *pb.ExecInput
}

func (s *envCaptureTestServer) Exec(stream pb.ContainerService_ExecServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	s.inputs <- first
	return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT})
}

func newEnvCaptureClient(t *testing.T, commandEnv []string) (*Client, *envCaptureTestServer) {
	t.Helper()
	server := &envCaptureTestServer{inputs: make(chan *pb.ExecInput, 4)}
	client := newTestClient(t, server)
	client.commandEnv = commandEnv
	return client, server
}

func TestCommandEnvGoesAheadOfTheCallersEnv(t *testing.T) {
	client, server := newEnvCaptureClient(t, []string{"HOME=/data", "PATH=/workspace/bin"})
	if _, err := client.ExecWithOptions(context.Background(), "true", "/data", 5, nil, ExecOptions{Env: []string{"HOME=/agent"}}); err != nil {
		t.Fatalf("ExecWithOptions: %v", err)
	}
	// The bridge keeps the last value of a repeated name, so the caller's
	// HOME wins and the workspace PATH stays.
	want := []string{"HOME=/data", "PATH=/workspace/bin", "HOME=/agent"}
	if got := (<-server.inputs).GetEnv(); !slices.Equal(got, want) {
		t.Fatalf("Env = %q, want %q", got, want)
	}
}

func TestCommandEnvReachesStreamsAndTerminals(t *testing.T) {
	client, server := newEnvCaptureClient(t, []string{"HOME=/data"})
	stream, err := client.ExecStreamWithOptions(context.Background(), "true", "/data", 5, ExecOptions{})
	if err != nil {
		t.Fatalf("ExecStreamWithOptions: %v", err)
	}
	_ = stream.Close()
	if got := (<-server.inputs).GetEnv(); !slices.Equal(got, []string{"HOME=/data"}) {
		t.Fatalf("stream Env = %q", got)
	}
	pty, err := client.ExecStreamPTYWithOptions(context.Background(), "bash", "/data", 80, 24, ExecOptions{})
	if err != nil {
		t.Fatalf("ExecStreamPTYWithOptions: %v", err)
	}
	_ = pty.Close()
	if got := (<-server.inputs).GetEnv(); !slices.Equal(got, []string{"HOME=/data"}) {
		t.Fatalf("terminal Env = %q", got)
	}
}

func TestUnsetEnvRemovesFromTheCommandEnv(t *testing.T) {
	client, server := newEnvCaptureClient(t, []string{"HOME=/data", "XDG_CONFIG_HOME=/data/.config", "XDG_CACHE_HOME=/data/.cache", "PATH=/workspace/bin"})
	opts := ExecOptions{Env: []string{"PATH=/agent/bin"}, UnsetEnv: []string{"XDG_*", "HOME"}}
	if _, err := client.ExecWithOptions(context.Background(), "true", "/data", 5, nil, opts); err != nil {
		t.Fatalf("ExecWithOptions: %v", err)
	}
	want := []string{"PATH=/workspace/bin", "PATH=/agent/bin"}
	if got := (<-server.inputs).GetEnv(); !slices.Equal(got, want) {
		t.Fatalf("Env = %q, want %q", got, want)
	}
}

func TestClientWithoutCommandEnvSendsOnlyTheCallersEnv(t *testing.T) {
	// A connected computer's client is created on its own, not by the pool,
	// and keeps its owner's environment.
	client, server := newEnvCaptureClient(t, nil)
	if _, err := client.ExecWithOptions(context.Background(), "true", "", 5, nil, ExecOptions{Env: []string{"A=1"}}); err != nil {
		t.Fatalf("ExecWithOptions: %v", err)
	}
	if got := (<-server.inputs).GetEnv(); !slices.Equal(got, []string{"A=1"}) {
		t.Fatalf("Env = %q", got)
	}
}

func TestPoolGivesItsClientsTheCommandEnv(t *testing.T) {
	pool := NewPool(func(string) string { return "unix:///nonexistent/bridge.sock" })
	pool.SetCommandEnv([]string{"HOME=/data"})
	client, err := pool.Get(context.Background(), "bot")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(pool.CloseAll)
	if !slices.Equal(client.commandEnv, []string{"HOME=/data"}) {
		t.Fatalf("commandEnv = %q", client.commandEnv)
	}
}
