package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

const checkPayload = `{"installed":"1.0.0","latest":"1.2.0","update_available":true}`

func seedInstalled(f *serviceFixture, botID, targetID, depID, version string) InstallationKey {
	rec := Installation{BotID: botID, WorkspaceTargetID: targetID, DependencyID: depID, Source: InstallationSourceManaged, Status: StatusInstalled, InstalledVersion: version}
	f.store.seed(rec)
	return keyOf(rec)
}

func TestUpdateWorkerRunOnceDedupesAndFansOut(t *testing.T) {
	f := newServiceFixture(t)
	keyA := seedInstalled(f, "bot-a", TargetNative, "tool-y", "1.0.0")
	keyB := seedInstalled(f, "bot-b", TargetNative, "tool-y", "1.0.1")
	keyC := seedInstalled(f, "bot-c", TargetNative, "tool-y", "1.0.0")
	keyD := seedInstalled(f, "bot-d", "remote-1", "tool-y", "1.0.0")
	keyE := seedInstalled(f, "bot-e", TargetNative, "tool-y", "1.0.0")
	keyAgent := seedInstalled(f, "bot-a", TargetNative, "agent-x", "1.9.0")
	f.store.seed(Installation{BotID: "bot-f", WorkspaceTargetID: TargetNative, DependencyID: "tool-y", Status: StatusFailed})
	f.ws.setState("bot-c", TargetNative, WorkspaceNotRunning)
	// bot-e runs on another platform, so it forms its own group.
	f.svc.cache.Put("bot-e", TargetNative, Snapshot{Platform: Platform{OS: "linux", Arch: "arm64", Libc: "musl", TmpDir: "/tmp"}})
	f.setRun(func(spec RunSpec) (Result, error) {
		if spec.Action != catalog.ActionCheckUpdate {
			t.Errorf("unexpected action %s", spec.Action)
		}
		return Result{Raw: json.RawMessage(checkPayload)}, nil
	})
	worker := NewUpdateWorker(f.svc, 0, slog.New(slog.DiscardHandler))
	if worker.interval != DefaultUpdateCheckInterval {
		t.Errorf("interval = %v, want the default", worker.interval)
	}
	factoryCalls := 0
	worker.SetContextFactory(func(ctx context.Context) context.Context {
		factoryCalls++
		return ctx
	})

	checks, err := worker.RunOnce(f.ctx())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if checks != 2 || factoryCalls != 1 {
		t.Errorf("checks = %d, factory calls = %d; want 2 and 1", checks, factoryCalls)
	}
	specs := f.runSpecs()
	if len(specs) != 2 {
		t.Fatalf("runs = %+v, want one per (dependency, platform) group", specs)
	}
	platforms := map[string]string{}
	for _, spec := range specs {
		if spec.DepID != "tool-y" || spec.Timeout != time.Duration(catalog.DefaultCheckUpdateTimeout)*time.Second {
			t.Errorf("spec = %+v", spec)
		}
		platforms[spec.Platform.Arch+"/"+spec.Platform.Libc] = spec.CurrentVersion
	}
	if platforms["amd64/glibc"] != "1.0.0" || platforms["arm64/musl"] != "1.0.0" {
		t.Errorf("groups = %v", platforms)
	}

	for _, key := range []InstallationKey{keyA, keyB, keyE} {
		rec, _ := f.store.get(key)
		if rec.LatestVersion != "1.2.0" || rec.LastCheckedAt == nil || !rec.LastCheckedAt.Equal(f.now) || rec.LastError != "" || rec.Status != StatusInstalled {
			t.Errorf("record %s/%s = %+v, want fanned-out result", key.BotID, key.DependencyID, rec)
		}
	}
	for _, key := range []InstallationKey{keyC, keyD, keyAgent} {
		rec, _ := f.store.get(key)
		if rec.LatestVersion != "" || rec.LastCheckedAt != nil {
			t.Errorf("record %s/%s = %+v, want untouched", key.BotID, key.DependencyID, rec)
		}
	}
	if rec, _ := f.store.get(InstallationKey{BotID: "bot-f", WorkspaceTargetID: TargetNative, DependencyID: "tool-y"}); rec.LastCheckedAt != nil {
		t.Errorf("failed record was checked: %+v", rec)
	}

	// A failed round records the error and the time, nothing else (WD-UPD-004).
	// A day later bot-e's cached snapshot has expired; probe it again so it
	// still forms its own platform group.
	f.now = f.now.Add(24 * time.Hour)
	f.svc.cache.Put("bot-e", TargetNative, Snapshot{Platform: Platform{OS: "linux", Arch: "arm64", Libc: "musl", TmpDir: "/tmp"}})
	f.setRun(func(RunSpec) (Result, error) {
		return Result{ExitCode: 1}, &ExitError{Code: 1, StderrTail: "npm view: ETIMEDOUT"}
	})
	checks, err = worker.RunOnce(f.ctx())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if checks != 2 {
		t.Errorf("checks = %d", checks)
	}
	for _, key := range []InstallationKey{keyA, keyB, keyE} {
		rec, _ := f.store.get(key)
		if rec.Status != StatusInstalled || rec.LatestVersion != "1.2.0" || !strings.Contains(rec.LastError, "ETIMEDOUT") || !rec.LastCheckedAt.Equal(f.now) {
			t.Errorf("record after failed round = %+v", rec)
		}
	}

	// A malformed payload is a failure too, and a busy dependency is skipped.
	f.setRun(func(RunSpec) (Result, error) { return Result{Raw: json.RawMessage(`{"latest":""}`)}, nil })
	f.svc.locks.tryLock(keyE)
	defer f.svc.locks.unlock(keyE)
	if _, err := worker.RunOnce(f.ctx()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if rec, _ := f.store.get(keyA); !strings.Contains(rec.LastError, "no latest version") {
		t.Errorf("record after malformed payload = %+v", rec)
	}
}

func TestUpdateWorkerStartAndStop(t *testing.T) {
	f := newServiceFixture(t)
	seedInstalled(f, "bot-a", TargetNative, "tool-y", "1.0.0")
	ran := make(chan struct{}, 8)
	f.setRun(func(RunSpec) (Result, error) {
		ran <- struct{}{}
		return Result{Raw: json.RawMessage(checkPayload)}, nil
	})
	ticks := make(chan time.Time)
	stopped := false
	worker := NewUpdateWorker(f.svc, time.Hour, slog.New(slog.DiscardHandler))
	worker.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		if d != time.Hour {
			t.Errorf("ticker interval = %v", d)
		}
		return ticks, func() { stopped = true }
	}

	worker.Start(f.ctx())
	worker.Start(f.ctx()) // idempotent
	ticks <- f.now
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("tick did not trigger a round")
	}
	worker.Stop()
	worker.Stop() // idempotent
	if !stopped {
		t.Error("ticker was not stopped")
	}
	select {
	case ticks <- f.now:
		t.Error("a stopped worker consumed a tick")
	default:
	}
}

func TestUpdateWorkerReportsListErrors(t *testing.T) {
	f := newServiceFixture(t)
	seedInstalled(f, "bot-a", TargetNative, "tool-y", "1.0.0")
	f.ws.setState("bot-a", TargetNative, WorkspaceRunning)
	probeErr := errors.New("probe exploded")
	f.svc.probe = func(context.Context, *bridge.Client) (Platform, error) { return Platform{}, probeErr }
	worker := NewUpdateWorker(f.svc, time.Hour, nil)
	checks, err := worker.RunOnce(f.ctx())
	if checks != 0 || !errors.Is(err, probeErr) {
		t.Errorf("RunOnce = %d, %v; want 0 and the probe error", checks, err)
	}
}
