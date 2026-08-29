// Package client manages Agent Control Protocol processes, sessions, and events.
package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/toolcontext"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	DefaultRunTimeout          = 20 * time.Minute
	maxWriteToolContentPreview = 64 * 1024
	// permissionStateWaitTimeout bounds request/notification correlation so a
	// missing session update cannot hold the agent prompt open indefinitely.
	permissionStateWaitTimeout = 30 * time.Second
	// genericPermissionStateWaitTimeout lets an ordinary request_permission
	// correlate with the preceding session/update without holding a prompt open
	// when that update never arrives.
	genericPermissionStateWaitTimeout = 300 * time.Millisecond
	// tombstoneReclaimWaitTimeout gives a live turn that legitimately reuses a
	// tombstoned tool-call ID time for its session/update to reclaim the ID
	// before the request is answered as cancelled.
	tombstoneReclaimWaitTimeout = 2 * time.Second
	// approvalGrantTTL bounds how long a RequestPermission grant stays
	// consumable by the follow-up client-capability callback. Deliberately its
	// own constant: it is unrelated to how long the approval flow waits for a
	// user decision, even though both happen to be generous.
	approvalGrantTTL = 10 * time.Minute
)

type Workspace interface {
	bridge.Provider
	bridge.WorkspaceInfoProvider
}

type ToolApprovalService interface {
	EvaluatePolicy(ctx context.Context, input toolapproval.CreatePendingInput) (toolapproval.Evaluation, error)
	CreatePending(ctx context.Context, input toolapproval.CreatePendingInput) (toolapproval.Request, error)
	Get(ctx context.Context, approvalID string) (toolapproval.Request, error)
	Reject(ctx context.Context, approvalID, actorID, reason string) (toolapproval.Request, error)
	WaitForDecision(ctx context.Context, approvalID string) (toolapproval.Request, error)
	RegisterWaiter(approvalID string) func()
}

type UserInputService interface {
	userinput.FlowService
}

type Runner struct {
	logger    *slog.Logger
	workspace Workspace
	command   string
	args      []string
	timeout   time.Duration
}

type RunRequest struct {
	AgentID     string
	BotID       string
	Task        string
	ProjectPath string
	Command     string
	Args        []string
	Env         []string
	SetupMode   SetupMode
	Timeout     time.Duration
}

type RunResult struct {
	SessionID   string              `json:"session_id,omitempty"`
	ProjectPath string              `json:"project_path,omitempty"`
	Text        string              `json:"text,omitempty"`
	StopReason  string              `json:"stop_reason,omitempty"`
	Events      []event.StreamEvent `json:"events,omitempty"`
	// Output is the in-process transcript used for persistence.
	Output []sdk.Message `json:"-"`
}

func NewRunner(log *slog.Logger, workspace Workspace) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		logger:    log.With(slog.String("component", "acpclient")),
		workspace: workspace,
		timeout:   DefaultRunTimeout,
	}
}

func (r *Runner) WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error) {
	if r == nil || r.workspace == nil {
		return bridge.WorkspaceInfo{}, errors.New("ACP workspace provider is not configured")
	}
	return r.workspace.WorkspaceInfo(ctx, botID)
}

func (r *Runner) MCPClient(ctx context.Context, botID string) (*bridge.Client, error) {
	if r == nil || r.workspace == nil {
		return nil, errors.New("ACP workspace provider is not configured")
	}
	return r.workspace.MCPClient(ctx, botID)
}

