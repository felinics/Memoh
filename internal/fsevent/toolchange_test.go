package fsevent

import "testing"

func TestToolChangePathScopedTools(t *testing.T) {
	for _, name := range []string{"write", "edit"} {
		paths, mutating := ToolChange(name, map[string]any{"path": "/data/a.txt"})
		if !mutating {
			t.Fatalf("%s: mutating = false, want true", name)
		}
		if len(paths) != 1 || paths[0] != "/data/a.txt" {
			t.Fatalf("%s: paths = %v, want [/data/a.txt]", name, paths)
		}
	}
}

func TestToolChangeRelativePathFallsBackToWildcard(t *testing.T) {
	paths, mutating := ToolChange("write", map[string]any{"path": "notes/a.txt"})
	if !mutating || paths != nil {
		t.Fatalf("paths, mutating = %v, %v; want nil, true", paths, mutating)
	}
}

func TestToolChangeWildcardTools(t *testing.T) {
	for _, name := range []string{"exec", "apply_patch"} {
		paths, mutating := ToolChange(name, map[string]any{"command": "make"})
		if !mutating || paths != nil {
			t.Fatalf("%s: paths, mutating = %v, %v; want nil, true", name, paths, mutating)
		}
	}
}

func TestToolChangeIgnoresNonMutatingTools(t *testing.T) {
	if _, mutating := ToolChange("read", map[string]any{"path": "/data/a.txt"}); mutating {
		t.Fatal("read reported as mutating")
	}
	if _, mutating := ToolChange("send_message", nil); mutating {
		t.Fatal("send_message reported as mutating")
	}
}

func TestToolChangeMalformedInputFallsBackToWildcard(t *testing.T) {
	paths, mutating := ToolChange("write", "not a map")
	if !mutating || paths != nil {
		t.Fatalf("paths, mutating = %v, %v; want nil, true", paths, mutating)
	}
}
