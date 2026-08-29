package bridgesvc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type watchStream struct {
	ctx     context.Context
	batches chan []string
}

func newWatchStream(ctx context.Context) *watchStream {
	return &watchStream{ctx: ctx, batches: make(chan []string, 16)}
}

func (s *watchStream) Send(msg *pb.WatchDirEvent) error {
	s.batches <- append([]string(nil), msg.GetPaths()...)
	return nil
}

func (s *watchStream) Context() context.Context   { return s.ctx }
func (*watchStream) SetHeader(metadata.MD) error  { return nil }
func (*watchStream) SendHeader(metadata.MD) error { return nil }
func (*watchStream) SetTrailer(metadata.MD)       {}
func (*watchStream) SendMsg(any) error            { return nil }
func (*watchStream) RecvMsg(any) error            { return nil }

func waitBatch(t *testing.T, s *watchStream) []string {
	t.Helper()
	select {
	case batch := <-s.batches:
		return batch
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for watch batch")
		return nil
	}
}

func TestWatchDirStreamsChildChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{DefaultWorkDir: "/data", WorkspaceRoot: root, DataMount: "/data"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newWatchStream(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.WatchDir(&pb.WatchDirRequest{Path: "/data/sub"}, stream) }()

	// Give the watcher a beat to register before mutating.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	batch := waitBatch(t, stream)
	found := false
	for _, p := range batch {
		if p == "/data/sub/a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("batch = %v, want to contain /data/sub/a.txt", batch)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchDir returned %v after cancel, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WatchDir did not return after cancel")
	}
}

func TestWatchDirCoalescesBursts(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{DefaultWorkDir: "/data", WorkspaceRoot: root, DataMount: "/data"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newWatchStream(ctx)
	go func() { _ = srv.WatchDir(&pb.WatchDirRequest{Path: "/data"}, stream) }()

	time.Sleep(100 * time.Millisecond)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	batch := waitBatch(t, stream)
	if len(batch) < 2 {
		t.Fatalf("batch = %v, want the burst coalesced into one batch", batch)
	}
}

func TestWatchDirRejectsMissingDir(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{DefaultWorkDir: "/data", WorkspaceRoot: root, DataMount: "/data"})

	stream := newWatchStream(context.Background())
	err := srv.WatchDir(&pb.WatchDirRequest{Path: "/data/absent"}, stream)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("error = %v, want NotFound", err)
	}
}

func TestWatchDirRejectsFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{DefaultWorkDir: "/data", WorkspaceRoot: root, DataMount: "/data"})

	stream := newWatchStream(context.Background())
	err := srv.WatchDir(&pb.WatchDirRequest{Path: "/data/f.txt"}, stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
}

func TestWatchDirEmptyPathRejected(t *testing.T) {
	srv := New(Options{DefaultWorkDir: "/data", WorkspaceRoot: t.TempDir(), DataMount: "/data"})
	stream := newWatchStream(context.Background())
	err := srv.WatchDir(&pb.WatchDirRequest{Path: "  "}, stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
}

func TestWatchDirStreamsShareOneWatcherInstance(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{DefaultWorkDir: "/data", WorkspaceRoot: root, DataMount: "/data"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rootStream := newWatchStream(ctx)
	subStream := newWatchStream(ctx)
	go func() { _ = srv.WatchDir(&pb.WatchDirRequest{Path: "/data"}, rootStream) }()
	go func() { _ = srv.WatchDir(&pb.WatchDirRequest{Path: "/data/sub"}, subStream) }()

	time.Sleep(150 * time.Millisecond)
	// Every inotify instance is a scarce kernel resource (max_user_instances
	// defaults to 128); concurrent streams must multiplex one watcher.
	if got := srv.activeWatchInstances(); got != 1 {
		t.Fatalf("watcher instances = %d, want 1", got)
	}

	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	batch := waitBatch(t, subStream)
	found := false
	for _, p := range batch {
		if p == "/data/sub/a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sub batch = %v", batch)
	}
}

func TestWatchDirSameDirSecondStreamSurvivesFirstCancel(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{DefaultWorkDir: "/data", WorkspaceRoot: root, DataMount: "/data"})

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	streamA := newWatchStream(ctxA)
	streamB := newWatchStream(ctxB)
	doneA := make(chan error, 1)
	go func() { doneA <- srv.WatchDir(&pb.WatchDirRequest{Path: "/data"}, streamA) }()
	go func() { _ = srv.WatchDir(&pb.WatchDirRequest{Path: "/data"}, streamB) }()

	time.Sleep(150 * time.Millisecond)
	cancelA()
	select {
	case <-doneA:
	case <-time.After(3 * time.Second):
		t.Fatal("stream A did not end after cancel")
	}

	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	batch := waitBatch(t, streamB)
	found := false
	for _, p := range batch {
		if p == "/data/b.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("surviving stream batch = %v", batch)
	}
}
