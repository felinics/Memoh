package agentproc

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type fakeResolver struct {
	launcher external.Launcher
	err      error
}

func (f *fakeResolver) ResolveLauncher(context.Context, string, string) (external.Launcher, error) {
	return f.launcher, f.err
}

func (*fakeResolver) ObserveLauncherVersion(context.Context, string, string, string) {}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/opt/x/codex":  "'/opt/x/codex'",
		"/data/my deps": "'/data/my deps'",
		"/data/it's":    `'/data/it'\''s'`,
		"/data/$HOME/x": "'/data/$HOME/x'",
		"":              "''",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveLauncher(t *testing.T) {
	ctx := context.Background()
	if l, err := ResolveLauncher(ctx, nil, "bot", "codex", "/opt/toolkit/codex"); err != nil || l.Path != "/opt/toolkit/codex" || l.Source != external.LauncherSourceToolkit {
		t.Fatalf("without a resolver: %+v, %v", l, err)
	}

	managed := external.Launcher{Path: "/data/.memoh/deps/codex/current/bin/codex", Source: external.LauncherSourceManaged}
	if l, err := ResolveLauncher(ctx, &fakeResolver{launcher: managed}, "bot", "codex", ""); err != nil || l != managed {
		t.Fatalf("managed: %+v, %v", l, err)
	}

	// A missing dependency stays one, named and marked in progress when a
	// task runs, so the application can tell the user what to do.
	_, err := ResolveLauncher(ctx, &fakeResolver{err: &external.DependencyMissingError{TaskID: "task-1"}}, "bot", "codex", "")
	var missing *external.DependencyMissingError
	if !errors.As(err, &missing) || external.IsFailure(err) || *missing != (external.DependencyMissingError{DependencyID: "codex", TaskID: "task-1", OperationInProgress: true}) {
		t.Fatalf("missing dependency: %v", err)
	}

	// Anything else is Memoh's own failure, kept as it is.
	cause := errors.New("bridge down")
	_, err = ResolveLauncher(ctx, &fakeResolver{err: cause}, "bot", "codex", "")
	if !errors.Is(err, cause) || external.IsFailure(err) {
		t.Fatalf("resolver failure: %v", err)
	}
	if _, err := ResolveLauncher(ctx, &fakeResolver{launcher: external.Launcher{Source: external.LauncherSourceManaged}}, "bot", "codex", ""); err == nil {
		t.Fatal("an empty launcher path was accepted")
	}
}
