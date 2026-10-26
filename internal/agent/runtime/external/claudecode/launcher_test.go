package claudecode

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/claudecode/claudecfg"
)

// observingResolver records the versions it is told about.
type observingResolver struct {
	mu       sync.Mutex
	observed []string
}

func (*observingResolver) ResolveLauncher(context.Context, string, string) (external.Launcher, error) {
	return external.Launcher{Path: "/x"}, nil
}

func (f *observingResolver) ObserveLauncherVersion(_ context.Context, botID, depID, version string) {
	f.mu.Lock()
	f.observed = append(f.observed, botID+"/"+depID+"="+version)
	f.mu.Unlock()
}

func TestDriverDeclaresClaudeCodeDependency(t *testing.T) {
	if depID := (&Driver{}).RequiredDependency(); depID != "claude-code" {
		t.Fatalf("RequiredDependency = %q, want claude-code", depID)
	}
}

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

func TestHandshakeVersionFeedsResolver(t *testing.T) {
	resolver := &observingResolver{}
	d := &Driver{logger: slog.Default()}
	d.SetLauncherResolver(resolver)
	sink := &recordingSink{}
	r := newTestRunner(sink)
	r.onCLIVersion = d.versionObserver("bot-1")
	feed(t, r, `{"type":"system","subtype":"init","session_id":"s","claude_code_version":"2.1.100"}`)
	feed(t, r, `{"type":"system","subtype":"init","session_id":"s"}`) // no version: nothing to observe
	r.close()
	if len(resolver.observed) != 1 || resolver.observed[0] != "bot-1/claude-code=2.1.100" {
		t.Fatalf("observed = %v", resolver.observed)
	}
}
