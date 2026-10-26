package grok

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
)

// What the driver reads and drives through Grok's own extension methods; the
// methods and their shapes are listed in config.go.

// gatewayStatusTimeout bounds how long a turn waits for Grok to say whether
// it connected to the Memoh tool gateway. Grok reports within a moment of a
// session opening; a report that never comes does not hold the turn.
const gatewayStatusTimeout = 3 * time.Second

// mcpStatus is Grok's report on one MCP server of a session.
type mcpStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// subscribe takes the notifications the driver reads from the process.
func (s *server) subscribe() {
	s.conn.Subscribe(modelsUpdateMethod, s.updateModels)
	s.conn.Subscribe(mcpStatusMethod, s.updateToolStatus)
}

func (s *server) updateModels(params json.RawMessage) {
	var state map[string]any
	if json.Unmarshal(params, &state) != nil || state["availableModels"] == nil {
		return
	}
	s.mu.Lock()
	s.models = state
	s.mu.Unlock()
}

// modelState is the catalog as Grok last reported it: pushed since the
// process started, else read from Grok, else the snapshot initialize
// carried. The snapshot alone goes stale: Grok learns of new models after
// it has answered initialize.
func (s *server) modelState(ctx context.Context) map[string]any {
	s.mu.Lock()
	state := s.models
	s.mu.Unlock()
	if state != nil {
		return state
	}
	var answer struct {
		Result map[string]any `json:"result"`
	}
	if err := s.conn.Call(ctx, modelsMethod, map[string]any{}, &answer); err == nil && answer.Result["availableModels"] != nil {
		// Kept like a pushed update, so one read serves until Grok pushes.
		s.mu.Lock()
		if s.models == nil {
			s.models = answer.Result
		}
		state = s.models
		s.mu.Unlock()
		return state
	}
	state, _ = s.conn.AgentMeta()["modelState"].(map[string]any)
	return state
}

// forgetToolStatus drops Grok's report on a session that is opened again or
// let go: the next open is reported afresh.
func (s *server) forgetToolStatus(sessionID string) {
	s.mu.Lock()
	delete(s.toolStatus, sessionID)
	s.mu.Unlock()
}

func (s *server) updateToolStatus(params json.RawMessage) {
	var report struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		mcpStatus
	}
	if json.Unmarshal(params, &report) != nil || !acpclient.IsMemohToolsMCPServerName(report.Name) {
		return
	}
	s.mu.Lock()
	s.toolStatus[report.SessionID] = report.mcpStatus
	close(s.statusWake)
	s.statusWake = make(chan struct{})
	s.mu.Unlock()
}

// gatewayStatus waits for Grok's report on the session's Memoh tool gateway.
func (s *server) gatewayStatus(ctx context.Context, sessionID string, timeout time.Duration) (mcpStatus, bool) {
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		status, ok := s.toolStatus[sessionID]
		wake := s.statusWake
		s.mu.Unlock()
		if ok {
			return status, true
		}
		select {
		case <-wake:
		case <-deadline:
			return mcpStatus{}, false
		case <-ctx.Done():
			return mcpStatus{}, false
		}
	}
}

// serveGateway opens the Memoh tool gateway route for a thread. A route
// that cannot be served leaves the thread without Memoh tools.
func (s *server) serveGateway(ctx context.Context, th *thread, gateway toolmount.Gateway, inputURL string) (*toolmount.Mount, bool) {
	//nolint:contextcheck // the route serves every later turn of the conversation, not only the one that opened it.
	mount, err := toolmount.Serve(s.mountCtx, s.client, s.workspaceInfo, inputURL, gateway, func() mcp.ToolSessionContext {
		return s.toolSession(th)
	})
	if err != nil {
		s.logger.WarnContext(ctx, "grok: Memoh tool gateway unavailable", slog.String("bot_id", s.botID), slog.Any("error", err))
		return nil, false
	}
	return mount, true
}

// compact runs Grok's compaction of the session; the session keeps its id.
func (s *server) compact(ctx context.Context, sessionID string) error {
	return s.conn.Call(ctx, compactMethod, map[string]any{"session_id": sessionID}, nil)
}

// fork copies the session whole into a new one and returns its id.
func (s *server) fork(ctx context.Context, sessionID, cwd string) (string, error) {
	var answer struct {
		NewSessionID string `json:"newSessionId"`
	}
	if err := s.conn.Call(ctx, forkMethod, map[string]any{"sourceSessionId": sessionID, "sourceCwd": cwd, "newCwd": cwd}, &answer); err != nil {
		return "", err
	}
	if strings.TrimSpace(answer.NewSessionID) == "" {
		return "", errs.NewDependency("grok fork returned no session id")
	}
	return answer.NewSessionID, nil
}

// sessionInfo is what Grok reports about a session's model and context use.
type sessionInfo struct {
	Model   string `json:"model"`
	Context struct {
		Used  int `json:"used"`
		Total int `json:"total"`
	} `json:"context"`
}

func (s *server) sessionInfo(ctx context.Context, sessionID string) (sessionInfo, error) {
	var answer struct {
		Result sessionInfo `json:"result"`
	}
	err := s.conn.Call(ctx, sessionInfoMethod, map[string]any{"sessionId": sessionID}, &answer)
	return answer.Result, err
}
