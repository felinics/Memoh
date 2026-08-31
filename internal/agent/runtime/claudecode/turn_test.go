package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type testCLIProcess struct {
	bytes.Buffer
	done chan struct{}
}

func (*testCLIProcess) CloseStdin()             {}
func (p *testCLIProcess) Done() <-chan struct{} { return p.done }
func (*testCLIProcess) StderrTail() string      { return "" }
func (*testCLIProcess) Close() error            { return nil }

type recordingSink struct {
	mu     sync.Mutex
	events []event.StreamEvent
}

type cancelAwareApproval struct {
	waiting  chan struct{}
	rejected chan struct{}
}

func (*cancelAwareApproval) EvaluatePolicy(context.Context, approval.CreatePendingInput) (approval.Evaluation, error) {
	return approval.Evaluation{Decision: approval.DecisionNeedsApproval}, nil
}

func (*cancelAwareApproval) CreatePending(_ context.Context, input approval.CreatePendingInput) (approval.Request, error) {
	return approval.Request{
		ID: "approval-1", BotID: input.BotID, SessionID: input.SessionID,
		ToolCallID: input.ToolCallID, ToolName: input.ToolName, Status: approval.StatusPending,
	}, nil
}

func (*cancelAwareApproval) Get(context.Context, string) (approval.Request, error) {
	return approval.Request{}, approval.ErrNotFound
}

func (a *cancelAwareApproval) Reject(context.Context, string, string, string) (approval.Request, error) {
	close(a.rejected)
	return approval.Request{ID: "approval-1", Status: approval.StatusRejected}, nil
}

func (a *cancelAwareApproval) WaitForDecision(ctx context.Context, _ string) (approval.Request, error) {
	close(a.waiting)
	<-ctx.Done()
	return approval.Request{}, ctx.Err()
}

func (s *recordingSink) EmitStreamEvent(ev event.StreamEvent) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *recordingSink) snapshot() []event.StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.StreamEvent(nil), s.events...)
}

func newTestRunner(sink *recordingSink) *turnRunner {
	return newTurnRunner(context.Background(), external.PromptInput{
		BotID:    "bot-1",
		ThreadID: "session-1",
		Prompt:   "fix the bug",
		Sink:     sink,
	}, &testCLIProcess{done: make(chan struct{})}, nil, nil, slog.Default())
}

func feed(t *testing.T, r *turnRunner, line string) {
	t.Helper()
	msg, err := decodeInbound([]byte(line))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r.handleMessage(msg)
}

