package agentproc

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func newTestBridgeClient(t *testing.T) *bridge.Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bridge requires /bin/sh")
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: t.TempDir(), AllowHostAbsolute: true}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough://bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := bridge.NewClientFromConn(conn)
	return client
}

func TestProcessStdinEOFDrainsOutputAndReportsExit(t *testing.T) {
	client := newTestBridgeClient(t)
	for _, code := range []string{"0", "3"} {
		t.Run(code, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			proc, err := Spawn(ctx, client, "while read line; do :; done; printf flushed; exit "+code, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = proc.Close() }()
			if _, err := proc.Write([]byte("input\n")); err != nil {
				t.Fatal(err)
			}
			proc.CloseStdin()
			output, err := io.ReadAll(proc)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-proc.Done():
			case <-ctx.Done():
				t.Fatal("stdin EOF never reached the process")
			}
			if string(output) != "flushed" || (proc.Err() == nil) != (code == "0") {
				t.Fatalf("output %q, exit error %v", output, proc.Err())
			}
		})
	}
}

// A command that stops reading never holds a writer: writes are queued, and
// one that leaves the queue unread is killed and taken out of service.
func TestWritesNeverWaitOnACommandThatStoppedReading(t *testing.T) {
	client := newTestBridgeClient(t)
	proc, err := Spawn(t.Context(), client, "sleep 60", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	chunk := make([]byte, 64*1024)
	result := make(chan error, 1)
	go func() {
		for range 2 * inputQueue {
			if _, err := proc.Write(chunk); err != nil {
				result <- err
				return
			}
		}
		result <- nil
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrNotReading) {
			t.Fatalf("writes to a command that reads nothing = %v, want ErrNotReading", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a write waits on a command that stopped reading")
	}
	select {
	case <-proc.Done():
	default:
		t.Fatal("a command that stopped reading is still in service")
	}
}

// Kill takes the process out of service before it returns, whatever state
// the command is in, and the command is terminated.
func TestKillTakesTheProcessOutOfServiceAtOnce(t *testing.T) {
	client := newTestBridgeClient(t)
	proc, err := Spawn(t.Context(), client, "sleep 60", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	proc.Kill()
	select {
	case <-proc.Done():
	default:
		t.Fatal("Done is open after Kill returned")
	}
	select {
	case <-proc.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the command still runs after Kill")
	}
}

// stdin is a byte stream: a write larger than one bridge frame reaches the
// command whole.
func TestAWriteLargerThanABridgeFrameArrivesWhole(t *testing.T) {
	client := newTestBridgeClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	proc, err := Spawn(ctx, client, "wc -c", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	const size = 5 << 20 // past gRPC's default 4 MiB message limit
	if _, err := proc.Write(make([]byte, size)); err != nil {
		t.Fatal(err)
	}
	proc.CloseStdin()
	output, err := io.ReadAll(proc)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output)); got != strconv.Itoa(size) {
		t.Fatalf("the command read %s bytes, want %d", got, size)
	}
}

// Closing never waits on output nobody reads: the kill releases the receive
// loop's pending write.
func TestCloseDoesNotWaitOnUnreadOutput(t *testing.T) {
	client := newTestBridgeClient(t)
	proc, err := Spawn(t.Context(), client, "printf pending; sleep 60", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		_ = proc.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waits on output nobody reads")
	}
}
