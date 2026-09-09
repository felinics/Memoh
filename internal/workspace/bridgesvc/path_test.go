package bridgesvc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestPathResolverMapsDataMountToWorkspaceRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srv := New(Options{
		DefaultWorkDir:    root,
		WorkspaceRoot:     root,
		DataMount:         "/data",
		AllowHostAbsolute: true,
	})

	if _, err := srv.WriteFile(context.Background(), &pb.WriteFileRequest{
		Path:    "/data/notes/demo.txt",
		Content: []byte("demo"),
	}); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "notes", "demo.txt")) //nolint:gosec // test path is under t.TempDir
	if err != nil {
		t.Fatalf("read mapped file failed: %v", err)
	}
	if string(got) != "demo" {
		t.Fatalf("mapped file = %q, want demo", string(got))
	}
}

func TestPathResolverAllowsHostAbsolutePath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	srv := New(Options{
		DefaultWorkDir:    root,
		WorkspaceRoot:     root,
		DataMount:         "/data",
		AllowHostAbsolute: true,
	})

	if _, err := srv.WriteFile(context.Background(), &pb.WriteFileRequest{
		Path:    outside,
		Content: []byte("outside"),
	}); err != nil {
		t.Fatalf("WriteFile absolute path failed: %v", err)
	}
	got, err := os.ReadFile(outside) //nolint:gosec // test path is under t.TempDir
	if err != nil {
		t.Fatalf("read absolute file failed: %v", err)
	}
	if string(got) != "outside" {
		t.Fatalf("absolute file = %q, want outside", string(got))
	}
}

func TestWriteFileHonorsCancelledContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srv := New(Options{
		DefaultWorkDir: root,
		WorkspaceRoot:  root,
		DataMount:      "/data",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := srv.WriteFile(ctx, &pb.WriteFileRequest{
		Path:    "/data/cancelled.txt",
		Content: []byte("should not land"),
	})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("WriteFile error code = %v, want canceled: %v", status.Code(err), err)
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancelled write left file behind: %v", err)
	}
}
