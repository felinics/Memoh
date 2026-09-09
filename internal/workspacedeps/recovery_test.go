package workspacedeps

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestDisconnectedScriptCompletionRecoversWithoutServerMemory(t *testing.T) {
	for _, action := range []catalog.Action{catalog.ActionInstall, catalog.ActionRemove} {
		t.Run(string(action), func(t *testing.T) {
			f := newServiceFixture(t)
			fifo := filepath.Join(t.TempDir(), "continue")
			if output, err := exec.CommandContext(f.ctx(), "mkfifo", fifo).CombinedOutput(); err != nil { //nolint:gosec // Test-owned FIFO coordinates a real workspace process.
				t.Fatalf("mkfifo: %v: %s", err, output)
			}
			// O_RDWR keeps a reader and writer open, so neither side relies on process
			// scheduling or a sleep to coordinate the disconnected workspace process.
			gate, err := os.OpenFile(fifo, os.O_RDWR, 0) //nolint:gosec // This path is a FIFO created in t.TempDir above.
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Close() }()
			wait := "dep_log receipt-ready\nIFS= read -r continue < " + shellQuote(fifo) + "\n"
			install := strings.ReplaceAll(e2eFooInstall, `dep_log "installed foo $version"`, "")
			remove := e2eFooRemove
			if action == catalog.ActionInstall {
				install = wait + install
			} else {
				remove = wait + remove
			}
			cat, err := catalog.LoadFS(fstest.MapFS{
				"foo/dependency.yaml": &fstest.MapFile{Data: []byte(e2eFooYAML)},
				"foo/install.sh":      &fstest.MapFile{Data: []byte(install)},
				"foo/remove.sh":       &fstest.MapFile{Data: []byte(remove)},
			})
			if err != nil {
				t.Fatal(err)
			}
			f.cat, f.svc.catalog = cat, cat
			f.svc.run, f.svc.discover = Run, Discover
			f.platform, err = ProbePlatform(f.ctx(), f.client)
			if err != nil {
				t.Fatal(err)
			}
			if action == catalog.ActionRemove {
				if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "foo", "1.0.0", nil); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(f.ctx())
			defer cancel()
			ready := false
			sink := LogFunc(func(_, line string) {
				if line == "receipt-ready" {
					ready = true
					cancel()
				}
			})
			if action == catalog.ActionInstall {
				_, err = f.svc.Install(ctx, testBot, testTarget, "foo", "1.0.0", sink)
			} else {
				_, err = f.svc.Remove(ctx, testBot, testTarget, "foo", sink)
			}
			if !ready || !errors.Is(err, ErrOperationUncertain) {
				t.Fatalf("did not disconnect a live script: ready=%v err=%v", ready, err)
			}
			rec, _ := f.store.get(f.key("foo"))
			if !rec.Status.InProgress() {
				t.Fatalf("live script falsely finalized: %+v", rec)
			}
			// A new Server instance has only the persistent store and workspace. It
			// must report the live process, then recover its durable exit receipt.
			restarted := NewService(Options{Workspace: f.ws, Store: f.store, Catalog: f.cat, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return f.now }})
			restarted.probe = func(context.Context, *bridge.Client) (Platform, error) { return f.platform, nil }
			result, err := restarted.Refresh(f.ctx(), testBot, testTarget)
			if err != nil {
				t.Fatal(err)
			}
			if !f.entry(t, result, "foo").Status.InProgress() {
				t.Fatal("live owner was reclaimed")
			}
			// Even a stale discovery reporting no lock must not reclaim a real
			// owner: the cancellation fence itself must acquire the same kernel lock.
			if _, err := restarted.markInterrupted(f.ctx(), f.key("foo"), rec); !errors.Is(err, ErrBusy) {
				t.Fatalf("reaper crossed a live workspace lock: %v", err)
			}
			if current, _ := f.store.get(f.key("foo")); current.OperationID != rec.OperationID || !current.Status.InProgress() {
				t.Fatalf("live operation lost ownership: %+v", current)
			}
			if _, err := gate.WriteString("continue\n"); err != nil {
				t.Fatal(err)
			}
			// Acquiring the same kernel lock proves the command has finished writing
			// its exit receipt. This waits on the owner, not on an elapsed-time guess.
			lock := lockPath(f.home("foo"), "foo")
			command := "flock -w 10 " + shellQuote(lock) + " true"
			if f.platform.OS == "darwin" {
				command = "lockf -k -t 10 " + shellQuote(lock) + " true"
			}
			drained, err := f.client.ExecWithOptions(f.ctx(), command, "", 15, nil, bridge.ExecOptions{})
			if err != nil || drained.ExitCode != 0 {
				t.Fatalf("wait for owner: %+v %v", drained, err)
			}
			_, err = restarted.Refresh(f.ctx(), testBot, testTarget)
			if err != nil {
				t.Fatal(err)
			}
			if action == catalog.ActionInstall {
				rec, exists := f.store.get(f.key("foo"))
				if !exists || rec.Status != StatusInstalled || rec.InstalledVersion != "1.0.0" {
					t.Fatalf("unrecorded successful install: %+v", rec)
				}
				output, err := exec.CommandContext(f.ctx(), f.shimPath("foo")).Output() //nolint:gosec // Executes the synthetic shim this test installed.
				if err != nil || strings.TrimSpace(string(output)) != "foo 1.0.0" {
					t.Fatalf("recovered CLI not runnable: %q %v", output, err)
				}
			} else {
				if rec, exists := f.store.get(f.key("foo")); exists {
					t.Fatalf("removed dependency record survived: %+v", rec)
				}
				if _, err := os.Stat(f.shimPath("foo")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("removed shim survived: %v", err)
				}
			}
		})
	}
}

