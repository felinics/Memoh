package botworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// jobRecords returns the result records written since the last call.
func (b *logBuffer) jobRecords(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if record["msg"] == "job" {
			out = append(out, record)
		}
	}
	b.buf.Reset()
	return out
}

func withJobLog(svc *Service) *logBuffer {
	buf := &logBuffer{}
	svc.log = slog.New(slog.NewJSONHandler(buf, nil))
	return buf
}

func oneJob(t *testing.T, buf *logBuffer, operation string) map[string]any {
	t.Helper()
	records := buf.jobRecords(t)
	if len(records) != 1 || records[0]["operation"] != operation {
		t.Fatalf("job records = %v, want one %s", records, operation)
	}
	return records[0]
}

// Each provisioning attempt is one unit with one result record. An attempt
// inside the fast retry budget says the reconciler retries it; the attempt
// that spends the budget does not.
func TestProvisionAttemptIsOneUnitMarkedWhileRetrying(t *testing.T) {
	backend := &fakeBackend{provisionErr: &StepError{Phase: PhaseStart, Retryable: true, Err: errors.New("start failed")}}
	svc, repo, _, clk := newTestService(t, backend)
	buf := withJobLog(svc)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")

	for attempt, wantRetry := range []bool{true, true, false} {
		if attempt > 0 {
			clk.advance(time.Hour)
		}
		_, _ = svc.ReconcileOnce(ctx)
		record := oneJob(t, buf, "workspace.provision")
		if record["bot_id"] != bot || record["desired"] != DesiredPresent {
			t.Fatalf("attempt %d record = %v, want the workspace's fields", attempt+1, record)
		}
		if got, _ := record["will_retry"].(bool); got != wantRetry {
			t.Fatalf("attempt %d will_retry = %v, want %v", attempt+1, record["will_retry"], wantRetry)
		}
		if !strings.Contains(record["error"].(string), "start failed") {
			t.Fatalf("attempt %d error = %v, want the step's failure", attempt+1, record["error"])
		}
	}
	// The row keeps the code; the step's text lives in the unit's record.
	if got := repo.get(bot); got.LastError != "" || got.LastErrorCode != "workspace_setup_failed" {
		t.Fatalf("last_error/code = %q/%q, want empty/workspace_setup_failed", got.LastError, got.LastErrorCode)
	}
}

// A workspace image the user named wrong is recorded under the same code in
// the row and in the unit's result.
func TestProvisionImageNotFoundNamesTheCodeInTheResult(t *testing.T) {
	backend := &fakeBackend{provisionErr: &StepError{Phase: PhaseImagePrepare, Err: fmt.Errorf("pull image: %w", ErrImageNotFound)}}
	svc, repo, _, _ := newTestService(t, backend)
	buf := withJobLog(svc)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)

	record := oneJob(t, buf, "workspace.provision")
	if record["reason"] != "workspace.image_not_found" {
		t.Fatalf("record reason = %v, want workspace.image_not_found", record["reason"])
	}
	if got := repo.get(bot).LastErrorCode; got != "workspace.image_not_found" {
		t.Fatalf("last_error_code = %q, want workspace.image_not_found", got)
	}
}

func TestProvisionSuccessIsOneInfoUnit(t *testing.T) {
	svc, _, _, _ := newTestService(t, &fakeBackend{})
	buf := withJobLog(svc)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)

	if record := oneJob(t, buf, "workspace.provision"); record["level"] != "INFO" {
		t.Fatalf("record = %v, want INFO", record)
	}
}

// A claimant that lost the row to another instance abandons the pass; that is
// a skip, not a failure.
func TestSupersededProvisionIsASkip(t *testing.T) {
	svc, repo, _, _ := newTestService(t, &fakeBackend{})
	buf := withJobLog(svc)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	repo.beforeWrite = func(w *Workspace) { w.Version++ }
	_, _ = svc.ReconcileOnce(ctx)

	record := oneJob(t, buf, "workspace.provision")
	if record["level"] != "INFO" || record["skipped"] != "version_conflict" {
		t.Fatalf("record = %v, want an INFO skip", record)
	}
}

func TestTeardownAttemptIsOneUnitMarkedWhileRetrying(t *testing.T) {
	backend := &fakeBackend{}
	svc, _, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	buf := withJobLog(svc)
	backend.teardownErr = errors.New("remove failed")
	if _, err := svc.RequestAbsent(ctx, bot, false); err != nil {
		t.Fatal(err)
	}

	for attempt, wantRetry := range []bool{true, true, false} {
		if attempt > 0 {
			clk.advance(time.Hour)
		}
		_, _ = svc.ReconcileOnce(ctx)
		record := oneJob(t, buf, "workspace.teardown")
		if got, _ := record["will_retry"].(bool); got != wantRetry {
			t.Fatalf("attempt %d will_retry = %v, want %v", attempt+1, record["will_retry"], wantRetry)
		}
	}
}
