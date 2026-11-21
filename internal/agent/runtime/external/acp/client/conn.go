package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/version"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// sessionOpenTimeout bounds session/new and the session/resume or
// session/load that restores a stored session. Conn.opening is held until
// session/new answers, so an agent that never did would keep every other
// conversation of the process from opening a session; a restore that never
// answers would hold its own turn for the turn's whole deadline.
const sessionOpenTimeout = 60 * time.Second

// Conn is an initialized ACP connection that hosts any number of sessions on
// one agent process. The caller owns the process: closing a Session leaves the
// connection and its other sessions running.
//
// Updates are applied on the one loop all its sessions share, in the order
// the agent sent them, so a request returns with the updates sent before its
// response applied. The sink a prompt is given is called on that loop and
// must not wait on its consumer: it would hold every other session back.
//
// A Conn connected with ClientCapabilities serves the agent's file and
// terminal calls from the bot's workspace; one connected without them leaves
// the agent its own file and shell tools, so what it wants approved arrives
// as a session/request_permission.
type Conn struct {
	rpc  *clientConnection
	caps acp.AgentCapabilities
	meta map[string]any

	// opening admits one new session at a time. Until session/new returns,
	// the session's notifications carry an ID nothing has registered, and
	// only a single opening session can claim them unambiguously. A resumed
	// session knows its ID up front and does not wait. It is a channel so a
	// waiter can give up with its context.
	opening chan struct{}
}

// SessionConfig describes one session on a Conn.
type SessionConfig struct {
	// WorkspaceRoot bounds the paths a permission request may name.
	WorkspaceRoot string
	ProjectPath   string
	McpServers    []acp.McpServer
	// Meta travels as _meta on session/new and session/resume. ACP reserves
	// it for agent extensions, so its keys belong to the caller.
	Meta                 map[string]any
	ToolSession          ToolSessionContext
	ToolApproval         ToolApprovalService
	ToolPreflightGateway *mcp.ToolGatewayService
	// MCPToolCall decodes the MCP server and tool a permission request is
	// for, when the agent encodes them its own way. A call it attributes to
	// the Memoh tool gateway is allowed without a second prompt: the gateway
	// applies Memoh's own policy to it.
	MCPToolCall func(rawInput any) (serverName, toolName string, ok bool)
	// RuntimeOwnsApproval marks an agent whose own permission mode decides
	// what to ask. Its requests go to the user; Memoh policy never answers
	// them on the user's behalf.
	RuntimeOwnsApproval bool
	// UserInput delivers the agent's questions to the user, both ACP
	// elicitation and UserQuestions.
	UserInput UserInputService
	// UserQuestions answers an agent that asks the user through its own
	// request method instead of ACP elicitation.
	UserQuestions *UserQuestionCodec

	// Workspace serves the agent's file and terminal calls when the Conn
	// advertised them; ToolEnv and UnsetEnv shape the terminals' environment.
	Workspace *bridge.Client
	ToolEnv   []string
	UnsetEnv  []string
	// Stderr returns the agent's recent stderr, added to a failed prompt.
	Stderr func() string
	// SessionGone reports whether an error the agent answered a restore with
	// means it no longer has the session. ACP names no error for that, so by
	// default any answer but auth_required does; an agent that says so
	// precisely narrows it, and its other answers keep the stored session.
	SessionGone func(error) bool
}

// ClientCapabilities are what a Conn offers the agent beyond ACP's core.
type ClientCapabilities struct {
	// Workspace serves file reads and writes and terminals from the bot's
	// workspace (SessionConfig.Workspace).
	Workspace bool
	// Elicitation puts the agent's form questions to the user.
	Elicitation bool
}

// Connect performs the ACP handshake over a running agent's stdio,
// advertising no client capability.
func Connect(ctx context.Context, transport io.ReadWriter, logger *slog.Logger) (*Conn, error) {
	return ConnectWith(ctx, transport, logger, ClientCapabilities{})
}

// ConnectWith performs the ACP handshake advertising caps.
func ConnectWith(ctx context.Context, transport io.ReadWriter, logger *slog.Logger, caps ClientCapabilities) (*Conn, error) {
	if logger == nil {
		logger = slog.Default()
	}
	rpc := newSharedClientConnection(logger, transport, transport)
	var clientCaps acp.ClientCapabilities
	if caps.Workspace {
		clientCaps.Fs = acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true}
		clientCaps.Terminal = true
	}
	if caps.Elicitation {
		clientCaps.Elicitation = &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}
	}
	resp, err := rpc.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientInfo:         &acp.Implementation{Name: "memoh", Version: version.Version},
		ClientCapabilities: clientCaps,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize ACP agent: %w", err)
	}
	if resp.ProtocolVersion != acp.ProtocolVersionNumber {
		return nil, errs.NewDependency(fmt.Sprintf(
			"initialize ACP agent: unsupported protocol version %d (client supports %d)",
			resp.ProtocolVersion,
			acp.ProtocolVersionNumber,
		))
	}
	return &Conn{rpc: rpc, caps: resp.AgentCapabilities, meta: resp.Meta, opening: make(chan struct{}, 1)}, nil
}