func TestCrossServerClaimAndRecoveryRejectSupersededReceipt(t *testing.T) {
	f := newServiceFixture(t)
	cat, err := catalog.LoadFS(fstest.MapFS{
		"foo/dependency.yaml": &fstest.MapFile{Data: []byte(e2eFooYAML)},
		"foo/install.sh":      &fstest.MapFile{Data: []byte(e2eFooInstall)},
		"foo/remove.sh":       &fstest.MapFile{Data: []byte(e2eFooRemove)},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.cat, f.svc.catalog = cat, cat
	f.platform, err = ProbePlatform(f.ctx(), f.client)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.discover = Discover
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		result, err := Run(ctx, client, spec, sink)
		if err != nil {
			return result, err
		}
		return result, ErrOperationUncertain
	}
	if _, err := f.svc.Install(f.ctx(), testBot, testTarget, "foo", "1.0.0", nil); !errors.Is(err, ErrOperationUncertain) {
		t.Fatal(err)
	}
	receipt, err := ReadOperationReceipt(f.ctx(), f.client, f.home("foo"), "foo")
	if err != nil || receipt == nil || !receipt.Completed {
		t.Fatalf("completed receipt missing: %+v %v", receipt, err)
	}
	other := NewService(Options{Workspace: f.ws, Store: f.store, Catalog: cat, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return f.now }})
	other.probe = func(context.Context, *bridge.Client) (Platform, error) { return f.platform, nil }
	// The script has released its workspace lock but finalization has not run.
	// A second Server still cannot replace its durable operation ownership.
	if _, err := other.Install(f.ctx(), testBot, testTarget, "foo", "2.0.0", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("cross-server admission replaced unfinished operation: %v", err)
	}
	if _, err := f.svc.Refresh(f.ctx(), testBot, testTarget); err != nil {
		t.Fatal(err)
	}
	started, proceed := make(chan struct{}), make(chan struct{})
	other.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		close(started)
		<-proceed
		return Run(ctx, client, spec, sink)
	}
	done := make(chan error, 1)
	go func() { _, err := other.Install(f.ctx(), testBot, testTarget, "foo", "2.0.0", nil); done <- err }()
	<-started
	// Model a stale discovery arriving after another Server has admitted a new
	// operation. Recovery must compare the durable ID again under the kernel
	// lease before touching state or shims, even with an old success receipt.
	if _, err := f.svc.recoverReceipt(f.ctx(), f.key("foo"), cat.MustGet("foo"), f.platform, receipt); !errors.Is(err, ErrBusy) {
		t.Errorf("stale receipt recovery = %v", err)
	}
	if state := f.readState(t, "foo"); state.Version != "1.0.0" {
		t.Errorf("stale recovery changed state: %+v", state)
	}
	close(proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.store.get(f.key("foo")); rec.Status != StatusInstalled || rec.InstalledVersion != "2.0.0" {
		t.Fatalf("new operation was overwritten: %+v", rec)
	}
	output, err := exec.CommandContext(f.ctx(), f.shimPath("foo")).Output() //nolint:gosec // Executes the synthetic shim this test installed.
	if err != nil || strings.TrimSpace(string(output)) != "foo 2.0.0" {
		t.Fatalf("wrong command after interleaving: %s %v", output, err)
	}
}

