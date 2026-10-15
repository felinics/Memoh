package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/runtimekind"
)

type SubagentBackend struct {
	BotAgentID  string `json:"bot_agent_id"`
	Name        string `json:"name"`
	RuntimeType string `json:"runtime_type"`
	Available   bool   `json:"available"`
	Code        string `json:"code,omitempty"`
}

type ExternalSubagentRun struct {
	Parent           SessionContext
	SessionID        string
	BotAgentID       string
	RuntimeType      string
	Query            string
	Admission        SubagentAdmission
	RequestMessageID string
}

type ExternalSubagentService interface {
	// HeldExternalSubagents names the bot's children whose durable execution
	// record is unresolved: running, or ended without confirmed termination.
	HeldExternalSubagents(context.Context, string) ([]string, error)
	// SettleExternalSubagents confirms the termination of the bot's children
	// whose last run ended unconfirmed, where that is possible now.
	SettleExternalSubagents(context.Context, string) error
	ListSubagentBackends(context.Context, SessionContext) ([]SubagentBackend, error)
	PrepareExternalSubagent(context.Context, SessionContext, string) (sessionpkg.CreateInput, error)
	RunExternalSubagent(context.Context, ExternalSubagentRun) (*SpawnResult, error)
}

func (p *SpawnProvider) SetExternalSubagents(service ExternalSubagentService) {
	p.externalSubagents = service
}

func (p *SpawnProvider) spawnExternalSubagent(ctx context.Context, parent SessionContext, id, task string, args spawnAgentArgs) (any, error) {
	if p.externalSubagents == nil || p.sessionService == nil {
		return nil, errors.New("external subagent service unavailable")
	}
	input, err := p.externalSubagents.PrepareExternalSubagent(ctx, parent, strings.TrimSpace(args.BotAgentID))
	if err != nil {
		return nil, err
	}
	if !runtimekind.IsExternal(input.RuntimeType) || input.BotAgentID != strings.TrimSpace(args.BotAgentID) {
		return nil, errors.New("invalid external subagent binding")
	}
	input.Type = sessionpkg.TypeSubagent
	input.SessionMode = sessionpkg.TypeSubagent
	input.Title = truncateTitle(task, 100)
	input.Metadata = map[string]any{"agent_id": id, "agent_control_version": agentControlVersion}
	sess, _, err := p.sessionService.CreateSubagent(ctx, sessionpkg.CreateSubagentInput{Thread: input})
	if err != nil {
		return nil, p.sessionStoreError(ctx, "agent.subagent_create", err)
	}
	rec := agentRecord{AgentID: id, SessionID: sess.ID, RuntimeType: input.RuntimeType, BotAgentID: input.BotAgentID, Title: sess.Title, CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt}
	return p.submitAgentTask(ctx, parent, rec, sessionpkg.SubagentConfig{}, task, args.RunInBackground)
}

func (p *SpawnProvider) runExternalSubagentTask(ctx context.Context, req *agentRequest) agentRunResult {
	res := agentRunResult{AgentID: req.agentID, SessionID: req.agentSessionID, TaskID: req.taskID, RuntimeType: req.runtimeType, BotAgentID: req.botAgentID, Message: req.message}
	if err := p.runSubagentHook(ctx, hooks.EventSubagentStart, req, res); err != nil {
		return res.failed(err)
	}
	defer func() {
		if err := p.runSubagentHook(context.WithoutCancel(ctx), hooks.EventSubagentStop, req, res); err != nil {
			p.logger.WarnContext(ctx, "external subagent stop hook failed", slog.Any("error", err))
		}
	}()
	result, err := p.externalSubagents.RunExternalSubagent(ctx, ExternalSubagentRun{Parent: req.parentSession, SessionID: req.agentSessionID, RuntimeType: req.runtimeType, BotAgentID: req.botAgentID, Query: req.message, Admission: req.admission})
	if result != nil {
		res.Text = result.Text
		res.ContextLifecycle = result.ContextLifecycle
		res.Usage = result.Usage
		res.AttemptResolved = result.TerminalResolved
		res.AttemptOutcome = result.TerminalOutcome
	}
	if err != nil {
		res.fail(err)
		res.Status = string(background.TaskFailed)
	}
	if result != nil && result.TerminalResolved && result.TerminalOutcome == SpawnAttemptCompleted {
		res.Status = string(background.TaskCompleted)
	} else if (result != nil && result.TerminalResolved && result.TerminalOutcome == SpawnAttemptAbort) || (!res.AttemptResolved && ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled)) {
		res.Status = string(background.TaskKilled)
		res.Cause = context.Cause(ctx)
	}
	return res
}

func marshalSubagentUsage(usage *sdk.Usage) json.RawMessage {
	if usage == nil {
		return nil
	}
	raw, _ := json.Marshal(usage)
	return raw
}