// AgentMeta is the _meta of the agent's initialize response. ACP leaves its
// keys to the agent, so reading them belongs to the caller.
func (c *Conn) AgentMeta() map[string]any { return c.meta }

// Authenticate runs the agent's own sign-in with one of the methods it
// advertised and returns the response's _meta. It returns only once the
// sign-in has ended: an agent that needs the user waits for them here, and
// the caller learns how to reach them through the agent's extension methods.
func (c *Conn) Authenticate(ctx context.Context, methodID string) (map[string]any, error) {
	resp, err := acp.SendRequest[acp.AuthenticateResponse](c.rpc.conn, ctx, acp.AgentMethodAuthenticate, acp.AuthenticateRequest{MethodId: methodID})
	return resp.Meta, peerError(ctx, err)
}

// Subscribe delivers the agent's notifications of one of its own methods to
// fn. It is called on the loop every session shares, so it must not block.
// The method and its params belong to the caller.
func (c *Conn) Subscribe(method string, fn func(params json.RawMessage)) { c.rpc.subscribe(method, fn) }

// Call sends one of the agent's extension requests and decodes its result
// into out, which may be nil. The method and its shapes belong to the caller.
func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	raw, err := acp.SendRequest[json.RawMessage](c.rpc.conn, ctx, method, params)
	if err != nil {
		return peerError(ctx, err)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Done closes when the connection can carry no more requests. The process
// may still be running: the connection also ends when the agent breaks the
// protocol.
func (c *Conn) Done() <-chan struct{} { return c.rpc.conn.Done() }

// SupportsHTTPMCP reports whether sessions may carry HTTP MCP servers.
func (c *Conn) SupportsHTTPMCP() bool { return c.caps.McpCapabilities.Http }

func (c *Conn) NewSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	return c.open(ctx, "", "", cfg)
}

// OpenSession restores the stored session when there is one and creates a
// new one when there is none or the agent cannot restore it; replaced
// reports the latter. A restore that timed out or broke the connection is
// ErrSessionRestoreFailed and keeps the stored ID: the session may be intact.
func (c *Conn) OpenSession(ctx context.Context, storedID string, cfg SessionConfig) (sess *Session, replaced bool, err error) {
	if storedID = strings.TrimSpace(storedID); storedID != "" {
		sess, err = c.RestoreSession(ctx, storedID, cfg)
		if !errors.Is(err, ErrNativeSessionGone) {
			return sess, false, err
		}
		c.rpc.logger.WarnContext(ctx, "ACP agent cannot restore its session; starting a new one",
			slog.String("native_session_id", storedID), slog.Any("error", err))
		replaced = true
	}
	sess, err = c.NewSession(ctx, cfg)
	return sess, replaced, err
}

// RestoreSession reattaches a stored session, preferring session/resume,
// which does not replay the conversation, and falling back to session/load.
// It returns ErrNativeSessionGone only when the agent offers neither or said
// the session is gone (SessionConfig.SessionGone).
func (c *Conn) RestoreSession(ctx context.Context, storedID string, cfg SessionConfig) (*Session, error) {
	id := acp.SessionId(strings.TrimSpace(storedID))
	if id == "" {
		return nil, errs.New("ACP session id is required")
	}
	gone := cfg.SessionGone
	if gone == nil {
		gone = func(error) bool { return true }
	}
	try := func(method string) (*Session, error) {
		openCtx, cancel := context.WithTimeout(ctx, sessionOpenTimeout)
		defer cancel()
		sess, err := c.open(openCtx, method, id, cfg)
		if err == nil {
			return sess, nil
		}
		switch answer, answered := agentAnswer(openCtx, c.rpc, err); {
		case ctx.Err() != nil, answered && answer.Code == authRequiredCode:
			// A canceled turn says nothing about the session, and a new
			// session needs the credential too.
			return nil, fmt.Errorf("restore ACP session: %w", err)
		case answered && gone(err):
			return nil, fmt.Errorf("%w: %s: %w", ErrNativeSessionGone, method, err)
		case answered:
			return nil, fmt.Errorf("restore ACP session: %w", err)
		case openCtx.Err() != nil:
			err = errs.NewDependency("agent did not answer " + method + " in time")
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrSessionRestoreFailed, method, err)
	}
	err := fmt.Errorf("%w: agent offers neither session/resume nor session/load", ErrNativeSessionGone)
	if c.caps.SessionCapabilities.Resume != nil {
		var sess *Session
		if sess, err = try(acp.AgentMethodSessionResume); !errors.Is(err, ErrNativeSessionGone) {
			return sess, err
		}
	}
	if c.caps.LoadSession {
		return try(acp.AgentMethodSessionLoad)
	}
	return nil, err
}

