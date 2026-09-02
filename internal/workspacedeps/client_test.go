package workspacedeps

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

const testBufSize = 1024 * 1024

// newExecTestClient wires a bridge client to a real bridgesvc server in the
// test process, so every exec runs an actual /bin/sh with the production
// stdin/stdout plumbing. AllowHostAbsolute lets tests point the runner at
// t.TempDir() paths as if they were the workspace data root.
func newExecTestClient(t *testing.T) *bridge.Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bridgesvc exec runs commands via /bin/sh")
	}
	server := bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir:    t.TempDir(),
		AllowHostAbsolute: true,
	})

	lis := bufconn.Listen(testBufSize)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, server)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return bridge.NewClientFromConn(conn)
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// writeExecutable writes a sh script to dir/name and marks it executable.
func writeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // G306: test fixture must be executable.
		t.Fatalf("write %s: %v", target, err)
	}
	return target
}

// recordingSink collects runner output per stream.
type recordingSink struct {
	mu    sync.Mutex
	lines map[string][]string
}

func newRecordingSink() *recordingSink {
	return &recordingSink{lines: make(map[string][]string)}
}

func (s *recordingSink) Log(stream, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines[stream] = append(s.lines[stream], line)
}

func (s *recordingSink) get(stream string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines[stream]...)
}

func (s *recordingSink) has(stream, want string) bool {
	for _, line := range s.get(stream) {
		if line == want {
			return true
		}
	}
	return false
}
