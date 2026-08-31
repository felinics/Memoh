package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/version"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// appServer is one long-lived `codex app-server` process for one Bot Agent.
type appServer struct {
	botID      string
	botAgentID string
	proc       *appServerProcess
	conn       *conn
	logger     *slog.Logger
	// client is the workspace bridge the process runs over; tool-gateway
	// mounts reuse it.
	client *bridge.Client
	// workspaceInfo captures the workspace backend and its container-local
	// tools proxy address for tool-gateway mounts.
	workspaceInfo bridge.WorkspaceInfo
	// mountCtx bounds thread tool-gateway mounts to the server's lifetime.
	mountCtx    context.Context
	mountCancel context.CancelFunc

	codexVersion string

	mu    sync.Mutex
	turns map[string]*turnState // active turn per thread id
	// loadedThreads tracks thread ids this process has started or resumed;
	// resuming an already-loaded thread is a no-op server-side but tracking
	// avoids redundant calls.
	loadedThreads map[string]bool
	// toollessThreads marks threads whose start-time config carried no Memoh
	// tool gateway; the driver re-emits a notice for them every turn.
	toollessThreads map[string]bool
	// toolLookup reports whether a tool name is served by the Memoh gateway
	// for this bot; the MCP consent path uses it turn-independently.
	toolLookup func(context.Context, string) bool
	authReady  bool
	// toolMounts holds each thread's live gateway route (see tools.go).
	toolMounts map[string]*toolmount.Mount
	// logins tracks in-flight device-code logins by login id; outcomes arrive
	// via the account/login/completed notification.
	logins map[string]*loginOutcome
}

// loginOutcome is the terminal state of one device-code login.
type loginOutcome struct {
	Done    bool
	Success bool
	Error   string
}

// ErrAuthRequired identifies a configured Codex runtime that still needs the
// user to complete account authorization in the Bot's workspace.
var ErrAuthRequired = errors.New("codex runtime authentication is required")

// handshakeTimeout bounds initialize plus auth setup on a fresh process.
const handshakeTimeout = 60 * time.Second

func startAppServerSession(ctx context.Context, botID, botAgentID string, client *bridge.Client, cfg Config, logger *slog.Logger) (*appServer, error) {
	proc, err := startAppServer(ctx, client, defaultProjectPath, codexHome(botAgentID), cfg)
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	mountCtx, mountCancel := context.WithCancel(ctx)
	srv := &appServer{
		botID:           botID,
		botAgentID:      botAgentID,
		proc:            proc,
		logger:          logger,
		client:          client,
		mountCtx:        mountCtx,
		mountCancel:     mountCancel,
		turns:           map[string]*turnState{},
		loadedThreads:   map[string]bool{},
		toollessThreads: map[string]bool{},
		logins:          map[string]*loginOutcome{},
		toolMounts:      map[string]*toolmount.Mount{},
	}
	srv.conn = newConn(proc, srv, logger)

	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var initResp protocol.InitializeResponse
	err = srv.conn.Call(handshakeCtx, "initialize", protocol.InitializeParams{
		ClientInfo: protocol.ClientInfo{
			Name:    "memoh",
			Version: version.ShortCommitHash(),
		},
	}, &initResp)
	if err != nil {
		_ = srv.conn.Close()
		return nil, fmt.Errorf("codex initialize failed: %w (stderr: %s)", err, proc.StderrTail())
	}
	if err := srv.conn.Notify(protocol.MethodInitialized, nil); err != nil {
		_ = srv.conn.Close()
		return nil, err
	}
	srv.codexVersion = codexVersionFromUserAgent(initResp.UserAgent)
	if srv.codexVersion != protocol.PinnedCodexVersion {
		// The generated protocol types match the pinned CLI exactly. A drifted
		// binary usually still speaks a compatible superset (unknown fields
		// and methods are tolerated by design), so warn loudly instead of
		// refusing service; the toolkit pin and this check must converge.
		logger.Warn("codex CLI version differs from the pinned protocol snapshot",
			slog.String("bot_id", botID),
			slog.String("cli_version", srv.codexVersion),
			slog.String("pinned", protocol.PinnedCodexVersion),
		)
	}
	return srv, nil
}

// codexVersionFromUserAgent extracts the CLI version from the initialize
// userAgent, e.g. "memoh/0.151.0 (Mac OS 27.0; arm64) …" → "0.151.0".
func codexVersionFromUserAgent(userAgent string) string {
	head, _, _ := strings.Cut(userAgent, " ")
	_, ver, ok := strings.Cut(head, "/")
	if !ok {
		return ""
	}
	return ver
}

