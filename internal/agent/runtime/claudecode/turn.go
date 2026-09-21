package claudecode

import (
	"bufio"
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
	"sync/atomic"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
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
	proc      cliProcess
	userInput userinput.FlowService

	// ctx is the turn-scoped context bounding approval decisions.
	ctx    context.Context
	cancel context.CancelFunc

	// onCLIVersion, when set, receives the version the CLI reports in its
	// system/init handshake so the launcher resolver can correct its cache.
	// It runs on the read loop and must return quickly.
	onCLIVersion func(ctx context.Context, version string)

	writeMu      sync.Mutex
	submissionMu sync.Mutex
	nextID       atomic.Uint64

	done     chan struct{}
	readDone chan struct{}

	mu              sync.Mutex
	closed          bool
	events          []event.StreamEvent
	assistantTxt    strings.Builder
	streamedText    strings.Builder // deltas for the current native assistant message
	turnHasText     bool
	sessionID       string
	result          *inboundMessage
	pendingCtrl     map[string]chan controlResult
	inboundCancels  map[string]context.CancelFunc
	memohMCPCallIDs map[string]struct{}
	doneOnce        sync.Once
	permissionMode  string
	runtimeMetadata map[string]any
	protocolErr     error
	// exitErr records a process teardown failure (non-zero exit, transport
	// loss, or no exit after stdin EOF). It never changes the protocol
	// outcome the CLI already reported; it only gates checkpoint staging and
	// explains a turn that ended without a result.
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
	resultIDs       map[string]struct{}
}

func newTurnRunner(parent context.Context, input external.PromptInput, proc cliProcess, approvalSvc approval.FlowService, waiter func(string) func(), logger *slog.Logger) *turnRunner {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	return &turnRunner{
		input:           input,
		approval:        approvalSvc,
		waiter:          waiter,
		logger:          logger,
		proc:            proc,
		ctx:             ctx,
		cancel:          cancel,
		done:            make(chan struct{}),
		readDone:        make(chan struct{}),
		pendingCtrl:     map[string]chan controlResult{},
		inboundCancels:  map[string]context.CancelFunc{},
		memohMCPCallIDs: map[string]struct{}{},
		runtimeMetadata: map[string]any{},
		ready:           make(chan struct{}), steers: map[string]*pendingSteer{},
		resultIDs: map[string]struct{}{},
	}
}

// close stops event delivery and unwinds waiting decisions.
func (t *turnRunner) close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.cancel()
}

// emit forwards one stream event to the sink and records it for the
// transcript. After close it is a no-op so late decision outcomes cannot
// crash a finished stream.
func (t *turnRunner) emit(ev event.StreamEvent) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.events = append(t.events, ev)
	t.mu.Unlock()
	t.input.Sink.EmitStreamEvent(ev)
}

func (t *turnRunner) finish() {
	t.doneOnce.Do(func() { close(t.done) })
}

func (t *turnRunner) writeLine(line []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err := t.proc.Write(append(line, '\n'))
	return err
}

type controlResult struct {
	response json.RawMessage
	err      error
}

type controlRejection struct{ diagnostic string }

func (e *controlRejection) Error() string { return e.diagnostic }

// callControl bounds every handshake and removes abandoned waiters. Wire
// errors remain private diagnostics and are mapped at the application boundary.
func (t *turnRunner) callControl(ctx context.Context, subtype string, extra map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	id := "memoh-" + strconv.FormatUint(t.nextID.Add(1), 10)
	line, err := controlRequestLine(id, subtype, extra)
	if err != nil {
		return nil, err
	}
	ch := make(chan controlResult, 1)
	t.mu.Lock()
	t.pendingCtrl[id] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pendingCtrl, id)
		t.mu.Unlock()
	}()
	if err := t.writeLine(line); err != nil {
		return nil, err
	}
	select {
	case result := <-ch:
		return result.response, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.ctx.Done():
		return nil, t.ctx.Err()
	case <-t.proc.Done():
		return nil, t.processError(subtype)
	case <-t.done:
		return nil, t.processError(subtype)
	}
}

func (t *turnRunner) processError(operation string) error {
	t.mu.Lock()
	protocolErr := t.protocolErr
	t.mu.Unlock()
	return errors.Join(
		fmt.Errorf("claude %s: stream ended; stderr: %s", operation, t.proc.StderrTail()),
		t.proc.Err(), protocolErr,
	)
}

func (t *turnRunner) getSettings(ctx context.Context) (settingsResponse, error) {
	raw, err := t.callControl(ctx, "get_settings", nil)
	if err != nil {
		return settingsResponse{}, err
	}
	var settings settingsResponse
	err = json.Unmarshal(raw, &settings)
	return settings, err
}

