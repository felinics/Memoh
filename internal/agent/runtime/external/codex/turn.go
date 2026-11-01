package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/codex/protocol"
	"github.com/felinics/memoh/internal/errs"
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
	followsGoal   bool
	goalStarting  bool
	goalActive    bool
	steers        map[string]*pendingSteer
	finalText     string
	usage         *sdk.Usage
	threadTotals  *protocol.TokenUsageBreakdown
	contextWindow *int64
	turn          *protocol.Turn
	turnErr       *protocol.TurnError
	toolNames     map[string]string // item id → emitted tool name
	// processIDs are the unified-exec processes this turn's commands started.
	// turn/interrupt only ends the turn; a stop terminates these explicitly.
	processIDs []string
	// inflight tracks decision goroutines by server-request id so a
	// serverRequest/resolved notification can withdraw them.
	inflight map[string]context.CancelFunc
	closed   bool
	doneOnce sync.Once

	// stream decouples the shared connection read loop from sink delivery: a
	// stalled consumer must not head-of-line block the bot's other threads.
	stream *external.TurnStream
}

func newTurnState(parent context.Context, input external.PromptInput, threadID string, approvalSvc approval.FlowService, waiter func(string) func(), userInput UserInputService, toolLookup func(context.Context, string) bool, logger *slog.Logger) *turnState {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	t := &turnState{
		input:        input,
		approval:     approvalSvc,
		waiter:       waiter,
		userInput:    userInput,
		toolLookup:   toolLookup,
		logger:       logger,
		threadID:     threadID,
		followsGoal:  input.Command == "goal",
		goalStarting: input.Command == "goal",
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		toolNames:    map[string]string{},
		inflight:     map[string]context.CancelFunc{},
		stream:       external.NewTurnStream(input.Sink, input.ToolOutputLimit),
	}
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
	t.mu.Unlock()
	t.stream.Finish()
	for _, cancel := range inflight {
		cancel()
	}
	t.cancel()
}

