package toolexec_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

func objSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "object"} }

func echoTool(name string, executed *bool) toolexec.Tool {
	return toolexec.Tool{
		Name:       name,
		Parameters: objSchema(),
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			if executed != nil {
				*executed = true
			}
			return sdk.TextOutput("output-" + name), nil
		},
	}
}

func TestExecuteTools_DeferralPartialResults(t *testing.T) {
	var executedA, executedB bool
	toolA := echoTool("tool-a", &executedA)
	toolA.RequireApproval = true
	toolB := echoTool("tool-b", &executedB)
	toolB.RequireApproval = true

	calls := []sdk.ToolCall{
		{ToolCallID: "c1", ToolName: "tool-a", Input: sdk.ParseToolArguments(`{"n":1}`)},
		{ToolCallID: "c2", ToolName: "tool-b", Input: sdk.ParseToolArguments(`{"n":2}`)},
	}

	outcome, err := toolexec.ExecuteTools(context.Background(), calls, toolexec.ToolExecOptions{
		Tools: []toolexec.Tool{toolA, toolB},
		Approve: func(_ context.Context, tc sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
			switch tc.ToolCallID {
			case "c1":
				return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionRejected, Reason: "nope"}, nil
			case "c2":
				return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionDeferred, ApprovalID: "approval-2"}, nil
			}
			t.Fatalf("unexpected call %q", tc.ToolCallID)
			return toolexec.ToolApprovalResult{}, nil
		},
	})
	if err != nil {
		t.Fatalf("deferral must not be an error, got: %v", err)
	}
	if outcome.Deferred == nil || outcome.Deferred.ApprovalID != "approval-2" {
		t.Fatalf("Deferred: got %#v", outcome.Deferred)
	}
	// A deferred batch executes nothing and carries no results: the step is
	// persisted with its calls open and the whole batch runs when the
	// decision resumes the run.
	if len(outcome.Results) != 0 {
		t.Fatalf("Results: got %d entries, want none: %#v", len(outcome.Results), outcome.Results)
	}
	if executedA || executedB {
		t.Fatalf("no tool should execute (rejected + deferred): a=%v b=%v", executedA, executedB)
	}
}

// A call that needs no approval and precedes the deferred one is not run
// either: a result computed here would be persisted in a step whose run ends
// before the model can see what it carried (read_media's image carrier).
func TestExecuteTools_DeferralExecutesNothing(t *testing.T) {
	var executedA bool
	toolA := echoTool("tool-a", &executedA)
	toolB := echoTool("tool-b", nil)
	toolB.RequireApproval = true

	calls := []sdk.ToolCall{
		{ToolCallID: "c1", ToolName: "tool-a"},
		{ToolCallID: "c2", ToolName: "tool-b"},
	}

	outcome, err := toolexec.ExecuteTools(context.Background(), calls, toolexec.ToolExecOptions{
		Tools: []toolexec.Tool{toolA, toolB},
		Approve: func(_ context.Context, _ sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
			return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionDeferred, ApprovalID: "approval-b"}, nil
		},
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if executedA {
		t.Fatal("tool-a precedes the deferral point but must not execute while the batch is parked")
	}
	if outcome.Deferred == nil || outcome.Deferred.ApprovalID != "approval-b" {
		t.Fatalf("deferral marker: %#v", outcome.Deferred)
	}
	if len(outcome.Results) != 0 {
		t.Fatalf("Results: got %#v, want none", outcome.Results)
	}
}

func TestExecuteTools_ParallelExecution(t *testing.T) {
	tools := []toolexec.Tool{echoTool("t1", nil), echoTool("t2", nil), echoTool("t3", nil)}
	calls := []sdk.ToolCall{
		{ToolCallID: "c1", ToolName: "t1"},
		{ToolCallID: "c2", ToolName: "t2"},
		{ToolCallID: "c3", ToolName: "t3"},
	}

	outcome, err := toolexec.ExecuteTools(context.Background(), calls, toolexec.ToolExecOptions{Tools: tools})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if outcome.Deferred != nil {
		t.Fatalf("unexpected deferral: %#v", outcome.Deferred)
	}
	if len(outcome.Results) != 3 {
		t.Fatalf("Results: got %d entries, want 3", len(outcome.Results))
	}
	for i, want := range []string{"output-t1", "output-t2", "output-t3"} {
		if outcome.Results[i].Result.Text != want || outcome.Results[i].IsError {
			t.Fatalf("Results[%d]: got %#v, want %q", i, outcome.Results[i], want)
		}
	}
}

func TestExecuteTools_SingleExecution(t *testing.T) {
	var executed bool
	outcome, err := toolexec.ExecuteTools(context.Background(),
		[]sdk.ToolCall{{ToolCallID: "c1", ToolName: "only"}},
		toolexec.ToolExecOptions{Tools: []toolexec.Tool{echoTool("only", &executed)}},
	)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !executed {
		t.Fatal("tool did not execute")
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Result.Text != "output-only" {
		t.Fatalf("Results: got %#v", outcome.Results)
	}
}

func TestExecuteTools_NilOnPart(t *testing.T) {
	denied := toolexec.Tool{
		Name: "denied", Parameters: objSchema(), RequireApproval: true,
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			return sdk.TextOutput("x"), nil
		},
	}
	rejected := toolexec.Tool{
		Name: "rejected", Parameters: objSchema(), RequireApproval: true,
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			return sdk.TextOutput("y"), nil
		},
	}

	// Approve nil + RequireApproval: denied, no panic with nil OnPart.
	outcome, err := toolexec.ExecuteTools(context.Background(),
		[]sdk.ToolCall{{ToolCallID: "c1", ToolName: "denied"}},
		toolexec.ToolExecOptions{Tools: []toolexec.Tool{denied}},
	)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].IsError {
		t.Fatalf("Results: got %#v, want denied IsError", outcome.Results)
	}

	// Rejected decision, nil OnPart: no panic, IsError result.
	outcome, err = toolexec.ExecuteTools(context.Background(),
		[]sdk.ToolCall{{ToolCallID: "c2", ToolName: "rejected"}},
		toolexec.ToolExecOptions{
			Tools: []toolexec.Tool{rejected},
			Approve: func(_ context.Context, _ sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
				return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionRejected}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].IsError {
		t.Fatalf("Results: got %#v, want rejected IsError", outcome.Results)
	}
}

