package workspacedeps

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

const (
	managedPath = "/data/.memoh/deps/agent-x/current/bin/agent-x"
	toolkitPath = "/opt/memoh/toolkit/bin/agent-x"
	pathCopy    = "/usr/local/bin/agent-x"
)

func managed(version string) Candidate {
	return Candidate{Source: SourceManaged, Path: managedPath, Version: version}
}

func toolkit(version string) Candidate {
	return Candidate{Source: SourceToolkit, Path: toolkitPath, Version: version}
}

func onPath(version string) Candidate {
	return Candidate{Source: SourcePath, Path: pathCopy, Version: version}
}

// seedCandidates stores a discovery snapshot for (testBot, target) in which
// agent-x has exactly the given copies, in discovery order.
func (f *serviceFixture) seedCandidates(targetID string, candidates ...Candidate) {
	obs := Observed{DepID: "agent-x"}
	if len(candidates) > 0 {
		obs.Present = true
		obs.Source = candidates[0].Source
		obs.Command = candidates[0].Path
		obs.Version = candidates[0].Version
		obs.Candidates = candidates
	}
	f.svc.cache.Put(testBot, normalizeTargetID(targetID), Snapshot{
		Platform: f.platform,
		Observed: map[string]Observed{"agent-x": obs},
	})
}

func (f *serviceFixture) cachedCandidates(targetID string) []Candidate {
	snap, ok := f.svc.cache.Get(testBot, normalizeTargetID(targetID))
	if !ok {
		f.t.Fatalf("no snapshot cached for %s", targetID)
	}
	return snap.Observed["agent-x"].Candidates
}

func TestResolveLauncherOrdersCandidates(t *testing.T) {
	tests := []struct {
		name       string
		candidates []Candidate
		want       external.Launcher
	}{
		{
			name:       "managed at pin beats everything",
			candidates: []Candidate{managed("2.0.0"), toolkit("2.0.0"), onPath("2.0.0")},
			want:       external.Launcher{Path: managedPath, Version: "2.0.0", Source: external.LauncherSourceManaged},
		},
		{
			name:       "toolkit at pin beats a managed copy off pin",
			candidates: []Candidate{managed("1.9.0"), toolkit("2.0.0"), onPath("2.0.0")},
			want:       external.Launcher{Path: toolkitPath, Version: "2.0.0", Source: external.LauncherSourceToolkit},
		},
		{
			name:       "any managed copy beats toolkit and PATH off pin",
			candidates: []Candidate{managed("1.9.0"), toolkit("1.8.0"), onPath("2.0.0")},
			want:       external.Launcher{Path: managedPath, Version: "1.9.0", Source: external.LauncherSourceManaged, Mismatch: true},
		},
		{
			name:       "any toolkit copy beats PATH",
			candidates: []Candidate{toolkit("1.8.0"), onPath("2.0.0")},
			want:       external.Launcher{Path: toolkitPath, Version: "1.8.0", Source: external.LauncherSourceToolkit, Mismatch: true},
		},
		{
			name:       "PATH copy is the last resort",
			candidates: []Candidate{onPath("1.0.0")},
			want:       external.Launcher{Path: pathCopy, Version: "1.0.0", Source: external.LauncherSourcePath, Mismatch: true},
		},
		{
			name:       "unknown version is not a mismatch",
			candidates: []Candidate{managed(""), toolkit("1.8.0")},
			want:       external.Launcher{Path: managedPath, Source: external.LauncherSourceManaged},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newServiceFixture(t)
			f.seedCandidates(testTarget, tt.candidates...)
			got, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0")
			if err != nil {
				t.Fatalf("ResolveLauncher: %v", err)
			}
			if got != tt.want {
				t.Fatalf("launcher = %+v, want %+v", got, tt.want)
			}
			if f.discovered() != 0 {
				t.Fatalf("discover calls = %d, want the cached snapshot to be used", f.discovered())
			}
		})
	}
}

