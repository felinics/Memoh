package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
)

const (
	// maxLineBytes bounds one NDJSON line from the CLI; large tool results
	// ride inside, so the ceiling is generous.
	maxLineBytes = 32 * 1024 * 1024
)

// turnRunner drives one Memoh run, including native turns started by steering.
type turnRunner struct {
	input     external.PromptInput
	approval  approval.FlowService
	waiter    func(approvalID string) func()
	logger    *slog.Logger
	userInput userinput.FlowService
	// s is the process the turn runs on.
	s *cliSession

	// ctx is the turn-scoped context bounding approval decisions.
	ctx    context.Context
	cancel context.CancelFunc

	// onCLIVersion, when set, receives the version the CLI reports in its
	// system/init handshake so the launcher resolver can correct its cache.
	// It runs on the read loop and must return quickly.
	onCLIVersion func(ctx context.Context, version string)

	submissionMu sync.Mutex

	done chan struct{}

	// stream delivers events off the read loop: a slow consumer must not
	// hold up control responses the CLI waits for.
	stream *external.TurnStream

	mu              sync.Mutex
	closed          bool
	assistantTxt    strings.Builder
	streamedText    strings.Builder // deltas for the current native assistant message
	turnHasText     bool
	sessionID       string
	result          *inboundMessage
	inboundCancels  map[string]context.CancelFunc
	memohMCPCallIDs map[string]struct{}
	toolCalls       map[string]toolCall
	doneOnce        sync.Once
	permissionMode  string
	runtimeMetadata map[string]any
	protocolErr     error
	// exitErr records a process teardown failure (non-zero exit, transport
	// loss, or no exit after stdin EOF). It never changes the protocol
	// outcome the CLI already reported; it only explains a turn that ended
	// without a result.
	exitErr         error
	compactBoundary bool
	compactFailed   bool
	ready           chan struct{}
	readyOnce       sync.Once
	steerSupported  bool
	steers          map[string]*pendingSteer
	awaitingResult  bool
	ending          bool
	usage           resultUsage
	hasUsage        bool
	// apiError is the category of the last failed API call, until a later
	// reply recovered.
	apiError  string
	resultIDs map[string]struct{}

	// result.result repeats the last assistant message; when that message
	// was a native command's receipt, the result is the receipt, not a reply.
	lastIsCommandOutput bool

	// The open thinking block and the deltas it received, and whether a model
	// text block is open (closed by endText); read loop only.
	thinkingOpen     bool
	streamedThinking strings.Builder
	textOpen         bool
}

// newTurnRunner attaches a turn to a process of its own.
func newTurnRunner(parent context.Context, input external.PromptInput, proc cliProcess, approvalSvc approval.FlowService, waiter func(string) func(), logger *slog.Logger) *turnRunner {
	return newCLISession(proc, logger).beginTurn(parent, input, approvalSvc, waiter)
}

func newTurn(parent context.Context, s *cliSession, input external.PromptInput, approvalSvc approval.FlowService, waiter func(string) func()) *turnRunner {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	return &turnRunner{
		input:           input,
		approval:        approvalSvc,
		waiter:          waiter,
		logger:          s.logger,
		s:               s,
		ctx:             ctx,
		cancel:          cancel,
		done:            make(chan struct{}),
		inboundCancels:  map[string]context.CancelFunc{},
		memohMCPCallIDs: map[string]struct{}{},
		toolCalls:       map[string]toolCall{},
		runtimeMetadata: map[string]any{},
		ready:           make(chan struct{}), steers: map[string]*pendingSteer{},
		resultIDs: map[string]struct{}{},
		stream:    external.NewTurnStream(input.Sink, input.ToolOutputLimit),
	}
}

// close stops event delivery and unwinds waiting decisions.
func (t *turnRunner) close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.stream.Finish()
	t.cancel()
	t.s.endTurn(t)
}

// emit queues one stream event for the sink and the transcript. After close
// it is a no-op so late decision outcomes cannot crash a finished stream.
func (t *turnRunner) emit(ev event.StreamEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.stream.EmitStreamEvent(ev)
	}
}

func (t *turnRunner) finish() {
	t.doneOnce.Do(func() { close(t.done) })
}