func TestExecuteTools_OnPartObservesEvents(t *testing.T) {
	rejected := toolexec.Tool{
		Name: "rejected", Parameters: objSchema(), RequireApproval: true,
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			return sdk.TextOutput("never"), nil
		},
	}
	approved := echoTool("approved", nil)
	approved.RequireApproval = true

	var mu sync.Mutex
	var parts []sdk.StreamPart

	outcome, err := toolexec.ExecuteTools(context.Background(),
		[]sdk.ToolCall{
			{ToolCallID: "c1", ToolName: "rejected", Input: sdk.ParseToolArguments(`{"k":"v"}`)},
			{ToolCallID: "c2", ToolName: "approved"},
		},
		toolexec.ToolExecOptions{
			Tools: []toolexec.Tool{rejected, approved},
			Approve: func(_ context.Context, tc sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
				if tc.ToolCallID == "c1" {
					return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionRejected, ApprovalID: "a1"}, nil
				}
				return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionApproved}, nil
			},
			OnPart: func(p sdk.StreamPart) {
				mu.Lock()
				parts = append(parts, p)
				mu.Unlock()
			},
		},
	)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(outcome.Results) != 2 {
		t.Fatalf("Results: got %#v", outcome.Results)
	}

	var sawApprovalRequest, sawDenied, sawResult bool
	for _, p := range parts {
		switch part := p.(type) {
		case *toolexec.ToolApprovalRequestPart:
			if part.ToolCallID == "c1" && part.ApprovalID == "a1" {
				sawApprovalRequest = true
			}
		case *toolexec.ToolOutputDeniedPart:
			if part.ToolCallID == "c1" {
				sawDenied = true
			}
		case *toolexec.StreamToolResultPart:
			if part.ToolCallID == "c2" && part.Output.Text == "output-approved" {
				sawResult = true
			}
		}
	}
	if !sawApprovalRequest || !sawDenied || !sawResult {
		t.Fatalf("missing events: approvalRequest=%v denied=%v result=%v (parts=%#v)",
			sawApprovalRequest, sawDenied, sawResult, parts)
	}
}

