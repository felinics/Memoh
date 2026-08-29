package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/toolcontext"
	"github.com/felinics/memoh/internal/version"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type ToolSessionContext = toolcontext.Session

// RuntimeSyncGuard runs one runtime-configuration workspace operation while
// its caller holds the durable bot generation lock. The client intentionally
// knows nothing about the persistence implementation behind this callback.
type RuntimeSyncGuard func(context.Context, func(context.Context) error) error

// ErrRuntimeSyncGuardRejected identifies a durable generation/reset guard
// rejection. Unlike an ordinary best-effort artifact sync failure, continuing
// the native conversation after this error could advance stale configuration.
var (
	ErrRuntimeSyncGuardRejected = errors.New("ACP runtime synchronization guard rejected the process")
	// ErrRuntimeSyncGenerationStale is permanent for this process. A
	// bot-scoped reset in progress is transient and intentionally keeps the
	// periodic loop alive for a later retry.
	ErrRuntimeSyncGenerationStale = errors.New("ACP runtime synchronization generation is stale")
	ErrRuntimeSyncResetInProgress = errors.New("ACP runtime synchronization reset is in progress")
)

type StartRequest struct {
	AgentID     string
	BotID       string
	ProjectPath string
	// Resume restores a reusable host-spooled adapter-native session into the
	// fresh process-owned runtime directory and reconnects to it through ACP. A
	// non-nil value is a strict resume request: unsupported or missing native
	// state is an error and never falls back to a divergent new session. The
	// caller owns the snapshot and must close it after all startup fallbacks.
	Resume    *SessionStateSnapshot
	Command   string
	Args      []string
	Env       []string
	CleanEnv  bool
	UnsetEnv  []string
	Resolved  *ResolvedSessionContext
	SetupMode SetupMode
	// SessionMode, when set, is pinned via session/set_mode right after the
	// session is created (see acpprofile.Profile.SessionModeID).
	SessionMode string
	// ReasoningConfigID is a profile compatibility mapping used only when the
	// agent omits ACP's thought_level category. DefaultReasoningEffort is the
	// profile default applied at startup; per-turn choices are applied by the
	// session pool immediately before Prompt.
	ReasoningConfigID      string
	DefaultReasoningEffort string
	Timeout                time.Duration
	ToolSession            ToolSessionContext
	ToolApproval           ToolApprovalService
	UserInput              UserInputService
	ToolGateway            *mcp.ToolGatewayService
	ToolPreflightGateway   *mcp.ToolGatewayService
	ToolHTTPURL            string
	ToolHTTPHandler        http.Handler
	RuntimeSyncGuard       RuntimeSyncGuard
}

type PromptResult struct {
	StopReason string              `json:"stop_reason,omitempty"`
	Text       string              `json:"text,omitempty"`
	Events     []event.StreamEvent `json:"events,omitempty"`
	Usage      *sdk.Usage          `json:"usage,omitempty"`
	// CheckpointStaged is set by SessionPool only after this exact run's native
	// state has been durably staged. The application uses it to choose between
	// publishing a resumable checkpoint head and an explicit reset head.
	CheckpointStaged bool `json:"-"`
	// StateReceipt is an opaque agent-native acknowledgement used only to
	// validate the durable JSONL snapshot for this prompt.
	StateReceipt *SessionStateReceipt `json:"-"`
	// Output is the in-process transcript used for persistence.
	Output []sdk.Message `json:"-"`
}

// PromptResource is an embedded text resource sent alongside an ACP prompt.
type PromptResource struct {
	URI      string
	MimeType string
	Text     string
}

// PromptImage is an inline image sent as an ACP image content block. Data is
// raw base64 without the data URL prefix; MimeType must be an image MIME type.
type PromptImage struct {
	Data     string
	MimeType string
}

type PromptOptions struct {
	ToolOutputLimit   ToolOutputLimit
	Images            []PromptImage
	AllowResourceOnly bool
	// RequiredCommand is an exact, opaque Agent command name. When set, the
	// Session rechecks its latest available_commands snapshot immediately
	// before dispatching the prompt.
	RequiredCommand string
}

type Session struct {
	logger                    *slog.Logger
	proc                      *bridgeProcess
	callbacks                 *clientCallbacks
	conn                      *clientConnection
	sessionID                 acp.SessionId
	sessionStateLocator       acpprofile.RuntimeSessionLocator
	restoredSessionCursor     SessionStateCursor
	projectPath               string
	modelSelector             modelSelector
	modelState                ModelState
	reasoningConfigFallbackID string
	reasoningConfigID         string
	reasoningState            ReasoningState
	modeState                 ModeState
	availableCommands         []AvailableCommandInfo
	modeRevision              uint64
	embeddedContext           bool
	imagePromptSupported      bool
	closeSessionSupported     bool
	defaultSink               EventSink
	lifecycleCtx              context.Context
	cancel                    context.CancelFunc
	reverseHTTPStop           func()

	promptMu     sync.Mutex
	mu           sync.Mutex
	promptCancel context.CancelFunc
	promptDone   <-chan struct{}
	promptToken  *struct{}
	closed       bool
}

