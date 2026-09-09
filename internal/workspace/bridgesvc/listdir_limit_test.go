package bridgesvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// The max_entries bound is a resource boundary: the server must refuse an
// oversized tree during traversal instead of collecting it into memory first.
func TestListDirMaxEntriesBoundsFlatListing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	srv := New(Options{DefaultWorkDir: root, AllowHostAbsolute: true})
	for index := range 3 {
		writeListDirLimitFile(t, filepath.Join(root, fmt.Sprintf("file-%d.jsonl", index)))
	}

	// Exactly at the cap succeeds.
	resp, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: root, MaxEntries: 3})
	if err != nil {
		t.Fatalf("ListDir at the cap: %v", err)
	}
	if len(resp.GetEntries()) != 3 {
		t.Fatalf("entries = %d, want 3", len(resp.GetEntries()))
	}
	// One entry over the cap is ResourceExhausted, not a truncated listing.
	if _, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: root, MaxEntries: 2}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("ListDir over the cap = %v, want ResourceExhausted", err)
	}
	// Zero keeps the unbounded legacy behavior.
	if resp, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: root}); err != nil || len(resp.GetEntries()) != 3 {
		t.Fatalf("unbounded ListDir = (%d entries, %v)", len(resp.GetEntries()), err)
	}
}

func TestListDirMaxEntriesBoundsRecursiveTraversal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	srv := New(Options{DefaultWorkDir: root, AllowHostAbsolute: true})
	if err := os.MkdirAll(filepath.Join(root, "nested", "deeper"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeListDirLimitFile(t, filepath.Join(root, "top.jsonl"))
	writeListDirLimitFile(t, filepath.Join(root, "nested", "middle.jsonl"))
	writeListDirLimitFile(t, filepath.Join(root, "nested", "deeper", "leaf.jsonl"))
	// Recursive listings count directories as entries too: nested, deeper, and
	// the three files make five.
	resp, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: root, Recursive: true, MaxEntries: 5})
	if err != nil {
		t.Fatalf("recursive ListDir at the cap: %v", err)
	}
	if len(resp.GetEntries()) != 5 {
		t.Fatalf("recursive entries = %d, want 5", len(resp.GetEntries()))
	}
	if _, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: root, Recursive: true, MaxEntries: 4}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("recursive ListDir over the cap = %v, want ResourceExhausted", err)
	}
}

func writeListDirLimitFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
