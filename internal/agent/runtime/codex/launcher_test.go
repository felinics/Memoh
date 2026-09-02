package codex

import (
	"context"
	"errors"
	"net/http"
	"testing"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
)

// fakeLauncherResolver scripts one ResolveLauncher answer and records the
// handshake versions fed back through VersionObserver.
type fakeLauncherResolver struct {
	launcher external.Launcher
	err      error
	calls    []resolveCall
	observed []resolveCall
}

type resolveCall struct{ botID, depID, version string }

func (f *fakeLauncherResolver) ResolveLauncher(_ context.Context, botID, depID, requiredVersion string) (external.Launcher, error) {
	f.calls = append(f.calls, resolveCall{botID, depID, requiredVersion})
	if f.err != nil {
		return external.Launcher{}, f.err
	}
	return f.launcher, nil
}

func (f *fakeLauncherResolver) ObserveLauncherVersion(_ context.Context, botID, depID, version string) {
	f.observed = append(f.observed, resolveCall{botID, depID, version})
}

// resolveOnly is a resolver without a version cache.
type resolveOnly struct{ inner *fakeLauncherResolver }

func (r resolveOnly) ResolveLauncher(ctx context.Context, botID, depID, requiredVersion string) (external.Launcher, error) {
	return r.inner.ResolveLauncher(ctx, botID, depID, requiredVersion)
}

type captureSink struct{ events []event.StreamEvent }

func (c *captureSink) EmitStreamEvent(ev event.StreamEvent) { c.events = append(c.events, ev) }

func newTestAppServer(launcher external.Launcher) *appServer {
	return &appServer{
		launcher:        launcher,
		toollessThreads: map[string]bool{},
		mismatchNoticed: map[string]bool{},
	}
}

func TestRequiredDependencyIsCodexAtProtocolPin(t *testing.T) {
	depID, version := (&Driver{}).RequiredDependency()
	if depID != "codex" || version != protocol.PinnedCodexVersion {
		t.Fatalf("RequiredDependency() = (%q, %q), want (codex, %s)", depID, version, protocol.PinnedCodexVersion)
	}
}

func TestResolveLauncherWithoutResolverUsesToolkitPath(t *testing.T) {
	launcher, err := (&Driver{}).resolveLauncher(context.Background(), "bot")
	if err != nil {
		t.Fatalf("resolveLauncher: %v", err)
	}
	if launcher.Path != defaultLauncherPath || launcher.Source != external.LauncherSourceToolkit || launcher.Mismatch {
		t.Fatalf("launcher = %+v, want toolkit default", launcher)
	}
	if got := appServerCommand(launcher.Path); got != "/opt/memoh/toolkit/bin/codex app-server" {
		t.Fatalf("appServerCommand = %q", got)
	}
}

