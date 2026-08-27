package runtimeauth

import "testing"

func TestRootForRejectsUntrustedRuntimeIDs(t *testing.T) {
	for _, value := range []string{"", "runtime-1", "rt_../secret", "rt_not-a-uuid", "/tmp/rt_00000000-0000-0000-0000-000000000000"} {
		if _, err := RootFor(value); err == nil {
			t.Fatalf("RootFor(%q) succeeded", value)
		}
	}
}

func TestRootForAndChild(t *testing.T) {
	root, err := RootFor("rt_00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if root != "/tmp/memoh-auth/rt_00000000-0000-0000-0000-000000000001" {
		t.Fatalf("root = %q", root)
	}
	child, err := Child(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if child != root+"/codex" {
		t.Fatalf("child = %q", child)
	}
	if _, err := Child(root, "../escape"); err == nil {
		t.Fatal("unsafe child accepted")
	}
}