// Run is a convenience wrapper that performs a single-shot ACP exchange:
// start a session, send one prompt, then close. Production code that needs a
// persistent session should use StartSession + (*Session).Prompt directly.
//
// (*Session).Close uses its own short-lived background context so cleanup
// always runs even if the caller's ctx was cancelled; that disconnect trips
// contextcheck, so we silence it here.
//
//nolint:contextcheck // lifecycle close intentionally uses background ctx.
func (r *Runner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if strings.TrimSpace(req.Task) == "" {
		return RunResult{}, errors.New("task is required")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = r.timeout
	}
	if timeout <= 0 {
		timeout = DefaultRunTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sess, err := r.StartSession(runCtx, StartRequest{
		AgentID:     req.AgentID,
		BotID:       req.BotID,
		ProjectPath: req.ProjectPath,
		Command:     req.Command,
		Args:        req.Args,
		Env:         req.Env,
		SetupMode:   req.SetupMode,
		Timeout:     timeout,
	}, nil)
	if err != nil {
		return RunResult{}, err
	}
	defer func() { _ = sess.Close() }()

	prompt, err := sess.Prompt(runCtx, req.Task)
	result := RunResult{
		SessionID:   sess.ID(),
		ProjectPath: sess.ProjectPath(),
		Text:        prompt.Text,
		StopReason:  prompt.StopReason,
		Events:      prompt.Events,
		Output:      prompt.Output,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

type clientCallbacks struct {
	client         *bridge.Client
	logger         *slog.Logger
	root           string
	cwd            string
	approval       ToolApprovalService
	userInput      UserInputService
	toolGateway    *mcp.ToolGatewayService
	baseSession    ToolSessionContext
	mu             sync.RWMutex
	collector      *eventCollector
	stateReceipt   *sessionStateReceiptCollector
	sink           EventSink
	promptSession  ToolSessionContext
	approvalGrants map[string]approvedToolGrant
	events         *toolEventEmitter
	toolMapper     *acpToolEventMapper
	terminals      *terminalManager
	toolLimit      ToolOutputLimit
	runtimeSession *Session
	// pendingCurrentModes retains the latest mode replacement when a
	// session/update overtakes session/new. It is fenced by ACP session ID in
	// exactly the same way as pendingAvailableCommands below.
	pendingCurrentModes map[acp.SessionId]acp.SessionModeId
	// pendingAvailableCommands retains the latest Agent-declared command set
	// when session/update overtakes the session/new response. The ACP SDK waits
	// for pre-response notifications before returning session/new, so the
	// canonical Session cannot be attached until after those callbacks finish.
	pendingAvailableCommands map[acp.SessionId][]AvailableCommandInfo
	// quirks carries the per-agent title heuristics (profile owns them);
	// the zero value behaves like the defaults.
	quirks acpprofile.ToolQuirks
	// decisions counts in-flight permission/Form callbacks. ACP dispatches
	// inbound requests on connection-scoped goroutines that survive prompt
	// cancellation, so a cancelled turn waits on this gauge before the runtime
	// is handed to the next turn.
	decisions decisionInflight
}

type approvedToolGrant struct {
	ToolCallID string
	ExpiresAt  time.Time
}

// decisionInflight is a counter with an idle broadcast: enter/exit bracket a
// decision callback, waitIdle reports whether every in-flight callback
// finished before the deadline.
type decisionInflight struct {
	mu   sync.Mutex
	n    int
	idle chan struct{}
}

func (g *decisionInflight) enter() {
	g.mu.Lock()
	g.n++
	g.mu.Unlock()
}

func (g *decisionInflight) exit() {
	g.mu.Lock()
	g.n--
	if g.n <= 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
	g.mu.Unlock()
}

func (g *decisionInflight) waitIdle(timeout time.Duration) bool {
	g.mu.Lock()
	if g.n <= 0 {
		g.mu.Unlock()
		return true
	}
	if g.idle == nil {
		g.idle = make(chan struct{})
	}
	idle := g.idle
	g.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}

func newClientCallbacks(ctx context.Context, client *bridge.Client, root, cwd string, timeout time.Duration, sink EventSink, env []string, cleanEnv bool, unsetEnv []string, approval ToolApprovalService, toolGateway *mcp.ToolGatewayService, toolSession ToolSessionContext, quirks acpprofile.ToolQuirks) *clientCallbacks {
	timeoutSeconds := int32(timeout.Seconds())
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultTerminalTimeout
	}
	events := &toolEventEmitter{}
	return &clientCallbacks{
		client:      client,
		root:        root,
		cwd:         cwd,
		approval:    approval,
		toolGateway: toolGateway,
		baseSession: toolSession,
		sink:        sink,
		events:      events,
		toolMapper:  newACPToolEventMapper(quirks),
		terminals:   newTerminalManager(ctx, client, root, cwd, timeoutSeconds, env, cleanEnv, unsetEnv, events),
		quirks:      quirks,
	}
}

func (c *clientCallbacks) close() {
	if c != nil && c.terminals != nil {
		c.terminals.killAll()
	}
}

func (c *clientCallbacks) setPromptState(collector *eventCollector, sink EventSink, toolSession ToolSessionContext, limits ...ToolOutputLimit) {
	if c == nil {
		return
	}
	if c.toolMapper != nil {
		c.toolMapper.setPromptActive(collector != nil)
	}
	var limit ToolOutputLimit
	if len(limits) > 0 {
		limit = limits[0]
	}
	c.mu.Lock()
	c.collector = collector
	c.sink = sink
	c.promptSession = toolSession
	c.toolLimit = limit
	c.approvalGrants = nil
	c.mu.Unlock()
	if c.events != nil {
		c.events.setPromptState(collector, sink, limit)
	}
	if c.terminals != nil {
		c.terminals.setToolOutputLimit(limit)
	}
}

// markPromptCancelled records the cancelled prompt's tool calls so late
// permission callbacks resolve as cancelled (see isTombstoned). It must run
// before setPromptState wipes the per-prompt tool states.
func (c *clientCallbacks) markPromptCancelled() {
	if c == nil {
		return
	}
	if c.toolMapper != nil {
		c.toolMapper.tombstoneActiveToolCalls()
	}
}

// waitDecisionCallbacksIdle reports whether every in-flight permission/Form
// callback finished before the timeout. A cancelled prompt uses it as the
// quiescence barrier before the runtime is reused.
func (c *clientCallbacks) waitDecisionCallbacksIdle(timeout time.Duration) bool {
	if c == nil {
		return true
	}
	return c.decisions.waitIdle(timeout)
}

func (c *clientCallbacks) setSession(session *Session) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.runtimeSession = session
	if session != nil {
		if modeID, ok := c.pendingCurrentModes[session.sessionID]; ok {
			session.updateCurrentMode(session.sessionID, modeID)
		}
		if commands, ok := c.pendingAvailableCommands[session.sessionID]; ok {
			session.replaceAvailableCommands(session.sessionID, commands)
		}
		// One client connection owns one canonical Session. Notifications for
		// any other ID are stale or invalid and must not survive attachment.
		c.pendingCurrentModes = nil
		c.pendingAvailableCommands = nil
	}
	c.mu.Unlock()
}

func (c *clientCallbacks) updateCurrentMode(sessionID acp.SessionId, modeID acp.SessionModeId) (ModeState, bool) {
	if c == nil {
		return ModeState{Supported: false}, false
	}
	c.mu.Lock()
	if c.runtimeSession == nil {
		if c.pendingCurrentModes == nil {
			c.pendingCurrentModes = make(map[acp.SessionId]acp.SessionModeId)
		}
		// current_mode_update is a replacement. Preserve only the newest value
		// for each not-yet-attached session.
		c.pendingCurrentModes[sessionID] = modeID
		c.mu.Unlock()
		return ModeState{Supported: false}, false
	}
	if sessionID != c.runtimeSession.sessionID {
		c.mu.Unlock()
		return ModeState{Supported: false}, false
	}
	session := c.runtimeSession
	c.mu.Unlock()
	return session.updateCurrentMode(sessionID, modeID), true
}

func (c *clientCallbacks) updateAvailableCommands(sessionID acp.SessionId, commands []acp.AvailableCommand) ([]AvailableCommandInfo, bool) {
	if c == nil {
		return nil, false
	}
	parsed := availableCommandsFromACP(commands)
	c.mu.Lock()
	if c.runtimeSession == nil {
		if c.pendingAvailableCommands == nil {
			c.pendingAvailableCommands = make(map[acp.SessionId][]AvailableCommandInfo)
		}
		// available_commands_update is a full replacement, not a delta. Store
		// only the newest set for this session, including an explicit empty set.
		c.pendingAvailableCommands[sessionID] = cloneAvailableCommands(parsed)
		c.mu.Unlock()
		return nil, false
	}
	if sessionID != c.runtimeSession.sessionID {
		c.mu.Unlock()
		return nil, false
	}
	session := c.runtimeSession
	c.mu.Unlock()
	return session.replaceAvailableCommands(sessionID, parsed), true
}

func (c *clientCallbacks) ReadTextFile(ctx context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	session := c.currentToolSession()
	ctx, cancel := toolcontext.Bind(ctx, session)
	defer cancel()

	toolID := "read-" + uuid.NewString()
	input := map[string]any{"path": p.Path}
	if p.Line != nil && *p.Line > 0 {
		input["line"] = *p.Line
	}
	if p.Limit != nil && *p.Limit > 0 {
		input["limit"] = *p.Limit
	}
	toolID, approval, err := c.approveCallbackTool(ctx, toolID, "read", input)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	if !approval.Approved {
		err := errors.New(c.limitedApprovalRejectionMessage("read", approval))
		c.emitToolCallEnd(toolID, "read", input, toolErrorResult(err), err)
		return acp.ReadTextFileResponse{}, err
	}
	c.emitToolCallStart(toolID, "read", input)
	var toolErr error
	defer func() {
		result := map[string]any{}
		if toolErr != nil {
			result = toolErrorResult(toolErr)
		}
		c.emitToolCallEnd(toolID, "read", input, result, toolErr)
	}()

	path, err := c.resolvePath(p.Path)
	if err != nil {
		toolErr = err
		return acp.ReadTextFileResponse{}, err
	}
	line := int32(1)
	if p.Line != nil && *p.Line > 0 {
		line = boundedPositiveInt32(*p.Line)
	}
	limit := int32(0)
	if p.Limit != nil && *p.Limit > 0 {
		limit = boundedPositiveInt32(*p.Limit)
	}
	if err := toolcontext.ValidateRuntimeGuard(ctx, session); err != nil {
		toolErr = err
		return acp.ReadTextFileResponse{}, err
	}
	resp, err := c.client.ReadFile(ctx, path, line, limit)
	if err != nil {
		toolErr = err
		return acp.ReadTextFileResponse{}, err
	}
	if resp.GetBinary() {
		toolErr = fmt.Errorf("path %q is binary; ACP text file reads only support text", p.Path)
		return acp.ReadTextFileResponse{}, toolErr
	}
	content := resp.GetContent()
	if limit := c.currentToolOutputLimit(); hasToolOutputLimit(limit) {
		content = limitToolOutputString(content, "tool result (read.content)", limit)
	}
	if content == "" {
		content = "\n"
	}
	return acp.ReadTextFileResponse{Content: content}, nil
}

func (c *clientCallbacks) currentToolOutputLimit() ToolOutputLimit {
	if c == nil {
		return ToolOutputLimit{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.toolLimit
}

func (c *clientCallbacks) limitedApprovalRejectionMessage(toolName string, approval toolapproval.FlowResult) string {
	message := toolapproval.RejectionMessage(approval)
	if limit := c.currentToolOutputLimit(); hasToolOutputLimit(limit) {
		return limitToolOutputString(message, "tool result ("+toolName+")", limit)
	}
	return message
}

func (c *clientCallbacks) WriteTextFile(ctx context.Context, p acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	session := c.currentToolSession()
	ctx, cancel := toolcontext.Bind(ctx, session)
	defer cancel()

	toolID := "write-" + uuid.NewString()
	input := writeToolInput(p.Path, p.Content)
	toolID, approval, err := c.approveCallbackTool(ctx, toolID, "write", input)
	if err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	if !approval.Approved {
		err := errors.New(c.limitedApprovalRejectionMessage("write", approval))
		c.emitToolCallEnd(toolID, "write", input, toolErrorResult(err), err)
		return acp.WriteTextFileResponse{}, err
	}
	c.emitToolCallStart(toolID, "write", input)
	var toolErr error
	defer func() {
		result := map[string]any{}
		if toolErr != nil {
			result = toolErrorResult(toolErr)
		}
		c.emitToolCallEnd(toolID, "write", input, result, toolErr)
	}()

	path, err := c.resolvePath(p.Path)
	if err != nil {
		toolErr = err
		return acp.WriteTextFileResponse{}, err
	}
	if err := toolcontext.ValidateRuntimeGuard(ctx, session); err != nil {
		toolErr = err
		return acp.WriteTextFileResponse{}, err
	}
	if err := c.client.WriteFile(ctx, path, []byte(p.Content)); err != nil {
		toolErr = err
		return acp.WriteTextFileResponse{}, err
	}
	return acp.WriteTextFileResponse{}, nil
}

func writeToolInput(path, content string) map[string]any {
	contentBytes := len(content)
	input := map[string]any{
		"path":               path,
		"content":            content,
		"content_bytes":      contentBytes,
		"content_line_count": lineCount(content),
	}
	if contentBytes <= maxWriteToolContentPreview {
		return input
	}
	sum := sha256.Sum256([]byte(content))
	preview := strings.ToValidUTF8(content[:maxWriteToolContentPreview], "")
	input["content"] = preview
	input["content_sha256"] = hex.EncodeToString(sum[:])
	input["content_truncated"] = true
	return input
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}

func (c *clientCallbacks) emitToolCallStart(id, name string, input map[string]any) {
	if c == nil || c.events == nil {
		return
	}
	c.events.emit(event.StreamEvent{
		Type:       event.ToolCallStart,
		ToolCallID: id,
		ToolName:   name,
		Input:      input,
	})
}

func (c *clientCallbacks) emitToolCallEnd(id, name string, input map[string]any, result any, err error) {
	if c == nil || c.events == nil {
		return
	}
	ev := event.StreamEvent{
		Type:       event.ToolCallEnd,
		ToolCallID: id,
		ToolName:   name,
		Input:      input,
		Result:     result,
	}
	if err != nil {
		ev.Error = err.Error()
	}
	c.events.emit(ev)
}

func toolErrorResult(err error) map[string]any {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{
			"type": "text",
			"text": message,
		}},
	}
}

func (c *clientCallbacks) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	// ACP permissions stay scoped to the active prompt. When an ACP agent asks
	// permission before calling a client capability like fs/write_text_file, the
	// callback consumes this one-shot grant so users see one approval, not two.
	if c == nil {
		return cancelledPermission(), nil
	}
	c.decisions.enter()
	defer c.decisions.exit()
	approvalOptions, err := approvalOptionsFromACP(p.Options)
	if err != nil {
		if errors.Is(err, errUnsupportedOptionKind) {
			// A vendor or future option kind is not a protocol violation; the
			// options simply cannot be shown to a decider, so resolve the
			// request as cancelled instead of aborting the agent's turn.
			if c.logger != nil {
				c.logger.Warn("permission options unmappable; cancelling",
					slog.String("tool_call_id", strings.TrimSpace(string(p.ToolCall.ToolCallId))),
					slog.String("error", err.Error()))
			}
			return cancelledPermission(), nil
		}
		return acp.RequestPermissionResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if c.toolMapper != nil && c.toolMapper.isTombstoned(p.SessionId, string(p.ToolCall.ToolCallId)) {
		// The tool call matches a prompt the user already stopped. The SDK
		// dispatches requests concurrently with the ordered notification
		// queue, so give a live turn that legitimately reuses the ID a short
		// window for its session/update to arrive and reclaim the tombstone
		// (ensureTool deletes it); a genuinely stale callback has no such
		// update coming and resolves as cancelled instead of correlating
		// against - and creating an approval row under - the next turn.
		if c.toolMapper != nil {
			waitCtx, cancelWait := context.WithTimeout(ctx, tombstoneReclaimWaitTimeout)
			c.toolMapper.waitForPermissionState(waitCtx, p.SessionId, p.ToolCall, func(*acpToolState) bool {
				return !c.toolMapper.isTombstoned(p.SessionId, string(p.ToolCall.ToolCallId))
			})
			cancelWait()
		}
		if c.toolMapper.isTombstoned(p.SessionId, string(p.ToolCall.ToolCallId)) {
			return cancelledPermission(), nil
		}
	}
	session := c.currentToolSession()
	ctx, cancel := toolcontext.Bind(ctx, session)
	defer cancel()
	allowWithGuard := func() (acp.RequestPermissionResponse, error) {
		resp := allowOncePermission(p)
		if resp.Outcome.Cancelled != nil {
			return resp, nil
		}
		if err := toolcontext.ValidateRuntimeGuard(ctx, session); err != nil {
			return acp.RequestPermissionResponse{}, err
		}
		return resp, nil
	}
	state := c.permissionState(p)
	if isMCPToolApprovalRequest(p) && !mcpPermissionStateReady(state) && c.toolMapper != nil {
		// acp-go-sdk v0.13.5 dispatches inbound requests concurrently with its
		// ordered notification queue, so this request can overtake the earlier
		// session/update that carries the MCP identity. Correlate the exact
		// prompt/session/tool call until that state arrives or the request is
		// cancelled. Keep a generous upper bound so a missing update fails closed
		// instead of holding the prompt open indefinitely.
		waitCtx, cancelWait := context.WithTimeout(ctx, permissionStateWaitTimeout)
		state = c.toolMapper.waitForPermissionState(waitCtx, p.SessionId, p.ToolCall, mcpPermissionStateReady)
		cancelWait()
	} else if !genericPermissionStateReady(state) && c.toolMapper != nil {
		waitCtx, cancelWait := context.WithTimeout(ctx, genericPermissionStateWaitTimeout)
		state = c.toolMapper.waitForPermissionState(waitCtx, p.SessionId, p.ToolCall, genericPermissionStateReady)
		cancelWait()
	}
	if err := c.validatePermissionScope(state); err != nil {
		// Security-relevant rejection: an agent asked to act outside the
		// workspace root. Log it (an agent probing the boundary is exactly what
		// we want visibility into) before refusing.
		if c.logger != nil {
			c.logger.Warn("rejecting out-of-scope ACP permission request", slog.Any("error", err))
		}
		return rejectOncePermission(p), nil
	}
	// Consent is never a policy shortcut. ACP adapters encode fallback
	// elicitation consent in the request shape, so the user must see the Agent's
	// choices even when its server name would otherwise pass MCP preflight.
	// Correlated real Memoh MCP tool calls remain delegated to the gateway's own
	// scoped policy and approval flow rather than creating a duplicate prompt.
	consentShaped := isConsentShapedPermission(state)
	forceReview := consentShaped
	mcpShaped := false
	if preflight, ok := mcpPermissionPreflightFromState(state); ok {
		mcpShaped = true
		if !forceReview && c.allowsMemohMCPToolPreflight(ctx, preflight) {
			return allowWithGuard()
		}
		// Servers outside Memoh's scoped tool gateway are still ones the user
		// configured in their agent deliberately; the user decides, not a
		// blanket refusal - and "the user decides" must hold in every policy
		// configuration, so a disabled approval policy cannot silently select
		// an allow option on the user's behalf.
		forceReview = true
		if c.logger != nil {
			c.logger.Info("routing non-Memoh MCP permission request to generic approval",
				slog.String("tool_call_id", state.id),
				slog.String("server_name", preflight.serverName),
				slog.String("tool_name", preflight.toolName),
				slog.String("shape", preflight.shape))
		}
	}
	toolCallID, toolName, input, native := permissionNativeToolState(state, c.quirks)
	if !native {
		if isThinkPermissionState(state) && !forceReview {
			// Pure thought carries no side effect worth gating.
			return allowWithGuard()
		}
		// Every other shape that maps to no concrete tool - network grants,
		// mode switches, fetch/search asks, novel encodings - becomes a
		// generic permission approval carrying the agent's own title and
		// options, so nothing is silently answered on the user's behalf.
		// ForceReview keeps that promise when the approval policy is disabled.
		toolCallID, toolName, input = permissionApprovalToolState(state)
		forceReview = true
		if !mcpShaped && !consentShaped {
			// Only an agent-declared kind whose path/command failed to parse
			// inherits the classified deny posture (policy_kind). MCP consents
			// carry a Memoh-synthesized kind - denying them under the shell
			// exec policy class would silently kill the agent's MCP tools -
			// so they stay on the human-review lane.
			if kind := stringFromAny(input["kind"]); kind != "" {
				input["policy_kind"] = kind
			}
		}
		if c.logger != nil {
			// The raw-input summary reports the shape without echoing values
			// into logs, which is how a new agent encoding gets noticed.
			c.logger.Info("routing unmapped ACP permission request to generic approval",
				slog.String("tool_call_id", toolCallID),
				slog.String("title", stringFromAny(input["title"])),
				slog.String("kind", stringFromAny(input["kind"])),
				slog.String("raw_input", permissionRawInputSummary(state.input)))
		}
	}
	if c.approval == nil {
		return rejectOncePermission(p), nil
	}
	result, err := c.requireToolApproval(ctx, toolCallID, toolName, input, approvalOptions, forceReview)
	if err != nil {
		if ctx.Err() != nil {
			// The prompt turn itself is going away; ACP reserves the cancelled
			// outcome for exactly this case, so resolve the pending request
			// with it instead of surfacing a JSON-RPC error.
			return cancelledPermission(), nil
		}
		return acp.RequestPermissionResponse{}, err
	}
	if !result.Approved {
		if strings.EqualFold(result.Status, toolapproval.StatusCancelled) {
			// The row was cancelled because the turn itself is going away
			// (session/cancel, runtime close) - exactly what ACP reserves the
			// cancelled outcome for.
			return cancelledPermission(), nil
		}
		if strings.TrimSpace(result.SelectedOptionID) != "" {
			return selectedPermission(acp.PermissionOptionId(result.SelectedOptionID)), nil
		}
		if result.DecidedByUser {
			// A live user said no: select the agent's reject option so the
			// turn continues with a clean refusal instead of an aborted turn.
			return rejectOncePermission(p), nil
		}
		// System outcomes (policy deny, approval timeout, non-interactive
		// auto-reject, missing session identity) cancel the request: there
		// was no user decision to report, and answering with reject_once
		// would invite an unattended agent to retry-loop against
		// auto-rejections no user made.
		return cancelledPermission(), nil
	}
	resp := allowOncePermission(p)
	if strings.TrimSpace(result.SelectedOptionID) != "" {
		resp = selectedPermission(acp.PermissionOptionId(result.SelectedOptionID))
	}
	if resp.Outcome.Cancelled != nil {
		// The user approved but the agent offered no allow_once option, so the
		// agent sees a cancellation and will not act - leaving a consumable
		// grant behind would let a later callback run on a permission the
		// agent believes was cancelled.
		if c.logger != nil {
			c.logger.Warn("approval granted but the agent offered no allow_once option; cancelling",
				slog.String("tool_call_id", toolCallID),
				slog.String("tool_name", toolName))
		}
		return resp, nil
	}
	if err := toolcontext.ValidateRuntimeGuard(ctx, session); err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	if native {
		c.rememberApprovalGrant(toolCallID, toolName, input)
	}
	return resp, nil
}

func (c *clientCallbacks) permissionState(p acp.RequestPermissionRequest) *acpToolState {
	if c != nil && c.toolMapper != nil {
		return c.toolMapper.permissionState(p.SessionId, p.ToolCall)
	}
	state := &acpToolState{
		sessionID: strings.TrimSpace(string(p.SessionId)),
		id:        strings.TrimSpace(string(p.ToolCall.ToolCallId)),
	}
	mergePermissionToolUpdate(state, p.ToolCall)
	return state
}

func isMCPToolApprovalRequest(p acp.RequestPermissionRequest) bool {
	marked, ok := p.Meta["is_mcp_tool_approval"].(bool)
	return ok && marked
}

func mcpPermissionStateReady(state *acpToolState) bool {
	preflight, ok := mcpPermissionPreflightFromState(state)
	return ok && strings.TrimSpace(preflight.serverName) != ""
}

func genericPermissionStateReady(state *acpToolState) bool {
	if state == nil {
		return false
	}
	return strings.TrimSpace(state.title) != "" ||
		strings.TrimSpace(state.kind) != "" ||
		strings.TrimSpace(state.name) != "" ||
		state.input != nil || state.nativeIn != nil ||
		len(state.locations) > 0 || len(state.content) > 0
}

// normalizeACPOptionKind canonicalizes an agent-provided option kind for
// comparison. Storage (approvalOptionsFromACP) and classification must agree,
// or a non-canonically-cased kind would be approved by Memoh yet unanswered
// toward the agent.
func normalizeACPOptionKind(kind acp.PermissionOptionKind) acp.PermissionOptionKind {
	return acp.PermissionOptionKind(strings.ToLower(strings.TrimSpace(string(kind))))
}

func allowOncePermission(p acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	for _, opt := range p.Options {
		if normalizeACPOptionKind(opt.Kind) == acp.PermissionOptionKindAllowOnce {
			return selectedPermission(opt.OptionId)
		}
	}
	return cancelledPermission()
}

// rejectOncePermission selects the agent's reject_once option so any live-turn
// denial — user rejection, policy deny, timeout, out-of-scope, untrusted MCP,
// or an unmapped shape — reads as a clean per-tool refusal. ACP reserves the
// cancelled outcome for genuine turn cancellation (agents treat it as a
// whole-turn abort), so cancellation is only the fallback when the agent
// offered no reject_once option.
func rejectOncePermission(p acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	for _, opt := range p.Options {
		if normalizeACPOptionKind(opt.Kind) == acp.PermissionOptionKindRejectOnce {
			return selectedPermission(opt.OptionId)
		}
	}
	return cancelledPermission()
}

// approveCallbackTool resolves the approval for a client-capability tool call,
// consuming the one-shot grant left by RequestPermission when one matches. It
// returns the tool call ID the events should be emitted under.
func (c *clientCallbacks) approveCallbackTool(ctx context.Context, fallbackToolCallID, toolName string, input map[string]any) (string, toolapproval.FlowResult, error) {
	// fs/terminal capability callbacks block on the same user-decision flow
	// as permissions, so they join the quiescence gauge a cancelled prompt
	// waits on before the runtime is reused.
	c.decisions.enter()
	defer c.decisions.exit()
	fallbackToolCallID = strings.TrimSpace(fallbackToolCallID)
	if fallbackToolCallID == "" {
		fallbackToolCallID = "acp-callback-" + uuid.NewString()
	}
	if grantedID, ok := c.consumeApprovalGrant(toolName, input); ok {
		// The grant exists because the user approved the matching
		// RequestPermission moments ago.
		return grantedID, toolapproval.FlowResult{
			Approved:      true,
			Status:        toolapproval.StatusApproved,
			DecidedByUser: true,
		}, nil
	}
	result, err := c.requireToolApproval(ctx, fallbackToolCallID, toolName, input, nil, false)
	return fallbackToolCallID, result, err
}

func (c *clientCallbacks) requireToolApproval(ctx context.Context, toolCallID, toolName string, input map[string]any, options []toolapproval.PermissionOption, forceReview bool) (toolapproval.FlowResult, error) {
	if c == nil || c.approval == nil {
		return toolapproval.FlowResult{Status: toolapproval.StatusRejected}, nil
	}
	session := c.currentToolSession()
	if strings.TrimSpace(session.BotID) == "" || strings.TrimSpace(session.SessionID) == "" {
		return toolapproval.FlowResult{}, nil
	}
	cancelOnAbort := func(ctx context.Context, req toolapproval.Request, reason string) (toolapproval.Request, error) {
		// ACP can cancel one nested request without stopping its owning prompt.
		// Only a stopped run owns the session-wide cancellation semantics; an
		// isolated request cancellation retains the legacy single-row rejection.
		if session.RunContext == nil || session.RunContext.Err() == nil {
			return c.approval.Reject(ctx, req.ID, "", reason)
		}
		return c.cancelApprovalOnAbort(ctx, req, reason)
	}
	_, workspaceTargeted := toolapproval.OperationForTool(toolName)
	ctx = runtimefence.WithContext(ctx, session.RuntimeFence)
	return toolapproval.RunFlow(ctx, c.approval, toolapproval.FlowRequest{
		Input: toolapproval.CreatePendingInput{
			BotID:                        session.BotID,
			SessionID:                    session.SessionID,
			RouteID:                      session.RouteID,
			ChannelIdentityID:            session.ChannelIdentityID,
			WorkspaceTargetID:            session.WorkspaceTargetID,
			RequestedByChannelIdentityID: session.ChannelIdentityID,
			ToolCallID:                   toolCallID,
			ToolName:                     toolName,
			ToolInput:                    input,
			Options:                      options,
			ForceReview:                  forceReview,
			SourcePlatform:               session.CurrentPlatform,
			ReplyTarget:                  session.ReplyTarget,
			ConversationType:             session.ConversationType,
			WorkspaceTargeted:            workspaceTargeted,
		},
		Interactive:    strings.TrimSpace(session.RunID) != "",
		RegisterWaiter: c.approval.RegisterWaiter,
		Emit:           c.emitToolApprovalRequest,
		CancelOnAbort:  cancelOnAbort,
	})
}

func (c *clientCallbacks) cancelApprovalOnAbort(ctx context.Context, req toolapproval.Request, reason string) (toolapproval.Request, error) {
	canceller, ok := c.approval.(interface {
		CancelPendingForSession(context.Context, string, string, string) ([]toolapproval.Request, error)
	})
	if !ok || strings.TrimSpace(req.BotID) == "" || strings.TrimSpace(req.SessionID) == "" {
		return c.approval.Reject(ctx, req.ID, "", reason)
	}
	cancelled, err := canceller.CancelPendingForSession(ctx, req.BotID, req.SessionID, reason)
	if err != nil {
		return toolapproval.Request{}, err
	}
	for _, candidate := range cancelled {
		if candidate.ID == req.ID {
			return candidate, nil
		}
	}
	// Pool-level Stop cleanup may have won the race. Preserve that durable
	// terminal status rather than trying to rewrite it as rejected.
	resolved, err := c.approval.Get(ctx, req.ID)
	if err != nil {
		return toolapproval.Request{}, err
	}
	if !strings.EqualFold(toolapproval.NormalizedStatus(resolved.Status), toolapproval.StatusPending) {
		return resolved, nil
	}
	return toolapproval.Request{}, toolapproval.ErrAlreadyDecided
}

func (c *clientCallbacks) rememberApprovalGrant(toolCallID, toolName string, input map[string]any) {
	if c == nil {
		return
	}
	key := approvalGrantKey(toolName, input)
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.approvalGrants == nil {
		c.approvalGrants = map[string]approvedToolGrant{}
	}
	c.approvalGrants[key] = approvedToolGrant{
		ToolCallID: strings.TrimSpace(toolCallID),
		ExpiresAt:  time.Now().Add(approvalGrantTTL),
	}
}

func (c *clientCallbacks) consumeApprovalGrant(toolName string, input map[string]any) (string, bool) {
	if c == nil {
		return "", false
	}
	key := approvalGrantKey(toolName, input)
	if key == "" {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	grant, ok := c.approvalGrants[key]
	if !ok {
		return "", false
	}
	delete(c.approvalGrants, key)
	if !grant.ExpiresAt.IsZero() && now.After(grant.ExpiresAt) {
		return "", false
	}
	if strings.TrimSpace(grant.ToolCallID) == "" {
		return "", false
	}
	return grant.ToolCallID, true
}

func approvalGrantKey(toolName string, input map[string]any) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return ""
	}
	normalized := map[string]any{}
	switch toolName {
	case "read":
		normalized["path"] = stringFromAny(input["path"])
	case "write":
		normalized["path"] = stringFromAny(input["path"])
		normalized["content"] = stringFromAny(input["content"])
		normalized["content_bytes"] = input["content_bytes"]
		normalized["content_sha256"] = stringFromAny(input["content_sha256"])
	case "edit":
		normalized["path"] = stringFromAny(input["path"])
		normalized["old_text"] = stringFromAny(input["old_text"])
		normalized["new_text"] = stringFromAny(input["new_text"])
	case "exec":
		// Permission-time input carries only the agent's raw command, while
		// terminal/create rebuilds it from Command+Args and may add a cwd the
		// permission request never mentioned. Key on the whitespace-normalized
		// command alone so the one-shot grant still matches.
		normalized["command"] = strings.Join(strings.Fields(stringFromAny(input["command"])), " ")
	default:
		for k, v := range input {
			normalized[k] = v
		}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return toolName + ":" + string(raw)
}

func (c *clientCallbacks) currentToolSession() ToolSessionContext {
	if c == nil {
		return ToolSessionContext{}
	}
	c.mu.RLock()
	base := c.baseSession
	prompt := c.promptSession
	c.mu.RUnlock()
	return toolcontext.Merge(base, prompt)
}

func permissionNativeToolState(state *acpToolState, quirks acpprofile.ToolQuirks) (toolCallID, toolName string, input map[string]any, ok bool) {
	if state == nil {
		return "", "", nil, false
	}
	toolCallID = strings.TrimSpace(state.id)
	if toolCallID == "" {
		toolCallID = "acp-permission-" + uuid.NewString()
	}
	// nativeToolFromACPState now applies the edit->write title reclassification
	// itself, so the approval name here always matches the streamed tool-event
	// name for the same call.
	toolName, input, ok = nativeToolFromACPState(state, quirks)
	if !ok {
		return "", "", nil, false
	}
	return toolCallID, toolName, input, true
}

func isThinkPermissionState(state *acpToolState) bool {
	_, kind := permissionToolIdentityFromState(state)
	return kind == string(acp.ToolKindThink)
}

// isConsentShapedPermission recognizes the MCP elicitation fallback shapes
// codex-acp emits when the client lacks the elicitation capability: a URL
// consent ("open this link") arrives with an "elicitation-" tool call id and a
// {serverName, url} raw input. Selecting an accept option on the user's behalf
// would report a consent no user gave, so these force review even when the
// approval policy is disabled.
func isConsentShapedPermission(state *acpToolState) bool {
	if state == nil {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(state.id), "elicitation-") {
		return true
	}
	input, ok := state.input.(map[string]any)
	if !ok {
		return false
	}
	// Match on the load-bearing fields rather than an exact key count: an
	// upstream adapter adding a field must not silently reclassify a consent
	// as an auto-allowable tool preflight.
	return strings.TrimSpace(stringFromAny(input["serverName"])) != "" &&
		strings.TrimSpace(stringFromAny(input["url"])) != ""
}

// permissionApprovalToolState shapes an unmapped ACP permission request into
// the generic "permission" approval: the agent's own title, kind, and raw
// input reach the user, and the decision returns the agent's option id.
func permissionApprovalToolState(state *acpToolState) (toolCallID, toolName string, input map[string]any) {
	toolCallID = strings.TrimSpace(state.id)
	if toolCallID == "" {
		toolCallID = "acp-permission-" + uuid.NewString()
	}
	title, kind := permissionToolIdentityFromState(state)
	input = map[string]any{}
	if title != "" {
		input["title"] = title
	}
	if kind != "" {
		input["kind"] = kind
	}
	if detail, lang := permissionRequestDetail(state.input); detail != "" {
		input["request"] = detail
		input["request_lang"] = lang
	}
	return toolCallID, "permission", input
}

// permissionRequestDetail renders what an agent is asking to do for the user
// who must answer it. Unlike permissionRawInputSummary - a log formatter that
// deliberately elides values - this shows the values themselves, because a
// user cannot give informed consent to "map keys=command,host". It returns the
// rendered text and the language to highlight it as.
func permissionRequestDetail(raw any) (detail, lang string) {
	switch value := raw.(type) {
	case nil:
		return "", ""
	case string:
		// Agent-controlled content: the budget applies to every shape,
		// including a bare string.
		return limitPermissionDetail(strings.TrimSpace(value)), "text"
	}
	if input, ok := raw.(map[string]any); ok {
		// A request whose only content is one descriptive string reads better
		// as that sentence than as a wrapper object.
		if len(input) == 1 {
			for _, key := range []string{"description", "command", "prompt", "message", "url", "content"} {
				if text := strings.TrimSpace(stringFromAny(input[key])); text != "" {
					return limitPermissionDetail(text), "text"
				}
			}
		}
	}
	encoded, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return "", ""
	}
	return limitPermissionDetail(string(encoded)), "json"
}