func (t *turnRunner) writeLine(line []byte) error {
	return t.s.writeLine(line)
}

type controlResult struct {
	response json.RawMessage
	err      error
}

type controlRejection struct{ diagnostic string }

func (e *controlRejection) Error() string { return e.diagnostic }

// callControl asks the process on the turn's behalf; the wait ends with the
// turn.
func (t *turnRunner) callControl(ctx context.Context, subtype string, extra map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-t.done:
		case <-t.ctx.Done():
		case <-ctx.Done():
		}
		cancel()
	}()
	raw, err := t.s.callControl(ctx, subtype, extra)
	if err != nil && (t.ctx.Err() != nil || t.isDone()) {
		return nil, t.processError(subtype)
	}
	return raw, err
}

func (t *turnRunner) isDone() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

func (t *turnRunner) processError(operation string) error {
	t.mu.Lock()
	protocolErr := t.protocolErr
	t.mu.Unlock()
	return errors.Join(t.s.processError(operation), protocolErr)
}

func (t *turnRunner) initialize(ctx context.Context) (initializeResponse, error) {
	raw, err := t.callControl(ctx, "initialize", nil)
	if err != nil {
		return initializeResponse{}, err
	}
	var response initializeResponse
	err = json.Unmarshal(raw, &response)
	return response, errs.WrapDependency(err, "decode claude initialize response")
}

func (t *turnRunner) getSettings(ctx context.Context) (settingsResponse, error) {
	raw, err := t.callControl(ctx, "get_settings", nil)
	if err != nil {
		return settingsResponse{}, err
	}
	var settings settingsResponse
	err = json.Unmarshal(raw, &settings)
	return settings, errs.WrapDependency(err, "")
}