//nolint:contextcheck // startup failure closes the owned process through its lifecycle API.
func (r *Runner) StartSession(ctx context.Context, req StartRequest, sink EventSink) (*Session, error) {
	if r == nil || r.workspace == nil {
		return nil, errors.New("ACP workspace provider is not configured")
	}
	if strings.TrimSpace(req.BotID) == "" {
		return nil, errors.New("bot_id is required")
	}
	// Codex was the only ACP runtime before profiles were introduced, and the
	// direct Runner API historically allowed callers (including embedders) to
	// omit AgentID. Keep that compatibility default while still rejecting any
	// explicit unknown profile in prepareRuntimeLease.
	if strings.TrimSpace(req.AgentID) == "" {
		req.AgentID = acpprofile.AgentCodexID
	}

	info, err := r.workspace.WorkspaceInfo(ctx, req.BotID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	var root string
	var projectPath string
	var backend WorkspaceBackend
	if req.Resolved != nil {
		root = strings.TrimSpace(req.Resolved.WorkspaceRoot)
		projectPath = strings.TrimSpace(req.Resolved.ProjectPath)
		backend = req.Resolved.Backend
		if root == "" || projectPath == "" {
			return nil, errors.New("resolved ACP session context is incomplete")
		}
	} else {
		root, projectPath, backend, err = resolveWorkspacePaths(info, req.ProjectPath)
		if err != nil {
			return nil, fmt.Errorf("invalid project_path: %w", err)
		}
	}

	client, err := r.workspace.MCPClient(ctx, req.BotID)
	if err != nil {
		return nil, fmt.Errorf("connect workspace bridge: %w", err)
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = r.timeout
	}
	if timeout <= 0 {
		timeout = DefaultRunTimeout
	}

	lifecycleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	startupDone := make(chan struct{})
	var startupDoneOnce sync.Once
	finishStartup := func() {
		startupDoneOnce.Do(func() {
			close(startupDone)
		})
	}
	defer finishStartup()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-startupDone:
		}
	}()
	command := strings.TrimSpace(req.Command)
	args := append([]string(nil), req.Args...)
	if command == "" {
		command = strings.TrimSpace(r.command)
		if len(args) == 0 {
			args = append(args, r.args...)
		}
	}

	toolHTTPURL := strings.TrimSpace(req.ToolHTTPURL)
	toolHTTPHandler := req.ToolHTTPHandler
	var toolHTTPStop func()
	if backend == WorkspaceBackendContainer && toolHTTPHandler != nil &&
		toolHTTPURL != "" &&
		toolHTTPURL == strings.TrimSpace(info.ACPToolsHTTPURL) {
		guardedURL, guardedPath, guardedHandler, err := guardToolHTTPHandler(toolHTTPURL, toolHTTPHandler)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("prepare Memoh tools bridge: %w", err)
		}
		toolHTTPURL = guardedURL
		var stop func()
		client, stop, err = r.startMemohToolsBridge(lifecycleCtx, req.BotID, client, guardedPath, guardedHandler)
		if err != nil {
			if r.logger != nil {
				r.logger.Warn("Memoh tools bridge unavailable; starting ACP session without Memoh tools",
					slog.String("agent_id", req.AgentID),
					slog.String("bot_id", req.BotID),
					slog.Any("error", err),
				)
			}
			toolHTTPURL = ""
		} else {
			toolHTTPStop = stop
		}
	}

	proc, err := startBridgeProcess(lifecycleCtx, client, command, args, projectPath, timeout, processOptions{
		Backend:          backend,
		BotID:            req.BotID,
		AgentID:          req.AgentID,
		SetupMode:        req.SetupMode,
		Resume:           req.Resume,
		Env:              req.Env,
		CleanEnv:         req.CleanEnv,
		UnsetEnv:         req.UnsetEnv,
		NoTimeout:        true,
		Logger:           r.logger,
		RuntimeSyncGuard: req.RuntimeSyncGuard,
	})
	if err != nil {
		if toolHTTPStop != nil {
			toolHTTPStop()
		}
		cancel()
		return nil, fmt.Errorf("start %s: %w", buildShellCommand(command, args), err)
	}

	toolSession := req.ToolSession
	if strings.TrimSpace(toolSession.BotID) == "" {
		toolSession.BotID = req.BotID
	}
	if strings.TrimSpace(toolSession.ChatID) == "" {
		toolSession.ChatID = toolSession.BotID
	}
	preflightGateway := req.ToolPreflightGateway
	if preflightGateway == nil {
		preflightGateway = req.ToolGateway
	}
	callbacks := newClientCallbacks(lifecycleCtx, client, root, projectPath, timeout, sink, proc.toolEnv, proc.cleanEnv, proc.unsetEnv, req.ToolApproval, preflightGateway, toolSession, acpprofile.QuirksFor(req.AgentID))
	callbacks.userInput = req.UserInput
	callbacks.logger = r.logger
	conn := newClientConnection(callbacks, proc, proc)

	clientCapabilities := acp.ClientCapabilities{
		Fs: acp.FileSystemCapabilities{
			ReadTextFile:  true,
			WriteTextFile: true,
		},
		Terminal: true,
	}
	if req.UserInput != nil {
		clientCapabilities.Elicitation = &acp.ElicitationCapabilities{
			Form: &acp.ElicitationFormCapabilities{},
		}
	}
	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientInfo:         &acp.Implementation{Name: "memoh", Version: version.Version},
		ClientCapabilities: clientCapabilities,
	})
	if err != nil {
		callbacks.close()
		_ = proc.Close()
		if toolHTTPStop != nil {
			toolHTTPStop()
		}
		cancel()
		return nil, fmt.Errorf("initialize ACP agent: %w", err)
	}
	if initResp.ProtocolVersion != acp.ProtocolVersionNumber {
		callbacks.close()
		_ = proc.Close()
		if toolHTTPStop != nil {
			toolHTTPStop()
		}
		cancel()
		return nil, fmt.Errorf(
			"initialize ACP agent: unsupported protocol version %d (client supports %d)",
			initResp.ProtocolVersion,
			acp.ProtocolVersionNumber,
		)
	}

	mcpServers := []acp.McpServer{}
	forceHTTPMCPServer := acpprofile.ShouldForceHTTPMCPServer(req.AgentID)
	if initResp.AgentCapabilities.McpCapabilities.Http || forceHTTPMCPServer {
		if server := memohToolsHTTPMCPServer(toolHTTPURL, toolSession); server.Http != nil {
			mcpServers = append(mcpServers, server)
		}
	}
	if r.logger != nil {
		caps := initResp.AgentCapabilities.McpCapabilities
		r.logger.Info("ACP agent initialized",
			slog.String("agent_id", req.AgentID),
			slog.String("bot_id", req.BotID),
			slog.Bool("mcp_acp", caps.Acp),
			slog.Bool("mcp_http", caps.Http),
			slog.Bool("mcp_sse", caps.Sse),
			slog.Bool("mcp_http_forced", forceHTTPMCPServer && !caps.Http),
			slog.Bool("memoh_tools_http_configured", toolHTTPURL != ""),
			slog.String("memoh_tools_http_url", redactedToolHTTPURL(toolHTTPURL)),
			slog.Int("mcp_servers", len(mcpServers)),
		)
		if toolHTTPURL != "" && len(mcpServers) == 0 {
			r.logger.Warn("Memoh tools were not exposed to ACP agent because no supported MCP transport was available",
				slog.String("agent_id", req.AgentID),
				slog.String("bot_id", req.BotID),
				slog.Bool("agent_supports_acp_mcp", caps.Acp),
				slog.Bool("agent_supports_http_mcp", caps.Http),
				slog.Bool("agent_supports_sse_mcp", caps.Sse),
				slog.Bool("http_mcp_url_configured", toolHTTPURL != ""),
			)
		}
	}
	sessionStateLocator := acpprofile.RuntimeSessionLocatorNone
	if proc.lease != nil {
		if locator, locatorErr := proc.lease.sessionStateLocator(); locatorErr == nil {
			sessionStateLocator = locator
		}
	}
	sess, err := startOrResumeSession(ctx, conn, callbacks, initResp.AgentCapabilities, req.Resume, projectPath, mcpServers, toolSession, sessionStateLocator)
	if err != nil {
		callbacks.close()
		_ = proc.Close()
		if toolHTTPStop != nil {
			toolHTTPStop()
		}
		cancel()
		return nil, err
	}
	if err := pinSessionMode(ctx, conn, sess.SessionId, sess.Modes, req.SessionMode, r.logger, req.AgentID); err != nil {
		callbacks.close()
		_ = proc.Close()
		if toolHTTPStop != nil {
			toolHTTPStop()
		}
		cancel()
		return nil, err
	}
	clientSession := &Session{
		logger:                    r.logger,
		proc:                      proc,
		callbacks:                 callbacks,
		conn:                      conn,
		sessionID:                 sess.SessionId,
		sessionStateLocator:       sessionStateLocator,
		restoredSessionCursor:     cursorFromSnapshot(req.Resume),
		projectPath:               projectPath,
		reasoningConfigFallbackID: strings.TrimSpace(req.ReasoningConfigID),
		embeddedContext:           initResp.AgentCapabilities.PromptCapabilities.EmbeddedContext,
		imagePromptSupported:      initResp.AgentCapabilities.PromptCapabilities.Image,
		closeSessionSupported:     initResp.AgentCapabilities.SessionCapabilities.Close != nil,
		defaultSink:               sink,
		lifecycleCtx:              lifecycleCtx,
		cancel:                    cancel,
		reverseHTTPStop:           toolHTTPStop,
	}
	clientSession.replaceConfigOptions(sess.SessionId, sess.ConfigOptions)
	clientSession.installLegacyModels(sess.Models)
	clientSession.installModes(sess.Modes)
	callbacks.setSession(clientSession)
	if defaultReasoning := strings.TrimSpace(req.DefaultReasoningEffort); defaultReasoning != "" && clientSession.ReasoningState().Supported {
		if _, err := clientSession.SetReasoningEffort(ctx, defaultReasoning); err != nil {
			if errors.Is(err, ErrReasoningEffortUnavailable) ||
				errors.Is(err, ErrReasoningSelectionUnsupported) ||
				errors.Is(err, ErrReasoningEffortRequired) {
				if r.logger != nil {
					r.logger.Warn("failed to apply default ACP reasoning effort; leaving agent value",
						slog.String("agent_id", req.AgentID),
						slog.String("desired_effort", defaultReasoning),
						slog.Any("error", err))
				}
			} else {
				// A failed mutation may have reached the Agent even though its
				// authoritative config snapshot never reached Memoh. Do not return a
				// session whose cached equality checks can no longer be trusted.
				_ = clientSession.Close()
				return nil, fmt.Errorf("apply default ACP reasoning effort %q: %w", defaultReasoning, err)
			}
		}
	}

	proc.Activate()
	finishStartup()
	return clientSession, nil
}

