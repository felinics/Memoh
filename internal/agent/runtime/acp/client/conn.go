package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/version"
)

// sessionOpenTimeout bounds session/new and session/resume. Conn.opening is
// held until session/new answers, so an agent that never did would keep every
// other conversation of the process from opening a session; a resume that
// never answers would hold its own turn for the turn's whole deadline.
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
// Sessions on a Conn use the agent's own file and shell tools: the client
// file and terminal capabilities are not advertised, so what the agent wants
// approved arrives as a session/request_permission.
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
}

// Connect performs the ACP handshake over a running agent's stdio.
func Connect(ctx context.Context, transport io.ReadWriter, logger *slog.Logger) (*Conn, error) {
	rpc := newSharedClientConnection(logger, transport, transport)
	resp, err := rpc.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo:      &acp.Implementation{Name: "memoh", Version: version.Version},
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
	return c.open(ctx, "", cfg)
}

// ResumeSession reattaches a session the agent persisted. Its earlier turns
// are not replayed; Memoh already holds the visible history.
func (c *Conn) ResumeSession(ctx context.Context, sessionID string, cfg SessionConfig) (*Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errs.New("ACP session id is required")
	}
	if c.caps.SessionCapabilities.Resume == nil {
		return nil, ErrSessionResumeUnsupported
	}
	return c.open(ctx, acp.SessionId(sessionID), cfg)
}

func (c *Conn) open(ctx context.Context, resumeID acp.SessionId, cfg SessionConfig) (*Session, error) {
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
	}
	// The zero quirks are the shared defaults.
	callbacks.toolMapper = newACPToolEventMapper(callbacks.quirks)
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
		action = "resume"
		c.rpc.register(resumeID, callbacks)
		openCtx, cancelOpen := context.WithTimeout(ctx, sessionOpenTimeout)
		resp, err = c.rpc.ResumeSession(openCtx, acp.ResumeSessionRequest{
			SessionId:  resumeID,
			Cwd:        cfg.ProjectPath,
			McpServers: mcpServers,
			Meta:       cfg.Meta,
		})
		if err != nil && ctx.Err() == nil && openCtx.Err() != nil {
			err = errs.NewDependency("agent did not answer session/resume in time")
		}
		cancelOpen()
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
	}
	session.replaceConfigOptions(sessionID, resp.ConfigOptions)
	session.installLegacyModels(resp.Models)
	session.installModes(resp.Modes)
	callbacks.setSession(session)
	return session, nil
}