func limitPermissionDetail(text string) string {
	return limitToolOutputStringExact(text, "permission request", ToolOutputLimit{
		MaxBytes: 4096,
		MaxLines: 60,
	})
}

// approvalOptionsFromACP preserves the agent's permission options verbatim for
// the approval pipeline: ids are returned to the agent unchanged, names are
// rendered to the user, kinds drive approve/reject validation.
// errUnsupportedOptionKind marks an option set that is well-formed JSON-RPC
// but uses a kind outside the four canonical values. The SDK's option kind is
// an open string, so this degrades to a cancelled outcome rather than an
// InvalidParams hard failure that would abort the agent's whole turn.
var errUnsupportedOptionKind = errors.New("permission option kind is unsupported")

func approvalOptionsFromACP(options []acp.PermissionOption) ([]toolapproval.PermissionOption, error) {
	if len(options) == 0 {
		return nil, errors.New("permission options must not be empty")
	}
	converted := make([]toolapproval.PermissionOption, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for _, option := range options {
		id := string(option.OptionId)
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("permission option id must not be empty")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("permission option id %q is duplicated", id)
		}
		seen[id] = struct{}{}
		kind := string(normalizeACPOptionKind(option.Kind))
		switch kind {
		case toolapproval.OptionKindAllowOnce, toolapproval.OptionKindAllowAlways,
			toolapproval.OptionKindRejectOnce, toolapproval.OptionKindRejectAlways:
		default:
			return nil, fmt.Errorf("%w: option %q has kind %q", errUnsupportedOptionKind, id, option.Kind)
		}
		converted = append(converted, toolapproval.PermissionOption{
			ID:   id,
			Name: option.Name,
			Kind: kind,
		})
	}
	return converted, nil
}

