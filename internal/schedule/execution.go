package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimekind"
	"github.com/felinics/memoh/internal/workdir"
)

var (
	ErrTargetSessionRequired = invalidRequest("target_session_id is required for existing_session run target")
	ErrTargetSessionNotFound = invalidRequest("target session not found")
	// ErrTargetSessionGone marks a fire whose stored target session no
	// longer exists; the trigger path reports it and disables the schedule.
	ErrTargetSessionGone = errors.New("target session was deleted")
	// ErrModelRequired marks a schedule that would open a fresh session with
	// no model to run it: the bot has no default and none was given.
	ErrModelRequired = invalidRequest("this bot has no default model, so a scheduled run needs an explicit model")
)

// InvalidRequestError marks user-correctable validation failures so the API
// layer can answer 400 instead of 500.
type InvalidRequestError struct{ msg string }

func (e InvalidRequestError) Error() string { return e.msg }

func invalidRequest(msg string) error { return InvalidRequestError{msg: msg} }

func invalidRequestf(format string, args ...any) error {
	return InvalidRequestError{msg: fmt.Sprintf(format, args...)}
}

// normalizeExecution trims, defaults, and validates an execution parameter
// block against the bot's current state. It returns the canonical form that
// is persisted. DB CHECK constraints enforce the shape rules a second time;
// the cross-entity rules (session ownership and runtime, model existence,
// workdir liveness) can only live here where the referenced rows are visible.
func (s *Service) normalizeExecution(ctx context.Context, botID string, exec ExecutionConfig) (ExecutionConfig, error) {
	out := ExecutionConfig{
		RunTarget:       strings.TrimSpace(exec.RunTarget),
		TargetSessionID: strings.TrimSpace(exec.TargetSessionID),
		RuntimeType:     strings.TrimSpace(exec.RuntimeType),
		BotAgentID:      strings.TrimSpace(exec.BotAgentID),
		ACPAgentID:      strings.TrimSpace(exec.ACPAgentID),
		ModelID:         strings.TrimSpace(exec.ModelID),
		ACPModelID:      strings.TrimSpace(exec.ACPModelID),
		ReasoningEffort: strings.TrimSpace(exec.ReasoningEffort),
		WorkdirID:       strings.TrimSpace(exec.WorkdirID),
	}
	if out.RunTarget == "" {
		out.RunTarget = RunTargetNewSession
	}
	if out.ModelID != "" && out.ACPModelID != "" {
		return ExecutionConfig{}, invalidRequest("model_id and acp_model_id are mutually exclusive")
	}

	targetIsAgent := false
	switch out.RunTarget {
	case RunTargetExistingSession:
		if out.RuntimeType != "" || out.BotAgentID != "" || out.ACPAgentID != "" || out.WorkdirID != "" {
			return ExecutionConfig{}, invalidRequest("existing_session inherits runtime and workdir from the target session; runtime_type, bot_agent_id, acp_agent_id, and workdir_id must be empty")
		}
		agentTarget, err := s.validateTargetSession(ctx, botID, out.TargetSessionID)
		if err != nil {
			return ExecutionConfig{}, err
		}
		targetIsAgent = agentTarget
		if targetIsAgent && out.ModelID != "" {
			return ExecutionConfig{}, invalidRequest("target session runs an external agent; use acp_model_id instead of model_id")
		}
		if !targetIsAgent && out.ACPModelID != "" {
			return ExecutionConfig{}, invalidRequest("target session runs the native model runtime; use model_id instead of acp_model_id")
		}
	case RunTargetNewSession:
		if out.TargetSessionID != "" {
			return ExecutionConfig{}, invalidRequest("target_session_id is only valid with the existing_session run target")
		}
		if out.BotAgentID != "" {
			// The bot agent decides the actual runtime below; an explicit
			// runtime_type may only name an agent runtime and is checked
			// against the resolved one after resolution.
			if out.RuntimeType != "" && !runtimekind.IsExternal(out.RuntimeType) {
				return ExecutionConfig{}, invalidRequest("bot_agent_id conflicts with the selected runtime_type")
			}
			resolved, err := s.resolveBotAgentExecution(ctx, botID, out)
			if err != nil {
				return ExecutionConfig{}, err
			}
			out = resolved
		}
		switch out.RuntimeType {
		case "", RuntimeModel:
			if out.ACPAgentID != "" || out.ACPModelID != "" {
				return ExecutionConfig{}, invalidRequest("acp_agent_id and acp_model_id require runtime_type acp_agent")
			}
		case RuntimeACPAgent:
			if out.ACPAgentID == "" {
				return ExecutionConfig{}, invalidRequest("acp_agent_id is required for runtime_type acp_agent")
			}
			if out.ModelID != "" {
				return ExecutionConfig{}, invalidRequest("ACP schedules use acp_model_id; model_id is only valid for the native model runtime")
			}
		case botagents.RuntimeCodex, botagents.RuntimeClaudeCode:
			if out.BotAgentID == "" {
				return ExecutionConfig{}, invalidRequestf("runtime_type %q requires bot_agent_id", out.RuntimeType)
			}
			if out.ACPAgentID != "" || out.ModelID != "" {
				return ExecutionConfig{}, invalidRequest("direct external agent schedules carry no acp_agent_id or model_id")
			}
		default:
			return ExecutionConfig{}, invalidRequestf("unknown runtime_type %q", out.RuntimeType)
		}
		if err := s.validateWorkdirBinding(ctx, botID, out.WorkdirID, out.RuntimeType); err != nil {
			return ExecutionConfig{}, err
		}
	default:
		return ExecutionConfig{}, invalidRequestf("unknown run_target %q", out.RunTarget)
	}

	if out.ModelID != "" {
		if err := s.validateNativeModel(ctx, out.ModelID); err != nil {
			return ExecutionConfig{}, err
		}
	}
	if err := s.requireResolvableModel(ctx, botID, out, targetIsAgent); err != nil {
		return ExecutionConfig{}, err
	}
	// Native efforts come from a fixed tier vocabulary, plus the "disable"
	// sentinel that turns reasoning off for the run — the same on/off value
	// bot settings and the chat composer store. Agent runtime efforts (ACP
	// and the direct external agents) are agent-defined — each reports its
	// own set at runtime — so only a shape check applies here; the agent
	// rejects unknown values at run time with a stable error.
	agentRun := runtimekind.ResolvesOwnModel(out.RuntimeType) || targetIsAgent
	if out.ReasoningEffort != "" && !agentRun &&
		!models.IsValidReasoningEffort(out.ReasoningEffort) &&
		!models.IsReasoningDisabled(out.ReasoningEffort) {
		return ExecutionConfig{}, invalidRequestf("unknown reasoning_effort %q", out.ReasoningEffort)
	}
	return out, nil
}