var (
	ErrSessionResumeUnsupported = errors.New("ACP agent does not support session resume or load")
	// ErrSessionResumeRejected means a process started but rejected the exact
	// durable native session selected by canonical history. Callers must not
	// silently replace it with a new session because that would fork history.
	ErrSessionResumeRejected = errors.New("ACP agent rejected the durable session resume")
)

// startOrResumeSession selects exactly one ACP lifecycle operation. The process
// launcher has already materialized persisted state before starting the
// adapter, so session/resume can locate it even when an adapter scans its home
// during initialization. Resume is preferred because session/load replays
// history notifications intended for a UI client; the fallback suppresses the
// startup sink so replay cannot be mistaken for output from the new turn.
func startOrResumeSession(
	ctx context.Context,
	conn *clientConnection,
	callbacks *clientCallbacks,
	capabilities acp.AgentCapabilities,
	resume *SessionStateSnapshot,
	projectPath string,
	mcpServers []acp.McpServer,
	toolSession ToolSessionContext,
	locator acpprofile.RuntimeSessionLocator,
) (sessionResponse, error) {
	meta := sessionLifecycleMeta(locator)
	if resume == nil {
		resp, err := conn.NewSession(ctx, acp.NewSessionRequest{
			Meta:       meta,
			Cwd:        projectPath,
			McpServers: mcpServers,
		})
		if err != nil {
			return sessionResponse{}, fmt.Errorf("create ACP session: %w", err)
		}
		if strings.TrimSpace(string(resp.SessionId)) == "" {
			return sessionResponse{}, errors.New("create ACP session: agent returned an empty session id")
		}
		return resp, nil
	}

	sessionID := strings.TrimSpace(resume.State().SessionID)
	if sessionID == "" {
		return sessionResponse{}, fmt.Errorf("%w: session id is required", ErrSessionResumeRejected)
	}
	if capabilities.SessionCapabilities.Resume != nil {
		resp, err := conn.ResumeSession(ctx, acp.ResumeSessionRequest{
			Meta:       meta,
			Cwd:        projectPath,
			McpServers: mcpServers,
			SessionId:  acp.SessionId(sessionID),
		})
		if err != nil {
			return sessionResponse{}, fmt.Errorf("%w: resume ACP session: %w", ErrSessionResumeRejected, err)
		}
		resp.SessionId = acp.SessionId(sessionID)
		return resp, nil
	}
	if capabilities.LoadSession {
		// load replays the historical session/update stream. No prompt collector
		// exists yet, and clearing the startup sink prevents those notifications
		// from being projected as fresh output by the caller.
		if callbacks != nil {
			callbacks.setPromptState(nil, nil, toolSession)
		}
		resp, err := conn.LoadSession(ctx, acp.LoadSessionRequest{
			Meta:       meta,
			Cwd:        projectPath,
			McpServers: mcpServers,
			SessionId:  acp.SessionId(sessionID),
		})
		if err != nil {
			return sessionResponse{}, fmt.Errorf("%w: load ACP session: %w", ErrSessionResumeRejected, err)
		}
		resp.SessionId = acp.SessionId(sessionID)
		return resp, nil
	}
	return sessionResponse{}, ErrSessionResumeUnsupported
}