func permissionToolIdentityFromState(state *acpToolState) (title, kind string) {
	if state == nil {
		return "", ""
	}
	return strings.TrimSpace(state.title), strings.ToLower(strings.TrimSpace(state.kind))
}

func isGenericMCPToolPermissionTitle(title string) bool {
	return strings.EqualFold(strings.TrimSpace(title), "Approve MCP tool call")
}

type mcpPermissionPreflight struct {
	toolName        string
	serverName      string
	hasToolName     bool
	supportedMethod bool
	shape           string
}

const (
	// Codex ACP asks permission with title "Approve MCP tool call" and puts
	// the MCP server/tool information in RawInput.
	mcpPermissionShapeGenericTitle = "generic_title"
	// Claude Code ACP asks permission with title "mcp__<server>__<tool>" and
	// puts the actual tool arguments directly in RawInput.
	mcpPermissionShapeStructuredTitle = "structured_title"
	// Codex ACP reports MCP calls as execute-kind tool updates with a title of
	// "mcp.<server>.<tool>" and structured server/tool/arguments raw input.
	mcpPermissionShapeStructuredState = "structured_state"
)

func mcpPermissionPreflightFromState(state *acpToolState) (mcpPermissionPreflight, bool) {
	if state == nil {
		return mcpPermissionPreflight{}, false
	}
	if input, ok := state.input.(map[string]any); ok {
		if strings.TrimSpace(stringFromAny(input["url"])) != "" {
			// A top-level url marks a consent-style ask ("open this link"),
			// never a tool-call preflight (tool arguments nest under their own
			// key). Refusing to parse it here means a consent whose id prefix
			// or key set drifts upstream still lands on the forced-review lane
			// instead of the Memoh-server auto-allow.
			return mcpPermissionPreflight{}, false
		}
	}
	title, _ := permissionToolIdentityFromState(state)
	if toolName, serverName, ok := mcpToolCallFromStructuredTitle(title); ok {
		return mcpPermissionPreflight{
			toolName:        toolName,
			serverName:      serverName,
			hasToolName:     true,
			supportedMethod: true,
			shape:           mcpPermissionShapeStructuredTitle,
		}, true
	}
	if isGenericMCPToolPermissionTitle(title) {
		toolName, serverName, hasToolName, supportedMethod := mcpToolCallFromRawInput(state.input)
		return mcpPermissionPreflight{
			toolName:        toolName,
			serverName:      serverName,
			hasToolName:     hasToolName,
			supportedMethod: supportedMethod,
			shape:           mcpPermissionShapeGenericTitle,
		}, true
	}
	toolName, serverName, hasToolName, supportedMethod := mcpToolCallFromRawInput(state.input)
	if !supportedMethod || !hasToolName || serverName == "" || title != "mcp."+serverName+"."+toolName {
		return mcpPermissionPreflight{}, false
	}
	return mcpPermissionPreflight{
		toolName:        toolName,
		serverName:      serverName,
		hasToolName:     true,
		supportedMethod: true,
		shape:           mcpPermissionShapeStructuredState,
	}, true
}

