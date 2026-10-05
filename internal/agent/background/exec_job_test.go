package background

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// jobRecords waits until the task's unit has written its result record and
// returns every log record seen so far.
func jobRecords(t *testing.T, buf *lockedBuffer) (job map[string]any, all []map[string]any) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf.mu.Lock()
		text := buf.buf.String()
		buf.mu.Unlock()
		all = all[:0]
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("decode log line %q: %v", line, err)
			}
			all = append(all, record)
			if record["msg"] == "job" {
				job = record
			}
		}
		if job != nil {
			return job, all
		}
		if time.Now().After(deadline) {
			t.Fatalf("no job record; got %v", all)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func spawnWith(t *testing.T, execFn ExecFunc, readFn ReadFileFunc) (*Manager, string, *lockedBuffer) {
	t.Helper()
	buf := &lockedBuffer{}
	mgr := New(logger.New(buf, "debug", "json"))
	taskID, _ := mgr.Spawn(context.Background(), "bot1", "sess1", "cmd", "/data", "Command", execFn, nil, readFn)
	return mgr, taskID, buf
}

func TestBackgroundNonZeroExitIsAnInfoUnit(t *testing.T) {
	_, taskID, buf := spawnWith(t, func(context.Context, string, string, int32) (*bridge.ExecResult, error) {
		return &bridge.ExecResult{ExitCode: 2}, nil
	}, nil)

	record, all := jobRecords(t, buf)
	if record["level"] != "INFO" || record["operation"] != "background.exec" || record["task_id"] != taskID || record["bot_id"] != "bot1" {
		t.Fatalf("job record = %v, want an INFO background.exec unit for the task", record)
	}
	if record["status"] != string(TaskFailed) || record["exit_code"] != float64(2) {
		t.Fatalf("status/exit_code = %v/%v, want failed/2", record["status"], record["exit_code"])
	}
	if _, ok := record["error"]; ok {
		t.Fatalf("a non-zero exit carries an error: %v", record)
	}
	for _, r := range all {
		if r["msg"] == "background task finished" {
			t.Fatalf("the unit's record duplicates a finished line: %v", all)
		}
	}
}

func TestBackgroundExecutionErrorWithoutExitIsAFailedUnit(t *testing.T) {
	_, _, buf := spawnWith(t, func(context.Context, string, string, int32) (*bridge.ExecResult, error) {
		return nil, errors.New("stream broke before exit")
	}, func(context.Context, string) ([]byte, error) {
		return nil, errors.New("no sentinel")
	})

	record, all := jobRecords(t, buf)
	if record["level"] != "ERROR" || record["exit_code"] != float64(-1) {
		t.Fatalf("job record = %v, want ERROR with exit_code -1", record)
	}
	if msg, _ := record["error"].(string); !strings.Contains(msg, "execute background command") {
		t.Fatalf("error = %v, want the execution failure", record["error"])
	}
	for _, r := range all {
		if r["msg"] == "background task: execFn returned error" {
			t.Fatalf("the failure is logged twice: %v", all)
		}
	}
}

func TestBackgroundRecoveredExitIsNotAFailure(t *testing.T) {
	_, _, buf := spawnWith(t, func(context.Context, string, string, int32) (*bridge.ExecResult, error) {
		return nil, errors.New("stream broke after exit")
	}, func(context.Context, string) ([]byte, error) {
		return []byte("0\n"), nil
	})

	record, _ := jobRecords(t, buf)
	if record["level"] != "INFO" || record["status"] != string(TaskCompleted) || record["exit_code"] != float64(0) {
		t.Fatalf("job record = %v, want INFO completed/0", record)
	}
}

func TestBackgroundKillIsNotAFailure(t *testing.T) {
	mgr, taskID, buf := spawnWith(t, func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	if err := mgr.KillForSession("bot1", "sess1", taskID); err != nil {
		t.Fatalf("KillForSession: %v", err)
	}

	record, _ := jobRecords(t, buf)
	if record["level"] != "INFO" || record["status"] != string(TaskKilled) {
		t.Fatalf("job record = %v, want INFO killed", record)
	}
}
