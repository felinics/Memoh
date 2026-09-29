package codex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/bridge"
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

func (f *fakeLauncherResolver) ResolveLauncher(_ context.Context, botID, depID string) (external.Launcher, error) {
	f.calls = append(f.calls, resolveCall{botID: botID, depID: depID})
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

func (r resolveOnly) ResolveLauncher(ctx context.Context, botID, depID string) (external.Launcher, error) {
	return r.inner.ResolveLauncher(ctx, botID, depID)
}

type captureSink struct{ events []event.StreamEvent }

func (c *captureSink) EmitStreamEvent(ev event.StreamEvent) { c.events = append(c.events, ev) }

func newTestAppServer(launcher external.Launcher) *appServer {
	return &appServer{
		launcher:        launcher,
		toollessThreads: map[string]bool{},
	}
}

func TestRequiredDependencyIsCodex(t *testing.T) {
	if depID := (&Driver{}).RequiredDependency(); depID != "codex" {
		t.Fatalf("RequiredDependency() = %q, want codex", depID)
	}
}

func TestContainerPathPrefersManagedOverlays(t *testing.T) {
	if !strings.HasPrefix(containerPath, "/data/.memoh/deps/bin:/opt/memoh/toolkit/bin:") {
		t.Fatalf("containerPath = %q, want the managed shim directory ahead of the toolkit", containerPath)
	}
}

func TestResolveLauncherWithoutResolverUsesToolkitPath(t *testing.T) {
	launcher, err := (&Driver{}).resolveLauncher(context.Background(), "bot")
	if err != nil {
		t.Fatalf("resolveLauncher: %v", err)
	}
	if launcher.Path != defaultLauncherPath || launcher.Source != external.LauncherSourceToolkit {
		t.Fatalf("launcher = %+v, want toolkit default", launcher)
	}
	if got := appServerCommand(launcher.Path); got != "/opt/memoh/toolkit/bin/codex app-server -c features.goals=true" {
		t.Fatalf("appServerCommand = %q", got)
	}
}

func TestResolveLauncherManagedPathReachesCommandLine(t *testing.T) {
	resolver := &fakeLauncherResolver{launcher: external.Launcher{
		Path:    "/data/.memoh/deps/codex/versions/0.151.0/bin/codex",
		Version: "0.151.0",
		Source:  external.LauncherSourceManaged,
	}}
	d := &Driver{launchers: resolver}
	launcher, err := d.resolveLauncher(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("resolveLauncher: %v", err)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != (resolveCall{botID: "bot-1", depID: "codex"}) {
		t.Fatalf("resolver calls = %+v", resolver.calls)
	}
	if got, want := appServerCommand(launcher.Path), "/data/.memoh/deps/codex/versions/0.151.0/bin/codex app-server -c features.goals=true"; got != want {
		t.Fatalf("appServerCommand = %q, want %q", got, want)
	}
}

func TestAppServerCommandQuotesUnsafePaths(t *testing.T) {
	cases := map[string]string{
		"/data/my deps/codex":             "'/data/my deps/codex' app-server -c features.goals=true",
		"/data/it's/codex":                `'/data/it'\''s/codex' app-server -c features.goals=true`,
		"/data/$HOME/codex":               "'/data/$HOME/codex' app-server -c features.goals=true",
		"/opt/memoh/toolkit/bin/codex":    "/opt/memoh/toolkit/bin/codex app-server -c features.goals=true",
		"  /opt/memoh/toolkit/bin/codex ": "/opt/memoh/toolkit/bin/codex app-server -c features.goals=true",
		"":                                "/opt/memoh/toolkit/bin/codex app-server -c features.goals=true",
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

func TestResolveLauncherMissingDependencyKeepsItsFields(t *testing.T) {
	resolver := &fakeLauncherResolver{err: &external.DependencyMissingError{
		DependencyID: "codex",
		TaskID:       "task-42",
	}}
	d := &Driver{launchers: resolver}
	_, err := d.resolveLauncher(context.Background(), "bot-1")
	if err == nil {
		t.Fatal("resolveLauncher returned no error for a missing dependency")
	}
	var missing *external.DependencyMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("error %T is not a missing dependency: %v", err, err)
	}
	want := external.DependencyMissingError{DependencyID: "codex", TaskID: "task-42", OperationInProgress: true}
	if *missing != want {
		t.Fatalf("missing = %+v, want %+v", *missing, want)
	}

	// The turn path shapes acquisition failures before returning them; the
	// missing dependency must come out intact for the application to
	// translate, not buried under runtime-unavailable.
	shaped := wrapServerError(err)
	if !errors.As(shaped, &missing) {
		t.Fatalf("wrapServerError hid the missing dependency: %v", shaped)
	}
	if external.IsFailure(shaped) {
		t.Fatalf("wrapServerError wrapped the missing dependency in a runtime failure: %v", shaped)
	}
}

func TestResolveLauncherMissingWithoutIDNamesCodex(t *testing.T) {
	d := &Driver{launchers: &fakeLauncherResolver{err: &external.DependencyMissingError{}}}
	_, err := d.resolveLauncher(context.Background(), "bot-1")
	var missing *external.DependencyMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("error %T is not a missing dependency: %v", err, err)
	}
	if missing.DependencyID != "codex" || missing.TaskID != "" || missing.OperationInProgress {
		t.Fatalf("missing = %+v, want codex without a task", *missing)
	}
}

func TestResolveLauncherOtherErrorsAreWrapped(t *testing.T) {
	cause := errors.New("bridge exploded")
	d := &Driver{launchers: &fakeLauncherResolver{err: cause}}
	_, err := d.resolveLauncher(context.Background(), "bot-1")
	if !errors.Is(err, cause) {
		t.Fatalf("cause lost: %v", err)
	}
	if errors.Is(err, external.ErrDependencyMissing) {
		t.Fatal("a plain resolver failure must not become a missing dependency")
	}
	var failure *external.Failure
	if shaped := wrapServerError(err); !errors.As(shaped, &failure) || failure.Kind != external.FailureUnavailable {
		t.Fatalf("wrapServerError = %T %v, want an unavailable runtime", shaped, shaped)
	}

	empty := &Driver{launchers: &fakeLauncherResolver{launcher: external.Launcher{Source: external.LauncherSourceManaged}}}
	if _, err := empty.resolveLauncher(context.Background(), "bot-1"); err == nil {
		t.Fatal("an empty launcher path was accepted")
	}
}

func TestThreadNoticesOnlyReportToollessThreads(t *testing.T) {
	srv := newTestAppServer(external.Launcher{Path: defaultLauncherPath, Version: "0.147.0", Source: external.LauncherSourceToolkit})
	sink := &captureSink{}
	emitThreadNotices(srv, "thread-a", sink)
	if len(sink.events) != 0 {
		t.Fatalf("a thread with tools emitted %+v", sink.events)
	}

	// Toolless threads keep their every-turn notice.
	srv.setThreadToolless("thread-a", true)
	emitThreadNotices(srv, "thread-a", sink)
	emitThreadNotices(srv, "thread-a", sink)
	if len(sink.events) != 2 || sink.events[0].NoticeKind != event.NoticeToolsUnavailable || sink.events[1].NoticeKind != event.NoticeToolsUnavailable {
		t.Fatalf("events = %+v", sink.events)
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

func TestMissingDependencyReportsAdministrativeOperation(t *testing.T) {
	missing := &external.DependencyMissingError{DependencyID: dependencyID, OperationInProgress: true}
	got := dependencyMissing(missing)
	if !got.OperationInProgress {
		t.Fatalf("existing administrative operation was lost: %+v", got)
	}
	if got.TaskID != "" {
		t.Fatal("administrative operation invented a background task")
	}
	missing.OperationInProgress = false
	if got := dependencyMissing(missing); got.OperationInProgress {
		t.Fatalf("missing dependency without an operation reported one: %+v", got)
	}
}

func TestRunningServerDetectsLauncherReplacement(t *testing.T) {
	toolkit := external.Launcher{Path: defaultLauncherPath, Version: "0.149.0", Source: external.LauncherSourceToolkit}
	srv := newTestAppServer(toolkit)
	srv.codexVersion = "0.150.0"
	if !srv.matchesLauncher(external.Launcher{Path: toolkit.Path, Source: toolkit.Source, Version: "0.150.0"}) {
		t.Fatal("handshake version correction should preserve the running server")
	}
	managed := external.Launcher{Path: "/data/.memoh/deps/codex/current/bin/codex", Version: "0.150.0", Source: external.LauncherSourceManaged}
	if srv.matchesLauncher(managed) {
		t.Fatal("managed installation should replace the toolkit process")
	}
	srv.launcher = managed
	managed.Version = "0.151.0"
	if srv.matchesLauncher(managed) {
		t.Fatal("updated CLI behind the same current symlink should replace the process")
	}
}

type remoteBridgeSource struct{ BridgeSource }

func (remoteBridgeSource) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, DefaultWorkDir: "/home/alice/workspace"}, nil
}

func TestAcquireServerRejectsRemoteBeforeReusingOrStartingProcess(t *testing.T) {
	// No process table or executable bridge is available. The workspace must
	// be rejected before either can be touched, even with a resolvable CLI.
	d := &Driver{bridges: remoteBridgeSource{}, launchers: &fakeLauncherResolver{launcher: external.Launcher{Path: "/home/alice/codex"}}}
	_, _, err := d.acquireServer(t.Context(), "bot", "agent")
	if !errors.Is(err, external.ErrContainerWorkspaceRequired) {
		t.Fatalf("remote process use was not blocked: %v", err)
	}
}