func TestClaudeTurnMapping(t *testing.T) {
	sink := &recordingSink{}
	r := newTestRunner(sink)

	feed(t, r, `{"type":"system","subtype":"init","session_id":"sess-abc","model":"claude-sonnet-5","claude_code_version":"2.1.250","capabilities":["interrupt_receipt_v1"],"tools":[]}`)
	feed(t, r, `{"type":"stream_event","session_id":"sess-abc","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`)
	feed(t, r, `{"type":"stream_event","session_id":"sess-abc","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Looking"}}}`)
	feed(t, r, `{"type":"assistant","session_id":"sess-abc","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"text","text":"Looking"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"go test ./..."}}]}}`)
	feed(t, r, `{"type":"user","session_id":"sess-abc","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"ok"}],"is_error":false}]}}`)
	feed(t, r, `{"type":"assistant","session_id":"sess-abc","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_memoh","name":"mcp__memoh__ask_user","input":{"questions":[]}}]}}`)
	feed(t, r, `{"type":"user","session_id":"sess-abc","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_memoh","content":{"status":"submitted"},"is_error":false}]}}`)
	// Subagent traffic must not leak into the main stream.
	feed(t, r, `{"type":"stream_event","session_id":"sess-abc","parent_tool_use_id":"tu_2","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"sub"}}}`)
	feed(t, r, `{"type":"result","subtype":"success","session_id":"sess-abc","is_error":false,"result":"Done. Tests pass.","usage":{"input_tokens":1200,"cache_read_input_tokens":800,"cache_creation_input_tokens":0,"output_tokens":90},"total_cost_usd":0.031}`)

	select {
	case <-r.done:
	default:
		t.Fatal("result did not finish the turn")
	}

	result, err := r.buildResult("")
	if err != nil {
		t.Fatal(err)
	}
	r.close()

	types := []event.StreamEventType{}
	for _, ev := range sink.snapshot() {
		types = append(types, ev.Type)
	}
	want := []event.StreamEventType{
		event.ReasoningDelta,
		event.TextDelta,
		event.ToolCallStart,
		event.ToolCallEnd,
	}
	if len(types) != len(want) {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event[%d] = %s, want %s", i, types[i], want[i])
		}
	}

	if !result.TurnCompleted || result.StopReason != "success" {
		t.Fatalf("result: %+v", result)
	}
	if result.Text != "Done. Tests pass." {
		t.Fatalf("text = %q", result.Text)
	}
	if result.Usage == nil || result.Usage.InputTokens != 1200 || result.Usage.CachedInputTokens != 800 {
		t.Fatalf("usage: %+v", result.Usage)
	}
	if result.RuntimeMetadata[metadataSessionIDKey] != "sess-abc" {
		t.Fatalf("runtime metadata: %+v", result.RuntimeMetadata)
	}
	if len(result.Output) == 0 {
		t.Fatal("transcript output is empty")
	}
	if raw, _ := json.Marshal(result.Output); !json.Valid(raw) {
		t.Fatal("transcript not serializable")
	}
}

func TestClaudeTurnErrorResult(t *testing.T) {
	sink := &recordingSink{}
	r := newTestRunner(sink)
	feed(t, r, `{"type":"system","subtype":"init","session_id":"sess-err","claude_code_version":"2.1.250"}`)
	feed(t, r, `{"type":"result","subtype":"error_during_execution","session_id":"sess-err","is_error":true,"result":"credit balance is too low"}`)

	r.close()
	result, err := r.buildResult("sess-err")
	if err == nil {
		t.Fatal("error result must return an error")
	}
	if result.TurnCompleted {
		t.Fatal("failed turn must not report completion")
	}
	if result.RuntimeMetadata != nil {
		t.Fatalf("unchanged session id must not produce a metadata delta: %+v", result.RuntimeMetadata)
	}
}

