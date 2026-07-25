package view

import (
	"testing"
)

func TestApplyToolResultRecognizesSpawnBackgroundStartByShape(t *testing.T) {
	msg := &UIMessage{Type: UIMessageTool, Name: "spawn"}
	applyToolResultToUIMessage(msg, map[string]any{
		"status":      "background_started",
		"task_id":     "bg_bot1_aaaa",
		"kind":        "spawn",
		"task_count":  2,
		"description": "spawn 2 task(s): alpha | beta",
		"message":     "2 subagent task(s) started in background",
	})

	if msg.Background == nil {
		t.Fatal("expected background task recognized from spawn tool result")
	}
	if msg.Background.TaskID != "bg_bot1_aaaa" || msg.Background.Status != "running" {
		t.Errorf("unexpected background state: %+v", msg.Background)
	}
	if msg.Background.Command != "spawn 2 task(s): alpha | beta" {
		t.Errorf("expected description used as display label, got %q", msg.Background.Command)
	}
	if msg.Running == nil || !*msg.Running {
		t.Error("expected tool to be marked running")
	}
}

func TestApplyToolResultStillRecognizesExecBackgroundStart(t *testing.T) {
	msg := &UIMessage{Type: UIMessageTool, Name: "exec"}
	applyToolResultToUIMessage(msg, map[string]any{
		"status":      "auto_backgrounded",
		"task_id":     "bg_bot1_bbbb",
		"output_file": "/tmp/memoh-bg/bg_bot1_bbbb.log",
	})

	if msg.Background == nil {
		t.Fatal("expected background task recognized from exec tool result")
	}
	if msg.Background.Status != "running" || msg.Background.OutputFile != "/tmp/memoh-bg/bg_bot1_bbbb.log" {
		t.Errorf("unexpected background state: %+v", msg.Background)
	}
}

func TestApplyToolResultIgnoresTerminalTaskStatusPayloads(t *testing.T) {
	// Background status inspection results carry task_id with a terminal status;
	// they must not turn the tool card into a running background task.
	msg := &UIMessage{Type: UIMessageTool, Name: "get_background_status"}
	applyToolResultToUIMessage(msg, map[string]any{
		"task_id": "bg_bot1_cccc",
		"kind":    "exec",
		"status":  "completed",
	})

	if msg.Background != nil {
		t.Fatalf("expected no background task for terminal status payload, got %+v", msg.Background)
	}
	if msg.Running == nil || *msg.Running {
		t.Error("expected tool to be marked not running")
	}
}
