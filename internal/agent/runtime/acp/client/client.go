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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
	sdk "github.com/memohai/twilight-ai/sdk"

	toolapproval "github.com/memohai/memoh/internal/agent/decision/approval"
	"github.com/memohai/memoh/internal/agent/event"
	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	"github.com/memohai/memoh/internal/mcp"
	"github.com/memohai/memoh/internal/runtimefence"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

const (
	DefaultRunTimeout          = 20 * time.Minute
	maxWriteToolContentPreview = 64 * 1024
	// permissionStateWaitTimeout bounds request/notification correlation so a
	// missing session update cannot hold the agent prompt open indefinitely.
	permissionStateWaitTimeout = 30 * time.Second
	// approvalGrantTTL bounds how long a RequestPermission grant stays
	// consumable by the follow-up client-capability callback. Deliberately its
	// own constant: it is unrelated to how long the approval flow waits for a
	// user decision, even though both happen to be generous.
	approvalGrantTTL = 10 * time.Minute
)

const acpAdapterVersionLookupTimeoutSeconds int32 = 90

var exactACPAdapterVersionPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

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

// ResolveACPAdapterVersion returns the exact version currently behind an npm
// package's latest dist-tag in the target workspace.
func (r *Runner) ResolveACPAdapterVersion(ctx context.Context, botID, packageName string, env []string) (string, error) {
	info, err := r.WorkspaceInfo(ctx, botID)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	client, err := r.MCPClient(ctx, botID)
	if err != nil {
		return "", fmt.Errorf("connect workspace bridge: %w", err)
	}
	command := buildShellCommand("npm", []string{"view", packageName, "dist-tags.latest", "--json"})
	result, err := client.ExecWithOptions(ctx, command, info.DefaultWorkDir, acpAdapterVersionLookupTimeoutSeconds, nil, bridge.ExecOptions{
		Env: append([]string(nil), env...),
	})
	if err != nil {
		return "", fmt.Errorf("resolve adapter version: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("resolve adapter version: npm exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	var version string
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &version); err != nil {
		return "", fmt.Errorf("parse adapter version: %w", err)
	}
	version = strings.TrimSpace(version)
	if !exactACPAdapterVersionPattern.MatchString(version) {
		return "", fmt.Errorf("npm returned invalid adapter version %q", version)
	}
	return version, nil
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
	client               *bridge.Client
	logger               *slog.Logger
	root                 string
	cwd                  string
	approval             ToolApprovalService
	toolGateway          *mcp.ToolGatewayService
	baseSession          ToolSessionContext
	mu                   sync.RWMutex
	collector            *eventCollector
	sink                 EventSink
	promptSession        ToolSessionContext
	approvalGrants       map[string]approvedToolGrant
	events               *toolEventEmitter
	toolMapper           *acpToolEventMapper
	terminals            *terminalManager
	toolLimit            ToolOutputLimit
	configOptionsHandler func(acp.SessionId, []acp.SessionConfigOption)
	// quirks carries the per-agent title heuristics (profile owns them);
	// the zero value behaves like the defaults.
	quirks acpprofile.ToolQuirks
}

type approvedToolGrant struct {
	ToolCallID string
	ExpiresAt  time.Time
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

func (c *clientCallbacks) setConfigOptionsHandler(handler func(acp.SessionId, []acp.SessionConfigOption)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.configOptionsHandler = handler
	c.mu.Unlock()
}

func (c *clientCallbacks) ReadTextFile(ctx context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	session := c.currentToolSession()
	ctx, cancel := mcp.BindRuntimeContext(ctx, session)
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
	if err := mcp.ValidateRuntimeGuard(ctx, session); err != nil {
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
	ctx, cancel := mcp.BindRuntimeContext(ctx, session)
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
	if err := mcp.ValidateRuntimeGuard(ctx, session); err != nil {
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
	session := c.currentToolSession()
	ctx, cancel := mcp.BindRuntimeContext(ctx, session)
	defer cancel()
	allowWithGuard := func() (acp.RequestPermissionResponse, error) {
		resp := allowOncePermission(p)
		if resp.Outcome.Cancelled != nil {
			return resp, nil
		}
		if err := mcp.ValidateRuntimeGuard(ctx, session); err != nil {
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
	}
	if err := c.validatePermissionScope(state); err != nil {
		// Security-relevant rejection: an agent asked to act outside the
		// workspace root. Log it (an agent probing the boundary is exactly what
		// we want visibility into) before cancelling.
		if c.logger != nil {
			c.logger.Warn("cancelling out-of-scope ACP permission request", slog.Any("error", err))
		}
		return cancelledPermission(), nil
	}
	if preflight, ok := mcpPermissionPreflightFromState(state); ok {
		if !c.allowsMemohMCPToolPreflight(ctx, preflight) {
			if c.logger != nil {
				c.logger.Warn("cancelling untrusted ACP MCP permission request",
					slog.String("tool_call_id", state.id),
					slog.String("server_name", preflight.serverName),
					slog.String("tool_name", preflight.toolName),
					slog.String("shape", preflight.shape))
			}
			return cancelledPermission(), nil
		}
		return allowWithGuard()
	}
	toolCallID, toolName, input, native := permissionNativeToolState(state, c.quirks)
	if !native {
		if !allowUnmappedPermission(state) {
			if c.logger != nil {
				title, kind := permissionToolIdentityFromState(state)
				c.logger.Warn("cancelling unmapped ACP permission request",
					slog.String("tool_call_id", state.id),
					slog.String("title", title),
					slog.String("kind", kind),
					slog.String("raw_input", permissionRawInputSummary(state.input)))
			}
			return cancelledPermission(), nil
		}
		// Only protocol-level ACP permissions are allowed here. MCP preflights
		// were classified and checked against the scoped gateway above. Unknown
		// shapes fail closed so a new write/exec encoding cannot bypass policy.
		if c.logger != nil {
			title, kind := permissionToolIdentityFromState(state)
			c.logger.Info("allowing ACP permission request that maps to no policy-gated tool",
				slog.String("tool_call_id", state.id),
				slog.String("title", title),
				slog.String("kind", kind))
		}
		return allowWithGuard()
	}
	if c.approval == nil {
		return allowWithGuard()
	}
	result, err := c.requireToolApproval(ctx, toolCallID, toolName, input)
	if err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	if !result.Approved {
		if result.DecidedByUser {
			// A live user said no: select the agent's reject option so the
			// turn continues with a clean refusal instead of an aborted turn.
			return rejectOncePermission(p), nil
		}
		// System outcomes (no session identity, non-interactive auto-reject)
		// cancel the request: there was no user decision to report.
		return cancelledPermission(), nil
	}
	resp, err := allowWithGuard()
	if err != nil {
		return acp.RequestPermissionResponse{}, err
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
	c.rememberApprovalGrant(toolCallID, toolName, input)
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

func allowOncePermission(p acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	for _, opt := range p.Options {
		if opt.Kind == acp.PermissionOptionKindAllowOnce {
			return selectedPermission(opt.OptionId)
		}
	}
	return cancelledPermission()
}

// rejectOncePermission selects the agent's reject_once option so a denied
// approval reads as a clean user rejection instead of an aborted turn;
// cancellation is the fallback.
func rejectOncePermission(p acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	for _, opt := range p.Options {
		if opt.Kind == acp.PermissionOptionKindRejectOnce {
			return selectedPermission(opt.OptionId)
		}
	}
	return cancelledPermission()
}

// approveCallbackTool resolves the approval for a client-capability tool call,
// consuming the one-shot grant left by RequestPermission when one matches. It
// returns the tool call ID the events should be emitted under.
func (c *clientCallbacks) approveCallbackTool(ctx context.Context, fallbackToolCallID, toolName string, input map[string]any) (string, toolapproval.FlowResult, error) {
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
	result, err := c.requireToolApproval(ctx, fallbackToolCallID, toolName, input)
	return fallbackToolCallID, result, err
}

func (c *clientCallbacks) requireToolApproval(ctx context.Context, toolCallID, toolName string, input map[string]any) (toolapproval.FlowResult, error) {
	if c == nil || c.approval == nil {
		return toolapproval.FlowResult{Approved: true}, nil
	}
	session := c.currentToolSession()
	if strings.TrimSpace(session.BotID) == "" || strings.TrimSpace(session.SessionID) == "" {
		return toolapproval.FlowResult{}, nil
	}
	ctx = runtimefence.WithContext(ctx, session.RuntimeFence)
	return toolapproval.RunFlow(ctx, c.approval, toolapproval.FlowRequest{
		Input: toolapproval.CreatePendingInput{
			BotID:                        session.BotID,
			SessionID:                    session.SessionID,
			RouteID:                      session.RouteID,
			ChannelIdentityID:            session.ChannelIdentityID,
			RequestedByChannelIdentityID: session.ChannelIdentityID,
			ToolCallID:                   toolCallID,
			ToolName:                     toolName,
			ToolInput:                    input,
			SourcePlatform:               session.CurrentPlatform,
			ReplyTarget:                  session.ReplyTarget,
			ConversationType:             session.ConversationType,
		},
		Interactive:    strings.TrimSpace(session.RunID) != "",
		RegisterWaiter: c.approval.RegisterWaiter,
		Emit:           c.emitToolApprovalRequest,
	})
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
	return mcp.MergeToolSessionContext(base, prompt)
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

func allowUnmappedPermission(state *acpToolState) bool {
	_, kind := permissionToolIdentityFromState(state)
	switch kind {
	case string(acp.ToolKindRead), string(acp.ToolKindSearch), string(acp.ToolKindFetch), string(acp.ToolKindThink), string(acp.ToolKindSwitchMode):
		return true
	default:
		return false
	}
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
			c.logger.Warn("cancelling MCP tool preflight for missing or non-Memoh server",
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
	c.events.emit(event.StreamEvent{
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
	})
	return true
}

func (c *clientCallbacks) SessionUpdate(_ context.Context, p acp.SessionNotification) error {
	c.mu.RLock()
	collector := c.collector
	sink := c.sink
	limit := c.toolLimit
	configOptionsHandler := c.configOptionsHandler
	c.mu.RUnlock()
	if p.Update.ConfigOptionUpdate != nil && configOptionsHandler != nil {
		configOptionsHandler(p.SessionId, p.Update.ConfigOptionUpdate.ConfigOptions)
	}
	var events []event.StreamEvent
	if c.toolMapper != nil {
		events = c.toolMapper.eventsFromNotification(p)
	}
	events = limitStreamEvents(events, limit)
	if collector != nil {
		collector.apply(p, events)
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
	ctx, cancel := mcp.BindRuntimeContext(ctx, session)
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
			return mcp.ValidateRuntimeGuard(guardCtx, session)
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