func (s *Service) resolveBotAgentExecution(ctx context.Context, botID string, exec ExecutionConfig) (ExecutionConfig, error) {
	if s.botAgents == nil {
		return ExecutionConfig{}, errors.New("bot agent service is not configured")
	}
	agent, err := s.botAgents.GetActive(ctx, botID, exec.BotAgentID)
	if err != nil {
		return ExecutionConfig{}, err
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return ExecutionConfig{}, invalidRequestf("invalid bot id: %v", err)
	}
	bot, err := s.queries.GetBotByID(ctx, botUUID)
	if err != nil {
		return ExecutionConfig{}, err
	}
	metadata := map[string]any{}
	if len(bot.Metadata) > 0 {
		if err := json.Unmarshal(bot.Metadata, &metadata); err != nil {
			return ExecutionConfig{}, err
		}
	}
	if err := s.botAgents.ValidateConfiguration(ctx, agent, metadata); err != nil {
		return ExecutionConfig{}, err
	}
	descriptor, err := botagents.DescriptorFor(agent)
	if err != nil {
		return ExecutionConfig{}, err
	}
	declared := strings.TrimSpace(exec.RuntimeType)
	exec.BotAgentID = agent.ID
	switch descriptor.Runtime {
	case botagents.RuntimeACP:
		exec.RuntimeType = RuntimeACPAgent
		exec.ACPAgentID = descriptor.Provider
	case botagents.RuntimeCodex, botagents.RuntimeClaudeCode:
		// Direct external agents carry the runtime type on the schedule row
		// itself; the fire dispatches through the external driver with no
		// acp_agent_id and no native model.
		exec.RuntimeType = descriptor.Runtime
	default:
		return ExecutionConfig{}, botagents.ErrInvalidRuntime
	}
	if declared != "" && declared != exec.RuntimeType {
		return ExecutionConfig{}, invalidRequest("bot_agent_id conflicts with the selected runtime_type")
	}
	return exec, nil
}

