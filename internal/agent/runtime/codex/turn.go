package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// interruptSettleTimeout bounds how long an interrupted turn may take to
// deliver its terminal notification before the driver gives up waiting.
const interruptSettleTimeout = 10 * time.Second

// turnState tracks one running turn: it receives routed notifications and
// server requests, translates them into stream events, and assembles the
// transcript.
type turnState struct {
	input     external.PromptInput
	approval  approval.FlowService
	waiter    func(approvalID string) func()
	userInput UserInputService
	// toolLookup reports whether a tool name exists on the Memoh gateway; the
	// MCP consent branch uses it to recognize Memoh-owned tools.
	toolLookup func(context.Context, string) bool
	logger     *slog.Logger
	threadID   string

	// ctx is the turn-scoped context: it outlives the caller's stream context
	// only long enough to unwind cleanly — it is cancelled when Prompt
	// returns, aborting any decision still waiting.
	ctx    context.Context
	cancel context.CancelFunc

	done chan struct{}

	mu            sync.Mutex
	turnID        string
	events        []event.StreamEvent
	finalText     string
	usage         *sdk.Usage
	threadTotals  *protocol.TokenUsageBreakdown
	contextWindow *int64
	turn          *protocol.Turn
	turnErr       *protocol.TurnError
	toolNames     map[string]string // item id → emitted tool name
	// inflight tracks decision goroutines by server-request id so a
	// serverRequest/resolved notification can withdraw them.
	inflight map[string]context.CancelFunc
	closed   bool

	// queue decouples the shared connection read loop from sink delivery: a
	// stalled consumer must not head-of-line block the bot's other threads.
	queue     []event.StreamEvent
	queueCond *sync.Cond
	doneOnce  sync.Once
	pumpDone  chan struct{}
}

func newTurnState(parent context.Context, input external.PromptInput, threadID string, approvalSvc approval.FlowService, waiter func(string) func(), userInput UserInputService, toolLookup func(context.Context, string) bool, logger *slog.Logger) *turnState {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	t := &turnState{
		input:      input,
		approval:   approvalSvc,
		waiter:     waiter,
		userInput:  userInput,
		toolLookup: toolLookup,
		logger:     logger,
		threadID:   threadID,
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		toolNames:  map[string]string{},
		inflight:   map[string]context.CancelFunc{},
		pumpDone:   make(chan struct{}),
	}
	t.queueCond = sync.NewCond(&t.mu)
	go t.pump()
	return t
}

// close stops event delivery and unwinds in-flight decisions. Called exactly
// once, by the driver, before Prompt returns.
func (t *turnState) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	inflight := t.inflight
	t.inflight = map[string]context.CancelFunc{}
	t.queueCond.Broadcast()
	t.mu.Unlock()
	for _, cancel := range inflight {
		cancel()
	}
	t.cancel()
	<-t.pumpDone
}

// emit records one stream event for the transcript and queues it for
// delivery. After close it is a no-op: the caller's event channel may already
// be gone, and a late decision outcome must never crash the stream.
func (t *turnState) emit(ev event.StreamEvent) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.events = append(t.events, ev)
	t.queue = append(t.queue, ev)
	t.queueCond.Signal()
	t.mu.Unlock()
}

// pump delivers queued events to the sink on its own goroutine.
func (t *turnState) pump() {
	defer close(t.pumpDone)
	for {
		t.mu.Lock()
		for len(t.queue) == 0 && !t.closed {
			t.queueCond.Wait()
		}
		if len(t.queue) == 0 && t.closed {
			t.mu.Unlock()
			return
		}
		batch := t.queue
		t.queue = nil
		t.mu.Unlock()
		for _, ev := range batch {
			t.input.Sink.EmitStreamEvent(ev)
		}
	}
}

func (t *turnState) finish() {
	t.doneOnce.Do(func() { close(t.done) })
}

// setTurnID pins the turn id this state accepts notifications for.
func (t *turnState) setTurnID(turnID string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	t.mu.Lock()
	if t.turnID == "" {
		t.turnID = turnID
	}
	t.mu.Unlock()
}

func (t *turnState) currentTurnID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.turnID
}

