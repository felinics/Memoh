package schedule

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/workdir"
)

var (
	ErrTargetSessionRequired = invalidRequest("target_session_id is required for existing_session run target")
	ErrTargetSessionNotFound = invalidRequest("target session not found")
	// ErrTargetSessionGone marks a fire whose stored target session no
	// longer exists; the trigger path reports it and disables the schedule.
	ErrTargetSessionGone = errors.New("target session was deleted")
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
		RunTarget:         strings.TrimSpace(exec.RunTarget),
		TargetSessionID:   strings.TrimSpace(exec.TargetSessionID),
		RuntimeType:       strings.TrimSpace(exec.RuntimeType),
		ACPAgentID:        strings.TrimSpace(exec.ACPAgentID),
		AgentCredentialID: strings.TrimSpace(exec.AgentCredentialID),
		ModelID:           strings.TrimSpace(exec.ModelID),
		ACPModelID:        strings.TrimSpace(exec.ACPModelID),
		ReasoningEffort:   strings.TrimSpace(exec.ReasoningEffort),
		WorkdirID:         strings.TrimSpace(exec.WorkdirID),
	}
	if out.RunTarget == "" {
		out.RunTarget = RunTargetNewSession
	}
	if out.ModelID != "" && out.ACPModelID != "" {
		return ExecutionConfig{}, invalidRequest("model_id and acp_model_id are mutually exclusive")
	}

	targetIsACP := false
	switch out.RunTarget {
	case RunTargetExistingSession:
		if out.RuntimeType != "" || out.ACPAgentID != "" || out.AgentCredentialID != "" || out.WorkdirID != "" {
			return ExecutionConfig{}, invalidRequest("existing_session inherits runtime and workdir from the target session; runtime_type, acp_agent_id, and workdir_id must be empty")
		}
		acp, err := s.validateTargetSession(ctx, botID, out.TargetSessionID)
		if err != nil {
			return ExecutionConfig{}, err
		}
		targetIsACP = acp
		if targetIsACP && out.ModelID != "" {
			return ExecutionConfig{}, invalidRequest("target session runs an ACP agent; use acp_model_id instead of model_id")
		}
		if !targetIsACP && out.ACPModelID != "" {
			return ExecutionConfig{}, invalidRequest("target session runs the native model runtime; use model_id instead of acp_model_id")
		}
	case RunTargetNewSession:
		if out.TargetSessionID != "" {
			return ExecutionConfig{}, invalidRequest("target_session_id is only valid with the existing_session run target")
		}
		switch out.RuntimeType {
		case "", RuntimeModel:
			if out.ACPAgentID != "" || out.AgentCredentialID != "" || out.ACPModelID != "" {
				return ExecutionConfig{}, invalidRequest("acp_agent_id and acp_model_id require runtime_type acp_agent")
			}
		case RuntimeACPAgent:
			if out.ACPAgentID == "" {
				return ExecutionConfig{}, invalidRequest("acp_agent_id is required for runtime_type acp_agent")
			}
			if out.ModelID != "" {
				return ExecutionConfig{}, invalidRequest("ACP schedules use acp_model_id; model_id is only valid for the native model runtime")
			}
			if out.AgentCredentialID == "" {
				return ExecutionConfig{}, invalidRequest("agent_credential_id is required for managed Agent schedules")
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
	// Native efforts come from a fixed tier vocabulary, plus the "disable"
	// sentinel that turns reasoning off for the run — the same on/off value
	// bot settings and the chat composer store. ACP efforts are
	// agent-defined (each agent reports its own set at runtime), so only a
	// shape check applies here; the agent rejects unknown values at run
	// time with a stable error.
	acpRun := out.RuntimeType == RuntimeACPAgent || targetIsACP
	if out.ReasoningEffort != "" && !acpRun &&
		!models.IsValidReasoningEffort(out.ReasoningEffort) &&
		!models.IsReasoningDisabled(out.ReasoningEffort) {
		return ExecutionConfig{}, invalidRequestf("unknown reasoning_effort %q", out.ReasoningEffort)
	}
	return out, nil
}

// validateTargetSession checks that the target session exists on this bot
// and is a kind a schedule can append to, and reports whether it runs an
// ACP agent.
func (s *Service) validateTargetSession(ctx context.Context, botID, sessionID string) (isACP bool, err error) {
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
	// Heartbeat, subagent, and discuss sessions back internal loops with
	// their own drivers; appending schedule turns to them would corrupt
	// those flows. Chat sessions and schedule-created sessions are fair
	// targets.
	switch strings.TrimSpace(sess.SessionMode) {
	case "chat", "schedule":
	default:
		return false, invalidRequestf("target session has mode %q; only chat or schedule sessions can host scheduled runs", sess.SessionMode)
	}
	return isACPSessionRow(sess.RuntimeType, sess.Type), nil
}

// isACPSessionRow mirrors the chat/thread runtime resolution for stored
// rows: an explicit runtime_type wins, with the legacy acp_agent type as
// fallback for rows that predate the split descriptor.
func isACPSessionRow(runtimeType, legacyType string) bool {
	if rt := strings.TrimSpace(runtimeType); rt != "" {
		return rt == RuntimeACPAgent
	}
	return strings.TrimSpace(legacyType) == RuntimeACPAgent
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
	// The ACP runtime runs inside the native workspace and cannot reach a
	// remote runtime's filesystem — same policy as interactive session
	// creation.
	if runtimeType == RuntimeACPAgent && wd.TargetKind == workdir.TargetKindRemote {
		return invalidRequest("ACP schedules cannot bind a remote workdir")
	}
	return nil
}