func TestToolCallResults_FillsInput(t *testing.T) {
	calls := []sdk.ToolCall{
		{ToolCallID: "c1", ToolName: "t1", Input: sdk.ParseToolArguments(`{"path":"/tmp/a"}`)},
		{ToolCallID: "c2", ToolName: "t2", Input: sdk.ToolArguments{Text: "raw-input"}},
	}
	parts := []sdk.ToolResultPart{
		{ToolCallID: "c1", ToolName: "t1", Result: sdk.TextOutput("r1")},
		{ToolCallID: "c2", ToolName: "t2", Result: sdk.TextOutput("r2"), IsError: true},
	}

	results := toolexec.ToolCallResults(calls, parts)
	if len(results) != 2 {
		t.Fatalf("got %d results", len(results))
	}
	var in map[string]any
	if err := results[0].Input.Unmarshal(&in); err != nil || in["path"] != "/tmp/a" {
		t.Fatalf("Results[0].Input: got %#v", results[0].Input)
	}
	if results[0].Output.Text != "r1" {
		t.Fatalf("Results[0].Output: got %#v", results[0].Output)
	}
	if results[1].Input.Text != "raw-input" || results[1].Output.Text != "r2" {
		t.Fatalf("Results[1]: got %#v", results[1])
	}
}

func TestBuildStepMessages_AssemblesAssistantAndToolMessages(t *testing.T) {
	usage := &sdk.Usage{InputTokens: 3, OutputTokens: 5}
	msgs := toolexec.BuildStepMessages(
		"hello",
		sdk.ProviderMetadata{"k": {"v": "v"}},
		[]sdk.ReasoningPart{{Text: "thinking"}},
		[]sdk.ToolCall{{ToolCallID: "c1", ToolName: "t1", Input: sdk.ToolArguments{Text: "in"}}},
		[]sdk.ToolResultPart{{ToolCallID: "c1", ToolName: "t1", Result: sdk.TextOutput("out")}},
		usage,
	)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	assistant := msgs[0]
	if assistant.Role != sdk.MessageRoleAssistant || assistant.Usage != usage {
		t.Fatalf("assistant message: %#v", assistant)
	}
	// Order: reasoning, text, tool call.
	if len(assistant.Content) != 3 {
		t.Fatalf("assistant content: %#v", assistant.Content)
	}
	if _, ok := assistant.Content[0].(sdk.ReasoningPart); !ok {
		t.Fatalf("content[0] is %T, want ReasoningPart", assistant.Content[0])
	}
	if tp, ok := assistant.Content[1].(sdk.TextPart); !ok || tp.Text != "hello" {
		t.Fatalf("content[1] is %#v, want TextPart hello", assistant.Content[1])
	}
	if tc, ok := assistant.Content[2].(sdk.ToolCallPart); !ok || tc.ToolCallID != "c1" {
		t.Fatalf("content[2] is %#v, want ToolCallPart c1", assistant.Content[2])
	}
	if msgs[1].Role != sdk.MessageRoleTool {
		t.Fatalf("second message role: %v", msgs[1].Role)
	}
}

// An approval handler may pin arguments it resolved (the canonical workspace
// target). The executor runs the call with those arguments and reports them
// as the call's input so the persisted step names the same target.
func TestExecuteTools_ApprovalInputRewritesCall(t *testing.T) {
	var seen string
	tool := toolexec.Tool{
		Name:            "exec",
		Parameters:      objSchema(),
		RequireApproval: true,
		Execute: func(_ *toolexec.ToolExecContext, args sdk.ToolArguments) (sdk.ToolOutput, error) {
			seen = string(args.Object())
			return sdk.TextOutput("ok"), nil
		},
	}
	pinned := sdk.ParseToolArguments(`{"cmd":"ls","target_id":"canonical-target"}`)
	calls := []sdk.ToolCall{
		{ToolCallID: "c1", ToolName: "exec", Input: sdk.ParseToolArguments(`{"cmd":"ls","target_id":"requested"}`)},
	}
	outcome, err := toolexec.ExecuteTools(context.Background(), calls, toolexec.ToolExecOptions{
		Tools: []toolexec.Tool{tool},
		Approve: func(context.Context, sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
			return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionApproved, Input: &pinned}, nil
		},
	})
	if err != nil {
		t.Fatalf("ExecuteTools: %v", err)
	}
	if seen != `{"cmd":"ls","target_id":"canonical-target"}` {
		t.Fatalf("tool executed with %s, want the approval-pinned arguments", seen)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].ToolCallID != "c1" {
		t.Fatalf("results = %#v, want the single call answered", outcome.Results)
	}
	// The step persists the calls slice, so the rewritten arguments must land
	// on it as well.
	if got := string(calls[0].Input.Object()); got != `{"cmd":"ls","target_id":"canonical-target"}` {
		t.Fatalf("persisted call input = %s, want the pinned arguments", got)
	}
}