// acceptsTurn reports whether a notification carrying turnID belongs to this
// turn. Late notifications from a previous turn on the same thread must not
// leak in (a stale turn/completed would end the new turn instantly).
func (t *turnState) acceptsTurn(turnID string) bool {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.turnID == "" || t.turnID == turnID
}

// handleNotification translates app-server notifications into stream events.
// It runs on the connection read loop and must stay non-blocking.
func (t *turnState) handleNotification(decoded any) {
	switch params := decoded.(type) {
	case *protocol.TurnStartedNotification:
		t.setTurnID(params.Turn.ID)
	case *protocol.AgentMessageDeltaNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		t.emit(event.StreamEvent{Type: event.TextDelta, Delta: params.Delta})
	case *protocol.ReasoningTextDeltaNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		t.emit(event.StreamEvent{Type: event.ReasoningDelta, Delta: params.Delta})
	case *protocol.ReasoningSummaryTextDeltaNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		t.emit(event.StreamEvent{Type: event.ReasoningDelta, Delta: params.Delta})
	case *protocol.ReasoningSummaryPartAddedNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		// Summary parts are separate paragraphs of the same reasoning stream.
		t.emit(event.StreamEvent{Type: event.ReasoningDelta, Delta: "\n\n"})
	case *protocol.ItemStartedNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		t.handleItemStarted(&params.Item)
	case *protocol.ItemCompletedNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		t.handleItemCompleted(&params.Item)
	case *protocol.CommandExecutionOutputDeltaNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		t.emit(event.StreamEvent{Type: event.ToolCallProgress, ToolCallID: params.ItemID, ToolName: t.toolName(params.ItemID), Progress: params.Delta})
	case *protocol.FileChangeOutputDeltaNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		t.emit(event.StreamEvent{Type: event.ToolCallProgress, ToolCallID: params.ItemID, ToolName: t.toolName(params.ItemID), Progress: params.Delta})
	case *protocol.ThreadTokenUsageUpdatedNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		// `last` is the most recent model request's usage and the turn spans
		// several requests, so the turn total is the running sum of `last`.
		t.mu.Lock()
		if t.usage == nil {
			t.usage = &sdk.Usage{}
		}
		last := params.TokenUsage.Last
		t.usage.InputTokens += int(last.InputTokens)
		t.usage.OutputTokens += int(last.OutputTokens)
		t.usage.TotalTokens += int(last.TotalTokens)
		t.usage.ReasoningTokens += int(last.ReasoningOutputTokens)
		t.usage.CachedInputTokens += int(last.CachedInputTokens)
		totals := params.TokenUsage.Total
		t.threadTotals = &totals
		t.contextWindow = params.TokenUsage.ModelContextWindow
		t.mu.Unlock()
	case *protocol.TurnCompletedNotification:
		if !t.acceptsTurn(params.Turn.ID) {
			return
		}
		t.mu.Lock()
		turn := params.Turn
		t.turn = &turn
		t.mu.Unlock()
		t.finish()
	case *protocol.ErrorNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		if params.WillRetry {
			t.logger.Warn("codex turn error, retrying", slog.String("thread_id", t.threadID), slog.String("message", params.Error.Message))
			return
		}
		t.mu.Lock()
		turnErr := params.Error
		t.turnErr = &turnErr
		t.mu.Unlock()
	case *protocol.ServerRequestResolvedNotification:
		// The server settled its own request (e.g. auto-review); withdraw the
		// matching pending decision instead of leaving a dead approval card.
		t.mu.Lock()
		cancel := t.inflight[params.RequestID.Key()]
		t.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case *protocol.ContextCompactedNotification: //nolint:staticcheck // still the wire shape for thread/compacted at the pinned version
		t.logger.Info("codex compacted thread context", slog.String("thread_id", t.threadID))
	}
}

