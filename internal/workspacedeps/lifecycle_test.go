package workspacedeps

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestUncertainOperationRemainsInProgressUntilOwnerDies(t *testing.T) {
	f := newServiceFixture(t)
	f.svc.run = func(context.Context, *bridge.Client, RunSpec, LogSink) (Result, error) {
		return Result{}, errors.Join(ErrOperationUncertain, context.Canceled)
	}
	_, err := f.svc.Install(f.ctx(), testBot, "tool-y", "1.0.0", nil)
	if !errors.Is(err, ErrOperationUncertain) {
		t.Fatalf("error = %v", err)
	}
	rec, _ := f.store.get(f.key("tool-y"))
	if rec.Status != StatusInstalling {
		t.Fatalf("unknown process falsely failed: %+v", rec)
	}
	f.mu.Lock()
	f.observed["tool-y"] = Observed{DepID: "tool-y", LockHeld: true}
	f.mu.Unlock()
	f.now = f.now.Add(48 * time.Hour)
	result, err := f.svc.Refresh(f.ctx(), testBot)
	if err != nil {
		t.Fatal(err)
	}
	if f.entry(t, result, "tool-y").Status != StatusInstalling {
		t.Fatal("live owner expired by time")
	}
	f.mu.Lock()
	f.observed["tool-y"] = Observed{DepID: "tool-y", LockAbandoned: true, Receipt: &OperationReceipt{ID: rec.OperationID, DependencyID: "tool-y"}}
	f.mu.Unlock()
	result, err = f.svc.Refresh(f.ctx(), testBot)
	if err != nil {
		t.Fatal(err)
	}
	if f.entry(t, result, "tool-y").Status != StatusFailed {
		t.Fatal("dead operation cannot be retried")
	}
}

func TestShutdownCancelsAndWaitsForFinalization(t *testing.T) {
	f := newServiceFixture(t)
	running := make(chan struct{})
	released := make(chan struct{})
	result := make(chan error, 1)
	f.svc.run = func(ctx context.Context, _ *bridge.Client, _ RunSpec, _ LogSink) (Result, error) {
		close(running)
		<-ctx.Done()
		<-released
		return Result{}, errors.Join(ErrOperationUncertain, ctx.Err())
	}
	go func() { _, err := f.svc.Install(f.ctx(), testBot, "tool-y", "", nil); result <- err }()
	<-running
	stopped := make(chan error, 1)
	go func() { stopped <- f.svc.Shutdown(f.ctx()) }()
	<-f.svc.shutdownCtx.Done()
	select {
	case err := <-stopped:
		t.Fatalf("shutdown returned before operation settled: %v", err)
	default:
	}
	if _, err := f.svc.Install(f.ctx(), testBot, "agent-x", "", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("new operation during shutdown = %v", err)
	}
	close(released)
	if err := <-result; !errors.Is(err, ErrOperationUncertain) {
		t.Errorf("operation result = %v", err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	rec, _ := f.store.get(f.key("tool-y"))
	if rec.Status != StatusInstalling {
		t.Fatalf("shutdown falsely declared process exit: %+v", rec)
	}
}

func TestAuthorizedReservationSharesForegroundLock(t *testing.T) {
	f := newServiceFixture(t)
	key := f.key("tool-y")
	ctx, release, err := f.svc.reserveOperation(f.ctx(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := f.svc.Install(f.ctx(), testBot, "tool-y", "", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("foreground stole reserved operation: %v", err)
	}
	f.setRun(func(spec RunSpec) (Result, error) { return f.installResult(spec.DepID, "1.0.0"), nil })
	if _, err := f.svc.Install(ctx, testBot, "tool-y", "", nil); err != nil {
		t.Fatal(err)
	}
	// Completion released the reservation. Its deferred duplicate release must
	// not unlock the next operation that acquired this key.
	if !f.svc.locks.tryLock(key) {
		t.Fatal("completed reservation was retained")
	}
	release()
	if !f.svc.locks.locked(key) {
		t.Fatal("duplicate release unlocked the next owner")
	}
	f.svc.locks.unlock(key)
}

func TestRequestedVersionRejectsPathsOptionsAndShellTokens(t *testing.T) {
	f := newServiceFixture(t)
	for _, version := range []string{"../1", "1/../../x", "-latest", "1\nother", "$(id)", strings.Repeat("x", 129)} {
		if _, err := f.svc.Install(f.ctx(), testBot, "tool-y", version, nil); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("version %q: %v", version, err)
		}
	}
	if f.store.writeCount() != 0 || len(f.runSpecs()) != 0 {
		t.Fatal("invalid version mutated workspace")
	}
	for _, version := range []string{"", "3", "3.15", "v22.1.0", "1.0.0-rc.1+build"} {
		if !ValidRequestedVersion(version) {
			t.Errorf("valid version rejected: %q", version)
		}
	}
}

// TestOperationFailureKeepsScriptOutputOutOfTheRow covers what a failed
// install records: the catalog code, and none of the script's output. The
// output still reaches the caller and the request's result record, which is
// where a diagnostic belongs.
func TestOperationFailureKeepsScriptOutputOutOfTheRow(t *testing.T) {
	f := newServiceFixture(t)
	f.env = []string{"NPM_TOKEN=opaque-secret"}
	f.setRun(func(RunSpec) (Result, error) {
		return Result{}, errors.New("download failed: opaque-secret; https://user:pass@example.test/file?token=query-secret\x00")
	})
	_, err := f.svc.Install(f.ctx(), testBot, "tool-y", "", nil)
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("Install error = %v, want the cause kept for the caller", err)
	}
	rec, _ := f.store.get(f.key("tool-y"))
	if rec.LastError != "" || rec.LastErrorCode != string(apperror.CodeWorkspaceDependencyOperationFailed) {
		t.Fatalf("row = %q/%q, want the operation-failed code and no text", rec.LastErrorCode, rec.LastError)
	}
}