func (t *turnRunner) handleMessage(msg *inboundMessage) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return
	}
	switch msg.Type {
	case "command_lifecycle":
		t.handleCommandLifecycle(msg)
	case "tool_progress":
		if msg.ParentToolUseID == nil && msg.ToolUseID != "" && !t.isMemohMCPWrapper(msg.ToolUseID, msg.ToolName) {
			t.emit(event.StreamEvent{
				Type: event.ToolCallMetadata, ToolCallID: msg.ToolUseID, ToolName: canonicalDisplayToolName(msg.ToolName),
				Metadata: map[string]any{"execution_progress": map[string]any{"elapsed_time_seconds": msg.ElapsedTimeSeconds}},
			})
		}
	case messageTypeSystem:
		switch msg.Subtype {
		case "status":
			code := ""
			if msg.Status != nil && *msg.Status == "compacting" {
				code = "compacting"
			}
			t.emit(event.StreamEvent{Type: event.RuntimeStatus, Code: code})
		case "api_retry":
			t.emit(event.StreamEvent{Type: event.RuntimeStatus, Code: "api_retry", Metadata: map[string]any{
				"attempt": strconv.Itoa(msg.Attempt), "max_retries": strconv.Itoa(msg.MaxRetries),
				"seconds": strconv.Itoa((msg.RetryDelayMS + 999) / 1000),
			}})
		}
		if msg.Subtype == "commands_changed" {
			// This is a full replacement, including an empty list after removal.
			t.mu.Lock()
			t.runtimeMetadata["claude_commands"] = msg.Commands
			t.mu.Unlock()
		}
		if msg.Subtype == "local_command_output" && msg.Content != "" {
			// Command receipts keep their own identity in the transcript; they
			// never join the model prose that result.result may replace.
			t.emitCommandOutput(msg.Content)
		}
		if msg.Subtype == "compact_boundary" && msg.CompactMetadata != nil && msg.CompactMetadata.Trigger == "manual" {
			t.mu.Lock()
			t.compactBoundary = true
			t.mu.Unlock()
		}
		if msg.CompactResult == "failed" {
			t.mu.Lock()
			t.compactFailed = true
			t.mu.Unlock()
		}
		t.observePermissionMode(msg.PermissionMode)
		if msg.Subtype == "init" {
			t.mu.Lock()
			t.steerSupported = slices.Contains(msg.Capabilities, "msg_lifecycle_v1") && slices.Contains(msg.Capabilities, "interrupt_cancel_queued_v1")
			if msg.Model != "" {
				t.runtimeMetadata["claude_model"] = msg.Model
			}
			if msg.Skills != nil {
				t.runtimeMetadata["claude_skills"] = msg.Skills
			}
			if msg.MCPServers != nil {
				t.runtimeMetadata["claude_mcp_servers"] = msg.MCPServers
			}
			t.sessionID = msg.SessionID
			t.mu.Unlock()
			t.s.mu.Lock()
			t.s.nativeID = msg.SessionID
			t.s.mu.Unlock()
			t.readyOnce.Do(func() { close(t.ready) })
			if msg.ClaudeCodeVersion != "" && msg.ClaudeCodeVersion != PinnedCLIVersion {
				t.logger.Warn("claude CLI version differs from the pinned wire contract",
					slog.String("cli_version", msg.ClaudeCodeVersion), slog.String("pinned", PinnedCLIVersion))
			}
			if msg.ClaudeCodeVersion != "" && t.onCLIVersion != nil {
				t.onCLIVersion(t.ctx, msg.ClaudeCodeVersion)
			}
		}
	case messageTypeAssistant:
		if msg.ParentToolUseID != nil {
			return // subagent traffic surfaces through its Task tool events
		}
		if msg.Error != "" {
			// API diagnostics are not model prose. The native result owns failure
			// settlement; an error frame may be followed by a successful recovery.
			t.logger.Warn("claude API error", slog.String("code", msg.Error), slog.String("message", truncateForLog(msg.Message)))
			t.mu.Lock()
			t.apiError = msg.Error
			t.mu.Unlock()
			return
		}
		chat, ok := decodeChatMessage(msg.Message)
		if !ok {
			return
		}
		t.mu.Lock()
		t.apiError = ""
		t.mu.Unlock()
		streamed := t.streamedText.String()
		if msg.LocalCommandSource == "" {
			t.streamedText.Reset()
		}
		for _, block := range chat.Content {
			switch block.Type {
			case "tool_use":
				if t.isMemohMCPWrapper(block.ID, block.Name) {
					continue
				}
				var input any
				if len(block.Input) > 0 {
					_ = json.Unmarshal(block.Input, &input)
				}
				toolName, input := canonicalDisplayToolCall(block.Name, input)
				t.endText()
				t.rememberToolCall(block.ID, toolName, input)
				t.emit(event.StreamEvent{Type: event.ToolCallStart, ToolCallID: block.ID, ToolName: toolName, Input: input})
			case "thinking":
				t.completeThinking(block.Thinking)
			case "text":
				if msg.LocalCommandSource != "" {
					t.emitCommandOutput(block.Text)
					continue
				}
				// Complete each native message at its own boundary. A full
				// assistant reply may arrive without deltas (or after a partial
				// stream); normalize the missing text onto the same event path.
				if strings.HasPrefix(streamed, block.Text) {
					streamed = strings.TrimPrefix(streamed, block.Text)
				} else {
					t.emitModelText(strings.TrimPrefix(block.Text, streamed))
					streamed = ""
				}
			}
		}
	case messageTypeUser:
		if msg.ParentToolUseID != nil {
			return
		}
		chat, ok := decodeChatMessage(msg.Message)
		if !ok {
			return
		}
		// tool_use_result belongs to the whole line, so it can only be
		// attributed to a result when the line carries just one.
		results := 0
		for _, block := range chat.Content {
			if block.Type == "tool_result" {
				results++
			}
		}
		structured := msg.ToolUseResult
		if results != 1 {
			structured = nil
		}
		for _, block := range chat.Content {
			if block.Type != "tool_result" {
				continue
			}
			if t.isMemohMCPWrapper(block.ToolUseID, "") {
				continue
			}
			call := t.takeToolCall(block.ToolUseID)
			result, failed := translateToolResult(call.name, call.input, block.Content, structured, block.IsError)
			ev := event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: block.ToolUseID, ToolName: call.name, Result: result}
			if failed {
				ev.Status = "failed"
			}
			t.emit(ev)
		}
	case messageTypeStreamEvent:
		if msg.ParentToolUseID != nil {
			return
		}
		var ev streamEvent
		if err := json.Unmarshal(msg.Event, &ev); err != nil {
			return
		}
		switch ev.Type {
		case "message_start":
			t.streamedText.Reset()
			t.emit(event.StreamEvent{Type: event.RuntimeStatus})
		case "content_block_start":
			if ev.ContentBlock.Type == "thinking" {
				t.openThinking()
				// Gateways may put the thinking text here instead of in deltas.
				t.emitThinking(ev.ContentBlock.Thinking)
			}
		case "content_block_stop":
			t.closeThinking()
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				t.streamedText.WriteString(ev.Delta.Text)
				t.emitModelText(ev.Delta.Text)
			case "thinking_delta":
				// Only deltas: the CLI builds the block's full message from them,
				// never from inline start text.
				t.streamedThinking.WriteString(ev.Delta.Thinking)
				t.emitThinking(ev.Delta.Thinking)
			}
		}
	case messageTypeResult:
		t.mu.Lock()
		if msg.UUID != "" {
			if _, exists := t.resultIDs[msg.UUID]; exists {
				t.mu.Unlock()
				return
			}
			t.resultIDs[msg.UUID] = struct{}{}
		}
		t.result = msg
		t.awaitingResult = false
		if msg.Usage != nil {
			t.hasUsage = true
			t.usage.InputTokens += msg.Usage.InputTokens
			t.usage.OutputTokens += msg.Usage.OutputTokens
			t.usage.CacheReadInputTokens += msg.Usage.CacheReadInputTokens
			t.usage.CacheCreationInputTokens += msg.Usage.CacheCreationInputTokens
			// Local commands can report zero usage without a model request.
			// Keep the last observed model usage in that case.
			if t.usage != (resultUsage{}) {
				t.runtimeMetadata["claude_usage"] = t.usage
			}
		}
		hasText := t.turnHasText
		repeatsCommand := t.lastIsCommandOutput
		t.mu.Unlock()
		t.closeThinking()
		if !hasText && !msg.IsError && !repeatsCommand {
			t.emitModelText(msg.Result)
		}
		t.mu.Lock()
		t.turnHasText = false
		t.mu.Unlock()
		t.streamedText.Reset()
		t.maybeFinishResult()
	case messageTypeControlRequest:
		var payload controlRequestPayload
		if err := json.Unmarshal(msg.Request, &payload); err != nil {
			t.respondControlError(msg.RequestID, "memoh could not decode this request")
			return
		}
		switch payload.Subtype {
		case "elicitation":
			requestCtx, done := t.beginInboundControl(msg.RequestID)
			go func() {
				defer done()
				t.handleElicitation(requestCtx, msg.RequestID, msg.Request)
			}()
		case "can_use_tool":
			requestCtx, done := t.beginInboundControl(msg.RequestID)
			go func() {
				defer done()
				t.handleCanUseTool(requestCtx, msg.RequestID, &payload)
			}()
		default:
			t.logger.Warn("claude: unhandled control request", slog.String("subtype", payload.Subtype))
			t.respondControlError(msg.RequestID, "memoh does not handle this request")
		}
	case messageTypeControlCancel:
		t.cancelInboundControl(msg.RequestID)
	case messageTypeControlResponse:
		t.s.resolveControl(msg)
	}
}