func (t *turnState) handleItemStarted(item *protocol.ThreadItem) {
	switch {
	case item.CommandExecution != nil:
		cmd := item.CommandExecution
		t.rememberTool(cmd.ID, "exec")
		t.emit(event.StreamEvent{
			Type: event.ToolCallStart, ToolCallID: cmd.ID, ToolName: "exec",
			Input: map[string]any{"command": cmd.Command, "cwd": cmd.Cwd},
		})
	case item.FileChange != nil:
		fc := item.FileChange
		t.rememberTool(fc.ID, "write")
		t.emit(event.StreamEvent{
			Type: event.ToolCallStart, ToolCallID: fc.ID, ToolName: "write",
			Input: fileChangeInput(fc),
		})
	case item.MCPToolCall != nil:
		mcpCall := item.MCPToolCall
		if isMemohMCPToolCall(mcpCall) {
			return
		}
		name := mcpToolName(mcpCall)
		t.rememberTool(mcpCall.ID, name)
		t.emit(event.StreamEvent{
			Type: event.ToolCallStart, ToolCallID: mcpCall.ID, ToolName: name,
			Input: mcpCall.Arguments,
		})
	case item.WebSearch != nil:
		search := item.WebSearch
		t.rememberTool(search.ID, "web_search")
		t.emit(event.StreamEvent{
			Type: event.ToolCallStart, ToolCallID: search.ID, ToolName: "web_search",
			Input: map[string]any{"query": search.Query},
		})
	}
}

func (t *turnState) handleItemCompleted(item *protocol.ThreadItem) {
	switch {
	case item.AgentMessage != nil:
		t.mu.Lock()
		if text := item.AgentMessage.Text; text != "" {
			if t.finalText != "" {
				t.finalText += "\n\n"
			}
			t.finalText += text
		}
		t.mu.Unlock()
	case item.CommandExecution != nil:
		cmd := item.CommandExecution
		result := map[string]any{}
		if cmd.ExitCode != nil {
			result["exitCode"] = *cmd.ExitCode
		}
		if cmd.AggregatedOutput != nil {
			result["output"] = *cmd.AggregatedOutput
		}
		ev := event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: cmd.ID, ToolName: t.toolName(cmd.ID), Result: result}
		if cmd.ExitCode != nil && *cmd.ExitCode != 0 {
			ev.Status = "failed"
		}
		t.emit(ev)
	case item.FileChange != nil:
		fc := item.FileChange
		t.emit(event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: fc.ID, ToolName: t.toolName(fc.ID), Result: fileChangeInput(fc)})
	case item.MCPToolCall != nil:
		mcpCall := item.MCPToolCall
		if isMemohMCPToolCall(mcpCall) {
			return
		}
		ev := event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: mcpCall.ID, ToolName: t.toolName(mcpCall.ID)}
		if mcpCall.Result != nil {
			ev.Result = *mcpCall.Result
		}
		if mcpCall.Status == protocol.McpToolCallStatusFailed {
			ev.Status = "failed"
			if mcpCall.Error != nil {
				ev.Error = mcpToolErrorMessage(mcpCall.Error)
			}
		}
		t.emit(ev)
	case item.WebSearch != nil:
		search := item.WebSearch
		t.emit(event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: search.ID, ToolName: t.toolName(search.ID)})
	}
}

func (t *turnState) rememberTool(itemID, name string) {
	t.mu.Lock()
	t.toolNames[itemID] = name
	t.mu.Unlock()
}

func (t *turnState) toolName(itemID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if name := t.toolNames[itemID]; name != "" {
		return name
	}
	return "exec"
}

