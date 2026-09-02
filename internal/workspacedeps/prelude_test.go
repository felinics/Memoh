package workspacedeps

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

// runShellcheck feeds script to `shellcheck -s sh -` and fails the test on
// any finding. It skips when shellcheck is not installed.
func runShellcheck(t *testing.T, label, script string) {
	t.Helper()
	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Skip("shellcheck not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "shellcheck", "-s", "sh", "-")
	cmd.Stdin = strings.NewReader(script)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil || out.Len() != 0 {
		t.Errorf("shellcheck on %s: %v\n%s", label, err, out.String())
	}
}

func TestPreludeShellcheck(t *testing.T) {
	runShellcheck(t, "prelude", WrapScript("true\n"))
}

func TestDiscoveryScriptShellcheck(t *testing.T) {
	deps := []catalog.Dependency{
		{ID: "tool-a", Provides: []string{"tool-a", "tool-a-helper"}},
		{ID: "tool-b", Provides: []string{"tool-b"}, Scripts: catalog.Scripts{Version: "version.sh"}},
	}
	runShellcheck(t, "discovery script", buildDiscoveryScript("/data", deps))
}

func TestShimScriptShellcheck(t *testing.T) {
	runShellcheck(t, "agent shim", ShimScript("/data/.memoh/deps/codex/current/bin/codex", true))
	runShellcheck(t, "plain shim", ShimScript("/data/it's here/bin/tool", false))
}

func TestPreludeLinesMatchesWrappedScript(t *testing.T) {
	const marker = "__BODY_MARKER__"
	lines := strings.Split(WrapScript(marker), "\n")
	index := -1
	for i, line := range lines {
		if line == marker {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatalf("body marker not found in wrapped script:\n%s", strings.Join(lines, "\n"))
	}
	// index is zero-based, so the body's first line is stdin line index+1 and
	// PreludeLines must equal index.
	if index != PreludeLines() {
		t.Errorf("PreludeLines() = %d, body starts at zero-based line %d", PreludeLines(), index)
	}
	if !strings.HasSuffix(WrapScript(marker), "}\nmemoh_dep_main < /dev/null\n") {
		t.Errorf("wrapped script does not end with the function call")
	}
}

func TestWrapScriptAddsMissingTrailingNewline(t *testing.T) {
	with := WrapScript("true\n")
	without := WrapScript("true")
	if with != without {
		t.Errorf("WrapScript must normalise the trailing newline:\n%q\nvs\n%q", with, without)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":    "'plain'",
		"it's":     `'it'\''s'`,
		"a b":      "'a b'",
		"":         "''",
		"$HOME/x*": "'$HOME/x*'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
