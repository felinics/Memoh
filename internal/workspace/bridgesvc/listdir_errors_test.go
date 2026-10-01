//go:build !windows

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

func TestListDirClassifiesFilesystemErrors(t *testing.T) {
	for _, recursive := range []bool{false, true} {
		name := "flat"
		if recursive {
			name = "recursive"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			srv := New(Options{DefaultWorkDir: root, AllowHostAbsolute: true})
			t.Run("not found", func(t *testing.T) {
				_, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: filepath.Join(root, "missing"), Recursive: recursive})
				if status.Code(err) != codes.NotFound {
					t.Fatalf("ListDir = %v, want NotFound", err)
				}
			})
			t.Run("file path", func(t *testing.T) {
				path := filepath.Join(root, "file.txt")
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: path, Recursive: recursive})
				if status.Code(err) != codes.NotFound {
					t.Fatalf("ListDir = %v, want NotFound", err)
				}
			})
			t.Run("permission denied", func(t *testing.T) {
				if os.Geteuid() == 0 {
					t.Skip("root can read directories with mode 000")
				}
				dir := filepath.Join(root, "denied")
				if err := os.Mkdir(dir, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(dir, 0o600); err != nil {
						t.Error(err)
					}
				})
				_, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: dir, Recursive: recursive})
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("ListDir = %v, want PermissionDenied", err)
				}
			})
			t.Run("other error", func(t *testing.T) {
				// A symlink loop produces ELOOP rather than ENOENT or EACCES.
				path := filepath.Join(root, "loop")
				if err := os.Symlink("loop", path); err != nil {
					t.Fatal(err)
				}
				_, err := srv.ListDir(context.Background(), &pb.ListDirRequest{Path: path, Recursive: recursive})
				if status.Code(err) != codes.Internal {
					t.Fatalf("ListDir = %v, want Internal", err)
				}
			})
		})
	}
}
