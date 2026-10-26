package grok

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// handshakeTimeout bounds the ACP initialize exchange on a fresh process.
const handshakeTimeout = 60 * time.Second

// server is one `grok agent stdio` process and the conversations it hosts.
type server struct {
	botID         string
	proc          *agentprocess.Process
	conn          *acpclient.Conn
	client        *bridge.Client
	workspaceInfo bridge.WorkspaceInfo
	launcher      external.Launcher
	// account marks a process signed in with a Grok account, which reads and
	// refreshes the login file in the Agent's home.
	account bool
	logger  *slog.Logger
	// mountCtx bounds thread tool-gateway mounts to the process's lifetime.
	mountCtx    context.Context
	mountCancel context.CancelFunc

	closeOnce sync.Once
	closeErr  error
	// done closes when teardown begins, so the table stops handing the
	// server out before the slower steps of Close have run.
	done chan struct{}

	mu      sync.Mutex
	threads map[string]*thread
	// models is the catalog Grok last pushed; nil until it has.
	models map[string]any
	// toolStatus is Grok's latest report on the Memoh tool gateway of each
	// session; statusWake closes, and is replaced, when a report arrives.
	toolStatus map[string]mcpStatus
	statusWake chan struct{}
}

// thread is the live Grok session behind one Memoh conversation.
type thread struct {
	session *acpclient.Session
	mount   *toolmount.Mount
	// Grok takes the permission mode and working directory only when a
	// session is created or resumed, so a change to either reopens it.
	mode string
	cwd  string
	// toolless marks a session opened without the Memoh tool gateway. A
	// later turn that can mount the gateway reopens the session: Grok's
	// mcp/upsert would write the gateway of this one conversation into the
	// Agent's own config, for every session to come.
	toolless bool
	// turn is the running turn; the tool mount reads the caller's identity
	// from it on every request.
	turn atomic.Pointer[turnContext]
}

type turnContext struct {
	ctx   context.Context
	input external.PromptInput
}

// startServer launches Grok on a home already prepared for the Agent's auth
// mode: apiKey for an API key Agent, empty for a Grok account one.
func startServer(ctx context.Context, botID, home string, client *bridge.Client, info bridge.WorkspaceInfo, apiKey string, launcher external.Launcher, logger *slog.Logger) (*server, error) {
	proc, conn, err := launch(ctx, client, home, apiKey, launcher, logger)
	if err != nil {
		return nil, err
	}
	mountCtx, mountCancel := context.WithCancel(ctx)
	srv := &server{
		botID:         botID,
		proc:          proc,
		conn:          conn,
		client:        client,
		workspaceInfo: info,
		launcher:      launcher,
		account:       apiKey == "",
		logger:        logger,
		mountCtx:      mountCtx,
		mountCancel:   mountCancel,
		done:          make(chan struct{}),
		threads:       map[string]*thread{},
		toolStatus:    map[string]mcpStatus{},
		statusWake:    make(chan struct{}),
	}
	srv.subscribe()
	if version, _ := conn.AgentMeta()["agentVersion"].(string); version != pinnedVersion {
		// Grok's extensions are read as its pinned release defines them. A
		// drifted binary usually still answers them, so serve and say so.
		logger.WarnContext(ctx, "grok CLI version differs from the release the driver was checked against",
			slog.String("bot_id", botID), slog.String("running", version), slog.String("pinned", pinnedVersion))
	}
	// The connection can end while Grok keeps running: the ACP client gives
	// it up when a line is too long or its queue overflows. Every later
	// request would then fail on a server still handed out, so either end
	// takes the other down.
	go func() { //nolint:contextcheck // teardown follows the process, not the turn that started it.
		select {
		case <-proc.Done():
		case <-conn.Done():
		}
		select {
		case <-srv.done:
		default:
			logger.Warn("grok agent ended", slog.String("bot_id", botID), slog.String("stderr", proc.StderrTail()))
		}
		_ = srv.Close()
	}()
	return srv, nil
}

