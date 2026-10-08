package toolexec

import (
	"context"
	"fmt"
	"sync"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"
)

// ToolExecOptions configures a single ExecuteTools invocation.
type ToolExecOptions struct {
	// Tools is the set of executable tool definitions. Calls are matched to
	// tools by name.
	Tools []Tool
	// Approve is consulted for every tool with RequireApproval set. When nil,
	// approval-required calls are denied with an IsError result. An approved
	// (or zero-value) decision continues to execution; rejected records an
	// IsError result; deferred stops the batch (see ToolExecOutcome.Deferred).
	Approve func(context.Context, sdk.ToolCall) (ToolApprovalResult, error)
	// OnPart, when non-nil, observes execution events: approval requests
	// (ToolApprovalRequestPart), denials (ToolOutputDeniedPart), progress
	// (ToolProgressPart), results (StreamToolResultPart), and errors
	// (StreamToolErrorPart). Parallel executions may invoke OnPart
	// concurrently; callers must synchronize if needed.
	OnPart func(sdk.StreamPart)
}

// ToolExecOutcome is the result of ExecuteTools over one batch of tool calls.
type ToolExecOutcome struct {
	// Results holds one ToolResultPart per call, in call order, including
	// rejected and not-found IsError results. It is nil when Deferred is set.
	Results []sdk.ToolResultPart
	// Refused counts the calls the executor did not run because their
	// arguments were not a JSON document or named a tool the model was not
	// offered; each has an IsError result. A loop can tell a batch the model
	// got entirely wrong from one that executed.
	Refused int
	// Deferred is non-nil when an approval handler returned
	// ToolApprovalDecisionDeferred. The batch parked at that call and nothing
	// in it executed; the step persists with its calls open and the decision
	// resumes the run. Deferral is a normal outcome, not an error.
	Deferred *ToolApprovalResult
}

// ExecuteTools resolves approvals for and executes one batch of tool calls.
//
// Approvals are resolved sequentially in call order; approved tools then
// execute (in parallel when more than one). When a deferred approval is
// encountered nothing in the batch executes: the outcome carries the deferral
// marker and no results, the step is persisted with its tool calls open, and
// the batch runs when the decision resumes the run. (This is the behaviour of
// the SDK executor Memoh ran before the copy; the copied revision executed
// the calls ahead of the deferral, which let a read_media result land in a
// step whose carrier never reached the model.) A non-nil error is returned
// only for handler failures (approval handler error, unknown decision); in
// that case the outcome is empty.
func ExecuteTools(ctx context.Context, calls []sdk.ToolCall, opts ToolExecOptions) (ToolExecOutcome, error) {
	toolMap := buildToolMap(opts.Tools)
	results := make([]sdk.ToolResultPart, len(calls))
	pending := make([]pendingToolExec, 0, len(calls))
	refused := 0

	for i, tc := range calls {
		if !tc.Input.Valid() {
			// The model's argument text was not a JSON document. The call is
			// answered, never run: the model reads the failure on its next
			// step and can correct the call (TRN-style invalid arguments). The
			// text it sent travels in the result; the step message replays
			// the call with an empty object (see BuildStepMessages).
			results[i] = failedToolResult(opts.OnPart, tc, fmt.Errorf("invalid tool arguments for %q: not a JSON document: %s", tc.ToolName, invalidArgumentExcerpt(tc.Input.Text)))
			refused++
			continue
		}
		tool, ok := toolMap[tc.ToolName]
		if !ok || tool.Execute == nil {
			results[i] = failedToolResult(opts.OnPart, tc, fmt.Errorf("tool %q not found or has no execute handler", tc.ToolName))
			refused++
			continue
		}

		if tool.RequireApproval {
			if opts.Approve == nil {
				if opts.OnPart != nil {
					opts.OnPart(&ToolOutputDeniedPart{
						ToolCallID: tc.ToolCallID,
						ToolName:   tc.ToolName,
					})
				}
				results[i] = sdk.ToolResultPart{
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					Result:     sdk.TextOutput("tool execution denied: no approval handler"),
					IsError:    true,
				}
				continue
			}

			approval, err := opts.Approve(ctx, tc)
			if err != nil {
				return ToolExecOutcome{}, fmt.Errorf("twilightai: approval handler for %q: %w", tc.ToolName, err)
			}
			switch approval.Decision {
			case "", ToolApprovalDecisionApproved:
				if approval.Input != nil {
					// The approved arguments are the ones that execute and the
					// ones the step record keeps (calls is the caller's slice).
					tc.Input = *approval.Input
					calls[i].Input = *approval.Input
				}
			case ToolApprovalDecisionRejected:
				if opts.OnPart != nil {
					opts.OnPart(&ToolApprovalRequestPart{
						ApprovalID: approval.ApprovalID,
						ToolCallID: tc.ToolCallID,
						ToolName:   tc.ToolName,
						Input:      tc.Input,
						Metadata:   approval.Metadata,
					})
					opts.OnPart(&ToolOutputDeniedPart{
						ToolCallID: tc.ToolCallID,
						ToolName:   tc.ToolName,
					})
				}
				results[i] = sdk.ToolResultPart{
					ToolCallID: tc.ToolCallID,
					ToolName:   tc.ToolName,
					Result:     sdk.TextOutput(rejectedToolResultText(approval)),
					IsError:    true,
				}
				continue
			case ToolApprovalDecisionDeferred:
				if approval.Input != nil {
					// The parked call is persisted and shown with the arguments
					// the decision was evaluated on, not the model's raw ones.
					tc.Input = *approval.Input
					calls[i].Input = *approval.Input
				}
				if opts.OnPart != nil {
					opts.OnPart(&ToolApprovalRequestPart{
						ApprovalID: approval.ApprovalID,
						ToolCallID: tc.ToolCallID,
						ToolName:   tc.ToolName,
						Input:      tc.Input,
						Metadata:   approval.Metadata,
					})
				}
				deferred := approval
				return ToolExecOutcome{Deferred: &deferred}, nil
			default:
				return ToolExecOutcome{}, fmt.Errorf("twilightai: unknown approval decision %q for %q", approval.Decision, tc.ToolName)
			}
		}

		pending = append(pending, pendingToolExec{idx: i, tc: tc, tool: tool})
	}

	runPendingTools(ctx, pending, results, opts.OnPart)
	return ToolExecOutcome{Results: results, Refused: refused}, nil
}