func (t *turnRunner) beginInboundControl(requestID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(t.ctx)
	t.mu.Lock()
	t.inboundCancels[requestID] = cancel
	t.mu.Unlock()
	return ctx, func() {
		cancel()
		t.mu.Lock()
		delete(t.inboundCancels, requestID)
		t.mu.Unlock()
	}
}

func (t *turnRunner) cancelInboundControl(requestID string) {
	t.mu.Lock()
	cancel := t.inboundCancels[requestID]
	delete(t.inboundCancels, requestID)
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (t *turnRunner) isMemohMCPWrapper(id, name string) bool {
	id = strings.TrimSpace(id)
	t.mu.Lock()
	defer t.mu.Unlock()
	if isMemohMCPToolName(name) {
		if id != "" {
			t.memohMCPCallIDs[id] = struct{}{}
		}
		return true
	}
	_, ok := t.memohMCPCallIDs[id]
	return ok
}

// toolCall is a started call, kept until its result arrives: the result line
// names only the call id, and its translation depends on the tool.
type toolCall struct {
	name  string
	input any
}

func (t *turnRunner) rememberToolCall(id, name string, input any) {
	t.mu.Lock()
	t.toolCalls[id] = toolCall{name: name, input: input}
	t.mu.Unlock()
}

// takeToolCall returns the started call for a result and forgets it.
func (t *turnRunner) takeToolCall(id string) toolCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	call := t.toolCalls[id]
	delete(t.toolCalls, id)
	return call
}

