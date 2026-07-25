package runtime

import "testing"

func TestDataMountPath(t *testing.T) {
	t.Parallel()

	if got := DataMountPath(""); got != "/data" {
		t.Fatalf("unexpected empty mount path: %q", got)
	}
	if got := DataMountPath("screenshot.png"); got != "/data/screenshot.png" {
		t.Fatalf("unexpected joined path: %q", got)
	}
	if got := DataMountPath("/already/abs"); got != "/data/already/abs" {
		t.Fatalf("unexpected trimmed join: %q", got)
	}
}

func TestDataSubpath(t *testing.T) {
	t.Parallel()

	if got, ok := DataSubpath("/data/work/demo.txt"); !ok || got != "work/demo.txt" {
		t.Fatalf("expected work/demo.txt, got %q ok=%v", got, ok)
	}
	if _, ok := DataSubpath("/tmp/work/demo.txt"); ok {
		t.Fatal("expected non-data path to be rejected")
	}
	if _, ok := DataSubpath("/data"); ok {
		t.Fatal("expected bare data mount to be rejected")
	}
}

func TestIsDataPath(t *testing.T) {
	t.Parallel()

	if !IsDataPath("/data/media/file.png") {
		t.Fatal("expected true for /data path")
	}
	if IsDataPath("/tmp/file.png") {
		t.Fatal("expected false for /tmp path")
	}
}