// requireResolvableModel rejects a schedule whose fires could not name a
// model at all.
//
// Model selection walks request → bot default → the session's last round. A
// fire that opens a FRESH session has no last round, so with neither an
// explicit model nor a bot default there is nothing left to fall back on and
// every fire dies with "chat model not configured". That is a save-time
// mistake surfacing as a silent runtime failure hours later, so it is caught
// here instead — for the tools as much as for the web form.
//
// Only the native runtime is checked: an ACP agent brings its own model, and
// an existing session can still fall back to the model that produced its
// latest round.
func (s *Service) requireResolvableModel(ctx context.Context, botID string, exec ExecutionConfig, targetIsAgent bool) error {
	if exec.RunTarget != RunTargetNewSession ||
		runtimekind.ResolvesOwnModel(exec.RuntimeType) ||
		targetIsAgent {
		// Agent runtimes resolve their model inside the agent; only the
		// native model runtime needs a resolvable Memoh model row.
		return nil
	}
	if exec.ModelID != "" {
		return nil
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return invalidRequestf("invalid bot id: %v", err)
	}
	row, err := s.queries.GetSettingsByBotID(ctx, botUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrModelRequired
		}
		return err
	}
	if !row.ChatModelID.Valid {
		return ErrModelRequired
	}
	return nil
}

// validateTargetSession checks that the target session exists on this bot
// and is a kind a schedule can append to, and reports whether it runs on an
// agent runtime (ACP or a direct external agent).
func (s *Service) validateTargetSession(ctx context.Context, botID, sessionID string) (isAgent bool, err error) {
	if sessionID == "" {
		return false, ErrTargetSessionRequired
	}
	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return false, invalidRequestf("invalid target_session_id: %v", err)
	}
	sess, err := s.queries.GetSessionByID(ctx, pgSessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrTargetSessionNotFound
		}
		return false, err
	}
	if sess.BotID.String() != botID {
		return false, ErrTargetSessionNotFound
	}
	// Subagent and discuss sessions back internal loops with
	// their own drivers; appending schedule turns to them would corrupt
	// those flows. Chat sessions and schedule-created sessions are fair
	// targets.
	switch strings.TrimSpace(sess.SessionMode) {
	case "chat", "schedule":
	default:
		return false, invalidRequestf("target session has mode %q; only chat or schedule sessions can host scheduled runs", sess.SessionMode)
	}
	return isAgentRuntimeSessionRow(sess.RuntimeType, sess.Type), nil
}

// isAgentRuntimeSessionRow mirrors the chat/thread runtime resolution for
// stored rows: an explicit runtime_type wins, with the legacy acp_agent type
// as fallback for rows that predate the split descriptor.
func isAgentRuntimeSessionRow(runtimeType, legacyType string) bool {
	if strings.TrimSpace(runtimeType) == "" {
		return strings.TrimSpace(legacyType) == RuntimeACPAgent
	}
	return runtimekind.IsExternal(runtimeType)
}

func (s *Service) validateNativeModel(ctx context.Context, modelID string) error {
	pgModelID, err := db.ParseUUID(modelID)
	if err != nil {
		return invalidRequestf("invalid model_id: %v", err)
	}
	model, err := s.queries.GetModelByID(ctx, pgModelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidRequest("model not found")
		}
		return err
	}
	if model.Type != "chat" {
		return invalidRequestf("model %s is a %s model; schedules need a chat model", modelID, model.Type)
	}
	if !model.Enable {
		return invalidRequestf("model %s is disabled", modelID)
	}
	return nil
}

func (s *Service) validateWorkdirBinding(ctx context.Context, botID, workdirID, runtimeType string) error {
	if workdirID == "" {
		return nil
	}
	if s.workdirs == nil {
		return errors.New("workdir validation is not configured")
	}
	wd, err := s.workdirs.RequireActive(ctx, botID, workdirID)
	if err != nil {
		return err
	}
	// External Agents run inside the native workspace and cannot reach a
	// remote runtime's filesystem — same policy as interactive sessions.
	if runtimekind.IsExternal(runtimeType) && wd.TargetKind == workdir.TargetKindRemote {
		return invalidRequest("external Agent schedules cannot bind a remote workdir")
	}
	return nil
}