func TestClaudeTurnDropsNoResponseSentinel(t *testing.T) {
	sink := &recordingSink{}
	r := newTestRunner(sink)
	feed(t, r, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"ask-1","name":"ask_user","input":{}}]}}`)
	feed(t, r, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"ask-1","content":{"status":"submitted"},"is_error":false}]}}`)
	feed(t, r, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"No response "}}}`)
	feed(t, r, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"requested."}}}`)
	feed(t, r, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"No response requested."}]}}`)
	feed(t, r, `{"type":"result","subtype":"success","session_id":"sess-abc","is_error":false,"result":"No response requested."}`)

	result, err := r.buildResult("")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "" {
		t.Fatalf("text = %q, want empty", result.Text)
	}
	if len(result.Output) != 2 {
		t.Fatalf("output = %#v, want tool call and result only", result.Output)
	}
}

func TestClaudeTurnProcessExitWithoutResult(t *testing.T) {
	sink := &recordingSink{}
	r := newTestRunner(sink)
	feed(t, r, `{"type":"system","subtype":"init","session_id":"s","claude_code_version":"2.1.250"}`)
	r.finish()
	r.close()
	if _, err := r.buildResult(""); err == nil {
		t.Fatal("missing result must be an error")
	}
}

func TestClaudeEmitAfterCloseIsDropped(t *testing.T) {
	sink := &recordingSink{}
	r := newTestRunner(sink)
	r.close()
	r.emit(event.StreamEvent{Type: event.TextDelta, Delta: "late"})
	time.Sleep(10 * time.Millisecond)
	if len(sink.snapshot()) != 0 {
		t.Fatal("post-close emit must be dropped")
	}
}

func TestClaudeControlCancelStopsPendingApproval(t *testing.T) {
	sink := &recordingSink{}
	approvals := &cancelAwareApproval{waiting: make(chan struct{}), rejected: make(chan struct{})}
	proc := &testCLIProcess{done: make(chan struct{})}
	r := newTurnRunner(context.Background(), external.PromptInput{
		BotID: "bot-1", ThreadID: "session-1", Sink: sink, CanRequestUserInput: true,
	}, proc, approvals, nil, slog.Default())

	feed(t, r, `{"type":"control_request","request_id":"request-1","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool-1","input":{"command":"pwd"}}}`)
	select {
	case <-approvals.waiting:
	case <-time.After(time.Second):
		t.Fatal("approval did not begin waiting")
	}
	feed(t, r, `{"type":"control_cancel_request","request_id":"request-1"}`)
	select {
	case <-approvals.rejected:
	case <-time.After(time.Second):
		t.Fatal("cancelled control request did not reject its pending approval")
	}
	time.Sleep(10 * time.Millisecond)
	if strings.Contains(proc.String(), `"type":"control_response"`) {
		t.Fatalf("cancelled control request emitted a late response: %s", proc.String())
	}
	r.close()
}

// Claude CLI tool names must land on Memoh's policy operations; otherwise a
// runtime-local spelling such as Bash silently bypasses the configured rule.
func TestCanonicalPolicyToolNames(t *testing.T) {
	for cliName, want := range map[string]string{
		"Bash":  approval.OperationExec,
		"Write": approval.OperationWrite,
		"Edit":  approval.OperationWrite,
		"Read":  approval.OperationRead,
		"Glob":  approval.OperationRead,
		"Grep":  approval.OperationRead,
	} {
		operation, ok := approval.OperationForTool(canonicalPolicyToolName(cliName))
		if !ok || operation != want {
			t.Fatalf("%s maps to %q (known=%t), want %q", cliName, operation, ok, want)
		}
	}
}

func TestCanonicalDisplayToolCall(t *testing.T) {
	name, input := canonicalDisplayToolCall("Write", map[string]any{
		"file_path": "/data/result.txt",
		"content":   "ok",
	})
	fields := input.(map[string]any)
	if name != "write" || fields["path"] != "/data/result.txt" || fields["content"] != "ok" {
		t.Fatalf("canonical write = %q %#v", name, fields)
	}
	if _, exists := fields["file_path"]; exists {
		t.Fatalf("canonical write retained file_path: %#v", fields)
	}
}

func TestClaudeModelCatalogFromInitialize(t *testing.T) {
	catalog := modelCatalogFromInitialize("custom-model", initializeResponse{Models: []initializeModel{
		{Value: "default", ResolvedModel: "claude-opus-5", DisplayName: "Default"},
		{Value: "opus", ResolvedModel: "claude-opus-5", DisplayName: "Opus", SupportsEffort: true, SupportedEffortLevels: []string{"low", "high"}},
		{Value: "haiku", ResolvedModel: "claude-haiku-4-5", DisplayName: "Haiku"},
	}})
	if catalog.ConfiguredModelID != "custom-model" || len(catalog.Models) != 3 {
		t.Fatalf("catalog = %#v", catalog)
	}
	if catalog.Models[0].ID != "custom-model" || catalog.Models[1].ID != "opus" || len(catalog.Models[1].ReasoningEfforts) != 2 {
		t.Fatalf("catalog models = %#v", catalog.Models)
	}
}

func TestClaudeCLIArgsIncludeTurnOverrides(t *testing.T) {
	args := strings.Join(cliArgs(claudecfg.Config{}, external.PromptInput{ModelID: "sonnet", ReasoningEffort: "high"}, "", ""), " ")
	if !strings.Contains(args, "--model 'sonnet'") || !strings.Contains(args, "--effort 'high'") {
		t.Fatalf("args = %q", args)
	}
}