func TestResolveLauncherDefaultsToCatalogPinAndDiscoversOnMiss(t *testing.T) {
	f := newServiceFixture(t)
	f.present("agent-x", SourceManaged, "1.9.0", nil)

	got, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "")
	if err != nil {
		t.Fatalf("ResolveLauncher: %v", err)
	}
	if !got.Mismatch || got.Version != "1.9.0" || got.Source != external.LauncherSourceManaged {
		t.Fatalf("launcher = %+v, want a mismatch against the 2.0.0 pin", got)
	}
	if f.discovered() != 1 {
		t.Fatalf("discover calls = %d, want one discovery on cache miss", f.discovered())
	}
	if _, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", ""); err != nil {
		t.Fatalf("ResolveLauncher again: %v", err)
	}
	if f.discovered() != 1 {
		t.Fatalf("discover calls = %d, want the second call served from cache", f.discovered())
	}
}

func TestResolveLauncherUsesCurrentTarget(t *testing.T) {
	f := newServiceFixture(t)
	f.ws.setCurrentTarget("remote-1", nil)
	f.seedCandidates(testTarget, managed("2.0.0"))
	f.seedCandidates("remote-1", onPath("2.0.0"))

	got, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0")
	if err != nil {
		t.Fatalf("ResolveLauncher: %v", err)
	}
	if got.Path != pathCopy || got.Source != external.LauncherSourcePath {
		t.Fatalf("launcher = %+v, want the remote target's copy", got)
	}

	f.ws.setCurrentTarget("", errors.New("primary lookup failed"))
	if _, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0"); err == nil || err.Error() != "primary lookup failed" {
		t.Fatalf("error = %v, want the target lookup failure", err)
	}
}

func TestResolveLauncherRejectsImageAndUnknownDependencies(t *testing.T) {
	f := newServiceFixture(t)
	for _, depID := range []string{"img-z", "nope"} {
		_, err := f.svc.ResolveLauncher(f.ctx(), testBot, depID, "1.0.0")
		if !errors.Is(err, ErrDependencyNotFound) {
			t.Errorf("ResolveLauncher(%s) error = %v, want ErrDependencyNotFound", depID, err)
		}
	}
	if f.discovered() != 0 {
		t.Fatalf("discover calls = %d, want none for rejected dependencies", f.discovered())
	}
}

func TestResolveLauncherMissingStartsOneBackgroundInstall(t *testing.T) {
	f := newServiceFixture(t)
	mgr := background.New(slog.New(slog.DiscardHandler))
	f.svc.background = mgr
	f.seedCandidates(testTarget)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.setRun(func(spec RunSpec) (Result, error) {
		started <- struct{}{}
		<-release
		return f.installResult(spec.DepID, "2.0.0"), nil
	})

	_, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0")
	var missing *external.DependencyMissingError
	if !errors.As(err, &missing) || !errors.Is(err, external.ErrDependencyMissing) {
		t.Fatalf("error = %v, want DependencyMissingError", err)
	}
	if missing.DependencyID != "agent-x" || missing.RequiredVersion != "2.0.0" || missing.TaskID == "" {
		t.Fatalf("missing = %+v", missing)
	}
	task := mgr.Get(missing.TaskID)
	if task == nil {
		t.Fatal("no background task registered")
	}
	if snap := task.Snapshot(); snap.Kind != background.KindDependency || snap.BotID != testBot || snap.Description != "Install Agent X 2.0.0" {
		t.Fatalf("task = %+v", snap)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("install script did not start")
	}

	// A second resolution while the install runs reuses the task.
	_, err = f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0")
	var again *external.DependencyMissingError
	if !errors.As(err, &again) || again.TaskID != missing.TaskID {
		t.Fatalf("second error = %v, want the same task id %s", err, missing.TaskID)
	}

	close(release)
	snap := waitTask(t, mgr, missing.TaskID)
	if snap.Status != background.TaskCompleted {
		t.Fatalf("task = %+v, want completed", snap)
	}
	if rec, ok := f.store.get(f.key("agent-x")); !ok || rec.Status != StatusInstalled || rec.InstalledVersion != "2.0.0" {
		t.Fatalf("record = %+v", rec)
	}
	if _, cached := f.svc.cache.Get(testBot, testTarget); cached {
		t.Fatal("snapshot was not invalidated after the install")
	}

	// Once the task is gone the next miss starts a fresh install.
	f.seedCandidates(testTarget)
	waitInstallCleared(t, f.svc, f.key("agent-x"))
	_, err = f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0")
	var fresh *external.DependencyMissingError
	if !errors.As(err, &fresh) || fresh.TaskID == "" || fresh.TaskID == missing.TaskID {
		t.Fatalf("third error = %v, want a new task", err)
	}
	waitTask(t, mgr, fresh.TaskID)
}