// ensureAuth verifies codex is authenticated, logging in with the bot's API
// key when needed. ChatGPT-mode credentials must already exist in CODEX_HOME
// (established via a login flow); their refresh is codex's own job.
func (s *appServer) ensureAuth(ctx context.Context, cfg Config) error {
	if cfg.Auth == AuthAPIKey && cfg.APIKey == "" {
		return ErrAuthRequired
	}
	s.mu.Lock()
	ready := s.authReady
	s.mu.Unlock()
	if ready {
		return nil
	}
	var account protocol.GetAccountResponse
	if err := s.conn.Call(ctx, protocol.MethodAccountRead, protocol.GetAccountParams{}, &account); err != nil {
		return fmt.Errorf("codex account/read: %w", err)
	}
	needsLogin := account.Account == nil
	if account.Account != nil {
		switch cfg.Auth {
		case AuthAPIKey:
			// account/read only reveals THAT an API-key login exists, never
			// which key it holds — CODEX_HOME may still carry a rotated or
			// revoked predecessor. The login below is a cheap local auth.json
			// write, so always re-login with the configured key: this app
			// server starts at most once per configuration (a metadata change
			// recycles it), making the login effectively once per key.
			needsLogin = true
		case AuthChatGPT:
			// Stored credentials must match the configured mode: a leftover
			// api-key login on a ChatGPT bot silently bills the wrong account.
			if account.Account.Chatgpt == nil {
				return fmt.Errorf("%w: account/read returned no ChatGPT credentials", ErrAuthRequired)
			}
		}
	}
	if needsLogin {
		switch cfg.Auth {
		case AuthAPIKey:
			var login protocol.LoginAccountResponse
			err := s.conn.Call(ctx, protocol.MethodAccountLoginStart, protocol.LoginAccountParams{
				APIKey: &protocol.APIKeyLoginAccountParams{APIKey: cfg.APIKey},
			}, &login)
			if err != nil {
				return fmt.Errorf("codex api-key login: %w", err)
			}
		case AuthChatGPT:
			return fmt.Errorf("%w: CODEX_HOME has no stored ChatGPT credentials", ErrAuthRequired)
		default:
			return ErrNotConfigured
		}
	}
	s.mu.Lock()
	s.authReady = true
	s.mu.Unlock()
	return nil
}

// registerTurn claims the thread's turn slot for routing inbound traffic.
func (s *appServer) registerTurn(threadID string, turn *turnState) {
	s.mu.Lock()
	s.turns[threadID] = turn
	s.mu.Unlock()
}

func (s *appServer) unregisterTurn(threadID string, turn *turnState) {
	s.mu.Lock()
	if s.turns[threadID] == turn {
		delete(s.turns, threadID)
	}
	s.mu.Unlock()
}

func (s *appServer) turnForThread(threadID string) *turnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turns[threadID]
}

func (s *appServer) hasActiveTurns() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.turns) > 0
}

func (s *appServer) markThreadLoaded(threadID string) {
	s.mu.Lock()
	s.loadedThreads[threadID] = true
	s.mu.Unlock()
}

func (s *appServer) threadLoaded(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadedThreads[threadID]
}

func (s *appServer) setThreadToolless(threadID string, toolless bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if toolless {
		s.toollessThreads[threadID] = true
	} else {
		delete(s.toollessThreads, threadID)
	}
}

func (s *appServer) threadToolless(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toollessThreads[threadID]
}

// HandleServerRequest routes app-server → Memoh requests to the owning turn.
// It runs on the read loop, so decisions are dispatched to goroutines.
func (s *appServer) HandleServerRequest(_ context.Context, req *protocol.Inbound) {
	decoded, known, err := protocol.DecodeServerRequestParams(req.Method, req.Params)
	if err != nil {
		s.logger.Error("codex: undecodable server request", slog.String("method", req.Method), slog.Any("error", err))
		_ = s.conn.RespondError(req.ID, -32602, "memoh could not decode this request")
		return
	}
	if !known {
		s.logger.Warn("codex: unhandled server request method", slog.String("method", req.Method))
		_ = s.conn.RespondError(req.ID, -32601, "memoh does not handle this request")
		return
	}
	switch params := decoded.(type) {
	case *protocol.McpServerElicitationRequestParams:
		go s.dispatchElicitation(req, params) //nolint:contextcheck // consent lookup and turn decisions own their lifetimes
		return
	case *protocol.ChatgptAuthTokensRefreshParams:
		// Token injection mode is not used: codex owns auth.json refresh.
		_ = s.conn.RespondError(req.ID, -32000, "memoh does not inject ChatGPT auth tokens")
		return
	}
	threadID := serverRequestThreadID(decoded)
	turn := s.turnForThread(threadID)
	if turn == nil {
		// Fail closed: an approval with no live turn has nobody to decide it.
		s.logger.Warn("codex: server request for idle thread", slog.String("method", req.Method), slog.String("thread_id", threadID))
		_ = s.conn.RespondError(req.ID, -32000, "no active turn for this thread")
		return
	}
	// The decision runs on the turn-scoped context, not the read loop's; the
	// turn owns its lifetime.
	go turn.handleServerRequest(s.conn, req, decoded) //nolint:contextcheck // turnState.ctx bounds the decision
}