// pinSessionMode forces the agent session into the requested permission mode
// so tool approvals flow through ACP regardless of ambient agent-side
// configuration (e.g. a host ~/.claude/settings.json defaultMode). A desired
// mode the agent does not advertise aborts startup because the session would
// otherwise run with unknown permission behavior.
func pinSessionMode(ctx context.Context, conn *clientConnection, sessionID acp.SessionId, modes *acp.SessionModeState, desired string, logger *slog.Logger, agentID string) error {
	desired = strings.TrimSpace(desired)
	if desired == "" {
		return nil
	}
	if modes == nil {
		return fmt.Errorf("pin ACP session mode %q: agent did not report session modes", desired)
	}
	if string(modes.CurrentModeId) == desired {
		return nil
	}
	available := false
	for _, mode := range modes.AvailableModes {
		if string(mode.Id) == desired {
			available = true
			break
		}
	}
	if !available {
		if logger != nil {
			logger.Warn("ACP agent does not advertise the pinned session mode",
				slog.String("agent_id", agentID),
				slog.String("desired_mode", desired),
				slog.String("current_mode", string(modes.CurrentModeId)))
		}
		return fmt.Errorf("pin ACP session mode %q: mode is not advertised by agent", desired)
	}
	if _, err := conn.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionId: sessionID,
		ModeId:    acp.SessionModeId(desired),
	}); err != nil {
		return fmt.Errorf("pin ACP session mode %q: %w", desired, err)
	}
	previousMode := modes.CurrentModeId
	modes.CurrentModeId = acp.SessionModeId(desired)
	if logger != nil {
		logger.Info("pinned ACP session mode",
			slog.String("agent_id", agentID),
			slog.String("mode", desired),
			slog.String("previous_mode", string(previousMode)))
	}
	return nil
}

