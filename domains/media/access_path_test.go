package media

import "testing"

func TestAccessPath(t *testing.T) {
	t.Parallel()

	if got := AccessPath("/data", "aa/demo.png"); got != "/data/media/aa/demo.png" {
		t.Fatalf("unexpected media access path: %q", got)
	}
	if got := AccessPath("/data", ""); got != "/data/media" {
		t.Fatalf("unexpected media root path: %q", got)
	}
}

func TestStorageKeyFromAccessPath(t *testing.T) {
	t.Parallel()

	if got := StorageKeyFromAccessPath("/data", "/data/media/aa/demo.png"); got != "aa/demo.png" {
		t.Fatalf("unexpected storage key: %q", got)
	}
	if got := StorageKeyFromAccessPath("/data", "/tmp/demo.png"); got != "" {
		t.Fatalf("expected empty storage key for non-media path, got %q", got)
	}
	if got := StorageKeyFromAccessPath("/data", "/data/media/"); got != "" {
		t.Fatalf("expected empty key for media root with slash, got %q", got)
	}
}
