package claudecode

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/runtime/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
)

// fakeLauncherResolver returns one canned launcher (or error) and records
// what it was asked for and told.
type fakeLauncherResolver struct {
	launcher external.Launcher
	err      error

	mu       sync.Mutex
	requests []string
	observed []string
}

func (f *fakeLauncherResolver) ResolveLauncher(_ context.Context, botID, depID string) (external.Launcher, error) {
	f.mu.Lock()
	f.requests = append(f.requests, botID+"/"+depID)
	f.mu.Unlock()
	if f.err != nil {
		return external.Launcher{}, f.err
	}
	return f.launcher, nil
}

func (f *fakeLauncherResolver) ObserveLauncherVersion(_ context.Context, botID, depID, version string) {
	f.mu.Lock()
	f.observed = append(f.observed, botID+"/"+depID+"="+version)
	f.mu.Unlock()
}

// resolveOnly implements just the resolver port, to prove the version
// observer is optional.
type resolveOnly struct{ launcher external.Launcher }

func (r resolveOnly) ResolveLauncher(context.Context, string, string) (external.Launcher, error) {
	return r.launcher, nil
}

func launcherDriver(resolver external.LauncherResolver) *Driver {
	d := &Driver{logger: slog.Default()}
	if resolver != nil {
		d.SetLauncherResolver(resolver)
	}
	return d
}

func TestDriverDeclaresClaudeCodeDependency(t *testing.T) {
	if depID := (&Driver{}).RequiredDependency(); depID != "claude-code" {
		t.Fatalf("RequiredDependency = %q, want claude-code", depID)
	}
}

func TestContainerPathPrefersManagedOverlays(t *testing.T) {
	if !strings.HasPrefix(containerPath, "/data/.memoh/deps/bin:/opt/memoh/toolkit/bin:") {
		t.Fatalf("containerPath = %q, want the managed shim directory ahead of the toolkit", containerPath)
	}
	if got := cliEnv(claudecfg.Config{}); got[0] != "PATH="+containerPath {
		t.Fatalf("cliEnv PATH = %q", got[0])
	}
}

func TestResolveLauncherWithoutResolverUsesToolkitPath(t *testing.T) {
	launcher, err := launcherDriver(nil).resolveLauncher(t.Context(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if launcher.Path != defaultLauncherPath || launcher.Source != external.LauncherSourceToolkit {
		t.Fatalf("launcher = %+v, want toolkit default", launcher)
	}
	command := cliCommand(launcher.Path, cliArgs(claudecfg.Config{}, external.PromptInput{}, "", ""))
	if !strings.HasPrefix(command, "'/opt/memoh/toolkit/bin/claude' --output-format stream-json") {
		t.Fatalf("command = %q", command)
	}
}

func TestResolveLauncherManagedPathEntersCommandQuoted(t *testing.T) {
	resolver := &fakeLauncherResolver{launcher: external.Launcher{
		Path:    "/data/.memoh/deps/claude-code/versions/2.1.250/bin/it's claude",
		Version: "2.1.250",
		Source:  external.LauncherSourceManaged,
	}}
	launcher, err := launcherDriver(resolver).resolveLauncher(t.Context(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolver.requests) != 1 || resolver.requests[0] != "bot-1/claude-code" {
		t.Fatalf("resolver requests = %v", resolver.requests)
	}
	command := cliCommand(launcher.Path, []string{"--output-format", "stream-json"})
	want := `'/data/.memoh/deps/claude-code/versions/2.1.250/bin/it'\''s claude' --output-format stream-json`
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
	if cliCommand(" /x/claude ", nil) != "'/x/claude'" {
		t.Fatalf("bare launcher = %q", cliCommand(" /x/claude ", nil))
	}
}

func TestResolveLauncherMissingYieldsDependencyFeedback(t *testing.T) {
	resolver := &fakeLauncherResolver{err: &external.DependencyMissingError{
		DependencyID: "claude-code", TaskID: "task-42",
	}}
	_, err := launcherDriver(resolver).resolveLauncher(t.Context(), "bot-1")
	var feedback *agentfeedback.Error
	if !errors.As(err, &feedback) {
		t.Fatalf("err = %T %v, want *agentfeedback.Error", err, err)
	}
	if feedback.Code != agentfeedback.CodeAgentDependencyMissing || feedback.HTTPStatus != http.StatusConflict ||
		feedback.I18nKey != "chat.externalAgent.dependencyMissing" || strings.TrimSpace(feedback.Message) == "" {
		t.Fatalf("feedback = %+v", *feedback)
	}
	want := map[string]string{"dep_id": "claude-code", "install_task_id": "task-42"}
	if len(feedback.Args) != len(want) {
		t.Fatalf("args = %v, want %v", feedback.Args, want)
	}
	for key, value := range want {
		if feedback.Args[key] != value {
			t.Fatalf("args[%s] = %q, want %q (all: %v)", key, feedback.Args[key], value, feedback.Args)
		}
	}
	// A wrapped sentinel still resolves to the feedback shape.
	resolver.err = errors.Join(errors.New("probe"), &external.DependencyMissingError{DependencyID: "claude-code"})
	_, err = launcherDriver(resolver).resolveLauncher(t.Context(), "bot-1")
	if !errors.As(err, &feedback) || feedback.Args["dep_id"] != "claude-code" || feedback.Args["install_task_id"] != "" {
		t.Fatalf("wrapped missing: err = %v", err)
	}
}

func TestResolveLauncherOtherErrorsAreRuntimeUnavailable(t *testing.T) {
	_, err := launcherDriver(&fakeLauncherResolver{err: errors.New("workspace is not running")}).resolveLauncher(t.Context(), "bot-1")
	if apperror.CodeOf(err) != apperror.CodeExternalRuntimeUnavailable {
		t.Fatalf("err = %v, want %s", err, apperror.CodeExternalRuntimeUnavailable)
	}
	var feedback *agentfeedback.Error
	if errors.As(err, &feedback) {
		t.Fatalf("generic resolver error must not become dependency feedback: %v", err)
	}
	_, err = launcherDriver(&fakeLauncherResolver{launcher: external.Launcher{Path: "  "}}).resolveLauncher(t.Context(), "bot-1")
	if apperror.CodeOf(err) != apperror.CodeExternalRuntimeUnavailable {
		t.Fatalf("empty path err = %v, want %s", err, apperror.CodeExternalRuntimeUnavailable)
	}
}

func TestHandshakeVersionFeedsResolver(t *testing.T) {
	resolver := &fakeLauncherResolver{launcher: external.Launcher{Path: "/x"}}
	d := launcherDriver(resolver)
	sink := &recordingSink{}
	r := newTestRunner(sink)
	r.onCLIVersion = d.versionObserver("bot-1")
	if r.onCLIVersion == nil {
		t.Fatal("resolver implementing VersionObserver must produce a callback")
	}
	feed(t, r, `{"type":"system","subtype":"init","session_id":"s","claude_code_version":"2.1.100"}`)
	feed(t, r, `{"type":"system","subtype":"init","session_id":"s"}`) // no version: nothing to observe
	r.close()
	if len(resolver.observed) != 1 || resolver.observed[0] != "bot-1/claude-code=2.1.100" {
		t.Fatalf("observed = %v", resolver.observed)
	}

	if launcherDriver(resolveOnly{}).versionObserver("bot-1") != nil {
		t.Fatal("resolver without VersionObserver must yield no callback")
	}
	if launcherDriver(nil).versionObserver("bot-1") != nil {
		t.Fatal("nil resolver must yield no callback")
	}
}