func (r *Runner) startMemohToolsBridge(ctx context.Context, botID string, client *bridge.Client, route string, handler http.Handler) (*bridge.Client, func(), error) {
	if client == nil {
		return nil, nil, errors.New("workspace bridge client is required")
	}
	current := client
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		stop, err := current.ServeReverseHTTPRoute(ctx, route, handler)
		if err == nil {
			return current, stop, nil
		}
		lastErr = err
		if ctx.Err() != nil || !isClosingBridgeClientError(err) || r == nil || r.workspace == nil || strings.TrimSpace(botID) == "" {
			return current, nil, err
		}
		_ = current.Close()
		if err := sleepContext(ctx, time.Duration(attempt+1)*150*time.Millisecond); err != nil {
			return current, nil, err
		}
		next, err := r.workspace.MCPClient(ctx, botID)
		if err != nil {
			return current, nil, fmt.Errorf("%w; reconnect workspace bridge: %w", lastErr, err)
		}
		current = next
	}
	return current, nil, lastErr
}

func isClosingBridgeClientError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "client connection is closing") ||
		strings.Contains(lower, "transport is closing") ||
		strings.Contains(lower, "use of closed network connection")
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func guardToolHTTPHandler(rawURL string, handler http.Handler) (string, string, http.Handler, error) {
	if handler == nil {
		return "", "", nil, errors.New("tool HTTP handler is required")
	}
	guardedURL, guardPath, err := guardedToolHTTPURL(rawURL)
	if err != nil {
		return "", "", nil, err
	}
	return guardedURL, guardPath, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req == nil || req.URL == nil || req.URL.Path != guardPath {
			http.NotFound(w, req)
			return
		}
		handler.ServeHTTP(w, req)
	}), nil
}

func guardedToolHTTPURL(rawURL string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("invalid Memoh tools URL %q", rawURL)
	}
	basePath := strings.TrimRight(u.Path, "/")
	if basePath == "" {
		basePath = "/mcp"
	}
	u.Path = basePath + "/" + uuid.NewString()
	return u.String(), u.Path, nil
}

func redactedToolHTTPURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	trimmedPath := strings.Trim(u.Path, "/")
	if trimmedPath != "" {
		parts := strings.Split(trimmedPath, "/")
		redacted := false
		for i, part := range parts {
			if i > 0 && strings.EqualFold(parts[i-1], "bots") {
				parts[i] = "redacted"
				redacted = true
				continue
			}
			if _, err := uuid.Parse(part); err == nil {
				parts[i] = "redacted"
				redacted = true
			}
		}
		if !redacted {
			parts[len(parts)-1] = "redacted"
		}
		u.Path = "/" + strings.Join(parts, "/")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (s *Session) ID() string {
	if s == nil {
		return ""
	}
	return string(s.sessionID)
}

func (s *Session) ProjectPath() string {
	if s == nil {
		return ""
	}
	return s.projectPath
}

// Done closes when the owned ACP process or its bridge transport exits.
func (s *Session) Done() (<-chan struct{}, bool) {
	if s == nil || s.proc == nil {
		return nil, false
	}
	return s.proc.Done(), true
}

// SnapshotSessionState captures the adapter-native JSONL files needed to
// reconstruct this session in a later process. The process owns path discovery
// and validation; callers only persist the returned opaque, ordered state.
func (s *Session) SnapshotSessionState(
	ctx context.Context,
	previous SessionStateCursor,
	receipt *SessionStateReceipt,
	boundaries map[string]int64,
) (*SessionStateSnapshot, error) {
	if s == nil {
		return nil, ErrSessionNotInitialized
	}
	s.mu.Lock()
	proc := s.proc
	sessionID := s.sessionID
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, ErrSessionClosed
	}
	if proc == nil || sessionID == "" {
		return nil, ErrSessionNotInitialized
	}
	return proc.SnapshotSessionState(ctx, string(sessionID), previous, receipt, boundaries)
}