// handleServerRequest decides one approval request through the Memoh approval
// flow and answers the app-server. Runs on its own goroutine, bounded by the
// turn-scoped context and cancellable via serverRequest/resolved.
func (t *turnState) handleServerRequest(c *conn, req *protocol.Inbound, decoded any) {
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	key := req.ID.Key()
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = c.RespondError(req.ID, -32000, "the turn ended before this request was decided")
		return
	}
	t.inflight[key] = cancel
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.inflight, key)
		t.mu.Unlock()
	}()

	switch params := decoded.(type) {
	case *protocol.CommandExecutionRequestApprovalParams:
		input := map[string]any{}
		if params.Command != nil {
			input["command"] = *params.Command
		}
		if params.Cwd != nil {
			input["cwd"] = *params.Cwd
		}
		if params.Reason != nil {
			input["reason"] = *params.Reason
		}
		if len(params.ProposedExecpolicyAmendment) > 0 {
			input["proposed_execpolicy_amendment"] = params.ProposedExecpolicyAmendment
		}
		if len(params.ProposedNetworkPolicyAmendments) > 0 {
			input["proposed_network_policy_amendments"] = params.ProposedNetworkPolicyAmendments
		}
		result := t.decide(ctx, approvalCallID(params.ApprovalID, params.ItemID), "exec", input, commandApprovalOptions(params))
		decision := commandApprovalDecision(params, result)
		_ = c.Respond(req.ID, protocol.CommandExecutionRequestApprovalResponse{Decision: decision})
	case *protocol.FileChangeRequestApprovalParams:
		input := map[string]any{}
		if params.Reason != nil {
			input["reason"] = *params.Reason
		}
		if params.GrantRoot != nil {
			input["grantRoot"] = *params.GrantRoot
		}
		result := t.decide(ctx, params.ItemID, "write", input, fileChangeApprovalOptions(params))
		decision := fileChangeApprovalDecision(result)
		_ = c.Respond(req.ID, protocol.FileChangeRequestApprovalResponse{Decision: decision})
	case *protocol.PermissionsRequestApprovalParams:
		// Granting permission-profile escalations needs a dedicated surface;
		// until then the request is declined, not silently granted.
		t.logger.Warn("codex: declining permission-profile escalation", slog.String("thread_id", t.threadID))
		_ = c.RespondError(req.ID, -32000, "memoh does not grant permission profile escalations yet")
	case *protocol.ToolRequestUserInputParams:
		_ = c.Respond(req.ID, t.requestUserInput(ctx, params))
	default:
		_ = c.RespondError(req.ID, -32601, "memoh does not handle this request")
	}
}

// approvalCallID prefers the dedicated approval callback id: one item can
// raise several approvals (sub-command review, stdin writes) and the pair
// (session, tool_call_id) must stay unique per decision.
func approvalCallID(approvalID *string, itemID string) string {
	if approvalID != nil && strings.TrimSpace(*approvalID) != "" {
		return strings.TrimSpace(*approvalID)
	}
	return itemID
}

func commandApprovalOptions(params *protocol.CommandExecutionRequestApprovalParams) []approval.PermissionOption {
	options := []approval.PermissionOption{
		{ID: protocol.CommandExecutionApprovalDecisionUnitAccept, Name: "Accept", Kind: approval.OptionKindAllowOnce},
		{ID: protocol.CommandExecutionApprovalDecisionUnitAcceptForSession, Name: "Accept for session", Kind: approval.OptionKindAllowAlways},
	}
	if len(params.ProposedExecpolicyAmendment) > 0 {
		options = append(options, approval.PermissionOption{
			ID:   "acceptWithExecpolicyAmendment",
			Name: "Accept and remember: " + strings.Join(params.ProposedExecpolicyAmendment, " "),
			Kind: approval.OptionKindAllowAlways,
		})
	}
	for i, amendment := range params.ProposedNetworkPolicyAmendments {
		kind := approval.OptionKindAllowAlways
		if amendment.Action == protocol.NetworkPolicyRuleActionDeny {
			kind = approval.OptionKindRejectAlways
		}
		options = append(options, approval.PermissionOption{
			ID:   fmt.Sprintf("applyNetworkPolicyAmendment:%d", i),
			Name: fmt.Sprintf("%s %s", amendment.Action, amendment.Host),
			Kind: kind,
		})
	}
	return append(options, approval.PermissionOption{
		ID: protocol.CommandExecutionApprovalDecisionUnitDecline, Name: "Decline", Kind: approval.OptionKindRejectOnce,
	})
}