func (t *turnRunner) respondControlError(requestID, message string) {
	line, err := controlErrorResponse(requestID, message)
	if err != nil {
		return
	}
	_ = t.writeLine(line)
}

// handleCanUseTool routes one permission callback through the Memoh approval
// flow. Runs on its own goroutine, bounded by the turn-scoped context.
func (t *turnRunner) handleCanUseTool(ctx context.Context, requestID string, payload *controlRequestPayload) {
	if payload.ToolName == "AskUserQuestion" {
		t.answerQuestions(ctx, requestID, payload)
		return
	}
	if t.isMemohMCPWrapper(payload.ToolUseID, payload.ToolName) {
		// Memoh tools enforce Memoh's own approval rules inside the tool
		// gateway when they execute, whatever Claude's permission mode is.
		// Claude's question about the wrapper would only add a second card.
		line, err := permissionAllowResponse(requestID, payload.Input, payload.ToolUseID)
		if err != nil {
			t.respondControlError(requestID, "memoh could not encode the decision")
			return
		}
		_ = t.writeLine(line)
		return
	}
	callID := strings.TrimSpace(payload.ToolUseID)
	if callID == "" {
		callID = requestID
	}
	// Reuse the transcript's presentation mapping without changing the native
	// input returned to Claude. Other requests use the existing permission card.
	toolName, toolInput := canonicalDisplayToolCall(payload.ToolName, maps.Clone(payload.Input))
	if _, ok := approval.OperationForTool(toolName); !ok {
		raw, err := json.MarshalIndent(payload.Input, "", "  ")
		if err != nil {
			t.respondControlError(requestID, "memoh could not encode the permission request")
			return
		}
		// The card is its own transcript entry. Under the real tool_use id the
		// recorder would replace the tool call's arguments with the card body.
		callID = "claude-permission-" + uuid.NewString()
		toolName = "permission"
		toolInput = map[string]any{"title": payload.ToolName, "request": string(raw), "request_lang": "json"}
	}
	result := t.decide(ctx, callID, toolName, toolInput)
	if ctx.Err() != nil {
		return
	}
	var line []byte
	var err error
	if result.Approved {
		line, err = permissionAllowResponse(requestID, payload.Input, payload.ToolUseID)
	} else {
		reason := result.DecisionReason
		if reason == "" {
			reason = "the user did not approve this tool call"
		}
		line, err = permissionDenyResponse(requestID, reason)
	}
	if err != nil {
		t.respondControlError(requestID, "memoh could not encode the decision")
		return
	}
	_ = t.writeLine(line)
}

func canonicalDisplayToolName(toolName string) string {
	name := strings.TrimSpace(toolName)
	lower := strings.ToLower(name)
	if isMemohMCPToolName(name) {
		return strings.TrimPrefix(lower, "mcp__"+memohMCPServerName+"__")
	}
	switch lower {
	case "askuserquestion":
		return userinput.ToolNameAskUser
	case "bash":
		return "exec"
	case "write":
		return "write"
	case "edit", "multiedit", "notebookedit":
		return "edit"
	case "read":
		return "read"
	case "webfetch":
		return "web_fetch"
	case "websearch":
		return "web_search"
	default:
		return name
	}
}

func isMemohMCPToolName(toolName string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(toolName)), "mcp__"+memohMCPServerName+"__")
}

func canonicalDisplayToolCall(toolName string, input any) (string, any) {
	name := canonicalDisplayToolName(toolName)
	fields, ok := input.(map[string]any)
	if !ok {
		return name, input
	}
	switch name {
	case "write", "edit", "read":
		renameDisplayInputField(fields, "file_path", "path")
		renameDisplayInputField(fields, "notebook_path", "path")
	}
	if name == "edit" {
		renameDisplayInputField(fields, "old_string", "old_text")
		renameDisplayInputField(fields, "new_string", "new_text")
		renameDisplayInputField(fields, "new_source", "new_text")
	}
	return name, fields
}

