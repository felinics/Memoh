package background

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestVideoTaskCompletesStoresResultAndWakesWaiter(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartVideoTask(context.Background(), "bot1", "sess1", "generate video: waves")
	if err != nil {
		t.Fatalf("StartVideoTask returned error: %v", err)
	}

	if ok := mgr.RecordVideoTaskProgress(taskID, map[string]any{
		"job_id":          "job-1",
		"provider_status": "running",
		"progress":        0.5,
	}, "Video job job-1 is running"); !ok {
		t.Fatal("RecordVideoTaskProgress returned false")
	}
	mgr.CompleteVideoTask(taskID, TaskCompleted, map[string]any{
		"job_id":          "job-1",
		"provider_status": "succeeded",
		"path":            "/data/generated-videos/bg_bot1_abcd.mp4",
	}, "")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, _, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Kind != KindVideo || snap.Status != TaskCompleted {
		t.Fatalf("snapshot = %+v, want completed video task", snap)
	}
	if snap.Result["job_id"] != "job-1" || snap.Result["path"] == "" {
		t.Fatalf("video result not preserved: %+v", snap.Result)
	}
	if !strings.Contains(snap.OutputTail, "Video job job-1 is running") {
		t.Fatalf("output tail = %q, want progress line", snap.OutputTail)
	}
}

func TestKilledVideoTaskDoesNotComplete(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartVideoTask(context.Background(), "bot1", "sess1", "generate video")
	if err != nil {
		t.Fatalf("StartVideoTask returned error: %v", err)
	}
	if err := mgr.KillForSession("bot1", "sess1", taskID); err != nil {
		t.Fatalf("KillForSession returned error: %v", err)
	}
	mgr.CompleteVideoTask(taskID, TaskCompleted, map[string]any{"path": "/data/out.mp4"}, "")

	snap := mgr.Get(taskID).Snapshot()
	if snap.Status != TaskKilled {
		t.Fatalf("status = %s, want killed", snap.Status)
	}
	if snap.Result["path"] != nil {
		t.Fatalf("killed task should not be overwritten by completion: %+v", snap.Result)
	}
}