func mcpToolCallFromStructuredTitle(title string) (toolName, serverName string, ok bool) {
	parts := strings.Split(strings.TrimSpace(title), "__")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "mcp") {
		return "", "", false
	}
	serverName = strings.TrimSpace(parts[1])
	toolName = strings.TrimSpace(parts[2])
	if serverName == "" || toolName == "" {
		return "", "", false
	}
	return toolName, serverName, true
}

func (c *clientCallbacks) allowsMemohMCPToolPreflight(ctx context.Context, preflight mcpPermissionPreflight) bool {
	if c == nil || c.toolGateway == nil {
		return false
	}
	if !preflight.supportedMethod {
		return false
	}
	if !isMemohToolsMCPServerName(preflight.serverName) {
		if c.logger != nil {
			c.logger.Warn("rejecting MCP tool preflight for missing or non-Memoh server",
				slog.String("shape", preflight.shape),
				slog.String("server_name", preflight.serverName),
				slog.String("tool_name", preflight.toolName))
		}
		return false
	}
	// Codex ACP preflights for MCP tools identify the server but may omit the
	// tool name. The actual call still goes through Memoh's scoped tool
	// gateway; when the name is present, classify it against that same gateway
	// so ACP sees the native-aligned Memoh tool surface.
	if !preflight.hasToolName {
		return true
	}
	session := c.currentToolSession()
	if strings.TrimSpace(session.BotID) == "" {
		return false
	}
	_, ok, err := c.toolGateway.LookupTool(ctx, session, preflight.toolName)
	if err != nil {
		if c.logger != nil {
			c.logger.Warn("failed to classify MCP tool preflight",
				slog.String("shape", preflight.shape),
				slog.String("tool_name", preflight.toolName),
				slog.Any("error", err))
		}
		return false
	}
	return ok
}