// launch starts one grok process on home and completes the ACP handshake.
func launch(ctx context.Context, client *bridge.Client, home, apiKey string, launcher external.Launcher, logger *slog.Logger) (*agentprocess.Process, *acpclient.Conn, error) {
	// Grok can also serve through a shared leader process that outlives its
	// stdio client; sessions and the login file must die with this process.
	proc, err := agentprocess.Start(ctx, client, shellQuote(launcher.Path)+" agent --no-leader stdio", defaultProjectPath, processEnv(home, apiKey))
	if err != nil {
		return nil, nil, errs.WrapDependency(err, "start grok agent")
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	conn, err := acpclient.Connect(handshakeCtx, proc, logger)
	if err != nil {
		stderr := proc.StderrTail()
		_ = proc.Close()
		if stderr != "" {
			return nil, nil, fmt.Errorf("%w: %s", err, stderr)
		}
		return nil, nil, err
	}
	return proc, conn, nil
}

// processEnv is the whole environment of a grok process. apiKey is empty for
// a Grok account login, which Grok reads from its home.
func processEnv(home, apiKey string) []string {
	env := []string{
		"PATH=" + containerPath,
		"HOME=" + defaultProjectPath,
		"GROK_HOME=" + home,
		// The managed dependency owns the CLI version.
		"GROK_DISABLE_AUTOUPDATER=1",
		// Memoh keeps the transcript: a stopped turn stays in Grok's history
		// as it stays in Memoh's.
		"GROK_CANCEL_REWIND=0",
		// Memoh shows none of these, and each costs a model call per turn.
		"GROK_TURN_SUMMARY=0",
		"GROK_SESSION_RECAP=0",
		// Memoh owns memory. Grok's would carry across every conversation of
		// the Agent, which share its home.
		"GROK_MEMORY=0",
	}
	if apiKey != "" {
		env = append(env, "XAI_API_KEY="+apiKey)
	}
	return env
}

func (s *server) hasActiveTurns() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.turn.Load() != nil {
			return true
		}
	}
	return false
}

// Close implements servertable.Resource. Sessions die with the process, so
// only the routes Memoh serves into the workspace need an explicit stop.
//
// A second caller returns only once the first has finished, which is what
// lets a starting process wait out its predecessor.
func (s *server) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mountCancel()
		s.mu.Lock()
		threads := s.threads
		s.threads = map[string]*thread{}
		s.mu.Unlock()
		for _, th := range threads {
			th.mount.Stop()
		}
		s.closeErr = s.proc.Close()
	})
	return s.closeErr
}

// Done implements servertable.Resource.
func (s *server) Done() <-chan struct{} { return s.done }

func (s *server) matchesLauncher(launcher external.Launcher) bool {
	return s.launcher.Path == launcher.Path && s.launcher.Version == launcher.Version
}

func (s *server) thread(threadID string) *thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threads[threadID]
}

// dropThread detaches a conversation's session. Grok keeps the session on
// disk, so a later turn resumes it. force skips session/close for a session
// whose cancellation never settled, where another request could block behind
// it.
func (s *server) dropThread(threadID string, th *thread, force bool) {
	s.mu.Lock()
	if s.threads[threadID] == th {
		delete(s.threads, threadID)
	}
	delete(s.toolStatus, th.session.ID())
	s.mu.Unlock()
	if force {
		_ = th.session.ForceClose()
	} else {
		_ = th.session.Close()
	}
	th.mount.Stop()
}