// readLoop consumes the CLI's NDJSON stream until the process exits.
func (t *turnRunner) readLoop() {
	defer close(t.readDone)
	scanner := bufio.NewScanner(t.proc)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		msg, err := decodeInbound(line)
		if err != nil {
			// The CLI occasionally writes plain text on stdout; survivable.
			t.logger.Warn("claude: undecodable line", slog.String("line", truncateForLog(line)))
			continue
		}
		t.handleMessage(msg)
	}
	if err := scanner.Err(); err != nil {
		t.observeExit(err)
	}
	t.finish()
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
			t.emit(event.StreamEvent{Type: event.CommandOutput, ToolName: t.input.Command, Delta: msg.Content})
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
			return
		}
		chat, ok := decodeChatMessage(msg.Message)
		if !ok {
			return
		}
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
				t.emit(event.StreamEvent{Type: event.ToolCallStart, ToolCallID: block.ID, ToolName: toolName, Input: input})
			case "text":
				if msg.LocalCommandSource != "" {
					t.emit(event.StreamEvent{Type: event.CommandOutput, ToolName: t.input.Command, Delta: block.Text})
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
		for _, block := range chat.Content {
			if block.Type != "tool_result" {
				continue
			}
			if t.isMemohMCPWrapper(block.ToolUseID, "") {
				continue
			}
			var content any
			if len(block.Content) > 0 {
				_ = json.Unmarshal(block.Content, &content)
			}
			ev := event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: block.ToolUseID, Result: content}
			if block.IsError {
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
		if ev.Type == "message_start" {
			t.streamedText.Reset()
			t.emit(event.StreamEvent{Type: event.RuntimeStatus})
		}
		if ev.Type != "content_block_delta" {
			return
		}
		switch ev.Delta.Type {
		case "text_delta":
			t.streamedText.WriteString(ev.Delta.Text)
			t.emitModelText(ev.Delta.Text)
		case "thinking_delta":
			if ev.Delta.Thinking != "" {
				t.emit(event.StreamEvent{Type: event.ReasoningDelta, Delta: ev.Delta.Thinking})
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
		t.mu.Unlock()
		if !hasText && !msg.IsError {
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
		var envelope struct {
			Subtype   string          `json:"subtype"`
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
			Error     string          `json:"error"`
		}
		if err := json.Unmarshal(msg.Response, &envelope); err != nil {
			return
		}
		t.mu.Lock()
		ch := t.pendingCtrl[envelope.RequestID]
		delete(t.pendingCtrl, envelope.RequestID)
		t.mu.Unlock()
		if ch != nil {
			result := controlResult{response: envelope.Response}
			if envelope.Subtype != "success" {
				result.err = &controlRejection{diagnostic: fmt.Sprintf("claude control %s: %s", envelope.Subtype, envelope.Error)}
			}
			ch <- result
		}
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
		CancelOnAbort: func(cancelCtx context.Context, req approval.Request, reason string) (approval.Request, error) {
			return t.approval.Reject(cancelCtx, req.ID, "", reason)
		},
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
	t.logger.Warn("claude CLI did not exit cleanly", slog.Any("error", err), slog.String("stderr", t.proc.StderrTail()))
}

// exitFailed reports whether the process teardown failed.
func (t *turnRunner) exitFailed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitErr != nil
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

func (t *turnRunner) emitModelText(text string) {
	if text == "" || strings.TrimSpace(text) == noResponseRequested {
		return
	}
	t.mu.Lock()
	t.assistantTxt.WriteString(text)
	t.turnHasText = true
	t.mu.Unlock()
	t.emit(event.StreamEvent{Type: event.TextDelta, Delta: text})
}

// buildResult assembles the durable outcome after the turn settled.
func (t *turnRunner) buildResult(storedSessionID string) (external.PromptResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	finalText := t.assistantTxt.String()
	if t.result != nil && !t.result.IsError {
		reported := strings.TrimSpace(t.result.Result)
		if reported != "" && reported != noResponseRequested {
			finalText = t.result.Result
		}
	}
	if strings.TrimSpace(finalText) == noResponseRequested {
		finalText = ""
	}
	recorder := external.NewTranscriptRecorder(t.input.ToolOutputLimit)
	var steerIDs []string
	for _, ev := range t.events {
		if ev.Type == steerInputEvent {
			recorder.AddUser(ev.Delta)
			steerIDs = append(steerIDs, ev.ToolCallID)
		} else {
			recorder.Add(ev)
		}
	}
	out := external.PromptResult{Output: withoutNoResponseSentinel(recorder.Messages(finalText)), Text: finalText, SteerInputIDs: steerIDs}
	if t.hasUsage {
		usage := t.usage
		out.Usage = &sdk.Usage{
			InputTokens:       usage.InputTokens,
			OutputTokens:      usage.OutputTokens,
			TotalTokens:       usage.InputTokens + usage.OutputTokens,
			CachedInputTokens: usage.CacheReadInputTokens,
		}
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
		return out, errors.New("claude exited with unsettled queued input")
	case t.input.Command == "compact" && (!t.compactBoundary || t.compactFailed):
		out.StopReason = "compaction_failed"
		return out, errors.New("claude did not complete manual compaction")
	case t.result == nil:
		out.StopReason = "process_exit"
		if t.exitErr != nil {
			return out, fmt.Errorf("claude CLI exited without a result: %w: %s", t.exitErr, t.proc.StderrTail())
		}
		return out, fmt.Errorf("claude CLI exited without a result: %s", t.proc.StderrTail())
	case t.result.IsError:
		out.StopReason = t.result.Subtype
		message := strings.TrimSpace(t.result.Result)
		if message == "" {
			message = strings.Join(t.result.Errors, "; ")
		}
		if message == "" {
			message = "claude turn failed (" + t.result.Subtype + ")"
		}
		return out, fmt.Errorf("%s", message)
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
