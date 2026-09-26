package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

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

// Both native command receipts stay separate from streamed and complete replies.
func TestClaudeCommandOutputKeepsModelReply(t *testing.T) {
	for _, receipt := range []string{
		`{"type":"system","subtype":"local_command_output","content":"Goal set: review"}`,
		`{"type":"assistant","local_command_source":"<local-command-stdout>Goal set: review</local-command-stdout>","message":{"content":[{"type":"text","text":"Goal set: review"}]}}`,
	} {
		for _, tc := range []struct{ name, delta, reply, result string }{
			{"streamed", "Reviewed.", "Reviewed.", "Reviewed."},
			{"complete", "", "Reviewed.", "Reviewed."},
			{"no response", "", noResponseRequested, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				sink := &recordingSink{}
				r := newTestRunner(sink)
				defer r.close()
				r.input.Command = "goal"
				feed(t, r, receipt)
				feed(t, r, `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":3,"retry_delay_ms":1500,"error":"PRIVATE"}`)
				feed(t, r, `{"type":"system","subtype":"status","status":"compacting"}`)
				feed(t, r, `{"type":"stream_event","event":{"type":"message_start"}}`)
				feed(t, r, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"`+tc.delta+`"}}}`)
				feed(t, r, `{"type":"assistant","message":{"content":[{"type":"text","text":"`+tc.reply+`"}]}}`)
				feed(t, r, `{"type":"result","subtype":"success","result":"`+tc.result+`"}`)
				result, err := r.buildResult("")
				if err != nil || result.Text != tc.result {
					t.Fatalf("model reply: %q, %v", result.Text, err)
				}
				wantMessages := 1
				if tc.result != "" {
					wantMessages++
				}
				if len(result.Output) != wantMessages {
					t.Fatalf("receipt and reply must stay separate: %#v", result.Output)
				}
				command := result.Output[0].Content[0].(sdk.TextPart)
				if command.Text != "Goal set: review" || command.ProviderMetadata["runtime_command"] != "goal" {
					t.Fatalf("receipt lost its identity: %#v", command)
				}
				if tc.result != "" {
					reply := result.Output[1].Content[0].(sdk.TextPart)
					if reply.Text != tc.result || reply.ProviderMetadata != nil {
						t.Fatalf("reply: %#v", reply)
					}
				}
				var text strings.Builder
				for _, ev := range sink.snapshot() {
					if ev.Type == event.TextDelta {
						text.WriteString(ev.Delta)
					}
					if ev.Type == event.Retry {
						t.Fatal("API retry reset the whole turn")
					}
				}
				wire, _ := json.Marshal(sink.snapshot())
				if text.String() != tc.result || bytes.Contains(wire, []byte("PRIVATE")) {
					t.Fatalf("feedback leaked into text or diagnostics: %s", wire)
				}
			})
		}
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
			fields, _ := call.Input.(map[string]any)
			if call.ToolName != "Glob" || fields["pattern"] != "*.go" {
				t.Fatalf("tool call lost its arguments: %#v", call)
			}
		}
	}
	if !seen {
		t.Fatal("tool call missing from the transcript")
	}
}

// Exercise the reader as well as process teardown: both can fail after a result.
type failingCLIProcess struct {
	testCLIProcess
	err error
}

func (p *failingCLIProcess) Read(data []byte) (int, error) {
	n, err := p.Buffer.Read(data)
	if errors.Is(err, io.EOF) {
		err = p.err
	}
	return n, err
}

func TestClaudeExitFailureDoesNotOverrideResult(t *testing.T) {
	exitErr := errors.New("transport lost")
	for _, line := range []string{
		`{"type":"result","subtype":"success","result":"Done."}`,
		`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["Credit balance is too low"]}`,
		"",
	} {
		r := newTestRunner(&recordingSink{})
		p := &failingCLIProcess{testCLIProcess: testCLIProcess{done: make(chan struct{})}, err: exitErr}
		_, _ = p.WriteString(line + "\n")
		r.proc = p
		r.readLoop()
		r.observeExit(exitErr)
		r.close()
		result, err := r.buildResult("")
		switch {
		case line == "":
			if !errors.Is(err, exitErr) || result.StopReason != "process_exit" {
				t.Fatalf("missing result: %+v, %v", result, err)
			}
		case strings.Contains(line, "is_error"):
			if err == nil || err.Error() != "Credit balance is too low" || result.Text != "" {
				t.Fatalf("diagnostic lost or published as prose: %+v, %v", result, err)
			}
		default:
			if err != nil || !result.TurnCompleted {
				t.Fatalf("success overwritten: %+v, %v", result, err)
			}
		}
	}
}

func TestCanonicalDisplayToolCall(t *testing.T) {
	if name := canonicalDisplayToolName("AskUserQuestion"); name != "ask_user" {
		t.Fatalf("completed question lost its display identity: %s", name)
	}
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

func TestClaudeCLIArgsIncludeTurnOverrides(t *testing.T) {
	args := strings.Join(cliArgs(claudecfg.Config{}, external.PromptInput{ModelID: "model-a", ReasoningEffort: "high"}, "resumed-session", ""), " ")
	if !strings.Contains(args, "--model 'model-a'") || !strings.Contains(args, "--effort 'high'") || !strings.Contains(args, "--resume 'resumed-session'") {
		t.Fatalf("args = %q", args)
	}
	if strings.Contains(args, "--thinking") {
		t.Fatalf("effort must preserve native thinking settings: %q", args)
	}
	args = strings.Join(cliArgs(claudecfg.Config{}, external.PromptInput{}, "", ""), " ")
	if strings.Contains(args, "--thinking") || strings.Contains(args, "--effort") {
		t.Fatalf("an inherited default must stay unset: %q", args)
	}
}

func TestClaudeCatalogPreservesAdvertisedDefaultWithoutConcreteAlias(t *testing.T) {
	for _, resolved := range []string{"", "unlisted-model"} {
		t.Run("resolved="+resolved, func(t *testing.T) {
			catalog := modelCatalogFromInitialize("", initializeResponse{Models: []initializeModel{
				{Value: "default", ResolvedModel: resolved, DisplayName: "Default"},
				{Value: "sonnet", ResolvedModel: "claude-sonnet"},
			}})
			if len(catalog.Models) != 2 || catalog.Models[0].ID != "default" || !catalog.Models[0].Default {
				t.Fatalf("catalog=%+v", catalog)
			}
		})
	}
}

func TestClaudeCatalogKeepsResolvedAlias(t *testing.T) {
	const full = "claude-opus-5"
	for _, configured := range []string{"", full} {
		catalog := modelCatalogFromInitialize(configured, initializeResponse{Models: []initializeModel{
			{Value: "default", ResolvedModel: full},
			{Value: "opus", ResolvedModel: full, SupportsEffort: true, SupportedEffortLevels: []string{"high"}},
		}})
		if catalog.ConfiguredModelID != configured || len(catalog.Models) != 1 || catalog.Models[0].ID != "opus" || catalog.Models[0].ResolvedModelID != full || catalog.Models[0].Default != (configured == "") {
			t.Fatalf("catalog=%+v", catalog)
		}
	}
}

func (*testCLIProcess) Err() error { return nil }
