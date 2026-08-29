package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func TestClientWatchDirReceivesBatches(t *testing.T) {
	root := t.TempDir()
	client := newTestClient(t, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir: "/data",
		WorkspaceRoot:  root,
		DataMount:      "/data",
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	batches := make(chan []string, 8)
	done := make(chan error, 1)
	go func() {
		done <- client.WatchDir(ctx, "/data", func(paths []string) { batches <- paths })
	}()

	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case batch := <-batches:
		found := false
		for _, p := range batch {
			if p == "/data/a.txt" {
				found = true
			}
		}
		if !found {
			t.Fatalf("batch = %v, want to contain /data/a.txt", batch)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for batch")
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

type unimplementedWatchServer struct {
	pb.UnimplementedContainerServiceServer
}

func TestClientWatchDirUnsupportedBridge(t *testing.T) {
	client := newTestClient(t, &unimplementedWatchServer{})
	err := client.WatchDir(context.Background(), "/data", func([]string) {})
	if !errors.Is(err, ErrWatchUnsupported) {
		t.Fatalf("error = %v, want ErrWatchUnsupported", err)
	}
}
