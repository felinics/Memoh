package codex

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/event"
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
	if got := appServerCommand(launcher.Path); got != "/opt/memoh/toolkit/bin/codex app-server" {
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
		DependencyID: "codex",
		TaskID:       "task-42",
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
		"dep_id":          "codex",
		"install_task_id": "task-42",
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
	if feedback.Args["dep_id"] != "codex" {
		t.Fatalf("dep_id = %q, want codex", feedback.Args["dep_id"])
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
	if len(sink.events) != 2 || sink.events[0].Code != "tools_unavailable" || sink.events[1].Code != "tools_unavailable" {
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