func TestResolveLauncherMissingWhenInstallFails(t *testing.T) {
	f := newServiceFixture(t)
	mgr := background.New(slog.New(slog.DiscardHandler))
	f.svc.background = mgr
	f.seedCandidates(testTarget)
	f.setRun(func(RunSpec) (Result, error) {
		return Result{ExitCode: 3}, &ExitError{Code: 3, StderrTail: "boom"}
	})

	_, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0")
	var missing *external.DependencyMissingError
	if !errors.As(err, &missing) || missing.TaskID == "" {
		t.Fatalf("error = %v, want a missing error with a task", err)
	}
	snap := waitTask(t, mgr, missing.TaskID)
	if snap.Status != background.TaskFailed || snap.Error == "" {
		t.Fatalf("task = %+v, want failed with the install error", snap)
	}
	if rec, ok := f.store.get(f.key("agent-x")); !ok || rec.Status != StatusFailed {
		t.Fatalf("record = %+v, want failed", rec)
	}
}

func TestResolveLauncherMissingWithoutBackground(t *testing.T) {
	f := newServiceFixture(t)
	f.seedCandidates(testTarget)

	_, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0")
	var missing *external.DependencyMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want DependencyMissingError", err)
	}
	if missing.TaskID != "" {
		t.Fatalf("task id = %q, want empty without a background manager", missing.TaskID)
	}
	if len(f.runSpecs()) != 0 {
		t.Fatalf("runs = %v, want no install attempt", f.runSpecs())
	}
}

func TestEnsureInstalledAsyncSkipsWhenOperationHoldsLock(t *testing.T) {
	f := newServiceFixture(t)
	f.svc.background = background.New(slog.New(slog.DiscardHandler))
	key := f.key("agent-x")
	if !f.svc.locks.tryLock(key) {
		t.Fatal("could not take the lock")
	}
	defer f.svc.locks.unlock(key)

	taskID, err := f.svc.EnsureInstalledAsync(f.ctx(), testBot, "", "agent-x")
	if err != nil || taskID != "" {
		t.Fatalf("EnsureInstalledAsync = %q, %v; want no task while another operation runs", taskID, err)
	}
	if _, err := f.svc.EnsureInstalledAsync(f.ctx(), testBot, "", "img-z"); !errors.Is(err, ErrActionUnsupported) {
		t.Fatalf("image dependency error = %v, want ErrActionUnsupported", err)
	}
	if _, err := f.svc.EnsureInstalledAsync(f.ctx(), testBot, "", "nope"); !errors.Is(err, ErrDependencyNotFound) {
		t.Fatalf("unknown dependency error = %v, want ErrDependencyNotFound", err)
	}
}

