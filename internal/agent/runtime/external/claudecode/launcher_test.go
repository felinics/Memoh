package claudecode

import (
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/claudecode/claudecfg"
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
	if got := cliEnv(claudecfg.Config{}); got[0] != "PATH="+agentproc.ContainerPath {
		t.Fatalf("cliEnv PATH = %q", got[0])
	}
}
