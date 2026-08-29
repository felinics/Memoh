package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestBackgroundProviderWaitAndInspectAgentResult(t *testing.T) {
	mgr := background.New(nil)
	p := NewBackgroundProvider(nil, mgr)
	session := SessionContext{BotID: "bot1", SessionID: "sess1"}

	taskID, _, err := mgr.StartAgentTask(context.Background(), "bot1", "sess1", "worker", "child-1", "input task", "worker: input task", false)
	if err != nil {
		t.Fatalf("StartAgentTask failed: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(10 * time.Millisecond)
		mgr.CompleteAgentTask(taskID, background.AgentTaskResult{
			AgentID:        "worker",
			AgentSessionID: "child-1",
			Message:        "input task",
			Status:         background.TaskCompleted,
			Report:         "final assistant text",
		})
	}()

	waitRes, err := p.execWaitUntil(context.Background(), session, map[string]any{"task_id": taskID}, nil)
	if err != nil {
		t.Fatalf("wait_until failed: %v", err)
	}
	if waitRes.(map[string]any)["status"] != "completed" {
		t.Fatalf("wait_until payload = %v, want completed", waitRes)
	}
	<-done

	statusRes, err := p.execGetBackgroundStatus(context.Background(), session, map[string]any{"task_id": taskID})
	if err != nil {
		t.Fatalf("get_background_status failed: %v", err)
	}
	sm := statusRes.(map[string]any)
	if sm["input"] != "input task" {
		t.Fatalf("input = %v, want original task", sm["input"])
	}
	if sm["result"] != "final assistant text" {
		t.Fatalf("result = %v, want final assistant text", sm["result"])
	}
	if _, ok := sm["message"]; ok {
		t.Fatalf("status payload should not use message for agent result: %v", sm)
	}
}

func TestBackgroundProviderSpawnResultShape(t *testing.T) {
	mgr := background.New(nil)
	p := NewBackgroundProvider(nil, mgr)
	session := SessionContext{BotID: "bot1", SessionID: "sess1"}

	taskID, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "spawn 2 task(s): alpha | beta")
	if err != nil {
		t.Fatalf("StartSpawnTask failed: %v", err)
	}
	mgr.CompleteSpawnTask(taskID, []background.SpawnBranch{
		{Task: "alpha", ChildSessionID: "child-a", Status: background.TaskCompleted, Report: "found A"},
		{Task: "beta", Status: background.TaskFailed, Error: "boom"},
	})

	statusRes, err := p.execGetBackgroundStatus(context.Background(), session, map[string]any{"task_id": taskID})
	if err != nil {
		t.Fatalf("get_background_status failed: %v", err)
	}
	sm := statusRes.(map[string]any)
	if sm["kind"] != "spawn" || sm["status"] != "failed" {
		t.Errorf("unexpected spawn status payload: %v", sm)
	}
	result := sm["result"].(map[string]any)
	branches := result["branches"].([]map[string]any)
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}
	if branches[0]["session_id"] != "child-a" || branches[0]["result"] != "found A" {
		t.Errorf("unexpected first branch payload: %v", branches[0])
	}
	if branches[1]["error"] != "boom" {
		t.Errorf("unexpected second branch payload: %v", branches[1])
	}
	if _, ok := branches[0]["report"]; ok {
		t.Errorf("branch payload should use result instead of report: %v", branches[0])
	}
}