// RestoredSessionStateCursor is the bounded freshness cursor computed while
// the reusable resume snapshot was spooled and verified.
func (s *Session) RestoredSessionStateCursor() SessionStateCursor {
	if s == nil {
		return SessionStateCursor{}
	}
	return s.restoredSessionCursor
}

func cursorFromSnapshot(snapshot *SessionStateSnapshot) SessionStateCursor {
	if snapshot == nil {
		return SessionStateCursor{}
	}
	return snapshot.Cursor()
}

// CancelPrompt asks an in-flight prompt to unwind without closing the ACP
// session. Pool shutdown uses this before waiting for the per-runtime operation
// lock: a prompt that already completed can finish its durable snapshot, while
// a prompt still blocked in the agent receives session/cancel and releases the
// lock. The actual session/process close happens only after that boundary.
func (s *Session) CancelPrompt() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.promptCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Session) Prompt(ctx context.Context, prompt string, sinks ...EventSink) (PromptResult, error) {
	return s.PromptWithResources(ctx, prompt, nil, sinks...)
}

// PromptWithResources sends a user prompt plus optional embedded resources.
func (s *Session) PromptWithResources(ctx context.Context, prompt string, resources []PromptResource, sinks ...EventSink) (PromptResult, error) {
	return s.PromptWithToolContext(ctx, prompt, resources, ToolSessionContext{}, sinks...)
}

// PromptWithToolContext sends a user prompt and binds request-scoped tool
// identity to ACP callbacks while that prompt is active.
func (s *Session) PromptWithToolContext(ctx context.Context, prompt string, resources []PromptResource, toolSession ToolSessionContext, sinks ...EventSink) (PromptResult, error) {
	return s.PromptWithToolContextOptions(ctx, prompt, resources, toolSession, PromptOptions{}, sinks...)
}

