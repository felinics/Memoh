package codex

import (
	"context"
	"errors"
	"log/slog"
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

type captureSink struct{ events []event.StreamEvent }

func (c *captureSink) EmitStreamEvent(ev event.StreamEvent) { c.events = append(c.events, ev) }

func newTestAppServer(launcher external.Launcher) *appServer {
	return &appServer{
		launcher:        launcher,
		toollessThreads: map[string]bool{},
	}
}

func TestAppServerCommandQuotesTheLauncher(t *testing.T) {
	for path, want := range map[string]string{
		"/data/it's/codex": `'/data/it'\''s/codex' app-server -c features.goals=true -c features.request_permissions_tool=true`,
		"  ":               "'/opt/memoh/toolkit/bin/codex' app-server -c features.goals=true -c features.request_permissions_tool=true",
	} {
		if got := appServerCommand(path); got != want {
			t.Errorf("appServerCommand(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestThreadWithoutGatewayRunsToollessAndSaysSoEveryTurn(t *testing.T) {
	srv := newTestAppServer(external.Launcher{Path: defaultLauncherPath, Version: "0.147.0", Source: external.LauncherSourceToolkit})
	sink := &captureSink{}
	emitThreadNotices(srv, "thread-a", sink)
	if len(sink.events) != 0 {
		t.Fatalf("a thread with tools emitted %+v", sink.events)
	}

	// A workspace with no reachable tools URL cannot be mounted: the thread
	// starts without the gateway instead of failing the turn.
	srv.workspaceInfo = bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote}
	d := &Driver{logger: slog.Default()}
	config, bind := d.prepareThreadTools(t.Context(), srv, external.PromptInput{BotID: "bot-1"})
	if config != nil {
		t.Fatalf("config = %v, want none", config)
	}
	bind("thread-a")
	emitThreadNotices(srv, "thread-a", sink)
	emitThreadNotices(srv, "thread-a", sink)
	if len(sink.events) != 2 || sink.events[0].NoticeKind != event.NoticeToolsUnavailable || sink.events[1].NoticeKind != event.NoticeToolsUnavailable {
		t.Fatalf("events = %+v", sink.events)
	}
}

func TestFingerprintChangesOnlyWithWhatTheProcessStartsFrom(t *testing.T) {
	cfg := Config{Auth: AuthAPIKey, APIKey: "sk-1", BaseURL: "https://relay.example", Model: "gpt-a", PermissionMode: "strict"}
	managed := external.Launcher{Path: "/data/.memoh/deps/codex/current/bin/codex", Version: "0.150.0", Source: external.LauncherSourceManaged}
	base := fingerprint(cfg, managed)

	perTurn := cfg
	perTurn.Model, perTurn.PermissionMode, perTurn.ReasoningEffort = "gpt-b", "yolo", "high"
	if fingerprint(perTurn, managed) != base {
		t.Fatal("model, permission mode, or effort restarted the app-server")
	}
	if strings.Contains(base, "sk-1") {
		t.Fatal("the fingerprint carries the API key")
	}
	rotated := cfg
	rotated.APIKey = "sk-2"
	updated := managed
	updated.Version = "0.151.0"
	toolkit := external.Launcher{Path: defaultLauncherPath, Source: external.LauncherSourceToolkit}
	for name, other := range map[string]string{
		"rotated key":     fingerprint(rotated, managed),
		"updated CLI":     fingerprint(cfg, updated),
		"toolkit CLI":     fingerprint(cfg, toolkit),
		"ChatGPT sign-in": fingerprint(Config{Auth: AuthChatGPT}, managed),
	} {
		if other == base {
			t.Fatalf("%s kept the app-server", name)
		}
	}
}

type remoteBridgeSource struct{ external.Workspaces }

func (remoteBridgeSource) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, DefaultWorkDir: "/home/alice/workspace"}, nil
}

func TestAcquireServerRejectsRemoteBeforeReusingOrStartingProcess(t *testing.T) {
	// No process table or executable bridge is available. The workspace must
	// be rejected before either can be touched, even with a resolvable CLI.
	d := &Driver{workspaces: remoteBridgeSource{}, launchers: &fakeLauncherResolver{launcher: external.Launcher{Path: "/home/alice/codex"}}}
	_, _, err := d.acquireServer(t.Context(), "bot", "agent")
	if !errors.Is(err, external.ErrContainerWorkspaceRequired) {
		t.Fatalf("remote process use was not blocked: %v", err)
	}
}