func TestBackgroundProviderVideoResultShape(t *testing.T) {
	mgr := background.New(nil)
	p := NewBackgroundProvider(nil, mgr)
	session := SessionContext{BotID: "bot1", SessionID: "sess1"}

	taskID, _, err := mgr.StartVideoTask(context.Background(), "bot1", "sess1", "generate video: waves")
	if err != nil {
		t.Fatalf("StartVideoTask failed: %v", err)
	}
	mgr.CompleteVideoTask(taskID, background.TaskCompleted, map[string]any{
		"job_id":           "job-1",
		"provider_status":  "succeeded",
		"model_id":         "video-model",
		"output_url":       "https://example.com/video.mp4",
		"path":             "/data/generated-videos/bg_bot1_abcd.mp4",
		"media_type":       "video/mp4",
		"size_bytes":       12,
		"duration_seconds": 8.0,
	}, "")

	statusRes, err := p.execGetBackgroundStatus(context.Background(), session, map[string]any{"task_id": taskID})
	if err != nil {
		t.Fatalf("get_background_status failed: %v", err)
	}
	sm := statusRes.(map[string]any)
	if sm["kind"] != "video" || sm["status"] != "completed" {
		t.Fatalf("unexpected video status payload: %v", sm)
	}
	result := sm["result"].(map[string]any)
	if result["job_id"] != "job-1" || result["path"] != "/data/generated-videos/bg_bot1_abcd.mp4" {
		t.Fatalf("video result not preserved: %v", result)
	}
	if _, ok := sm["exit_code"]; ok {
		t.Fatalf("video status should not expose exec exit_code: %v", sm)
	}
}

func TestBackgroundProviderListKillAndWait(t *testing.T) {
	mgr := background.New(nil)
	p := NewBackgroundProvider(nil, mgr)
	session := SessionContext{BotID: "bot1", SessionID: "sess1"}

	taskID, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "long spawn")
	if err != nil {
		t.Fatalf("StartSpawnTask failed: %v", err)
	}
	listRes, err := p.execListBackground(context.Background(), session, nil)
	if err != nil {
		t.Fatalf("list_background failed: %v", err)
	}
	entries := listRes.(map[string]any)["tasks"].([]map[string]any)
	if len(entries) != 1 || entries[0]["task_id"] != taskID {
		t.Fatalf("unexpected list payload: %v", listRes)
	}

	if _, err := p.execKillBackground(context.Background(), session, map[string]any{"task_id": taskID}); err != nil {
		t.Fatalf("kill_background failed: %v", err)
	}
	waitRes, err := p.execWaitUntil(context.Background(), session, map[string]any{"task_id": taskID}, nil)
	if err != nil {
		t.Fatalf("wait_until failed: %v", err)
	}
	if waitRes.(map[string]any)["status"] != "killed" {
		t.Fatalf("wait_until payload = %v, want killed", waitRes)
	}
	progressCount := 0
	if _, err := p.execWait(context.Background(), session, map[string]any{"duration": 0.001}, func(any) {
		progressCount++
	}); err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if progressCount == 0 {
		t.Fatal("wait did not emit progress")
	}
}

func TestBackgroundProviderWaitUntilEmitsProgressWhileWaiting(t *testing.T) {
	mgr := background.New(nil)
	p := NewBackgroundProvider(nil, mgr)
	session := SessionContext{BotID: "bot1", SessionID: "sess1"}

	taskID, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "long spawn")
	if err != nil {
		t.Fatalf("StartSpawnTask failed: %v", err)
	}

	var waitUntil sdk.Tool
	for _, tool := range mustTools(t, p, session) {
		if tool.Name == ToolWaitUntil().String() {
			waitUntil = tool
			break
		}
	}
	if waitUntil.Name == "" {
		t.Fatal("wait_until tool not registered")
	}

	progressCh := make(chan any, 1)
	done := make(chan struct{})
	var waitRes any
	var waitErr error
	go func() {
		defer close(done)
		waitRes, waitErr = waitUntil.Execute(&sdk.ToolExecContext{
			Context: context.Background(),
			SendProgress: func(content any) {
				select {
				case progressCh <- content:
				default:
				}
			},
		}, map[string]any{"task_id": taskID})
	}()

	select {
	case progress := <-progressCh:
		payload, ok := progress.(map[string]any)
		if !ok {
			t.Fatalf("progress payload = %T, want map", progress)
		}
		if payload["status"] != "waiting" || payload["task_id"] != taskID {
			t.Fatalf("progress payload = %v, want waiting task", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("wait_until did not emit progress while waiting")
	}

	mgr.CompleteSpawnTask(taskID, []background.SpawnBranch{{Task: "alpha", Status: background.TaskCompleted, Report: "done"}})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait_until did not return after task completed")
	}
	if waitErr != nil {
		t.Fatalf("wait_until failed: %v", waitErr)
	}
	if waitRes.(map[string]any)["status"] != "completed" {
		t.Fatalf("wait_until payload = %v, want completed", waitRes)
	}
}