func TestReaperFencesPausedClaimBeforeNewOperation(t *testing.T) {
	f := newServiceFixture(t)
	cat, err := catalog.LoadFS(fstest.MapFS{
		"foo/dependency.yaml": &fstest.MapFile{Data: []byte(e2eFooYAML)},
		"foo/install.sh":      &fstest.MapFile{Data: []byte(e2eFooInstall)},
		"foo/remove.sh":       &fstest.MapFile{Data: []byte(e2eFooRemove)},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.cat, f.svc.catalog = cat, cat
	f.platform, err = ProbePlatform(f.ctx(), f.client)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.discover = Discover
	claimed, resume := make(chan struct{}), make(chan struct{})
	f.svc.run = func(ctx context.Context, client *bridge.Client, spec RunSpec, sink LogSink) (Result, error) {
		close(claimed)
		<-resume
		return Run(ctx, client, spec, sink)
	}
	done := make(chan error, 1)
	go func() { _, err := f.svc.Install(f.ctx(), testBot, testTarget, "foo", "1.0.0", nil); done <- err }()
	<-claimed
	old, _ := f.store.get(f.key("foo"))
	f.now = f.now.Add(2 * time.Minute)
	other := NewService(Options{Workspace: f.ws, Store: f.store, Catalog: cat, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return f.now }})
	other.probe = func(context.Context, *bridge.Client) (Platform, error) { return f.platform, nil }
	if count, err := other.ReapStale(f.ctx()); err != nil || count != 1 {
		t.Fatalf("reap paused claim: count=%d err=%v", count, err)
	}
	if _, err := other.Install(f.ctx(), testBot, testTarget, "foo", "2.0.0", nil); err != nil {
		t.Fatal(err)
	}
	close(resume)
	var exitErr *ExitError
	if err := <-done; !errors.As(err, &exitErr) || exitErr.Code != 76 {
		t.Fatalf("late old operation ran after its fence: %v", err)
	}
	if rec, _ := f.store.get(f.key("foo")); rec.Status != StatusInstalled || rec.InstalledVersion != "2.0.0" || rec.OperationID != "" {
		t.Fatalf("late old operation changed new record: %+v", rec)
	}
	if state := f.readState(t, "foo"); state.Version != "2.0.0" {
		t.Fatalf("late old recipe changed workspace state: %+v", state)
	}
	if _, err := f.client.Stat(f.ctx(), filepath.Join(operationRoot(f.home("foo"), "foo"), ".cancelled-"+old.OperationID)); err != nil {
		t.Fatalf("late start fence was removed: %v", err)
	}
	output, err := exec.CommandContext(f.ctx(), f.shimPath("foo")).Output() //nolint:gosec // Executes the synthetic shim this test installed.
	if err != nil || strings.TrimSpace(string(output)) != "foo 2.0.0" {
		t.Fatalf("old recipe replaced the new CLI: %s %v", output, err)
	}
}

func TestLegacyOperationWithoutIdentityRequiresExplicitRecovery(t *testing.T) {
	f := newServiceFixture(t)
	f.store.seed(Installation{BotID: testBot, DependencyID: "tool-y", Status: StatusInstalling, UpdatedAt: f.now.Add(-48 * time.Hour)})
	if count, err := f.svc.ReapStale(f.ctx()); err != nil || count != 0 {
		t.Fatalf("legacy intent was reclaimed without a safe fence: %d %v", count, err)
	}
	if rec, _ := f.store.get(f.key("tool-y")); !rec.Status.InProgress() {
		t.Fatalf("legacy operation identity was guessed: %+v", rec)
	}
}