func (s *Session) PromptWithToolContextOptions(ctx context.Context, prompt string, resources []PromptResource, toolSession ToolSessionContext, options PromptOptions, sinks ...EventSink) (PromptResult, error) {
	if s == nil || s.conn == nil {
		return PromptResult{}, ErrSessionNotInitialized
	}
	prompt = strings.TrimSpace(prompt)
	images, err := NormalizePromptImages(options.Images)
	if err != nil {
		return PromptResult{}, err
	}
	if prompt == "" && len(images) == 0 && (!options.AllowResourceOnly || len(cleanPromptResources(resources)) == 0) {
		return PromptResult{}, ErrPromptRequired
	}
	if len(images) > 0 && !s.imagePromptSupported {
		return PromptResult{}, ErrImagePromptUnsupported
	}

	s.promptMu.Lock()
	defer s.promptMu.Unlock()

	promptCtx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	promptToken := &struct{}{}
	promptDone := make(chan struct{})

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		close(promptDone)
		return PromptResult{}, ErrSessionClosed
	}
	s.promptCancel = cancelPrompt
	s.promptDone = promptDone
	s.promptToken = promptToken
	conn := s.conn
	sessionID := s.sessionID
	sessionStateLocator := s.sessionStateLocator
	callbacks := s.callbacks
	proc := s.proc
	defaultSink := s.defaultSink
	s.mu.Unlock()
	defer func() {
		close(promptDone)
		s.mu.Lock()
		if s.promptToken == promptToken {
			s.promptCancel = nil
			s.promptDone = nil
			s.promptToken = nil
		}
		s.mu.Unlock()
	}()
	if conn == nil {
		return PromptResult{}, ErrSessionNotInitialized
	}

	promptBlocks := s.promptBlocks(prompt, resources, images)
	collector := newEventCollector(options.ToolOutputLimit)
	// The prompt context is the output boundary. Once Stop/Close cancels it,
	// late adapter notifications must not reach either history or the live UI.
	collector.bindContext(promptCtx)
	sink := defaultSink
	if len(sinks) > 0 {
		sink = sinks[0]
	}
	if callbacks != nil {
		callbacks.setPromptState(collector, sink, toolSession, options.ToolOutputLimit)
	}
	defer func() {
		if callbacks != nil {
			if promptCtx.Err() != nil {
				// Record the cancelled turn's tool calls before the per-prompt
				// states are wiped, so a late permission callback for one of
				// them resolves as cancelled instead of correlating against
				// the next turn.
				callbacks.markPromptCancelled()
			}
			callbacks.setPromptState(nil, nil, ToolSessionContext{}, ToolOutputLimit{})
		}
	}()
	if options.RequiredCommand != "" && !s.AdvertisesCommand(options.RequiredCommand) {
		return PromptResult{}, ErrAgentCommandUnavailable
	}

	stateReceiptCollector, receiptErr := callbacks.beginSessionStateReceipt(sessionStateLocator, string(sessionID))
	if receiptErr != nil {
		return PromptResult{}, fmt.Errorf("prepare ACP session-state receipt: %w", receiptErr)
	}

	resp, err := conn.Prompt(promptCtx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    promptBlocks,
	})
	stateReceipt, receiptErr := callbacks.finishSessionStateReceipt(stateReceiptCollector, err == nil)
	var runtimeGuardErr error
	if proc != nil {
		if syncErr := proc.SyncPromptState(promptCtx); syncErr != nil && s.logger != nil {
			if errors.Is(syncErr, ErrRuntimeSyncGuardRejected) {
				runtimeGuardErr = syncErr
			} else {
				s.logger.Warn("failed to synchronize ACP prompt runtime state",
					slog.String("session_id", string(sessionID)),
					slog.Any("error", syncErr))
			}
		} else if errors.Is(syncErr, ErrRuntimeSyncGuardRejected) {
			runtimeGuardErr = syncErr
		}
	}
	collected := collector.result()
	usage := promptUsageFromACP(resp.Usage)
	result := PromptResult{
		StopReason:   string(resp.StopReason),
		Text:         collected.Text,
		Events:       collected.Events,
		Usage:        usage,
		StateReceipt: stateReceipt,
		Output:       attachUsageToLastAssistant(collected.Output, usage),
	}
	if err != nil {
		if proc != nil {
			return result, proc.errorWithStderr(fmt.Errorf("send ACP prompt: %w", err))
		}
		return result, fmt.Errorf("send ACP prompt: %w", err)
	}
	if runtimeGuardErr != nil {
		return result, runtimeGuardErr
	}
	if receiptErr != nil {
		// A missing or inconsistent receipt only degrades resumability - the
		// pool declines staging without one and the turn publishes a reset
		// head. Failing the prompt here would discard a legitimately completed
		// turn's output (Claude emits non-"success" result subtypes for
		// refusals and turn limits), which is strictly worse.
		if s.logger != nil {
			s.logger.Warn("ACP session-state receipt unavailable; this turn will not stage a checkpoint",
				slog.String("session_id", string(sessionID)),
				slog.Any("error", receiptErr))
		}
		result.StateReceipt = nil
	}
	return result, nil
}

func promptUsageFromACP(usage *acp.Usage) *sdk.Usage {
	if usage == nil {
		return nil
	}
	out := &sdk.Usage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if usage.CachedReadTokens != nil {
		out.CachedInputTokens = *usage.CachedReadTokens
		out.InputTokenDetails.CacheReadTokens = *usage.CachedReadTokens
	}
	if usage.CachedWriteTokens != nil {
		out.InputTokenDetails.CacheWriteTokens = *usage.CachedWriteTokens
	}
	if usage.ThoughtTokens != nil {
		out.ReasoningTokens = *usage.ThoughtTokens
		out.OutputTokenDetails.ReasoningTokens = *usage.ThoughtTokens
	}
	return out
}

func attachUsageToLastAssistant(output []sdk.Message, usage *sdk.Usage) []sdk.Message {
	if usage == nil {
		return output
	}
	for i := len(output) - 1; i >= 0; i-- {
		if output[i].Role == sdk.MessageRoleAssistant {
			output[i].Usage = usage
			return output
		}
	}
	return output
}

func (s *Session) promptBlocks(prompt string, resources []PromptResource, images []PromptImage) []acp.ContentBlock {
	cleaned := cleanPromptResources(resources)
	blocks := make([]acp.ContentBlock, 0, 1+len(cleaned)+len(images))
	switch {
	case len(cleaned) == 0:
		if prompt != "" {
			blocks = append(blocks, acp.TextBlock(prompt))
		}
	case s != nil && s.embeddedContext:
		if prompt != "" {
			blocks = append(blocks, acp.TextBlock(prompt))
		}
		for _, resource := range cleaned {
			mimeType := resource.MimeType
			blocks = append(blocks, acp.ResourceBlock(acp.EmbeddedResourceResource{
				TextResourceContents: &acp.TextResourceContents{
					Uri:      resource.URI,
					MimeType: &mimeType,
					Text:     resource.Text,
				},
			}))
		}
	default:
		var sb strings.Builder
		for _, resource := range cleaned {
			sb.WriteString("<context ref=\"")
			sb.WriteString(resource.URI)
			sb.WriteString("\">\n")
			sb.WriteString(resource.Text)
			sb.WriteString("\n</context>\n\n")
		}
		sb.WriteString(prompt)
		if text := strings.TrimSpace(sb.String()); text != "" {
			blocks = append(blocks, acp.TextBlock(text))
		}
	}
	for _, image := range images {
		blocks = append(blocks, acp.ImageBlock(image.Data, image.MimeType))
	}
	return blocks
}