func commandApprovalDecision(params *protocol.CommandExecutionRequestApprovalParams, result approval.FlowResult) protocol.CommandExecutionApprovalDecision {
	switch result.SelectedOptionID {
	case protocol.CommandExecutionApprovalDecisionUnitAccept:
		return protocol.CommandExecutionApprovalDecision{Unit: protocol.CommandExecutionApprovalDecisionUnitAccept}
	case protocol.CommandExecutionApprovalDecisionUnitAcceptForSession:
		return protocol.CommandExecutionApprovalDecision{Unit: protocol.CommandExecutionApprovalDecisionUnitAcceptForSession}
	case protocol.CommandExecutionApprovalDecisionUnitDecline:
		return protocol.CommandExecutionApprovalDecision{Unit: protocol.CommandExecutionApprovalDecisionUnitDecline}
	case "acceptWithExecpolicyAmendment":
		return protocol.CommandExecutionApprovalDecision{
			AcceptWithExecpolicyAmendment: &protocol.CommandExecutionApprovalDecisionAcceptWithExecpolicyAmendment{
				ExecpolicyAmendment: params.ProposedExecpolicyAmendment,
			},
		}
	}
	for i, amendment := range params.ProposedNetworkPolicyAmendments {
		if result.SelectedOptionID == fmt.Sprintf("applyNetworkPolicyAmendment:%d", i) {
			return protocol.CommandExecutionApprovalDecision{
				ApplyNetworkPolicyAmendment: &protocol.CommandExecutionApprovalDecisionApplyNetworkPolicyAmendment{
					NetworkPolicyAmendment: amendment,
				},
			}
		}
	}
	if result.Approved {
		return protocol.CommandExecutionApprovalDecision{Unit: protocol.CommandExecutionApprovalDecisionUnitAccept}
	}
	if strings.EqualFold(result.Status, approval.StatusRejected) {
		return protocol.CommandExecutionApprovalDecision{Unit: protocol.CommandExecutionApprovalDecisionUnitDecline}
	}
	return protocol.CommandExecutionApprovalDecision{Unit: protocol.CommandExecutionApprovalDecisionUnitCancel}
}

func fileChangeApprovalOptions(params *protocol.FileChangeRequestApprovalParams) []approval.PermissionOption {
	options := []approval.PermissionOption{{
		ID: string(protocol.FileChangeApprovalDecisionAccept), Name: "Accept", Kind: approval.OptionKindAllowOnce,
	}}
	if params.GrantRoot != nil {
		options = append(options, approval.PermissionOption{
			ID: string(protocol.FileChangeApprovalDecisionAcceptForSession), Name: "Accept for session", Kind: approval.OptionKindAllowAlways,
		})
	}
	return append(options, approval.PermissionOption{
		ID: string(protocol.FileChangeApprovalDecisionDecline), Name: "Decline", Kind: approval.OptionKindRejectOnce,
	})
}

func fileChangeApprovalDecision(result approval.FlowResult) protocol.FileChangeApprovalDecision {
	switch result.SelectedOptionID {
	case string(protocol.FileChangeApprovalDecisionAccept):
		return protocol.FileChangeApprovalDecisionAccept
	case string(protocol.FileChangeApprovalDecisionAcceptForSession):
		return protocol.FileChangeApprovalDecisionAcceptForSession
	case string(protocol.FileChangeApprovalDecisionDecline):
		return protocol.FileChangeApprovalDecisionDecline
	}
	if result.Approved {
		return protocol.FileChangeApprovalDecisionAccept
	}
	if strings.EqualFold(result.Status, approval.StatusRejected) {
		return protocol.FileChangeApprovalDecisionDecline
	}
	return protocol.FileChangeApprovalDecisionCancel
}

