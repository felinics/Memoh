package claudecode

import (
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/workspace/userenv"
)

func TestCLICommandQuotesTheLauncher(t *testing.T) {
	command := cliCommand("/data/.memoh/deps/claude-code/versions/2.1.250/bin/it's claude", []string{"--output-format", "stream-json"})
	want := `'/data/.memoh/deps/claude-code/versions/2.1.250/bin/it'\''s claude' --output-format stream-json`
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
	if cliCommand(" /x/claude ", nil) != "'/x/claude'" {
		t.Fatalf("bare launcher = %q", cliCommand(" /x/claude ", nil))
	}
	got := cliEnv(claudecfg.Config{}, "agent")
	// The CLI runs in the workspace user environment, the terminal's.
	for _, want := range userenv.Env() {
		if !slices.Contains(got, want) {
			t.Fatalf("cliEnv = %q, missing %q", got, want)
		}
	}
	// Each Agent keeps its own login, sessions and settings.
	if !slices.Contains(got, "CLAUDE_CONFIG_DIR=/data/.claude-code/agents/agent") {
		t.Fatalf("cliEnv config dir = %q", got)
	}
}
