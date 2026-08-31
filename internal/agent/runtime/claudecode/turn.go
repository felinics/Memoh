package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

const (
	// maxLineBytes bounds one NDJSON line from the CLI; large tool results
	// ride inside, so the ceiling is generous.
	maxLineBytes = 32 * 1024 * 1024
	// interruptSettleTimeout bounds how long an interrupted turn may take to
	// deliver its result before the process is torn down.
	interruptSettleTimeout = 10 * time.Second
)

// turnRunner drives one CLI process through one turn.
type turnRunner struct {
	input    external.PromptInput
	approval approval.FlowService
	waiter   func(approvalID string) func()
	logger   *slog.Logger
	proc     cliProcess

	// ctx is the turn-scoped context bounding approval decisions.
	ctx    context.Context
	cancel context.CancelFunc

	writeMu sync.Mutex
	nextID  atomic.Uint64

	done chan struct{}

	mu              sync.Mutex
	closed          bool
	events          []event.StreamEvent
	assistantTxt    strings.Builder
	sessionID       string
	cliVersion      string
	result          *inboundMessage
	pendingCtrl     map[string]chan json.RawMessage
	inboundCancels  map[string]context.CancelFunc
	memohMCPCallIDs map[string]struct{}
	doneOnce        sync.Once
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
		pendingCtrl:     map[string]chan json.RawMessage{},
		inboundCancels:  map[string]context.CancelFunc{},
		memohMCPCallIDs: map[string]struct{}{},
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

// sendControl issues a Memoh → CLI control request and returns the pending
// response channel.
func (t *turnRunner) sendControl(subtype string, extra map[string]any) (chan json.RawMessage, string, error) {
	requestID := "memoh-" + strconv.FormatUint(t.nextID.Add(1), 10)
	ch := make(chan json.RawMessage, 1)
	t.mu.Lock()
	t.pendingCtrl[requestID] = ch
	t.mu.Unlock()
	line, err := controlRequestLine(requestID, subtype, extra)
	if err != nil {
		return nil, "", err
	}
	if err := t.writeLine(line); err != nil {
		t.mu.Lock()
		delete(t.pendingCtrl, requestID)
		t.mu.Unlock()
		return nil, "", err
	}
	return ch, requestID, nil
}

// readLoop consumes the CLI's NDJSON stream until the process exits.
func (t *turnRunner) readLoop() {
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
	t.finish()
}

func (t *turnRunner) handleMessage(msg *inboundMessage) {
	switch msg.Type {
	case messageTypeSystem:
		if msg.Subtype == "init" {
			t.mu.Lock()
			t.sessionID = msg.SessionID
			t.cliVersion = msg.ClaudeCodeVersion
			t.mu.Unlock()
			if msg.ClaudeCodeVersion != "" && msg.ClaudeCodeVersion != PinnedCLIVersion {
				t.logger.Warn("claude CLI version differs from the pinned wire contract",
					slog.String("cli_version", msg.ClaudeCodeVersion), slog.String("pinned", PinnedCLIVersion))
			}
		}
	case messageTypeAssistant:
		if msg.ParentToolUseID != nil {
			return // subagent traffic surfaces through its Task tool events
		}
		chat, ok := decodeChatMessage(msg.Message)
		if !ok {
			return
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
				t.mu.Lock()
				t.assistantTxt.WriteString(block.Text)
				t.mu.Unlock()
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
		if ev.Type != "content_block_delta" {
			return
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" && strings.TrimSpace(ev.Delta.Text) != noResponseRequested {
				t.emit(event.StreamEvent{Type: event.TextDelta, Delta: ev.Delta.Text})
			}
		case "thinking_delta":
			if ev.Delta.Thinking != "" {
				t.emit(event.StreamEvent{Type: event.ReasoningDelta, Delta: ev.Delta.Thinking})
			}
		}
	case messageTypeResult:
		t.mu.Lock()
		t.result = msg
		t.mu.Unlock()
		t.finish()
	case messageTypeControlRequest:
		var payload controlRequestPayload
		if err := json.Unmarshal(msg.Request, &payload); err != nil {
			t.respondControlError(msg.RequestID, "memoh could not decode this request")
			return
		}
		switch payload.Subtype {
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
			ch <- envelope.Response
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
	callID := strings.TrimSpace(payload.ToolUseID)
	if callID == "" {
		callID = requestID
	}
	result := t.decide(ctx, callID, canonicalPolicyToolName(payload.ToolName), payload.Input)
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

// canonicalPolicyToolName maps Claude Code CLI tool names onto the approval
// policy's operation vocabulary (the same translation the codex driver does
// with "exec"/"write"). Without it a CLI-native name like "Bash" matches no
// policy operation and bypasses the bot's exec policy entirely. Names outside
// the workspace surface keep their identity: non-workspace tools are not
// policy-governed, matching the native runtime.
func canonicalPolicyToolName(toolName string) string {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash":
		return "exec"
	case "write", "edit", "multiedit", "notebookedit":
		return "write"
	case "read", "glob", "grep":
		return "read"
	default:
		return toolName
	}
}

func canonicalDisplayToolName(toolName string) string {
	name := strings.TrimSpace(toolName)
	lower := strings.ToLower(name)
	if isMemohMCPToolName(name) {
		return strings.TrimPrefix(lower, "mcp__"+memohMCPServerName+"__")
	}
	switch lower {
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

// decide runs one approval through policy and the interactive decision flow.
func (t *turnRunner) decide(ctx context.Context, callID, toolName string, input map[string]any) approval.FlowResult {
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
		t.logger.Error("claude approval flow failed", slog.Any("error", err))
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

// interrupt asks the CLI to stop the running turn.
func (t *turnRunner) interrupt() {
	//nolint:contextcheck // the turn context is cancelled; the interrupt must still go out
	ch, _, err := t.sendControl("interrupt", nil)
	if err != nil {
		t.logger.Warn("claude interrupt send failed", slog.Any("error", err))
		return
	}
	select {
	case <-ch:
	case <-time.After(interruptSettleTimeout):
	case <-t.proc.Done():
	}
}

// buildResult assembles the durable outcome after the turn settled.
func (t *turnRunner) buildResult(storedSessionID string) (external.PromptResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	finalText := t.assistantTxt.String()
	if t.result != nil {
		reported := strings.TrimSpace(t.result.Result)
		if reported != "" && reported != noResponseRequested {
			finalText = t.result.Result
		}
	}
	if strings.TrimSpace(finalText) == noResponseRequested {
		finalText = ""
	}
	out := external.PromptResult{
		Output: withoutNoResponseSentinel(external.TranscriptFromEvents(t.events, finalText)),
		Text:   finalText,
	}
	if t.result != nil && t.result.Usage != nil {
		usage := t.result.Usage
		out.Usage = &sdk.Usage{
			InputTokens:       usage.InputTokens,
			OutputTokens:      usage.OutputTokens,
			TotalTokens:       usage.InputTokens + usage.OutputTokens,
			CachedInputTokens: usage.CacheReadInputTokens,
		}
	}
	if t.sessionID != "" && t.sessionID != storedSessionID {
		out.RuntimeMetadata = map[string]any{metadataSessionIDKey: t.sessionID}
	}

	switch {
	case t.result == nil:
		out.StopReason = "process_exit"
		return out, fmt.Errorf("claude CLI exited without a result: %s", t.proc.StderrTail())
	case t.result.IsError:
		out.StopReason = t.result.Subtype
		message := strings.TrimSpace(t.result.Result)
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
		return string(line[:limit])
	}
	return string(line[:limit]) + "…"
}