// HandleNotification routes app-server notifications to the owning turn.
func (s *appServer) HandleNotification(_ context.Context, note *protocol.Inbound) {
	decoded, known, err := protocol.DecodeServerNotificationParams(note.Method, note.Params)
	if err != nil {
		s.logger.Warn("codex: undecodable notification", slog.String("method", note.Method), slog.Any("error", err))
		return
	}
	if !known {
		return
	}
	threadID := notificationThreadID(decoded)
	if threadID == "" {
		s.handleGlobalNotification(note.Method, decoded)
		return
	}
	if turn := s.turnForThread(threadID); turn != nil {
		turn.handleNotification(decoded)
	}
}

func (s *appServer) handleGlobalNotification(method string, decoded any) {
	switch method {
	case protocol.MethodDeprecationNotice:
		s.logger.Warn("codex deprecation notice", slog.Any("notice", decoded))
	case protocol.MethodWarning, protocol.MethodConfigWarning:
		s.logger.Warn("codex warning", slog.String("method", method), slog.Any("payload", decoded))
	case protocol.MethodAccountLoginCompleted:
		completed, ok := decoded.(*protocol.AccountLoginCompletedNotification)
		if !ok || completed.LoginID == nil {
			return
		}
		outcome := &loginOutcome{Done: true, Success: completed.Success}
		if completed.Error != nil {
			outcome.Error = *completed.Error
		}
		s.mu.Lock()
		if _, tracked := s.logins[*completed.LoginID]; tracked {
			s.logins[*completed.LoginID] = outcome
		}
		if completed.Success {
			// Fresh credentials just landed in CODEX_HOME.
			s.authReady = true
		}
		s.mu.Unlock()
	}
}

// trackLogin registers a device-code login for completion tracking.
func (s *appServer) trackLogin(loginID string) {
	s.mu.Lock()
	s.logins[loginID] = &loginOutcome{}
	s.mu.Unlock()
}

// loginStatus returns the tracked outcome; ok is false for unknown logins.
func (s *appServer) loginStatus(loginID string) (loginOutcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	outcome, ok := s.logins[loginID]
	if !ok {
		return loginOutcome{}, false
	}
	return *outcome, true
}

func (s *appServer) forgetLogin(loginID string) {
	s.mu.Lock()
	delete(s.logins, loginID)
	s.mu.Unlock()
}

// Done reports process exit; the server lifecycle table watches it.
func (s *appServer) Done() <-chan struct{} { return s.proc.Done() }

func (s *appServer) Close() error {
	if s.mountCancel != nil {
		s.mountCancel()
	}
	s.stopToolMounts()
	return s.conn.Close()
}

// serverRequestThreadID extracts the thread id from a typed server request.
func serverRequestThreadID(decoded any) string {
	switch params := decoded.(type) {
	case *protocol.CommandExecutionRequestApprovalParams:
		return params.ThreadID
	case *protocol.FileChangeRequestApprovalParams:
		return params.ThreadID
	case *protocol.PermissionsRequestApprovalParams:
		return params.ThreadID
	case *protocol.ToolRequestUserInputParams:
		return params.ThreadID
	}
	return ""
}

// notificationThreadID extracts the thread id from a typed notification.
func notificationThreadID(decoded any) string {
	switch params := decoded.(type) {
	case *protocol.ThreadStartedNotification:
		return params.Thread.ID
	case *protocol.ThreadStatusChangedNotification:
		return params.ThreadID
	case *protocol.ThreadTokenUsageUpdatedNotification:
		return params.ThreadID
	case *protocol.ContextCompactedNotification: //nolint:staticcheck // still the wire shape for thread/compacted at the pinned version
		return params.ThreadID
	case *protocol.TurnStartedNotification:
		return params.ThreadID
	case *protocol.TurnCompletedNotification:
		return params.ThreadID
	case *protocol.TurnPlanUpdatedNotification:
		return params.ThreadID
	case *protocol.ItemStartedNotification:
		return params.ThreadID
	case *protocol.ItemCompletedNotification:
		return params.ThreadID
	case *protocol.AgentMessageDeltaNotification:
		return params.ThreadID
	case *protocol.ReasoningTextDeltaNotification:
		return params.ThreadID
	case *protocol.ReasoningSummaryTextDeltaNotification:
		return params.ThreadID
	case *protocol.ReasoningSummaryPartAddedNotification:
		return params.ThreadID
	case *protocol.CommandExecutionOutputDeltaNotification:
		return params.ThreadID
	case *protocol.FileChangeOutputDeltaNotification:
		return params.ThreadID
	case *protocol.ErrorNotification:
		return params.ThreadID
	case *protocol.ServerRequestResolvedNotification:
		return params.ThreadID
	}
	return ""
}
