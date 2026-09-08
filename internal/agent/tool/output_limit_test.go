package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
)

type outputLimitCaptureSink struct {
	events   []trajectory.Event
	contents map[string]string
}

func (s *outputLimitCaptureSink) Append(_ context.Context, event trajectory.Event, contents []trajectory.Content) error {
	s.events = append(s.events, event)
	for _, content := range contents {
		s.contents[content.Hash] = string(content.Data)
	}
	return nil
}

func TestToolErrorTrajectoryPreservesOriginalAndLimitedError(t *testing.T) {
	sink := &outputLimitCaptureSink{contents: make(map[string]string)}
	recorder := trajectory.NewRecorder(sink)
	recorder.Bind("run", "session")
	marker := "ORIGINAL_ERROR_MIDDLE"
	wrapped := WrapToolOutputLimits([]sdk.Tool{{
		Name: "failure",
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return nil, errors.New(strings.Repeat("left ", 500) + marker + strings.Repeat(" right", 500))
		},
	}}, ToolOutputLimit{MaxBytes: 512, MaxLines: 80})
	_, err := wrapped[0].Execute(&sdk.ToolExecContext{Context: trajectory.WithRecorder(t.Context(), recorder), ToolCallID: "error-call"}, nil)
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatal("fixture did not limit the tool error")
	}
	if len(sink.events) != 1 || len(sink.events[0].Blocks) != 3 {
		t.Fatalf("missing transformation capture: %#v", sink.events)
	}
	original, limited := sink.events[0].Blocks[1], sink.events[0].Blocks[2]
	if !strings.Contains(sink.contents[original.Chunks[0]], marker) || strings.Contains(sink.contents[limited.Chunks[0]], marker) {
		t.Fatal("original and limited errors cannot be compared")
	}
}

func TestLimitToolOutputPrunesLargeStringLeaves(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL"
	result := LimitToolOutput(map[string]any{
		"content": large,
		"ok":      true,
	}, "test_tool", ToolOutputLimit{MaxBytes: 512, MaxLines: 80})

	structured, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want map", result)
	}
	content, ok := structured["content"].(string)
	if !ok {
		t.Fatalf("content = %#v, want string", structured["content"])
	}
	if len(content) >= len(large) {
		t.Fatalf("content was not pruned: got %d bytes, original %d", len(content), len(large))
	}
	if !strings.Contains(content, "[memoh pruned]") {
		t.Fatalf("content missing prune marker:\n%s", content)
	}
	if !strings.Contains(content, "HEAD") || !strings.Contains(content, "TAIL") {
		t.Fatalf("content did not preserve head and tail:\n%s", content)
	}
	if structured["ok"] != true {
		t.Fatalf("non-string field changed: %#v", structured)
	}
}

func TestLimitToolOutputFallsBackWhenStructuredJSONStillExceedsLimit(t *testing.T) {
	t.Parallel()

	large := map[string]any{}
	for i := 0; i < 24; i++ {
		large[fmt.Sprintf("key_%02d", i)] = strings.Repeat("x", 128)
	}

	result := LimitToolOutput(large, "huge_tool", ToolOutputLimit{MaxBytes: 512, MaxLines: 120})
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if len(raw) > 512 {
		t.Fatalf("result JSON bytes = %d, want <= 512\n%s", len(raw), raw)
	}
	structured, ok := result.(map[string]any)
	if !ok || structured["_memoh_truncated"] != true {
		t.Fatalf("result = %#v, want fallback truncation marker", result)
	}
}

func TestLimitToolOutputNormalizesTinyPositiveByteLimit(t *testing.T) {
	t.Parallel()

	result := LimitToolOutput(map[string]any{
		"content": strings.Repeat("x", 1024),
	}, "tiny_tool", ToolOutputLimit{MaxBytes: 1, MaxLines: 80})

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if len(raw) > 256 {
		t.Fatalf("result JSON bytes = %d, want <= normalized minimum 256\n%s", len(raw), raw)
	}
}

func TestLimitToolOutputPreservesErrorSignalOnFallback(t *testing.T) {
	t.Parallel()

	result := LimitToolOutput(map[string]any{
		"isError": true,
		"content": "HEAD\n" + strings.Repeat("error detail ", 300) + "\nTAIL",
	}, "error_tool", ToolOutputLimit{MaxBytes: 512, MaxLines: 80})

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if len(raw) > 512 {
		t.Fatalf("result JSON bytes = %d, want <= 512\n%s", len(raw), raw)
	}
	structured, ok := result.(map[string]any)
	if !ok || structured["isError"] != true {
		t.Fatalf("result = %#v, want isError preserved", result)
	}
}

func TestWrapToolOutputLimitsPrunesErrors(t *testing.T) {
	t.Parallel()

	wrapped := WrapToolOutputLimits([]sdk.Tool{{
		Name: "broken_tool",
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return nil, errors.New("HEAD\n" + strings.Repeat("error detail ", 300) + "\nTAIL")
		},
	}}, ToolOutputLimit{MaxBytes: 512, MaxLines: 80})

	_, err := wrapped[0].Execute(&sdk.ToolExecContext{Context: context.Background(), ToolName: "broken_tool"}, nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want limited error")
	}
	if len(err.Error()) >= len("HEAD\n"+strings.Repeat("error detail ", 300)+"\nTAIL") {
		t.Fatalf("error was not pruned: %d bytes", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "[memoh pruned]") {
		t.Fatalf("limited error missing prune marker:\n%s", err.Error())
	}
	if !strings.Contains(err.Error(), "HEAD") || !strings.Contains(err.Error(), "TAIL") {
		t.Fatalf("limited error did not preserve head and tail:\n%s", err.Error())
	}
}