// emit records one stream event for the transcript and queues it for
// delivery. After close it is a no-op: the caller's event channel may already
// be gone, and a late decision outcome must never crash the stream.
func (t *turnState) emit(ev event.StreamEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.stream.EmitStreamEvent(ev)
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

func (t *turnState) rememberProcess(processID *string) {
	if processID == nil || strings.TrimSpace(*processID) == "" {
		return
	}
	id := strings.TrimSpace(*processID)
	t.mu.Lock()
	defer t.mu.Unlock()
	if !slices.Contains(t.processIDs, id) {
		t.processIDs = append(t.processIDs, id)
	}
}

func (t *turnState) startedProcesses() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.processIDs)
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
	case *protocol.ThreadGoalUpdatedNotification:
		t.updateGoal(&params.Goal)
	case *protocol.ThreadGoalClearedNotification:
		t.updateGoal(nil)
	case *protocol.TurnStartedNotification:
		t.startContinuation(params.Turn.ID)
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
		finish := !t.followsGoal || (!t.goalStarting && !t.goalActive) || turn.Status != protocol.TurnStatusCompleted
		t.mu.Unlock()
		if finish {
			t.finish()
		}
	case *protocol.ErrorNotification:
		if !t.acceptsTurn(params.TurnID) {
			return
		}
		if params.WillRetry {
			attrs := append([]slog.Attr{
				slog.String("thread_id", t.threadID),
				slog.String("message", errs.RedactURLs(turnErrorText(&params.Error))),
			}, summarizeErrorInfo(params.Error.CodexErrorInfo).attrs()...)
			t.logger.LogAttrs(t.ctx, slog.LevelWarn, "codex turn error, retrying", attrs...)
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
	if item.UserMessage != nil && item.UserMessage.ClientID != nil {
		t.recordSteerInput(*item.UserMessage.ClientID)
		return
	}

	switch {
	case item.CommandExecution != nil:
		cmd := item.CommandExecution
		t.rememberTool(cmd.ID, "exec")
		t.rememberProcess(cmd.ProcessID)
		t.emit(event.StreamEvent{
			Type: event.ToolCallStart, ToolCallID: cmd.ID, ToolName: "exec",
			Input: map[string]any{"command": cmd.Command, "cwd": cmd.Cwd},
		})
	case item.FileChange != nil:
		fc := item.FileChange
		t.rememberTool(fc.ID, "apply_patch")
		t.emit(event.StreamEvent{
			Type: event.ToolCallStart, ToolCallID: fc.ID, ToolName: "apply_patch",
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
	if item.UserMessage != nil && item.UserMessage.ClientID != nil {
		t.recordSteerInput(*item.UserMessage.ClientID)
		return
	}

	switch {
	case item.Plan != nil:
		// The completed plan is authoritative; partial plan deltas may differ.
		// Deliver it through the ordinary assistant transcript so it survives reload.
		text := item.Plan.Text
		if text == "" {
			return
		}
		t.mu.Lock()
		if t.finalText != "" {
			text = "\n\n" + text
		}
		t.finalText += text
		t.mu.Unlock()
		t.emit(event.StreamEvent{Type: event.TextDelta, Delta: text})
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
		result := map[string]any{"stdout": "", "stderr": ""}
		if cmd.ExitCode != nil {
			result["exit_code"] = *cmd.ExitCode
		}
		if cmd.AggregatedOutput != nil {
			result["stdout"] = *cmd.AggregatedOutput
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
		result := t.decide(ctx, params.ItemID, "apply_patch", input, fileChangeApprovalOptions(params))
		decision := fileChangeApprovalDecision(result)
		_ = c.Respond(req.ID, protocol.FileChangeRequestApprovalResponse{Decision: decision})
	case *protocol.PermissionsRequestApprovalParams:
		_ = c.Respond(req.ID, t.decidePermissions(ctx, params))
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

// decidePermissions puts a permission-profile escalation to the user as a
// generic permission card, which shows only title and request: what Codex asks
// for has to be in them for the decision to be an informed one.
func (t *turnState) decidePermissions(ctx context.Context, params *protocol.PermissionsRequestApprovalParams) protocol.PermissionsRequestApprovalResponse {
	input := map[string]any{"request_lang": "json"}
	if params.Reason != nil {
		input["title"] = *params.Reason
	}
	if request, err := json.MarshalIndent(map[string]any{"permissions": params.Permissions, "cwd": params.Cwd}, "", "  "); err == nil {
		input["request"] = string(request)
	}
	result := t.decide(ctx, params.ItemID, "permission", input, permissionsApprovalOptions())
	return permissionsApprovalResponse(params, result)
}

func permissionsApprovalOptions() []approval.PermissionOption {
	return []approval.PermissionOption{
		{ID: string(protocol.PermissionGrantScopeTurn), Name: "Allow for this turn", Kind: approval.OptionKindAllowOnce},
		{ID: string(protocol.PermissionGrantScopeSession), Name: "Allow for session", Kind: approval.OptionKindAllowAlways},
		{ID: "decline", Name: "Decline", Kind: approval.OptionKindRejectOnce},
	}
}

// permissionsApprovalResponse grants what Codex asked for, or nothing: an
// empty grant is how a declined escalation is answered.
func permissionsApprovalResponse(params *protocol.PermissionsRequestApprovalParams, result approval.FlowResult) protocol.PermissionsRequestApprovalResponse {
	scope := protocol.PermissionGrantScopeTurn
	if !result.Approved || result.SelectedOptionID == "decline" {
		return protocol.PermissionsRequestApprovalResponse{Scope: &scope}
	}
	if result.SelectedOptionID == string(protocol.PermissionGrantScopeSession) {
		scope = protocol.PermissionGrantScopeSession
	}
	return protocol.PermissionsRequestApprovalResponse{
		Permissions: protocol.GrantedPermissionProfile{FileSystem: params.Permissions.FileSystem, Network: params.Permissions.Network},
		Scope:       &scope,
	}
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

// decide runs one approval through the shared decision flow.
func (t *turnState) decide(ctx context.Context, callID, toolName string, input map[string]any, options []approval.PermissionOption) approval.FlowResult {
	return external.DecideApproval(ctx, t.approval, t.waiter, t.input, external.ApprovalCall{
		ToolCallID: callID, ToolName: toolName, Input: input, Options: options,
	}, t.emit, t.logger)
}

// result assembles the durable outcome after the turn settled.
func (t *turnState) result() (external.PromptResult, error) {
	t.mu.Lock()
	finalText := t.finalText
	t.mu.Unlock()
	// Not under t.mu: the stream may still be delivering a steer, which takes it.
	output := t.stream.Transcript(finalText)
	steerIDs := t.stream.SteerInputIDs()
	t.mu.Lock()
	defer t.mu.Unlock()

	out := external.PromptResult{
		Output:        output,
		SteerInputIDs: steerIDs,
		Text:          finalText,
	}
	if t.usage != nil {
		usage := *t.usage
		out.Usage = &usage
	}
	if t.threadTotals != nil {
		out.RuntimeMetadata = map[string]any{}
		// Context-occupancy data for the session UI: the thread's cumulative
		// token count against its model context window.
		out.RuntimeMetadata["codex_thread_total_tokens"] = t.threadTotals.TotalTokens
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
	out.FinalTurnAnchorOnly = t.followsGoal
	switch status {
	case protocol.TurnStatusCompleted:
		out.TurnCompleted = true
		return out, nil
	case protocol.TurnStatusInterrupted:
		return out, nil
	default:
		return out, failedTurnError(turnErr)
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