func renameDisplayInputField(fields map[string]any, from, to string) {
	if value, ok := fields[from]; ok {
		fields[to] = value
		delete(fields, from)
	}
}

// decide forwards a native tool question to a user who holds the matching
// workspace permission. Claude's own permission mode is the first and only
// policy layer for Claude's native tools; Memoh's allow/ask/deny rules govern
// Memoh-provided tools, which enforce them in the gateway. This mirrors the
// Codex runtime, so neither external agent re-runs Memoh policy here.
func (t *turnRunner) decide(ctx context.Context, callID, toolName string, input any) approval.FlowResult {
	if t.approval == nil {
		return approval.FlowResult{Status: approval.StatusRejected, DecisionReason: "approval service unavailable"}
	}
	result, err := approval.RunRuntimeFlow(ctx, t.approval, approval.FlowRequest{
		Input: approval.CreatePendingInput{
			BotID:                        t.input.BotID,
			SessionID:                    t.input.ThreadID,
			RouteID:                      t.input.RouteID,
			ChannelIdentityID:            t.input.ChannelIdentityID,
			RequestedByChannelIdentityID: t.input.ChannelIdentityID,
			ToolCallID:                   callID,
			ToolName:                     toolName,
			ToolInput:                    input,
		},
		Interactive:    t.input.CanRequestUserInput,
		RegisterWaiter: t.waiter,
		Emit:           t.emitApprovalRequest,
	})
	if err != nil {
		if ctx.Err() != nil {
			return approval.FlowResult{Status: approval.StatusCancelled, DecisionReason: "the turn ended before a decision arrived"}
		}
		t.logger.ErrorContext(ctx, "claude approval flow failed", slog.Any("error", err))
		return approval.FlowResult{Status: approval.StatusCancelled, DecisionReason: "approval flow failed"}
	}
	return result
}