// runPendingTools executes approved tool calls, in parallel when more than one,
// writing each result to its call index in results.
func runPendingTools(ctx context.Context, pending []pendingToolExec, results []sdk.ToolResultPart, onPart func(sdk.StreamPart)) {
	switch {
	case len(pending) == 1:
		results[pending[0].idx] = runTool(ctx, &pending[0].tc, pending[0].tool, onPart)
	case len(pending) > 1:
		var wg sync.WaitGroup
		wg.Add(len(pending))
		for _, p := range pending {
			go func(p pendingToolExec) {
				defer wg.Done()
				results[p.idx] = runTool(ctx, &p.tc, p.tool, onPart)
			}(p)
		}
		wg.Wait()
	}
}

// ToolCallResults joins tool result parts with their originating calls,
// filling ToolResult.Input from the matching ToolCall by ToolCallID.
func ToolCallResults(calls []sdk.ToolCall, parts []sdk.ToolResultPart) []ToolResult {
	inputs := make(map[string]sdk.ToolArguments, len(calls))
	for _, c := range calls {
		inputs[c.ToolCallID] = c.Input
	}
	out := make([]ToolResult, len(parts))
	for i, p := range parts {
		out[i] = ToolResult{
			ToolCallID: p.ToolCallID,
			ToolName:   p.ToolName,
			Input:      inputs[p.ToolCallID],
			Output:     p.Result,
		}
	}
	return out
}

func buildToolMap(tools []Tool) map[string]*Tool {
	m := make(map[string]*Tool, len(tools))
	for i := range tools {
		m[tools[i].Name] = &tools[i]
	}
	return m
}

type pendingToolExec struct {
	idx  int
	tc   sdk.ToolCall
	tool *Tool
}

func rejectedToolResultText(approval ToolApprovalResult) string {
	if approval.Reason != "" {
		return "tool execution denied by user: " + approval.Reason
	}
	return "tool execution denied by user"
}

func runTool(ctx context.Context, tc *sdk.ToolCall, tool *Tool, sendProgress func(sdk.StreamPart)) sdk.ToolResultPart {
	var progressFn func(content sdk.ToolOutput)
	if sendProgress != nil {
		progressFn = func(content sdk.ToolOutput) {
			sendProgress(&ToolProgressPart{
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Content:    content,
			})
		}
	}

	execCtx := &ToolExecContext{
		Context:      ctx,
		ToolCallID:   tc.ToolCallID,
		ToolName:     tc.ToolName,
		SendProgress: progressFn,
	}

	output, err := tool.Execute(execCtx, tc.Input)
	if err != nil {
		if sendProgress != nil {
			sendProgress(&StreamToolErrorPart{
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Error:      err,
			})
		}
		return sdk.ToolResultPart{
			ToolCallID: tc.ToolCallID,
			ToolName:   tc.ToolName,
			Result:     sdk.TextOutput(err.Error()),
			IsError:    true,
		}
	}

	if sendProgress != nil {
		sendProgress(&StreamToolResultPart{
			ToolCallID: tc.ToolCallID,
			ToolName:   tc.ToolName,
			Input:      tc.Input,
			Output:     output,
		})
	}
	return sdk.ToolResultPart{
		ToolCallID: tc.ToolCallID,
		ToolName:   tc.ToolName,
		Result:     output,
	}
}

// failedToolResult answers a call the executor refused to run. The error part
// closes the call's live block the way a failing tool would; without it a
// consumer that opened the block on tool_call_start waits for an end that
// never comes.
func failedToolResult(onPart func(sdk.StreamPart), tc sdk.ToolCall, err error) sdk.ToolResultPart {
	if onPart != nil {
		onPart(&StreamToolErrorPart{ToolCallID: tc.ToolCallID, ToolName: tc.ToolName, Error: err})
	}
	return sdk.ToolResultPart{
		ToolCallID: tc.ToolCallID,
		ToolName:   tc.ToolName,
		Result:     sdk.TextOutput(err.Error()),
		IsError:    true,
	}
}

// invalidArgumentExcerptLimit bounds the model's argument text echoed in an
// invalid-arguments result; the text is model output and can be arbitrarily
// long.
const invalidArgumentExcerptLimit = 2000

func invalidArgumentExcerpt(text string) string {
	if len(text) <= invalidArgumentExcerptLimit {
		return text
	}
	cut := invalidArgumentExcerptLimit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
