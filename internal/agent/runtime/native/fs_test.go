package native

import (
	"slices"
	"testing"
)

func TestSystemFileLoadOrderKeepsVolatileMemoryLast(t *testing.T) {
	t.Parallel()

	want := []string{"AGENTS.md", "PROFILES.md", "MEMORY.md"}
	if got := systemFileLoadOrder(); !slices.Equal(got, want) {
		t.Fatalf("system file order = %#v, want %#v", got, want)
	}
}
