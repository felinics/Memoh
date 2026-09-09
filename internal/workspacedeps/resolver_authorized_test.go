package workspacedeps

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/background"
)

func TestAuthorizedOperationRequiresConfirmedRevisionAndSession(t *testing.T) {
	f := newServiceFixture(t)
	f.svc.background = background.New(slog.New(slog.DiscardHandler))
	run := func(context.Context, LogSink) (OperationResult, error) {
		t.Error("unapproved operation executed")
		return OperationResult{}, nil
	}
	if _, err := f.svc.RunAuthorizedOperation(f.ctx(), testBot, testTarget, "agent-x", "", "Install Agent X", run, nil); err == nil {
		t.Fatal("missing revision accepted")
	}
	if _, err := f.svc.RunAuthorizedOperation(WithDefinitionRevision(f.ctx(), "confirmed"), testBot, testTarget, "agent-x", "wrong-session", "Install Agent X", run, nil); err == nil {
		t.Fatal("unvalidated session accepted")
	}
}

func TestAuthorizedOperationKeepsRevisionAndExcludesConcurrentInstall(t *testing.T) {
	f := newServiceFixture(t)
	mgr := background.New(slog.New(slog.DiscardHandler))
	f.svc.background = mgr
	f.svc.operationSessionValidator = func(_ context.Context, botID, sessionID string) error {
		if botID != testBot || sessionID != "session-a" {
			return errors.New("wrong session")
		}
		return nil
	}
	events := make(chan background.TaskEvent, 8)
	mgr.SetEventFunc(func(evt background.TaskEvent) { events <- evt })
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	parent, cancelParent := context.WithCancel(WithDefinitionRevision(f.ctx(), "confirmed-revision"))
	go func() {
		_, err := f.svc.RunAuthorizedOperation(parent, testBot, testTarget, "agent-x", "session-a", "Install Agent X", func(ctx context.Context, sink LogSink) (OperationResult, error) {
			if revision, _ := ctx.Value(revisionContextKey{}).(string); revision != "confirmed-revision" {
				t.Error("confirmed revision lost")
			}
			if _, limited := ctx.Deadline(); limited {
				t.Error("background exec cap imposed on install")
			}
			if releaseLock, err := f.svc.acquireOperation(ctx, f.key("agent-x")); err != nil {
				t.Errorf("reserved lock was not inherited: %v", err)
			} else {
				defer releaseLock()
			}
			close(started)
			<-release
			if err := ctx.Err(); err != nil {
				t.Errorf("HTTP cancellation aborted operation: %v", err)
			}
			sink.Log("stdout", "installed")
			return OperationResult{Version: "2.0.0"}, nil
		}, nil)
		done <- err
	}()
	<-started
	cancelParent()
	_, err := f.svc.RunAuthorizedOperation(WithDefinitionRevision(f.ctx(), "confirmed-revision"), testBot, testTarget, "agent-x", "session-a", "Install Agent X", func(context.Context, LogSink) (OperationResult, error) {
		t.Error("concurrent install executed")
		return OperationResult{}, nil
	}, nil)
	if !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent operation = %v, want busy before a task is started", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	first := <-events
	if first.Event != background.TaskEventStarted || first.SessionID != "session-a" {
		t.Fatalf("first event = %+v", first)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	snapshot, _, err := mgr.WaitForSessionTask(ctx, testBot, "session-a", first.TaskID, 0)
	if err != nil || snapshot.Status != background.TaskCompleted {
		t.Fatalf("task = %+v, %v", snapshot, err)
	}
	if len(mgr.ListForSession(testBot, "session-a")) != 1 {
		t.Fatal("concurrent install created a false failed task")
	}
	if !f.svc.locks.tryLock(f.key("agent-x")) {
		t.Fatal("operation lock leaked")
	}
	f.svc.locks.unlock(f.key("agent-x"))
}

func TestSlowOperationNotificationDoesNotBlockOtherBotLaunchers(t *testing.T) {
	f := newServiceFixture(t)
	mgr := background.New(slog.New(slog.DiscardHandler))
	f.svc.background = mgr
	blocked := make(chan struct{})
	release := make(chan struct{})
	mgr.SetEventFunc(func(evt background.TaskEvent) {
		if evt.Event == background.TaskEventStarted {
			close(blocked)
			<-release
		}
	})
	finished := make(chan error, 1)
	go func() {
		_, err := f.svc.RunAuthorizedOperation(WithDefinitionRevision(f.ctx(), "confirmed"), testBot, testTarget, "agent-x", "", "Install Agent X", func(context.Context, LogSink) (OperationResult, error) { return OperationResult{}, nil }, nil)
		finished <- err
	}()
	<-blocked
	f.svc.cache.Put("other-bot", testTarget, Snapshot{Platform: f.platform, Observed: map[string]Observed{"agent-x": {DepID: "agent-x", Present: true, Candidates: []Candidate{toolkit("2.0.0")}}}})
	resolved := make(chan error, 1)
	go func() { _, err := f.svc.ResolveLauncher(f.ctx(), "other-bot", "agent-x"); resolved <- err }()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case err := <-resolved:
		if err != nil {
			t.Error(err)
		}
	case <-ctx.Done():
		t.Error("a slow notification blocked an unrelated bot's launcher")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizedOperationReportsUnconfirmedExecution(t *testing.T) {
	f := newServiceFixture(t)
	mgr := background.New(slog.New(slog.DiscardHandler))
	f.svc.background = mgr
	_, err := f.svc.RunAuthorizedOperation(WithDefinitionRevision(f.ctx(), "confirmed"), testBot, testTarget, "agent-x", "", "Install Agent X", func(context.Context, LogSink) (OperationResult, error) {
		return OperationResult{}, ErrOperationUncertain
	}, nil)
	if !errors.Is(err, ErrOperationUncertain) {
		t.Fatalf("error = %v", err)
	}
	tasks := mgr.ListForSession(testBot, "")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	snap, outcome, err := mgr.WaitForSessionTask(ctx, testBot, "", tasks[0].ID, 0)
	if err != nil || snap.Status != background.TaskUnknown || outcome != background.WaitUnknown {
		t.Fatalf("task=%+v outcome=%s err=%v", snap, outcome, err)
	}
}