func TestObserveLauncherVersionRewritesTheLaunchedCopy(t *testing.T) {
	f := newServiceFixture(t)
	f.seedCandidates(testTarget, managed("1.9.0"), toolkit("2.0.0"))

	if _, err := f.svc.ResolveLauncher(f.ctx(), testBot, "agent-x", "2.0.0"); err != nil {
		t.Fatalf("ResolveLauncher: %v", err)
	}
	f.svc.ObserveLauncherVersion(f.ctx(), testBot, "agent-x", "2.1.0")
	got := f.cachedCandidates(testTarget)
	if got[0].Version != "1.9.0" || got[1].Version != "2.1.0" {
		t.Fatalf("candidates = %+v, want the toolkit copy corrected and the managed one untouched", got)
	}
	snap, _ := f.svc.cache.Get(testBot, testTarget)
	if snap.Observed["agent-x"].Version != "1.9.0" {
		t.Fatalf("winner version = %q, want unchanged", snap.Observed["agent-x"].Version)
	}

	// Without a prior resolution the default winning copy is corrected.
	f.seedCandidates(testTarget, managed("1.9.0"), toolkit("2.0.0"))
	f.svc.forgetLaunched(f.key("agent-x"))
	f.svc.ObserveLauncherVersion(f.ctx(), testBot, "agent-x", "1.9.5")
	got = f.cachedCandidates(testTarget)
	if got[0].Version != "1.9.5" || got[1].Version != "2.0.0" {
		t.Fatalf("candidates = %+v, want the winner corrected", got)
	}

	// Errors and blanks are ignored.
	f.svc.ObserveLauncherVersion(f.ctx(), testBot, "agent-x", "")
	f.ws.setCurrentTarget("", errors.New("offline"))
	f.svc.ObserveLauncherVersion(f.ctx(), testBot, "agent-x", "3.0.0")
	if got = f.cachedCandidates(testTarget); got[0].Version != "1.9.5" {
		t.Fatalf("candidates = %+v, want no change on error", got)
	}
}

func TestPreflightFollowsLauncherSelection(t *testing.T) {
	f := newServiceFixture(t)
	f.seedCandidates(testTarget, managed("1.9.0"), toolkit("2.0.0"))

	result, err := f.svc.Preflight(f.ctx(), testBot, testTarget, []string{"agent-x"})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	item := result.Items[0]
	if !item.Satisfied || item.InstalledVersion != "2.0.0" || item.Reason != "" {
		t.Fatalf("item = %+v, want satisfied by the toolkit copy at the pin", item)
	}

	f.seedCandidates(testTarget, managed("1.9.0"), toolkit("1.8.0"), onPath("2.0.0"))
	result, err = f.svc.Preflight(f.ctx(), testBot, testTarget, []string{"agent-x"})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	item = result.Items[0]
	if item.Satisfied || item.Reason != PreflightReasonVersionMismatch || item.InstalledVersion != "1.9.0" {
		t.Fatalf("item = %+v, want a mismatch on the managed copy the resolver would run", item)
	}
}

func TestNewServiceWiresBackground(t *testing.T) {
	f := newServiceFixture(t)
	mgr := background.New(slog.New(slog.DiscardHandler))
	svc := NewService(Options{
		Workspace:  f.ws,
		Store:      f.store,
		Catalog:    f.cat,
		Logger:     slog.New(slog.DiscardHandler),
		Background: mgr,
	})
	if svc.background != mgr {
		t.Fatal("Options.Background was not stored")
	}
}

func waitTask(t *testing.T, mgr *background.Manager, taskID string) background.TaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task := mgr.Get(taskID)
		if task == nil {
			t.Fatalf("task %s disappeared", taskID)
		}
		snap := task.Snapshot()
		if snap.Status == background.TaskCompleted || snap.Status == background.TaskFailed || snap.Status == background.TaskKilled {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not finish", taskID)
	return background.TaskSnapshot{}
}

// waitInstallCleared waits for the resolver to forget a finished install; the
// cleanup runs after the task's terminal state is published.
func waitInstallCleared(t *testing.T, svc *Service, key InstallationKey) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.resolverMu.Lock()
		_, running := svc.installs[key]
		svc.resolverMu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("install for %+v was not cleared", key)
}