// decide runs one approval through policy and, when needed, the interactive
// decision flow. Failures fail closed (cancelled).
func (t *turnState) decide(ctx context.Context, callID, toolName string, input map[string]any, options []approval.PermissionOption) approval.FlowResult {
	if t.approval == nil {
		return approval.FlowResult{Status: approval.StatusRejected, DecisionReason: "approval service unavailable"}
	}
	result, err := approval.RunFlow(ctx, t.approval, approval.FlowRequest{
		Input: approval.CreatePendingInput{
			BotID:                        t.input.BotID,
			SessionID:                    t.input.ThreadID,
			RouteID:                      t.input.RouteID,
			ChannelIdentityID:            t.input.ChannelIdentityID,
			RequestedByChannelIdentityID: t.input.ChannelIdentityID,
			ToolCallID:                   callID,
			ToolName:                     toolName,
			ToolInput:                    input,
			Options:                      options,
		},
		Interactive:    t.input.CanRequestUserInput,
		RegisterWaiter: t.waiter,
		Emit:           t.emitApprovalRequest,
		CancelOnAbort: func(cancelCtx context.Context, req approval.Request, reason string) (approval.Request, error) {
			return t.approval.Reject(cancelCtx, req.ID, "", reason)
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return approval.FlowResult{Status: approval.StatusCancelled, DecisionReason: "the turn ended before a decision arrived"}
		}
		t.logger.Error("codex approval flow failed", slog.String("thread_id", t.threadID), slog.Any("error", err))
		return approval.FlowResult{Status: approval.StatusCancelled, DecisionReason: "approval flow failed"}
	}
	return result
}

func (t *turnState) emitApprovalRequest(req approval.Request) bool {
	t.emit(event.StreamEvent{
		Type:       event.ToolApprovalRequest,
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		Input:      req.ToolInput,
		ApprovalID: req.ID,
		ShortID:    req.ShortID,
		Status:     approval.NormalizedStatus(req.Status),
		Metadata: map[string]any{
			"approval": approval.RequestMetadata(req),
		},
	})
	return true
}

// result assembles the durable outcome after the turn settled.
func (t *turnState) result(newThreadID string) (external.PromptResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := external.PromptResult{
		Output: external.TranscriptFromEvents(t.events, t.finalText),
		Text:   t.finalText,
	}
	if t.usage != nil {
		usage := *t.usage
		out.Usage = &usage
	}
	if newThreadID != "" || t.threadTotals != nil {
		out.RuntimeMetadata = map[string]any{}
		if newThreadID != "" {
			out.RuntimeMetadata[metadataThreadIDKey] = newThreadID
		}
		// Context-occupancy data for the session UI: the thread's cumulative
		// token count against its model context window.
		if t.threadTotals != nil {
			out.RuntimeMetadata["codex_thread_total_tokens"] = t.threadTotals.TotalTokens
		}
		if t.contextWindow != nil {
			out.RuntimeMetadata["codex_context_window"] = *t.contextWindow
		}
	}

	var turnErr *protocol.TurnError
	status := protocol.TurnStatusFailed
	if t.turn != nil {
		status = t.turn.Status
		turnErr = t.turn.Error
	}
	if turnErr == nil {
		turnErr = t.turnErr
	}
	out.StopReason = string(status)
	out.AgentTurnID = t.turnID
	switch status {
	case protocol.TurnStatusCompleted:
		out.TurnCompleted = true
		return out, nil
	case protocol.TurnStatusInterrupted:
		return out, nil
	default:
		message := "codex turn failed"
		if turnErr != nil && strings.TrimSpace(turnErr.Message) != "" {
			message = turnErr.Message
		}
		return out, errors.New(message)
	}
}

// buildTurnInput assembles the turn/start input items: the Memoh context
// document, the user's message, and any inline images as data URLs.
func buildTurnInput(input external.PromptInput) []protocol.UserInput {
	items := make([]protocol.UserInput, 0, 2+len(input.Images))
	if text := strings.TrimSpace(input.ContextMarkdown); text != "" {
		items = append(items, protocol.UserInput{Text: &protocol.TextUserInput{Text: text}})
	}
	items = append(items, protocol.UserInput{Text: &protocol.TextUserInput{Text: input.Prompt}})
	for _, img := range input.Images {
		if len(img.Data) == 0 {
			continue
		}
		mime := img.MimeType
		if mime == "" {
			mime = "image/png"
		}
		url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
		items = append(items, protocol.UserInput{Image: &protocol.ImageUserInput{URL: url}})
	}
	return items
}

// fileChangeInput summarizes a file-change item for tool-event display.
func fileChangeInput(fc *protocol.FileChangeThreadItem) map[string]any {
	raw, err := json.Marshal(fc)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	delete(out, "id")
	return out
}

func mcpToolName(call *protocol.McpToolCallThreadItem) string {
	server := strings.TrimSpace(call.Server)
	tool := strings.TrimSpace(call.Tool)
	if server == "" {
		return tool
	}
	return fmt.Sprintf("%s.%s", server, tool)
}

func isMemohMCPToolCall(call *protocol.McpToolCallThreadItem) bool {
	return strings.EqualFold(strings.TrimSpace(call.Server), memohMCPServerName)
}

func mcpToolErrorMessage(mcpErr *protocol.McpToolCallError) string {
	raw, err := json.Marshal(mcpErr)
	if err != nil {
		return "MCP tool call failed"
	}
	return string(raw)
}