// openThread returns the conversation's live session, opening it when the
// process does not hold one that still fits the turn. A stored session Grok
// no longer has is replaced by a new one when replaceLost says so, and
// resumeLost reports that; otherwise it is ErrThreadUnavailable.
func (s *server) openThread(ctx context.Context, gateway toolmount.Gateway, approval acpclient.ToolApprovalService, userInput acpclient.UserInputService, input external.PromptInput, mode string, replaceLost bool) (th *thread, resumeLost bool, err error) {
	resolved, err := acpclient.ResolveSessionContext(acpclient.SessionContextInput{
		Backend:     s.workspaceInfo.Backend,
		ProjectPath: metadataString(input.RuntimeMetadata, "project_path"),
	})
	if err != nil {
		return nil, false, err
	}
	// The stored id is the authority, not the session this process happens
	// to hold: clearing a conversation's history clears the id, and the
	// session Grok still remembers it by must not be picked up again.
	storedID := strings.TrimSpace(metadataString(input.RuntimeMetadata, metadataSessionIDKey))
	if input.ForceFreshRuntime {
		// Discuss turns re-inject the full composed context every round;
		// resuming would stack it on Grok's own saved history.
		storedID = ""
	}
	baseURL := ""
	if s.conn.SupportsHTTPMCP() {
		baseURL = toolmount.ResolveBaseURL(s.workspaceInfo, input.ToolHTTPURL)
	}
	if current := s.thread(input.ThreadID); current != nil {
		if current.turn.Load() != nil {
			return nil, false, external.Fail(external.FailureRuntimeBusy, nil)
		}
		fits := storedID != "" && current.session.ID() == storedID && current.mode == mode && current.cwd == resolved.ProjectPath
		if fits && (!current.toolless || baseURL == "") {
			return current, false, nil
		}
		s.dropThread(input.ThreadID, current, false) //nolint:contextcheck // the replaced session closes on its own lifecycle.
	}

	th = &thread{mode: mode, cwd: resolved.ProjectPath}
	cfg := acpclient.SessionConfig{
		WorkspaceRoot:        resolved.WorkspaceRoot,
		ProjectPath:          resolved.ProjectPath,
		Meta:                 sessionMeta(mode),
		ToolSession:          s.toolSession(th),
		ToolApproval:         approval,
		ToolPreflightGateway: gateway.Tools,
		MCPToolCall:          mcpToolCall,
		RuntimeOwnsApproval:  true,
		UserInput:            userInput,
		UserQuestions:        userQuestions,
	}
	if baseURL != "" {
		if mount, ok := s.serveGateway(ctx, th, gateway, baseURL); ok {
			th.mount = mount
			cfg.McpServers = append(cfg.McpServers, acpclient.MemohToolsMCPServer(mount.URL))
		}
	}
	if th.mount == nil {
		th.mount = &toolmount.Mount{}
		th.toolless = true
	}

	if storedID != "" {
		s.forgetToolStatus(storedID)
		th.session, err = s.conn.ResumeSession(ctx, storedID, cfg)
		if err != nil {
			if err = resumeFailure(ctx, err, replaceLost); err != nil {
				th.mount.Stop()
				return nil, false, err
			}
			// Grok no longer has the session and will say so on every later
			// turn: its memory of this conversation is already gone.
			s.logger.WarnContext(ctx, "grok: stored session could not be resumed; starting a new one",
				slog.String("bot_id", s.botID), slog.String("session_id", input.ThreadID))
			resumeLost = true
		}
	}
	if th.session == nil {
		if th.session, err = s.conn.NewSession(ctx, cfg); err != nil {
			th.mount.Stop()
			return nil, false, err
		}
	}
	s.mu.Lock()
	s.threads[input.ThreadID] = th
	s.mu.Unlock()
	return th, resumeLost, nil
}

// toolSession is the trusted identity of a thread's tool calls and approval
// requests. Outside a turn it names only the bot, and the gateway refuses to
// run tools for it.
func (s *server) toolSession(th *thread) mcp.ToolSessionContext {
	session := mcp.ToolSessionContext{
		BotID:            s.botID,
		ChatID:           s.botID,
		SessionType:      sessionmode.Chat,
		CanListUserInput: true,
		RequireActiveRun: true,
	}
	turn := th.turn.Load()
	if turn == nil {
		return session
	}
	in := turn.input
	session.ChatID = firstNonEmpty(in.ChatID, s.botID)
	session.SessionID = strings.TrimSpace(in.ThreadID)
	session.RunID = strings.TrimSpace(in.RunID)
	session.SessionType = firstNonEmpty(in.SessionMode, sessionmode.Chat)
	session.RouteID = in.RouteID
	session.CurrentPlatform = in.CurrentPlatform
	session.ReplyTarget = in.ReplyTarget
	session.ConversationType = in.ConversationType
	session.ChannelIdentityID = in.ChannelIdentityID
	session.SessionToken = in.SessionToken
	session.CanRequestUserInput = in.CanRequestUserInput
	session.RuntimeActive = true
	session.ContextBudgetMaxTokens = in.ContextBudgetMaxTokens
	if fence, ok := runtimefence.FromContext(turn.ctx); ok {
		session.RuntimeFence = fence
	}
	session.RunContext = turn.ctx
	return session
}

// shellQuote wraps one argument for the bridge's shell command line.
func shellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