func startRunningExecTask(t *testing.T, mgr *background.Manager) string {
	t.Helper()
	taskID, _ := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"npm run dev",
		"/data",
		"Dev server",
		func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil,
		nil,
	)
	t.Cleanup(func() { _ = mgr.Kill(taskID) })
	return taskID
}

func TestGetBackgroundStatusIncludesTailWhileRunning(t *testing.T) {
	mgr := background.New(nil)
	p := NewBackgroundProvider(nil, mgr)
	session := SessionContext{BotID: "bot1", SessionID: "sess1"}

	taskID := startRunningExecTask(t, mgr)
	mgr.RecordOutput(taskID, "stdout", "  ➜  Local: http://localhost:5173/\n")

	statusRes, err := p.execGetBackgroundStatus(context.Background(), session, map[string]any{"task_id": taskID})
	if err != nil {
		t.Fatalf("get_background_status failed: %v", err)
	}
	sm := statusRes.(map[string]any)
	if sm["status"] != "running" {
		t.Fatalf("status = %v, want running", sm["status"])
	}
	tail, _ := sm["output_tail"].(string)
	if !strings.Contains(tail, "localhost:5173") {
		t.Fatalf("output_tail = %q, want live tail while running", tail)
	}
	if _, ok := sm["exit_code"]; ok {
		t.Fatalf("running status should not expose exit_code: %v", sm)
	}
}

func TestWaitUntilReturnsIdleWithTailForQuietService(t *testing.T) {
	mgr := background.New(nil)
	p := NewBackgroundProvider(nil, mgr)
	session := SessionContext{BotID: "bot1", SessionID: "sess1"}

	taskID := startRunningExecTask(t, mgr)
	mgr.RecordOutput(taskID, "stdout", "  ➜  Local: http://localhost:5173/\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := p.execWaitUntil(ctx, session, map[string]any{"task_id": taskID, "idle_timeout": 1, "timeout": 30}, nil)
	if err != nil {
		t.Fatalf("wait_until failed: %v", err)
	}
	rm := res.(map[string]any)
	if rm["reason"] != "idle" || rm["status"] != "running" {
		t.Fatalf("wait_until payload = %v, want running/idle", rm)
	}
	tail, _ := rm["output_tail"].(string)
	if !strings.Contains(tail, "localhost:5173") {
		t.Fatalf("output_tail = %q, want ready banner", tail)
	}
}

func TestWaitUntilTimeoutReturnsSnapshotInsteadOfError(t *testing.T) {
	mgr := background.New(nil)
	p := NewBackgroundProvider(nil, mgr)
	session := SessionContext{BotID: "bot1", SessionID: "sess1"}

	taskID, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "long spawn")
	if err != nil {
		t.Fatalf("StartSpawnTask failed: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Kill(taskID) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := p.execWaitUntil(ctx, session, map[string]any{"task_id": taskID, "timeout": 1}, nil)
	if err != nil {
		t.Fatalf("wait_until should return a snapshot on timeout, got error: %v", err)
	}
	rm := res.(map[string]any)
	if rm["reason"] != "timeout" || rm["status"] != "running" {
		t.Fatalf("wait_until payload = %v, want running/timeout", rm)
	}
}

func mustTools(t *testing.T, p *BackgroundProvider, session SessionContext) []sdk.Tool {
	t.Helper()
	tools, err := p.Tools(context.Background(), session)
	if err != nil {
		t.Fatalf("Tools failed: %v", err)
	}
	return tools
}
