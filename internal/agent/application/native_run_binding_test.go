package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every native driver arms its run through bindNativeRun: no driver builds a
// step committer or binds continuation hooks on its own, so the hooks a run
// gets are decided in one place.
func TestNativeDriversBindThroughOnePlace(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "step_commit.go" {
			continue
		}
		source, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"newAgentStepCommitter(", ".bindContinuation("} {
			if strings.Contains(string(source), call) {
				t.Errorf("%s calls %s directly; native runs are armed by bindNativeRun", file, call)
			}
		}
	}
}
