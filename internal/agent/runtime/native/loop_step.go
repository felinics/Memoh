package native

import (
	"context"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// stepThread is the request state one dispatch advances call by call: the
// request template, the conversation the next call sends, and what a
// committed step left for the next call (a refreshed tool set, directive
// inputs). Both loops advance it the same way; only the inputs a loop can
// receive differ (the stream loop also drains live injections).
type stepThread struct {
	dispatch generateDispatch
	params   sdk.Request
	messages []sdk.Message
	// pendingRefresh is the tool set re-assembled after a committed step
	// reported a capability change; it is installed before the next call.
	pendingRefresh *refreshedTools
	// pendingDirectiveInputs are the inputs step commits handed back; they
	// join the conversation at the next boundary.
	pendingDirectiveInputs []DirectiveInput
	// refused counts consecutive steps whose whole batch the executor
	// refused; see noteRefused.
	refused refusedBatches
}

// stepBoundary describes the boundary before one model call.
type stepBoundary struct {
	// first is the dispatch's first call: it sends the dispatch's compiled
	// request as is and drains nothing (a retried dispatch starts over too).
	first bool
	// committed is the number of durable steps before this boundary.
	committed int
	// readMedia holds the images tools read since the previous call.
	readMedia *readMediaDecorationState
	// drainInjected appends the loop's live injections, when it has any.
	drainInjected func(boundary int, messages []sdk.Message) []sdk.Message
}

// reset starts the thread over from a compiled dispatch.
func (t *stepThread) reset(dispatch generateDispatch) {
	t.dispatch = dispatch
	t.params = dispatch.params
	t.messages = append([]sdk.Message(nil), dispatch.params.Messages...)
}

// advance prepares the request for the next model call. A pending capability
// refresh is installed first, before the prepare chain, so budgeting and
// reselection price the request that is actually sent. At every boundary
// after a dispatch's first call the loop-owned dynamic inputs (read-media
// carriers, live injections, directive inputs) join the conversation, then
// the prepare chain (before-model-call hook, background summary,
// provider-attempt budgeting/reselection) runs.
func (t *stepThread) advance(cfg RunConfig, dynamic *loopDynamicInputs, boundary stepBoundary) sdk.Request {
	if t.pendingRefresh != nil {
		t.messages = t.pendingRefresh.apply(&t.dispatch, &t.params, t.messages)
		t.pendingRefresh = nil
	}
	if !boundary.first {
		dynamic.beginBoundary(boundary.committed)
		if cfg.BackgroundManager != nil {
			t.messages = removeBackgroundSummaryMessages(t.messages, t.dispatch.initialMessageCount)
		}
		t.messages = drainReadMediaMessage(boundary.readMedia, dynamic, cfg.ContextMutations, boundary.committed, t.messages)
		if boundary.drainInjected != nil {
			t.messages = boundary.drainInjected(boundary.committed, t.messages)
		}
		if len(t.pendingDirectiveInputs) > 0 {
			inputs := t.pendingDirectiveInputs
			t.pendingDirectiveInputs = nil
			t.messages = appendDirectiveInputs(cfg, dynamic, t.messages, inputs)
		}
		t.params.Messages = t.messages
		if override := t.dispatch.prepareStep(&t.params); override != nil {
			t.params = *override
		}
		t.messages = t.params.Messages
	}
	request := t.params
	request.Messages = t.messages
	return request
}

// extend appends a committed step's messages to the conversation.
func (t *stepThread) extend(messages []sdk.Message) {
	t.messages = append(t.messages, messages...)
}

// takeDirective keeps the inputs a step commit handed back for the next
// boundary.
func (t *stepThread) takeDirective(dir StepDirective) {
	t.pendingDirectiveInputs = collectDirectiveInputs(t.pendingDirectiveInputs, dir.NextInputs)
}

// continues reports whether a final step gets the model another call on the
// same thread: a directive handed back input, or a committed step refreshed
// the tool set.
func (t *stepThread) continues() bool {
	return len(t.pendingDirectiveInputs) > 0 || t.pendingRefresh != nil
}

// stepKind is what settleStep decided about one model result.
type stepKind int

const (
	// stepFinal is a step without tool calls: none emitted, or a finish that
	// is not tool-calls. A call to a tool the model was not offered is a tool
	// step like any other; the executor answers it with an error result.
	stepFinal stepKind = iota
	// stepDeferred is a tool batch parked for a decision; nothing executed.
	stepDeferred
	// stepToolBatch is a tool batch that executed; the loop continues.
	stepToolBatch
	// stepRefusedBatch is a tool batch the executor refused entirely: every
	// call named a tool the model was not offered or carried arguments that
	// were not a JSON document. The step commits with its error results and
	// the loop continues, but a run of them ends the turn (see noteRefused).
	stepRefusedBatch
)

// refusedBatchEndsRun records the step's batch outcome and reports whether
// the run must end: maxRefusedBatches consecutive entirely-refused batches
// with nothing new to continue on. A final step or an executed batch starts
// the count over. So does input a commit handed back (a claimed steer, a
// refreshed tool set): that is new information for the model, and ending
// the run would drop the claimed input.
func (t *stepThread) refusedBatchEndsRun(kind stepKind) bool {
	if !t.refused.note(kind == stepRefusedBatch) {
		return false
	}
	if t.continues() {
		t.refused = 0
		return false
	}
	return true
}

// resetRefused starts the refused-batch count over: a steer checkpoint
// begins a fresh answer on new input.
func (t *stepThread) resetRefused() {
	t.refused = 0
}

// settleStep turns one model result into the step record both loops commit.
// It runs the tool batch when the result carries executable tool calls; it
// does not commit, emit, or retry. textMeta is the provider metadata of the
// step text (the stream loop accumulates it beside the result).
func settleStep(ctx context.Context, dispatch generateDispatch, result sdk.ModelResult, textMeta sdk.ProviderMetadata, onPart func(sdk.StreamPart)) (step.Record, stepKind, error) {
	if result.FinishReason != sdk.FinishReasonToolCalls || len(result.ToolCalls) == 0 {
		// The step's model result is the call's result verbatim: this path
		// executes no tools and defers nothing.
		messages := toolexec.BuildStepMessages(result.Text, textMeta, result.ReasoningParts, result.ToolCalls, nil, &result.Usage)
		return step.Record{Result: result, Messages: messages}, stepFinal, nil
	}
	// Deferral is a normal outcome; handler failures are errors.
	outcome, err := toolexec.ExecuteTools(ctx, result.ToolCalls, toolexec.ToolExecOptions{
		Tools:   dispatch.execTools,
		Approve: dispatch.approve,
		OnPart:  onPart,
	})
	if err != nil {
		return step.Record{}, stepFinal, err
	}
	messages := toolexec.BuildStepMessages(result.Text, textMeta, result.ReasoningParts, result.ToolCalls, outcome.Results, &result.Usage)
	record := step.Record{
		Result:      result,
		ToolResults: toolexec.ToolCallResults(result.ToolCalls, outcome.Results),
		Messages:    messages,
	}
	if outcome.Deferred != nil {
		// A deferred batch executes nothing: outcome.Results is empty and every
		// call of the step stays a dangling ToolCallPart until the decision
		// resumes the run. The approved call executes there; the step's other
		// open calls are closed with synthetic error results.
		record.Deferred = outcome.Deferred
		return record, stepDeferred, nil
	}
	if batchRefused(outcome.Refused, len(result.ToolCalls)) {
		return record, stepRefusedBatch, nil
	}
	return record, stepToolBatch, nil
}
