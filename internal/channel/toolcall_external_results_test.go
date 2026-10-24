package channel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type externalGoldenEntry struct {
	Tool   string         `json:"tool"`
	Input  any            `json:"input"`
	Result map[string]any `json:"result"`
	Status string         `json:"status"`
}

// externalExecResults returns the exec results with stdout that each external
// runtime's adapter produced (internal/agent/toolresult/testdata/golden).
func externalExecResults(t *testing.T) map[string][]externalGoldenEntry {
	t.Helper()
	dir := filepath.Join("..", "agent", "toolresult", "testdata", "golden")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden files in %s: %v", dir, err)
	}
	out := map[string][]externalGoldenEntry{}
	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // repository fixture
		if err != nil {
			t.Fatal(err)
		}
		var entries []externalGoldenEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		runtime := strings.TrimSuffix(filepath.Base(file), ".json")
		for _, entry := range entries {
			if entry.Tool == "exec" && entry.Status != "failed" {
				if stdout, _ := entry.Result["stdout"].(string); strings.TrimSpace(stdout) != "" {
					out[runtime] = append(out[runtime], entry)
				}
			}
		}
	}
	return out
}

func TestExternalRuntimeExecResultsReachTheIMCard(t *testing.T) {
	t.Parallel()

	byRuntime := externalExecResults(t)
	for _, runtime := range []string{"codex", "claude-code"} {
		if len(byRuntime[runtime]) == 0 {
			t.Fatalf("%s has no exec sample with stdout", runtime)
		}
	}
	for runtime, entries := range byRuntime {
		for _, entry := range entries {
			stdout, _ := entry.Result["stdout"].(string)
			first := firstLine(stdout)
			tc := &StreamToolCall{Name: "exec", Input: entry.Input, Result: entry.Result}

			card := BuildToolCallEnd(tc)
			if !hasTextBlock(card.Body, "stdout: "+truncLine(first)) {
				t.Errorf("%s: exec card has no stdout line for %q: %#v", runtime, first, card.Body)
			}
			if summary := SummarizeToolResult("exec", entry.Result); !strings.Contains(summary, "stdout: "+first) {
				t.Errorf("%s: result summary %q has no stdout %q", runtime, summary, first)
			}
		}
	}
}
