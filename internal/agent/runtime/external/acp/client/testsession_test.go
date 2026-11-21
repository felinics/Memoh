package client

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// Runner starts agent processes in a test workspace for the protocol tests.
type Runner struct {
	logger    *slog.Logger
	workspace interface {
		bridge.Provider
		bridge.WorkspaceInfoProvider
	}
}

func NewRunner(_ *slog.Logger, workspace interface {
	bridge.Provider
	bridge.WorkspaceInfoProvider
},
) *Runner {
	return &Runner{logger: slog.Default(), workspace: workspace}
}

// StartRequest describes one process and session for the protocol tests.
type StartRequest struct {
	AgentID                string
	BotID                  string
	ProjectPath            string
	Command                string
	Args                   []string
	Env                    []string
	UnsetEnv               []string
	Resolved               *ResolvedSessionContext
	NativeSessionID        string
	SessionMode            string
	DefaultReasoningEffort string
	Timeout                time.Duration
	ToolSession            ToolSessionContext
	ToolApproval           ToolApprovalService
	UserInput              UserInputService
	ToolGateway            *mcp.ToolGatewayService
	ToolPreflightGateway   *mcp.ToolGatewayService
	ToolHTTPURL            string
	ToolHTTPHandler        http.Handler
}

// StartSession assembles a process and its session from the client's parts
// the way the session pool does, so the protocol tests drive a real agent
// process. Closing the session stops the process.
func (r *Runner) StartSession(ctx context.Context, req StartRequest, sink EventSink) (*Session, error) {
	info, err := r.workspace.WorkspaceInfo(ctx, req.BotID)
	if err != nil {
		return nil, err
	}
	root, project := "", ""
	if req.Resolved != nil {
		root, project = req.Resolved.WorkspaceRoot, req.Resolved.ProjectPath
	} else if root, project, _, err = resolveWorkspacePaths(info, req.ProjectPath); err != nil {
		return nil, fmt.Errorf("invalid project_path: %w", err)
	}
	client, err := r.workspace.MCPClient(ctx, req.BotID)
	if err != nil {
		return nil, err
	}
	toolURL := strings.TrimSpace(req.ToolHTTPURL)
	var stopTools func()
	if req.ToolHTTPHandler != nil && toolURL != "" && toolURL == strings.TrimSpace(info.ToolsHTTPURL) {
		toolURL = strings.TrimRight(toolURL, "/") + "/" + uuid.NewString()
		parsed, err := url.Parse(toolURL)
		if err != nil {
			return nil, err
		}
		if stopTools, err = client.ServeReverseHTTPRoute(ctx, parsed.Path, req.ToolHTTPHandler); err != nil {
			if sink != nil {
				sink.EmitStreamEvent(event.StreamEvent{Type: event.RuntimeNotice, NoticeKind: event.NoticeToolsUnavailable})
			}
			toolURL, stopTools = "", nil
		}
	}
	stop := func() {
		if stopTools != nil {
			stopTools()
		}
	}
	proc, conn, err := Launch(ctx, client, req.Command, req.Args, project, ProcessOptions{
		BotID: req.BotID, AgentID: req.AgentID, Env: req.Env, UnsetEnv: req.UnsetEnv, Logger: r.logger,
	}, ClientCapabilities{Workspace: true, Elicitation: req.UserInput != nil})
	if err != nil {
		stop()
		return nil, err
	}
	toolSession := req.ToolSession
	if toolSession.BotID == "" {
		toolSession.BotID = req.BotID
	}
	if toolSession.ChatID == "" {
		toolSession.ChatID = toolSession.BotID
	}
	preflight := req.ToolPreflightGateway
	if preflight == nil {
		preflight = req.ToolGateway
	}
	cfg := SessionConfig{
		WorkspaceRoot: root, ProjectPath: project, ToolSession: toolSession,
		ToolApproval: req.ToolApproval, ToolPreflightGateway: preflight, UserInput: req.UserInput,
		Workspace: client, ToolEnv: proc.ToolEnv, UnsetEnv: proc.UnsetEnv,
		Stderr: proc.StderrTail,
	}
	if conn.SupportsHTTPMCP() {
		if server := MemohToolsHTTPMCPServer(toolURL, toolSession); server.Http != nil {
			cfg.McpServers = []acp.McpServer{server}
		}
	}
	sess, _, err := conn.OpenSession(ctx, req.NativeSessionID, cfg)
	if err != nil {
		_ = proc.Close()
		stop()
		return nil, err
	}
	release := sess.release
	sess.release = func() {
		release()
		_ = proc.Close()
		stop()
	}
	sess.defaultSink = sink
	if mode := strings.TrimSpace(req.SessionMode); mode != "" && sess.ModeState().CurrentModeID != mode {
		if _, err := sess.SetMode(ctx, mode); err != nil {
			_ = sess.Close() //nolint:contextcheck // test assembly mirrors lifecycle close.
			return nil, err
		}
	}
	if effort := strings.TrimSpace(req.DefaultReasoningEffort); effort != "" && sess.ReasoningState().Supported {
		if _, err := sess.SetReasoningEffort(ctx, effort); err != nil {
			_ = sess.Close() //nolint:contextcheck // test assembly mirrors lifecycle close.
			return nil, fmt.Errorf("apply default ACP reasoning effort %q: %w", effort, err)
		}
	}
	return sess, nil
}