func mcpToolCallFromRawInput(raw any) (toolName, serverName string, ok, supportedMethod bool) {
	return mcpToolCallFromRawInputDepth(raw, 0)
}

func mcpToolCallFromRawInputDepth(raw any, depth int) (toolName, serverName string, ok, supportedMethod bool) {
	if depth > 3 {
		return "", "", false, true
	}
	input, ok := raw.(map[string]any)
	if !ok || input == nil {
		return "", "", false, true
	}
	serverName = strings.TrimSpace(firstNonEmptyString(
		stringFromAny(input["server_name"]),
		stringFromAny(input["serverName"]),
		stringFromAny(input["server"]),
	))
	method := strings.TrimSpace(stringFromAny(input["method"]))
	if method != "" {
		if method != "tools/call" {
			return "", serverName, false, false
		}
		params, ok := input["params"].(map[string]any)
		if !ok || params == nil {
			return "", serverName, false, true
		}
		name := strings.TrimSpace(stringFromAny(params["name"]))
		return name, serverName, name != "", true
	}
	for _, key := range []string{"request", "tool_call", "toolCall"} {
		nested, nestedServer, ok, supported := mcpToolCallFromRawInputDepth(input[key], depth+1)
		if !supported {
			if serverName == "" {
				serverName = nestedServer
			}
			return "", serverName, false, false
		}
		if ok {
			if serverName == "" {
				serverName = nestedServer
			}
			return nested, serverName, true, true
		}
	}
	// Some ACP agents report the MCP CallToolParams payload directly instead
	// of wrapping it in a JSON-RPC envelope. This is still structured MCP data:
	// never infer the tool name from title/content/free text.
	if params, ok := input["params"].(map[string]any); ok && params != nil {
		name := strings.TrimSpace(stringFromAny(params["name"]))
		if name != "" {
			return name, serverName, true, true
		}
	}
	for _, key := range []string{"name", "tool", "tool_name", "toolName"} {
		name := strings.TrimSpace(stringFromAny(input[key]))
		if name != "" {
			return name, serverName, true, true
		}
	}
	return "", serverName, false, true
}