func (c *Conn) open(ctx context.Context, method string, resumeID acp.SessionId, cfg SessionConfig) (*Session, error) {
	if resumeID == "" {
		select {
		case c.opening <- struct{}{}:
			defer func() { <-c.opening }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	lifecycleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	events := &toolEventEmitter{}
	callbacks := &clientCallbacks{
		logger:      c.rpc.logger,
		root:        cfg.WorkspaceRoot,
		approval:    cfg.ToolApproval,
		toolGateway: cfg.ToolPreflightGateway,
		baseSession: cfg.ToolSession,
		events:      events,
		mcpToolCall: cfg.MCPToolCall,

		runtimeOwnsApproval: cfg.RuntimeOwnsApproval,
		userInput:           cfg.UserInput,
		userQuestions:       cfg.UserQuestions,
		client:              cfg.Workspace,
	}
	callbacks.toolMapper = newACPToolEventMapper()
	if cfg.Workspace != nil {
		callbacks.terminals = newTerminalManager(lifecycleCtx, cfg.Workspace, cfg.WorkspaceRoot, cfg.ProjectPath, defaultTerminalTimeout, cfg.ToolEnv, cfg.UnsetEnv, events)
	}
	mcpServers := cfg.McpServers
	if mcpServers == nil {
		mcpServers = []acp.McpServer{}
	}

	var (
		resp   sessionResponse
		err    error
		action = "create"
	)
	if resumeID != "" {
		action = "restore"
		c.rpc.register(resumeID, callbacks)
		// Load replays the earlier conversation as session/update, and some
		// agents replay on resume too; Memoh already holds that history.
		callbacks.setRestoring(true)
		openCtx, cancelOpen := context.WithTimeout(ctx, sessionOpenTimeout)
		if method == acp.AgentMethodSessionLoad {
			resp, err = c.rpc.LoadSession(openCtx, acp.LoadSessionRequest{
				SessionId:  resumeID,
				Cwd:        cfg.ProjectPath,
				McpServers: mcpServers,
				Meta:       cfg.Meta,
			})
		} else {
			resp, err = c.rpc.ResumeSession(openCtx, acp.ResumeSessionRequest{
				SessionId:  resumeID,
				Cwd:        cfg.ProjectPath,
				McpServers: mcpServers,
				Meta:       cfg.Meta,
			})
		}
		if err != nil && ctx.Err() == nil && openCtx.Err() != nil {
			err = errs.NewDependency("agent did not answer " + method + " in time")
		}
		cancelOpen()
		callbacks.setRestoring(false)
		resp.SessionId = resumeID
	} else {
		c.rpc.setOpening(true)
		openCtx, cancelOpen := context.WithTimeout(ctx, sessionOpenTimeout)
		resp, err = c.rpc.NewSession(openCtx, acp.NewSessionRequest{
			Cwd:        cfg.ProjectPath,
			McpServers: mcpServers,
			Meta:       cfg.Meta,
		})
		if err != nil && ctx.Err() == nil && openCtx.Err() != nil {
			err = errs.NewDependency("agent did not answer session/new in time")
		}
		cancelOpen()
		if err == nil && strings.TrimSpace(string(resp.SessionId)) == "" {
			err = errs.NewDependency("agent returned an empty session id")
		}
		if err == nil {
			c.rpc.opened(lifecycleCtx, resp.SessionId, callbacks)
		} else {
			c.rpc.setOpening(false)
		}
	}
	if err != nil {
		c.rpc.unregister(resp.SessionId, callbacks)
		callbacks.close()
		cancel()
		return nil, fmt.Errorf("%s ACP session: %w", action, err)
	}

	rpc := c.rpc
	sessionID := resp.SessionId
	session := &Session{
		callbacks:             callbacks,
		conn:                  rpc,
		sessionID:             sessionID,
		projectPath:           cfg.ProjectPath,
		embeddedContext:       c.caps.PromptCapabilities.EmbeddedContext,
		imagePromptSupported:  c.caps.PromptCapabilities.Image,
		closeSessionSupported: c.caps.SessionCapabilities.Close != nil,
		lifecycleCtx:          lifecycleCtx,
		cancel:                cancel,
		release:               func() { rpc.unregister(sessionID, callbacks) },
		commandsAdvertised:    make(chan struct{}),
		stderr:                cfg.Stderr,
	}
	session.replaceConfigOptions(sessionID, resp.ConfigOptions)
	session.installModes(resp.Modes)
	callbacks.setSession(session)
	return session, nil
}