// A call the executor refuses to run (arguments that are not a JSON
// document, or a tool the model was not offered) is answered with an error
// result and closes its live block with an error part, like a failing tool.
// The argument text the model sent travels in the invalid-arguments result.
func TestExecuteTools_RefusedCallsCloseTheirLiveBlock(t *testing.T) {
	var parts []sdk.StreamPart
	calls := []sdk.ToolCall{
		{ToolCallID: "c1", ToolName: "tool-a", Input: sdk.ParseToolArguments(`{"path":"a`)},
		{ToolCallID: "c2", ToolName: "missing"},
	}
	outcome, err := toolexec.ExecuteTools(context.Background(), calls, toolexec.ToolExecOptions{
		Tools:  []toolexec.Tool{echoTool("tool-a", nil)},
		OnPart: func(part sdk.StreamPart) { parts = append(parts, part) },
	})
	if err != nil {
		t.Fatalf("ExecuteTools: %v", err)
	}
	if len(outcome.Results) != 2 || !outcome.Results[0].IsError || !outcome.Results[1].IsError {
		t.Fatalf("results = %#v, want two error results", outcome.Results)
	}
	if text := outcome.Results[0].Result.String(); !strings.Contains(text, `{"path":"a`) {
		t.Fatalf("invalid-arguments result = %q, want the model's argument text", text)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %#v, want one error part per refused call", parts)
	}
	for i, part := range parts {
		errPart, ok := part.(*toolexec.StreamToolErrorPart)
		if !ok || errPart.ToolCallID != calls[i].ToolCallID {
			t.Fatalf("part %d = %#v, want StreamToolErrorPart for %s", i, part, calls[i].ToolCallID)
		}
	}
}

// A deferred approval that pinned arguments parks the call with them: the
// persisted call and the approval request name the target the decision was
// evaluated on.
func TestExecuteTools_DeferredApprovalPinsInput(t *testing.T) {
	tool := echoTool("exec", nil)
	tool.RequireApproval = true
	pinned := sdk.ParseToolArguments(`{"cmd":"ls","target_id":"canonical-target"}`)
	calls := []sdk.ToolCall{{ToolCallID: "c1", ToolName: "exec", Input: sdk.ParseToolArguments(`{"cmd":"ls","target_id":"requested"}`)}}
	var request *toolexec.ToolApprovalRequestPart
	outcome, err := toolexec.ExecuteTools(context.Background(), calls, toolexec.ToolExecOptions{
		Tools: []toolexec.Tool{tool},
		Approve: func(context.Context, sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
			return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionDeferred, ApprovalID: "a1", Input: &pinned}, nil
		},
		OnPart: func(part sdk.StreamPart) {
			if p, ok := part.(*toolexec.ToolApprovalRequestPart); ok {
				request = p
			}
		},
	})
	if err != nil || outcome.Deferred == nil {
		t.Fatalf("outcome = %#v err = %v, want a deferral", outcome, err)
	}
	if got := string(calls[0].Input.Object()); got != `{"cmd":"ls","target_id":"canonical-target"}` {
		t.Fatalf("parked call input = %s, want the pinned arguments", got)
	}
	if request == nil || string(request.Input.Object()) != `{"cmd":"ls","target_id":"canonical-target"}` {
		t.Fatalf("approval request part = %#v, want the pinned arguments", request)
	}
}

// Invalid arguments never reach a provider request: the persisted call
// replays as the empty object, and the text stays in the error result.
func TestBuildStepMessages_InvalidArgumentsReplayAsEmptyObject(t *testing.T) {
	calls := []sdk.ToolCall{
		{ToolCallID: "c1", ToolName: "tool-a", Input: sdk.ParseToolArguments(`{"path":"a`)},
		{ToolCallID: "c2", ToolName: "tool-b", Input: sdk.ParseToolArguments(`{"n":1}`)},
	}
	msgs := toolexec.BuildStepMessages("", nil, nil, calls, nil, nil)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want the assistant message only", len(msgs))
	}
	first := msgs[0].Content[0].(sdk.ToolCallPart)
	if !first.Input.Valid() || string(first.Input.Object()) != `{}` {
		t.Fatalf("invalid call replays as %#v, want {}", first.Input)
	}
	second := msgs[0].Content[1].(sdk.ToolCallPart)
	if string(second.Input.Object()) != `{"n":1}` {
		t.Fatalf("valid call changed: %#v", second.Input)
	}
}
