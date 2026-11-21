package grok

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/errs"
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

func (p *grokProcess) updateModels(params json.RawMessage) {
	var state map[string]any
	if json.Unmarshal(params, &state) != nil || state["availableModels"] == nil {
		return
	}
	p.mu.Lock()
	p.models = state
	p.mu.Unlock()
}

// modelState is the catalog as Grok last reported it: pushed since the
// process started, else read from Grok, else the snapshot initialize
// carried. The snapshot alone goes stale: Grok learns of new models after
// it has answered initialize.
func (p *grokProcess) modelState(ctx context.Context) map[string]any {
	p.mu.Lock()
	state := p.models
	p.mu.Unlock()
	if state != nil {
		return state
	}
	var answer struct {
		Result map[string]any `json:"result"`
	}
	if err := p.conn.Call(ctx, modelsMethod, map[string]any{}, &answer); err == nil && answer.Result["availableModels"] != nil {
		// Kept like a pushed update, so one read serves until Grok pushes.
		p.mu.Lock()
		if p.models == nil {
			p.models = answer.Result
		}
		state = p.models
		p.mu.Unlock()
		return state
	}
	state, _ = p.conn.AgentMeta()["modelState"].(map[string]any)
	return state
}

// forgetToolStatus drops Grok's report on a session that is opened again:
// the next open is reported afresh.
func (p *grokProcess) forgetToolStatus(sessionID string) {
	p.mu.Lock()
	delete(p.toolStatus, sessionID)
	p.mu.Unlock()
}

func (p *grokProcess) updateToolStatus(params json.RawMessage) {
	var report struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		mcpStatus
	}
	if json.Unmarshal(params, &report) != nil || !acpclient.IsMemohToolsMCPServerName(report.Name) {
		return
	}
	p.mu.Lock()
	p.toolStatus[report.SessionID] = report.mcpStatus
	close(p.statusWake)
	p.statusWake = make(chan struct{})
	p.mu.Unlock()
}

// gatewayStatus waits for Grok's report on the session's Memoh tool gateway.
func (p *grokProcess) gatewayStatus(ctx context.Context, sessionID string, timeout time.Duration) (mcpStatus, bool) {
	deadline := time.After(timeout)
	for {
		p.mu.Lock()
		status, ok := p.toolStatus[sessionID]
		wake := p.statusWake
		p.mu.Unlock()
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

// compact runs Grok's compaction of the session; the session keeps its id.
func (p *grokProcess) compact(ctx context.Context, sessionID string) error {
	return p.conn.Call(ctx, compactMethod, map[string]any{"session_id": sessionID}, nil)
}

// fork copies the session whole into a new one and returns its id.
func (p *grokProcess) fork(ctx context.Context, sessionID, cwd string) (string, error) {
	var answer struct {
		NewSessionID string `json:"newSessionId"`
	}
	if err := p.conn.Call(ctx, forkMethod, map[string]any{"sourceSessionId": sessionID, "sourceCwd": cwd, "newCwd": cwd}, &answer); err != nil {
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

func (p *grokProcess) sessionInfo(ctx context.Context, sessionID string) (sessionInfo, error) {
	var answer struct {
		Result sessionInfo `json:"result"`
	}
	err := p.conn.Call(ctx, sessionInfoMethod, map[string]any{"sessionId": sessionID}, &answer)
	return answer.Result, err
}
