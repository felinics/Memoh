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
	"github.com/felinics/memoh/internal/agent/event"
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

func (f *fakeLauncherResolver) ResolveLauncher(_ context.Context, botID, depID, requiredVersion string) (external.Launcher, error) {
	f.mu.Lock()
	f.requests = append(f.requests, botID+"/"+depID+"@"+requiredVersion)
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

func (r resolveOnly) ResolveLauncher(context.Context, string, string, string) (external.Launcher, error) {
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
	depID, version := (&Driver{}).RequiredDependency()
	if depID != "claude-code" || version != PinnedCLIVersion {
		t.Fatalf("RequiredDependency = (%q, %q), want (claude-code, %s)", depID, version, PinnedCLIVersion)
	}
}

func TestResolveLauncherWithoutResolverUsesToolkitPath(t *testing.T) {
	launcher, err := launcherDriver(nil).resolveLauncher(t.Context(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if launcher.Path != defaultLauncherPath || launcher.Mismatch {
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
		Version: PinnedCLIVersion,
		Source:  external.LauncherSourceManaged,
	}}
	launcher, err := launcherDriver(resolver).resolveLauncher(t.Context(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolver.requests) != 1 || resolver.requests[0] != "bot-1/claude-code@"+PinnedCLIVersion {
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
		DependencyID: "claude-code", RequiredVersion: PinnedCLIVersion, TaskID: "task-42",
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
	want := map[string]string{"dep_id": "claude-code", "required_version": PinnedCLIVersion, "install_task_id": "task-42"}
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
	if !errors.As(err, &feedback) || feedback.Args["required_version"] != PinnedCLIVersion || feedback.Args["install_task_id"] != "" {
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

func TestVersionMismatchNoticeOncePerThread(t *testing.T) {
	d := launcherDriver(nil)
	mismatch := external.Launcher{Path: "/opt/memoh/toolkit/bin/claude", Version: "2.1.100", Source: external.LauncherSourceToolkit, Mismatch: true}
	sink := &recordingSink{}
	input := external.PromptInput{BotID: "bot-1", ThreadID: "thread-1", Sink: sink}

	d.noticeVersionMismatch(input, mismatch) // first turn: notice
	d.noticeVersionMismatch(input, mismatch) // second turn: silent
	events := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want exactly one notice: %+v", len(events), events)
	}
	notice := events[0]
	if notice.Type != event.RuntimeNotice || notice.Code != agentfeedback.CodeAgentDependencyVersionMismatch {
		t.Fatalf("notice = %+v", notice)
	}
	if !strings.Contains(notice.Delta, "2.1.100") || !strings.Contains(notice.Delta, PinnedCLIVersion) {
		t.Fatalf("notice text = %q", notice.Delta)
	}
	for key, want := range map[string]any{"dep_id": "claude-code", "required_version": PinnedCLIVersion, "installed_version": "2.1.100"} {
		if notice.Metadata[key] != want {
			t.Fatalf("metadata[%s] = %v, want %v (all: %v)", key, notice.Metadata[key], want, notice.Metadata)
		}
	}

	// Another thread gets its own notice.
	other := &recordingSink{}
	d.noticeVersionMismatch(external.PromptInput{BotID: "bot-1", ThreadID: "thread-2", Sink: other}, mismatch)
	if len(other.snapshot()) != 1 {
		t.Fatalf("second thread events = %d, want 1", len(other.snapshot()))
	}

	// A different installed version on the same thread is news again.
	drifted := mismatch
	drifted.Version = "2.1.300"
	d.noticeVersionMismatch(input, drifted)
	if len(sink.snapshot()) != 2 {
		t.Fatalf("events after drift = %d, want 2", len(sink.snapshot()))
	}

	// An aligned launcher clears the slot; a later mismatch is announced again.
	d.noticeVersionMismatch(input, external.Launcher{Path: "/x", Version: PinnedCLIVersion})
	if len(sink.snapshot()) != 2 {
		t.Fatalf("aligned launcher must not notice: %d events", len(sink.snapshot()))
	}
	d.noticeVersionMismatch(input, mismatch)
	if len(sink.snapshot()) != 3 {
		t.Fatalf("events after re-drift = %d, want 3", len(sink.snapshot()))
	}
}

func TestVersionMismatchNoticeIsConcurrencySafe(t *testing.T) {
	d := launcherDriver(nil)
	mismatch := external.Launcher{Path: "/x", Version: "2.1.100", Mismatch: true}
	sink := &recordingSink{}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.noticeVersionMismatch(external.PromptInput{ThreadID: "thread-1", Sink: sink}, mismatch)
		}()
	}
	wg.Wait()
	if len(sink.snapshot()) != 1 {
		t.Fatalf("events = %d, want exactly one notice", len(sink.snapshot()))
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
