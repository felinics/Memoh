package agentproc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type fakeResolver struct {
	launcher external.Launcher
	err      error
	observed []string
}

func (f *fakeResolver) ResolveLauncher(context.Context, string, string) (external.Launcher, error) {
	return f.launcher, f.err
}

func (f *fakeResolver) ObserveLauncherVersion(_ context.Context, botID, depID, version string) {
	f.observed = append(f.observed, botID+"/"+depID+"/"+version)
}

func TestBaseEnvPutsManagedShimsFirstAndHomeOnTheDataVolume(t *testing.T) {
	env := BaseEnv("CODEX_HOME=/data/.codex")
	if !strings.HasPrefix(env[0], "PATH=/data/.memoh/deps/bin:/opt/memoh/toolkit/bin:") || env[1] != "HOME=/data" || env[2] != "CODEX_HOME=/data/.codex" {
		t.Fatalf("env = %v", env)
	}
}

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

	// Anything else is the runtime being unavailable.
	cause := errors.New("bridge down")
	_, err = ResolveLauncher(ctx, &fakeResolver{err: cause}, "bot", "codex", "")
	var failure *external.Failure
	if !errors.As(err, &failure) || failure.Kind != external.FailureUnavailable {
		t.Fatalf("resolver failure: %v", err)
	}
	if _, err := ResolveLauncher(ctx, &fakeResolver{launcher: external.Launcher{Source: external.LauncherSourceManaged}}, "bot", "codex", ""); err == nil {
		t.Fatal("an empty launcher path was accepted")
	}
}

func TestObserveVersion(t *testing.T) {
	r := &fakeResolver{}
	ObserveVersion(context.Background(), r, "bot", "codex", " 0.151.0 ")
	ObserveVersion(context.Background(), r, "bot", "codex", "")
	ObserveVersion(context.Background(), nil, "bot", "codex", "0.151.0")
	if len(r.observed) != 1 || r.observed[0] != "bot/codex/0.151.0" {
		t.Fatalf("observed = %v", r.observed)
	}
}