func TestResolveLauncherManagedPathReachesCommandLine(t *testing.T) {
	resolver := &fakeLauncherResolver{launcher: external.Launcher{
		Path:    "/data/.memoh/deps/codex/versions/0.151.0/bin/codex",
		Version: protocol.PinnedCodexVersion,
		Source:  external.LauncherSourceManaged,
	}}
	d := &Driver{launchers: resolver}
	launcher, err := d.resolveLauncher(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("resolveLauncher: %v", err)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != (resolveCall{"bot-1", "codex", protocol.PinnedCodexVersion}) {
		t.Fatalf("resolver calls = %+v", resolver.calls)
	}
	if got, want := appServerCommand(launcher.Path), "/data/.memoh/deps/codex/versions/0.151.0/bin/codex app-server"; got != want {
		t.Fatalf("appServerCommand = %q, want %q", got, want)
	}
}

func TestAppServerCommandQuotesUnsafePaths(t *testing.T) {
	cases := map[string]string{
		"/data/my deps/codex":             "'/data/my deps/codex' app-server",
		"/data/it's/codex":                `'/data/it'\''s/codex' app-server`,
		"/data/$HOME/codex":               "'/data/$HOME/codex' app-server",
		"/opt/memoh/toolkit/bin/codex":    "/opt/memoh/toolkit/bin/codex app-server",
		"  /opt/memoh/toolkit/bin/codex ": "/opt/memoh/toolkit/bin/codex app-server",
		"":                                "/opt/memoh/toolkit/bin/codex app-server",
	}
	for path, want := range cases {
		if got := appServerCommand(path); got != want {
			t.Errorf("appServerCommand(%q) = %q, want %q", path, got, want)
		}
	}
	if got := escapeShellArg(""); got != "''" {
		t.Errorf("escapeShellArg(\"\") = %q", got)
	}
}

func TestResolveLauncherMissingDependencyIsStableFeedback(t *testing.T) {
	resolver := &fakeLauncherResolver{err: &external.DependencyMissingError{
		DependencyID:    "codex",
		RequiredVersion: protocol.PinnedCodexVersion,
		TaskID:          "task-42",
	}}
	d := &Driver{launchers: resolver}
	_, err := d.resolveLauncher(context.Background(), "bot-1")
	if err == nil {
		t.Fatal("resolveLauncher returned no error for a missing dependency")
	}
	var feedback *agentfeedback.Error
	if !errors.As(err, &feedback) {
		t.Fatalf("error %T is not agent feedback: %v", err, err)
	}
	if feedback.Code != agentfeedback.CodeAgentDependencyMissing {
		t.Fatalf("code = %q", feedback.Code)
	}
	if feedback.HTTPStatus != http.StatusConflict || feedback.I18nKey != "chat.externalAgent.dependencyMissing" || feedback.Reason != "dependency_missing" {
		t.Fatalf("feedback shape = %+v", feedback)
	}
	want := map[string]string{
		"dep_id":           "codex",
		"required_version": protocol.PinnedCodexVersion,
		"install_task_id":  "task-42",
	}
	for key, value := range want {
		if feedback.Args[key] != value {
			t.Errorf("args[%q] = %q, want %q", key, feedback.Args[key], value)
		}
	}
	if len(feedback.Args) != len(want) {
		t.Errorf("args = %v, want exactly %v", feedback.Args, want)
	}

	// The turn path shapes acquisition failures before returning them; the
	// feedback must come out intact, not buried under an apperror code.
	shaped := wrapServerError(err)
	var again *agentfeedback.Error
	if !errors.As(shaped, &again) || again.Code != agentfeedback.CodeAgentDependencyMissing {
		t.Fatalf("wrapServerError hid the feedback: %v", shaped)
	}
	if apperror.CodeOf(shaped) != "" {
		t.Fatalf("wrapServerError wrapped feedback in apperror %q", apperror.CodeOf(shaped))
	}
}

func TestResolveLauncherMissingWithoutInstallTaskKeepsArgs(t *testing.T) {
	d := &Driver{launchers: &fakeLauncherResolver{err: &external.DependencyMissingError{DependencyID: "codex"}}}
	_, err := d.resolveLauncher(context.Background(), "bot-1")
	var feedback *agentfeedback.Error
	if !errors.As(err, &feedback) {
		t.Fatalf("error %T is not agent feedback: %v", err, err)
	}
	if feedback.Args["required_version"] != protocol.PinnedCodexVersion {
		t.Fatalf("required_version = %q, want the pin", feedback.Args["required_version"])
	}
	if _, ok := feedback.Args["install_task_id"]; !ok {
		t.Fatal("install_task_id key missing when no task was started")
	}
}

func TestResolveLauncherOtherErrorsAreWrapped(t *testing.T) {
	cause := errors.New("bridge exploded")
	d := &Driver{launchers: &fakeLauncherResolver{err: cause}}
	_, err := d.resolveLauncher(context.Background(), "bot-1")
	if !errors.Is(err, cause) {
		t.Fatalf("cause lost: %v", err)
	}
	var feedback *agentfeedback.Error
	if errors.As(err, &feedback) {
		t.Fatal("a plain resolver failure must not become user feedback")
	}
	if code := apperror.CodeOf(wrapServerError(err)); code != apperror.CodeExternalRuntimeUnavailable {
		t.Fatalf("wrapServerError code = %q", code)
	}

	empty := &Driver{launchers: &fakeLauncherResolver{launcher: external.Launcher{Source: external.LauncherSourceManaged}}}
	if _, err := empty.resolveLauncher(context.Background(), "bot-1"); err == nil {
		t.Fatal("an empty launcher path was accepted")
	}
}

func TestLauncherMismatchNoticeOncePerThread(t *testing.T) {
	srv := newTestAppServer(external.Launcher{
		Path:     "/opt/memoh/toolkit/bin/codex",
		Version:  "0.147.0",
		Source:   external.LauncherSourceToolkit,
		Mismatch: true,
	})
	sink := &captureSink{}

	emitThreadNotices(srv, "thread-a", sink)
	if len(sink.events) != 1 {
		t.Fatalf("first turn emitted %d events, want 1: %+v", len(sink.events), sink.events)
	}
	notice := sink.events[0]
	if notice.Type != event.RuntimeNotice || notice.Code != agentfeedback.CodeAgentDependencyVersionMismatch {
		t.Fatalf("notice = %+v", notice)
	}
	if notice.Delta == "" {
		t.Fatal("notice has no human-readable text")
	}
	wantMeta := map[string]any{
		"dep_id":            "codex",
		"required_version":  protocol.PinnedCodexVersion,
		"installed_version": "0.147.0",
	}
	for key, value := range wantMeta {
		if notice.Metadata[key] != value {
			t.Errorf("metadata[%q] = %v, want %v", key, notice.Metadata[key], value)
		}
	}

	emitThreadNotices(srv, "thread-a", sink)
	if len(sink.events) != 1 {
		t.Fatalf("second turn re-emitted the notice: %+v", sink.events)
	}
	emitThreadNotices(srv, "thread-b", sink)
	if len(sink.events) != 2 {
		t.Fatalf("a different thread was not told: %d events", len(sink.events))
	}
}

func TestLauncherMismatchNoticeSkippedWhenAligned(t *testing.T) {
	srv := newTestAppServer(external.Launcher{Path: defaultLauncherPath, Version: protocol.PinnedCodexVersion})
	sink := &captureSink{}
	emitThreadNotices(srv, "thread-a", sink)
	if len(sink.events) != 0 {
		t.Fatalf("aligned launcher emitted %+v", sink.events)
	}

	// Toolless threads keep their every-turn notice, ahead of the mismatch.
	srv = newTestAppServer(external.Launcher{Path: defaultLauncherPath, Mismatch: true})
	srv.codexVersion = "0.150.0"
	srv.setThreadToolless("thread-a", true)
	emitThreadNotices(srv, "thread-a", sink)
	if len(sink.events) != 2 || sink.events[0].Code != "tools_unavailable" || sink.events[1].Code != agentfeedback.CodeAgentDependencyVersionMismatch {
		t.Fatalf("events = %+v", sink.events)
	}
	if sink.events[1].Metadata["installed_version"] != "0.150.0" {
		t.Fatalf("handshake version not used when discovery had none: %+v", sink.events[1].Metadata)
	}
}

func TestObserveLauncherVersionFeedsResolverCache(t *testing.T) {
	resolver := &fakeLauncherResolver{}
	d := &Driver{launchers: resolver}
	d.observeLauncherVersion(context.Background(), "bot-1", " 0.151.0 ")
	d.observeLauncherVersion(context.Background(), "bot-1", "")
	if len(resolver.observed) != 1 || resolver.observed[0] != (resolveCall{"bot-1", "codex", "0.151.0"}) {
		t.Fatalf("observed = %+v", resolver.observed)
	}

	// Resolvers without a cache and the nil resolver are both fine.
	(&Driver{launchers: resolveOnly{inner: resolver}}).observeLauncherVersion(context.Background(), "bot-1", "0.151.0")
	(&Driver{}).observeLauncherVersion(context.Background(), "bot-1", "0.151.0")
	if len(resolver.observed) != 1 {
		t.Fatalf("observed grew through a cacheless resolver: %+v", resolver.observed)
	}
}

func TestSetLauncherResolverInstallsResolver(t *testing.T) {
	resolver := &fakeLauncherResolver{launcher: external.Launcher{Path: "/x/codex", Source: external.LauncherSourceManaged}}
	d := &Driver{}
	d.SetLauncherResolver(resolver)
	launcher, err := d.resolveLauncher(context.Background(), "bot-1")
	if err != nil || launcher.Path != "/x/codex" {
		t.Fatalf("launcher = %+v, err = %v", launcher, err)
	}
}