const maxPromptImageBytes int64 = 20 * 1024 * 1024

// NormalizePromptImages validates and normalizes images before a runtime is
// started. Session prompt dispatch calls it again as a defense-in-depth check.
func NormalizePromptImages(images []PromptImage) ([]PromptImage, error) {
	out := make([]PromptImage, 0, len(images))
	for i, image := range images {
		data := strings.TrimSpace(image.Data)
		mimeType := strings.ToLower(strings.TrimSpace(image.MimeType))
		if idx := strings.Index(mimeType, ";"); idx >= 0 {
			mimeType = strings.TrimSpace(mimeType[:idx])
		}
		if data == "" || !strings.HasPrefix(mimeType, "image/") || !validPromptImageBase64(data) {
			return nil, fmt.Errorf("%w at index %d", ErrInvalidPromptImage, i)
		}
		out = append(out, PromptImage{
			Data:     data,
			MimeType: mimeType,
		})
	}
	return out, nil
}

func validPromptImageBase64(data string) bool {
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(data))
	n, err := io.Copy(io.Discard, io.LimitReader(decoder, maxPromptImageBytes+1))
	return err == nil && n <= maxPromptImageBytes
}

func cleanPromptResources(resources []PromptResource) []PromptResource {
	out := make([]PromptResource, 0, len(resources))
	for _, resource := range resources {
		text := strings.TrimSpace(resource.Text)
		if text == "" {
			continue
		}
		uri := strings.TrimSpace(resource.URI)
		if uri == "" {
			continue
		}
		mimeType := strings.TrimSpace(resource.MimeType)
		if mimeType == "" {
			mimeType = "text/plain"
		}
		out = append(out, PromptResource{
			URI:      uri,
			MimeType: mimeType,
			Text:     text,
		})
	}
	return out
}

// AdvertisesCommand reports whether the live session currently declares the
// named agent command. Names are opaque and case-sensitive; the caller passes
// the exact selector, never a normalized form.
func (s *Session) AdvertisesCommand(name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, command := range s.availableCommands {
		if command.Name == name {
			return true
		}
	}
	return false
}

// WaitDecisionCallbacksIdle reports whether every in-flight permission/Form
// callback finished before the timeout. A cancelled prompt uses it as the
// quiescence barrier before the warm runtime is handed to the next turn.
func (s *Session) WaitDecisionCallbacksIdle(timeout time.Duration) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	callbacks := s.callbacks
	s.mu.Unlock()
	return callbacks.waitDecisionCallbacksIdle(timeout)
}

func (s *Session) Close() error {
	return s.close(false)
}

// ForceClose tears down the transport before attempting any protocol-level
// cleanup. It is reserved for an unconfirmed prompt cancellation, where a
// blocked JSON-RPC write can otherwise prevent graceful session/close from
// ever reaching the process close that would unblock it.
func (s *Session) ForceClose() error {
	return s.close(true)
}

func (s *Session) close(force bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conn := s.conn
	sessionID := s.sessionID
	callbacks := s.callbacks
	proc := s.proc
	cancel := s.cancel
	reverseHTTPStop := s.reverseHTTPStop
	closeSessionSupported := s.closeSessionSupported
	lifecycleCtx := s.lifecycleCtx
	promptCancel := s.promptCancel
	promptDone := s.promptDone
	s.mu.Unlock()

	if promptCancel != nil {
		promptCancel()
	}
	if !force && promptDone != nil {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-promptDone:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	if force {
		// Close the process/pipe before any graceful JSON-RPC request. This is the
		// operation that releases a writer stuck behind connection.writeMu.
		if cancel != nil {
			cancel()
		}
		var closeErr error
		if proc != nil {
			closeErr = proc.Close()
		}
		if callbacks != nil {
			callbacks.close()
		}
		if reverseHTTPStop != nil {
			reverseHTTPStop()
		}
		return closeErr
	}
	if closeSessionSupported && conn != nil && sessionID != "" {
		if lifecycleCtx == nil {
			lifecycleCtx = context.Background()
		}
		ctx, cancelClose := context.WithTimeout(context.WithoutCancel(lifecycleCtx), 2*time.Second)
		_, _ = conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sessionID})
		cancelClose()
	}
	if callbacks != nil {
		callbacks.close()
	}
	if reverseHTTPStop != nil {
		reverseHTTPStop()
	}
	if cancel != nil {
		cancel()
	}
	if proc != nil {
		return proc.Close()
	}
	return nil
}
