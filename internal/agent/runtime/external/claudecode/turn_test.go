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

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

type testCLIProcess struct {
	bytes.Buffer
	done chan struct{}
}

func (*testCLIProcess) CloseStdin()             {}
func (*testCLIProcess) Kill()                   {}
func (p *testCLIProcess) Done() <-chan struct{} { return p.done }
func (*testCLIProcess) StderrTail() string      { return "" }
func (*testCLIProcess) Close() error            { return nil }

type recordingSink struct {
	mu     sync.Mutex
	events []event.StreamEvent
}

type cancelAwareApproval struct {
	waiting   chan struct{}
	cancelled chan struct{}
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

func (*cancelAwareApproval) Reject(context.Context, string, string, string) (approval.Request, error) {
	return approval.Request{ID: "approval-1", Status: approval.StatusRejected}, nil
}

func (a *cancelAwareApproval) Cancel(context.Context, string, string) (approval.Request, error) {
	close(a.cancelled)
	return approval.Request{ID: "approval-1", Status: approval.StatusCancelled}, nil
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

	feed(t, r, `{"type":"system","subtype":"init","session_id":"sess-abc","model":"claude-sonnet-5","claude_code_version":"2.1.269","capabilities":["interrupt_receipt_v1"],"tools":[]}`)
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
		event.ReasoningStart,
		event.ReasoningDelta,
		event.ReasoningEnd,
		event.TextDelta,
		event.TextEnd,
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
	if result.Usage == nil || result.Usage.InputTokens != 2000 || result.Usage.CachedInputTokens != 800 {
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
	for _, partial := range []string{"", "Partial reply."} {
		t.Run(partial, func(t *testing.T) {
			sink := &recordingSink{}
			r := newTestRunner(sink)
			defer r.close()
			feed(t, r, `{"type":"system","subtype":"init","session_id":"sess-err","claude_code_version":"2.1.269"}`)
			feed(t, r, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"`+partial+`"}}}`)
			// The CLI wraps API diagnostics in assistant frames before its error result.
			feed(t, r, `{"type":"assistant","error":"unknown","message":{"content":[{"type":"text","text":"PRIVATE diagnostic"}]}}`)
			feed(t, r, `{"type":"result","subtype":"success","session_id":"sess-err","is_error":true,"result":"PRIVATE diagnostic"}`)

			result, err := r.buildResult("sess-err")
			if err == nil || !strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("error result must retain the private diagnostic: %v", err)
			}
			if result.TurnCompleted || result.Text != partial {
				t.Fatalf("failed turn must preserve only model text: %+v", result)
			}
			if result.RuntimeMetadata != nil {
				t.Fatalf("unchanged session id must not produce a metadata delta: %+v", result.RuntimeMetadata)
			}
			for name, value := range map[string]any{"stream": sink.snapshot(), "history": result.Output} {
				raw, _ := json.Marshal(value)
				if bytes.Contains(raw, []byte("PRIVATE")) || (partial != "" && !bytes.Contains(raw, []byte(partial))) {
					t.Errorf("%s must preserve the reply without API diagnostics: %s", name, raw)
				}
			}
		})
	}
}

func TestClaudeControlCancelStopsPendingApproval(t *testing.T) {
	sink := &recordingSink{}
	approvals := &cancelAwareApproval{waiting: make(chan struct{}), cancelled: make(chan struct{})}
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
	case <-approvals.cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancelled control request did not cancel its pending approval")
	}
	time.Sleep(10 * time.Millisecond)
	if strings.Contains(proc.String(), `"type":"control_response"`) {
		t.Fatalf("cancelled control request emitted a late response: %s", proc.String())
	}
	r.close()
}

// A tool outside the approval vocabulary is presented as a generic permission
// card. The card is its own transcript entry; the real tool call keeps its
// arguments.
func TestClaudePermissionCardKeepsToolArguments(t *testing.T) {
	sink := &recordingSink{}
	r := newTurnRunner(context.Background(), external.PromptInput{
		BotID: "bot-1", ThreadID: "session-1", Sink: sink, CanRequestUserInput: true,
	}, &testCLIProcess{done: make(chan struct{})}, &explicitRuntimeApproval{status: approval.StatusApproved}, nil, slog.Default())
	feed(t, r, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_glob","name":"Glob","input":{"pattern":"*.go"}}]}}`)
	r.handleCanUseTool(r.ctx, "request-1", &controlRequestPayload{ToolName: "Glob", ToolUseID: "tu_glob", Input: map[string]any{"pattern": "*.go"}})
	feed(t, r, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_glob","content":"main.go","is_error":false}]}}`)
	feed(t, r, `{"type":"result","subtype":"success","result":"Done."}`)
	result, err := r.buildResult("")
	r.close()
	if err != nil {
		t.Fatal(err)
	}
	var cardID string
	for _, ev := range sink.snapshot() {
		if ev.Type == event.ToolApprovalRequest {
			cardID = ev.ToolCallID
		}
	}
	if cardID == "" || cardID == "tu_glob" || !strings.HasPrefix(cardID, "claude-permission-") {
		t.Fatalf("permission card id = %q, must not reuse the tool_use id", cardID)
	}
	var seen bool
	for _, message := range result.Output {
		for _, part := range message.Content {
			call, ok := part.(sdk.ToolCallPart)
			if !ok || call.ToolCallID != "tu_glob" {
				continue
			}
			seen = true
			fields, _ := toolexec.ArgumentsValue(call.Input).(map[string]any)
			if call.ToolName != "Glob" || fields["pattern"] != "*.go" {
				t.Fatalf("tool call lost its arguments: %#v", call)
			}
		}
	}
	if !seen {
		t.Fatal("tool call missing from the transcript")
	}
}

func TestClaudeModelCatalogFromInitialize(t *testing.T) {
	catalog := modelCatalogFromInitialize("custom-model", initializeResponse{Models: []initializeModel{
		{Value: "default", ResolvedModel: "model-a-v1", DisplayName: "Default"},
		{Value: "model-a", ResolvedModel: "model-a-v1", DisplayName: "Model A", SupportsEffort: true, SupportedEffortLevels: []string{"low", "high"}, SupportsAutoMode: true},
		{Value: "model-b", ResolvedModel: "model-b-v1", DisplayName: "Model B"},
	}})
	if catalog.ConfiguredModelID != "custom-model" || len(catalog.Models) != 3 {
		t.Fatalf("catalog = %#v", catalog)
	}
	if catalog.Models[0].ID != "custom-model" || catalog.Models[1].ID != "model-a" || len(catalog.Models[1].ReasoningEfforts) != 2 {
		t.Fatalf("catalog models = %#v", catalog.Models)
	}
	if catalog.Models[1].ReasoningEfforts[0].ID != "low" || catalog.Models[1].ReasoningEfforts[1].ID != "high" || len(catalog.Models[2].ReasoningEfforts) != 0 {
		t.Fatalf("efforts must come from the runtime: %#v", catalog.Models)
	}
	if len(catalog.Models[1].UnavailablePermissionModes) != 0 || len(catalog.Models[2].UnavailablePermissionModes) != 1 || catalog.Models[2].UnavailablePermissionModes[0] != "auto" {
		t.Fatalf("permissions must follow capabilities, not model names: %#v", catalog.Models)
	}
}

func TestClaudeControlCLIRunsNoWorkspaceHooksOrMCPServers(t *testing.T) {
	args := strings.Join(controlCLIArgs(claudecfg.Config{}, ""), " ")
	if !strings.Contains(args, `--settings '{"disableAllHooks":true}'`) || !strings.Contains(args, "--strict-mcp-config") {
		t.Fatalf("args = %q", args)
	}
	if args := strings.Join(cliArgs("", "", "", "", "", false), " "); strings.Contains(args, "disableAllHooks") || strings.Contains(args, "--strict-mcp-config") {
		t.Fatalf("a turn must keep the workspace's hooks and MCP servers: %q", args)
	}
}

func (*testCLIProcess) Err() error { return nil }