func permissionRawInputSummary(raw any) string {
	if raw == nil {
		return "nil"
	}
	input, ok := raw.(map[string]any)
	if !ok {
		return fmt.Sprintf("%T", raw)
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{"map keys=" + strings.Join(keys, ",")}
	for _, key := range []string{"method", "server_name", "serverName", "name", "tool_name", "toolName"} {
		if value := strings.TrimSpace(stringFromAny(input[key])); value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	if request, ok := input["request"].(map[string]any); ok && request != nil {
		requestKeys := make([]string, 0, len(request))
		for key := range request {
			requestKeys = append(requestKeys, key)
		}
		sort.Strings(requestKeys)
		parts = append(parts, "request.keys="+strings.Join(requestKeys, ","))
		for _, key := range []string{"method", "name", "tool_name", "toolName"} {
			if value := strings.TrimSpace(stringFromAny(request[key])); value != "" {
				parts = append(parts, "request."+key+"="+value)
			}
		}
	}
	if params, ok := input["params"].(map[string]any); ok && params != nil {
		paramKeys := make([]string, 0, len(params))
		for key := range params {
			paramKeys = append(paramKeys, key)
		}
		sort.Strings(paramKeys)
		parts = append(parts, "params.keys="+strings.Join(paramKeys, ","))
		if value := strings.TrimSpace(stringFromAny(params["name"])); value != "" {
			parts = append(parts, "params.name="+value)
		}
	}
	return strings.Join(parts, " ")
}

func (c *clientCallbacks) emitToolApprovalRequest(req toolapproval.Request) bool {
	if c == nil || c.events == nil {
		return false
	}
	ev := event.StreamEvent{
		Type:       event.ToolApprovalRequest,
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		Input:      req.ToolInput,
		ApprovalID: req.ID,
		ShortID:    req.ShortID,
		Status:     toolapproval.NormalizedStatus(req.Status),
		Metadata: map[string]any{
			"approval": toolapproval.RequestMetadata(req),
		},
	}
	if !strings.EqualFold(toolapproval.NormalizedStatus(req.Status), toolapproval.StatusPending) {
		return c.events.emitTerminalDecision(ev)
	}
	return c.events.emit(ev)
}

func (c *clientCallbacks) SessionUpdate(_ context.Context, p acp.SessionNotification) error {
	c.mu.RLock()
	runtimeSession := c.runtimeSession
	c.mu.RUnlock()
	if p.Update.ConfigOptionUpdate != nil && runtimeSession != nil {
		runtimeSession.replaceConfigOptions(p.SessionId, p.Update.ConfigOptionUpdate.ConfigOptions)
	}
	if update := p.Update.CurrentModeUpdate; update != nil {
		_, _ = c.updateCurrentMode(p.SessionId, update.CurrentModeId)
	}
	if update := p.Update.AvailableCommandsUpdate; update != nil {
		_, _ = c.updateAvailableCommands(p.SessionId, update.AvailableCommands)
	}
	var events []event.StreamEvent
	if c.toolMapper != nil {
		events = append(events, c.toolMapper.eventsFromNotification(p)...)
	}
	// Keep the read lock through delivery. Clearing prompt state takes the
	// write lock, so Prompt cannot return and let its owner close the sink
	// while a callback that already observed that sink is still emitting.
	// Delivery is bounded: a sink whose consumer stops draining cancels its
	// own stream after acpSinkStallTimeout (application layer), so this lock
	// cannot pin the prompt's runtime slot indefinitely.
	c.mu.RLock()
	defer c.mu.RUnlock()
	collector := c.collector
	sink := c.sink
	limit := c.toolLimit
	events = limitStreamEvents(events, limit)
	if collector != nil {
		if !collector.apply(p, events) {
			return nil
		}
	}
	if sink != nil {
		for _, ev := range events {
			sink.EmitStreamEvent(ev)
		}
	}
	return nil
}

func (c *clientCallbacks) CreateTerminal(ctx context.Context, p acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	session := c.currentToolSession()
	ctx, cancel := toolcontext.Bind(ctx, session)
	defer cancel()
	return c.terminals.CreateTerminal(ctx, p, func(toolCallID string, input map[string]any) (terminalApprovalResult, error) {
		id, approval, err := c.approveCallbackTool(ctx, toolCallID, "exec", input)
		return terminalApprovalResult{
			Approved:         approval.Approved,
			ToolCallID:       id,
			RejectionMessage: c.limitedApprovalRejectionMessage("exec", approval),
		}, err
	}, terminalRuntimeScope{
		validate: func(guardCtx context.Context) error {
			return toolcontext.ValidateRuntimeGuard(guardCtx, session)
		},
		runContext: session.RunContext,
	})
}

func (c *clientCallbacks) KillTerminal(ctx context.Context, p acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return c.terminals.KillTerminal(ctx, p)
}

func (c *clientCallbacks) TerminalOutput(ctx context.Context, p acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return c.terminals.TerminalOutput(ctx, p)
}

func (c *clientCallbacks) ReleaseTerminal(ctx context.Context, p acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return c.terminals.ReleaseTerminal(ctx, p)
}

func (c *clientCallbacks) WaitForTerminalExit(ctx context.Context, p acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return c.terminals.WaitForTerminalExit(ctx, p)
}

func (c *clientCallbacks) resolvePath(path string) (string, error) {
	return ResolvePathUnderVirtualRoot(c.root, path)
}

// scopePathKeys is the union of every RawInput key any extraction layer reads
// a path from (pathFromACPInput plus the callback layer's cwd/old/new keys).
// The scope guard must validate the same projection that later gets approved
// and executed; otherwise an out-of-root path could pass the pre-approval
// check with no later callback boundary left to reject it.
var scopePathKeys = []string{
	"cwd", "work_dir",
	"path", "file_path", "filePath", "file", "filename",
	"old_path", "new_path",
}

func (c *clientCallbacks) validatePermissionScope(state *acpToolState) error {
	if state == nil {
		return nil
	}
	for _, loc := range state.locations {
		if strings.TrimSpace(loc.Path) == "" {
			continue
		}
		if _, err := c.resolvePath(loc.Path); err != nil {
			return err
		}
	}
	if raw, ok := state.input.(map[string]any); ok {
		for _, key := range scopePathKeys {
			value, ok := raw[key].(string)
			if !ok || strings.TrimSpace(value) == "" {
				continue
			}
			if _, err := c.resolvePath(value); err != nil {
				return err
			}
		}
	}
	// Edit permissions can carry their target path only inside a content
	// diff (editToolFromACPState falls back to it), so validate those too.
	for _, content := range state.content {
		if content.Diff == nil || strings.TrimSpace(content.Diff.Path) == "" {
			continue
		}
		if _, err := c.resolvePath(content.Diff.Path); err != nil {
			return err
		}
	}
	return nil
}

func selectedPermission(id acp.PermissionOptionId) acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{OptionId: id},
		},
	}
}

func cancelledPermission() acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Cancelled: &acp.RequestPermissionOutcomeCancelled{},
		},
	}
}

func boundedPositiveInt32(v int) int32 {
	const maxInt32 = int(^uint32(0) >> 1)
	if v <= 0 {
		return 0
	}
	if v > maxInt32 {
		return int32(maxInt32) //nolint:gosec // maxInt32 is exactly the largest int32 value.
	}
	return int32(v) //nolint:gosec // v is bounded to the int32 range above.
}