func (t *turnRunner) emitApprovalRequest(req approval.Request) bool {
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

// observeExit records how the process teardown failed after the turn settled.
func (t *turnRunner) observeExit(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	t.exitErr = errors.Join(t.exitErr, err)
	t.mu.Unlock()
	t.logger.Warn("claude CLI did not exit cleanly", slog.Any("error", err), slog.String("stderr", t.s.proc.StderrTail()))
}

// interrupt asks the CLI to stop the running turn.
func (t *turnRunner) interrupt() {
	// No response waiter: teardown waits for the native result and process exit.
	line, err := controlRequestLine("memoh-interrupt", "interrupt", map[string]any{"cancel_queued": true})
	if err == nil {
		err = t.writeLine(line)
	}
	if err != nil {
		t.logger.Warn("claude interrupt send failed", slog.Any("error", err))
	}
}

func (t *turnRunner) emitCommandOutput(text string) {
	t.endText()
	t.mu.Lock()
	t.lastIsCommandOutput = true
	t.mu.Unlock()
	t.emit(event.StreamEvent{Type: event.CommandOutput, ToolName: t.input.Command, Delta: text})
}

func (t *turnRunner) emitModelText(text string) {
	if text == "" || strings.TrimSpace(text) == noResponseRequested {
		return
	}
	t.mu.Lock()
	t.assistantTxt.WriteString(text)
	t.turnHasText = true
	t.lastIsCommandOutput = false
	t.mu.Unlock()
	t.closeThinking()
	t.textOpen = true
	t.emit(event.StreamEvent{Type: event.TextDelta, Delta: text})
}

// endText closes the open model text block. Reasoning, a tool call, a
// command receipt and queued input end it where the transcript starts a new
// part; live views split at that same point.
func (t *turnRunner) endText() {
	if t.textOpen {
		t.textOpen = false
		t.emit(event.StreamEvent{Type: event.TextEnd})
	}
}

func (t *turnRunner) openThinking() {
	t.closeThinking()
	t.thinkingOpen = true
	t.emit(event.StreamEvent{Type: event.ReasoningStart})
}

func (t *turnRunner) emitThinking(text string) {
	if text == "" {
		return
	}
	if !t.thinkingOpen {
		t.openThinking()
	}
	t.endText()
	t.emit(event.StreamEvent{Type: event.ReasoningDelta, Delta: text})
}

func (t *turnRunner) closeThinking() {
	if t.thinkingOpen {
		t.thinkingOpen = false
		t.emit(event.StreamEvent{Type: event.ReasoningEnd})
	}
	t.streamedThinking.Reset()
}

// completeThinking settles a full-message thinking block. The CLI sends it
// before the streamed block's stop, carrying that block's deltas, so a block
// continuing what was streamed only adds its missing rest (nothing for the
// usual echo), as the text path does. Any other block is a separate
// generation, such as the CLI's non-streaming retry of a broken stream, and
// stands alone instead of joining the cut one.
func (t *turnRunner) completeThinking(thinking string) {
	if rest, ok := strings.CutPrefix(thinking, t.streamedThinking.String()); ok && t.thinkingOpen {
		t.emitThinking(rest)
	} else {
		t.closeThinking()
		t.emitThinking(thinking)
	}
	t.closeThinking()
}

// buildResult assembles the durable outcome after the turn settled.
func (t *turnRunner) buildResult(storedSessionID string) (external.PromptResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	finalText := t.assistantTxt.String()
	if t.result != nil && !t.result.IsError {
		reported := strings.TrimSpace(t.result.Result)
		if reported != "" && reported != noResponseRequested && !t.lastIsCommandOutput {
			finalText = t.result.Result
		}
	}
	if strings.TrimSpace(finalText) == noResponseRequested {
		finalText = ""
	}
	out := external.PromptResult{Output: withoutNoResponseSentinel(t.stream.Transcript(finalText)), Text: finalText, SteerInputIDs: t.stream.SteerInputIDs()}
	if t.hasUsage {
		out.Usage = t.usage.sdkUsage()
	}
	if t.sessionID != "" && t.sessionID != storedSessionID {
		t.runtimeMetadata[metadataSessionIDKey] = t.sessionID
	}
	if len(t.runtimeMetadata) > 0 {
		out.RuntimeMetadata = maps.Clone(t.runtimeMetadata)
	}

	switch {
	case t.protocolErr != nil:
		out.StopReason = "control_error"
		return out, t.protocolErr
	case len(t.steers) > 0 && !t.ending:
		out.StopReason = "process_exit"
		return out, errs.NewDependency("claude exited with unsettled queued input")
	case t.input.Command == "compact" && (!t.compactBoundary || t.compactFailed):
		out.StopReason = "compaction_failed"
		return out, errs.NewDependency("claude did not complete manual compaction")
	case t.result == nil:
		out.StopReason = "process_exit"
		if t.exitErr != nil {
			return out, fmt.Errorf("claude CLI exited without a result: %w: %s", t.exitErr, t.s.proc.StderrTail())
		}
		return out, errs.NewDependency(fmt.Sprintf("claude CLI exited without a result: %s", t.s.proc.StderrTail()))
	case t.result.IsError:
		out.StopReason = t.result.Subtype
		message := strings.TrimSpace(t.result.Result)
		if message == "" {
			message = strings.Join(t.result.Errors, "; ")
		}
		if message == "" {
			message = "claude turn failed (" + t.result.Subtype + ")"
		}
		return out, failedResultError(message, t.apiError)
	default:
		out.StopReason = t.result.Subtype
		out.TurnCompleted = true
		return out, nil
	}
}

func withoutNoResponseSentinel(messages []sdk.Message) []sdk.Message {
	out := make([]sdk.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != sdk.MessageRoleAssistant {
			out = append(out, message)
			continue
		}
		parts := make([]sdk.MessagePart, 0, len(message.Content))
		for _, part := range message.Content {
			text, ok := part.(sdk.TextPart)
			if ok && strings.TrimSpace(text.Text) == noResponseRequested {
				continue
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 {
			continue
		}
		message.Content = parts
		out = append(out, message)
	}
	return out
}

func truncateForLog(line []byte) string {
	const limit = 512
	if len(line) <= limit {
		return string(line)
	}
	return string(line[:limit]) + "…"
}
