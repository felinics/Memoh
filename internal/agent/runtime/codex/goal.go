package codex

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

var _ external.GoalProvider = (*Driver)(nil)

// Codex permits explicit Plan turns but never automatically continues a goal
// in Plan mode. Goal activation and continuation must share this constraint.
func goalExecutionAllowed(input external.PromptInput) bool {
	return metadataString(input.RuntimeMetadata, "collaboration_mode") != "plan"
}

func (s *appServer) goalThreadID(input external.PromptInput) string {
	if id := metadataString(input.RuntimeMetadata, metadataThreadIDKey); id != "" {
		return id
	}
	// A new Codex thread is published durably when the admitted run finishes.
	// Until then its live turn supplies the identity for the goal status bar.
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, turn := range s.turns {
		if turn.input.ThreadID == input.ThreadID {
			return id
		}
	}
	return ""
}

func (d *Driver) Goal(ctx context.Context, input external.PromptInput) (*external.Goal, error) {
	if input.ThreadID == "" {
		return nil, nil
	}
	if metadataString(input.RuntimeMetadata, metadataThreadIDKey) == "" &&
		(d.servers == nil || d.servers.peek(serverKey(input.BotID, input.BotAgentID)) == nil) {
		return nil, nil
	}
	srv, release, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return nil, err
	}
	defer release()
	id := srv.goalThreadID(input)
	if id == "" {
		return nil, nil
	}
	var response protocol.ThreadGoalGetResponse
	if err := srv.conn.Call(ctx, protocol.MethodThreadGoalGet, protocol.ThreadGoalGetParams{ThreadID: id}, &response); err != nil {
		if goalStateUnavailable(err) {
			return nil, nil
		}
		return nil, err
	}
	if response.Goal == nil {
		return nil, nil
	}
	goal := response.Goal
	return &external.Goal{
		Objective:       goal.Objective,
		Status:          string(goal.Status),
		TokenBudget:     goal.TokenBudget,
		TokensUsed:      goal.TokensUsed,
		TimeUsedSeconds: goal.TimeUsedSeconds,
	}, nil
}

func (d *Driver) ControlGoal(ctx context.Context, input external.PromptInput, action string) error {
	srv, release, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return err
	}
	defer release()
	id := srv.goalThreadID(input)
	if id == "" {
		return external.ErrThreadUnavailable
	}
	switch action {
	case "pause":
		return srv.setGoalStatus(ctx, id, protocol.ThreadGoalStatusPaused)
	case "clear":
		var response protocol.ThreadGoalClearResponse
		if err := srv.conn.Call(ctx, protocol.MethodThreadGoalClear, protocol.ThreadGoalClearParams{ThreadID: id}, &response); err != nil {
			return err
		}
		if turn := srv.turnForThread(id); turn != nil {
			turn.updateGoal(nil)
		}
		return nil
	default:
		return external.ErrCommandUnavailable
	}
}

func (s *appServer) setGoalStatus(ctx context.Context, threadID string, status protocol.ThreadGoalStatus) error {
	var response protocol.ThreadGoalSetResponse
	if err := s.conn.Call(ctx, protocol.MethodThreadGoalSet, protocol.ThreadGoalSetParams{ThreadID: threadID, Status: &status}, &response); err != nil {
		return err
	}
	if turn := s.turnForThread(threadID); turn != nil {
		turn.updateGoal(&response.Goal)
	}
	return nil
}

// Register the goal before starting the explicit user turn, but keep automatic
// continuation paused until that turn has received the selected model, context,
// permissions. Goal execution requires Default mode; subsequent turns are
// started by Codex itself.
func startGoalTurn(ctx context.Context, srv *appServer, input external.PromptInput, params protocol.TurnStartParams, response *protocol.TurnStartResponse) error {
	objective := strings.TrimSpace(input.CommandArgs)
	if objective == "" {
		return errors.New("goal objective is empty")
	}
	request := protocol.ThreadGoalSetParams{ThreadID: params.ThreadID}
	paused := protocol.ThreadGoalStatusPaused
	request.Status = &paused
	if objective != "resume" {
		request.Objective = &objective
	}
	var goal protocol.ThreadGoalSetResponse
	if err := srv.conn.Call(ctx, protocol.MethodThreadGoalSet, request, &goal); err != nil {
		return err
	}
	input.Prompt = objective
	if objective == "resume" {
		input.Prompt = "Continue pursuing the current goal."
	}
	params.Input = buildTurnInput(input)
	if err := srv.conn.Call(ctx, protocol.MethodTurnStart, params, response); err != nil {
		return err
	}
	return srv.activateGoal(ctx, params.ThreadID)
}

// A Memoh run keeps receiving the runtime's own continuation turns. Goal
// completion/pausing never cuts off the final turn's output or approvals.
func (t *turnState) updateGoal(goal *protocol.ThreadGoal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.goalActive = goal != nil && goal.Status == protocol.ThreadGoalStatusActive
	if t.goalActive {
		t.followsGoal = true
	}
	t.finishGoalIfIdle()
}

// Caller holds mu. A paused/cleared goal may be between continuation turns.
func (t *turnState) finishGoalIfIdle() {
	if t.followsGoal && !t.goalStarting && !t.goalActive && t.turn != nil {
		t.finish()
	}
}

func (t *turnState) startContinuation(turnID string) {
	t.mu.Lock()
	continuation := t.followsGoal && t.turnID != "" && t.turnID != turnID
	if t.turnID == "" || t.followsGoal {
		t.turnID = turnID
		t.turn = nil
		t.turnErr = nil
	}
	t.mu.Unlock()
	if continuation {
		t.emit(event.StreamEvent{Type: event.TextEnd})
		t.emit(event.StreamEvent{Type: event.ReasoningEnd})
		t.emit(event.StreamEvent{Type: event.TextDelta, Delta: "\n\n"})
	}
}

func (d *Driver) pauseGoalOnExit(ctx context.Context, srv *appServer, turn *turnState) {
	turn.mu.Lock()
	active := turn.goalActive || turn.goalStarting
	turn.mu.Unlock()
	if !active {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.setGoalStatus(cleanup, turn.threadID, protocol.ThreadGoalStatusPaused); err != nil {
		d.logger.Warn("codex goal pause failed; draining app-server", slog.Any("error", err))
		d.ResetBotAgent(turn.input.BotID, turn.input.BotAgentID)
	}
}

// On an explicit user turn, suspend a stored active goal before thread/resume
// can autonomously start work. The admitted turn reactivates it after its
// model/permission/context setup only in Default mode. A Plan turn leaves
// the goal paused and completes normally. Missing old thread state still
// follows the driver's existing reseed path.
func (s *appServer) prepareGoal(ctx context.Context, input external.PromptInput) (bool, error) {
	id := metadataString(input.RuntimeMetadata, metadataThreadIDKey)
	if id == "" || input.ForceFreshRuntime {
		return false, nil
	}
	var response protocol.ThreadGoalGetResponse
	if err := s.conn.Call(ctx, protocol.MethodThreadGoalGet, protocol.ThreadGoalGetParams{ThreadID: id}, &response); err != nil {
		if goalStateUnavailable(err) {
			return false, nil
		}
		return false, err
	}
	if response.Goal == nil || response.Goal.Status != protocol.ThreadGoalStatusActive {
		return false, nil
	}
	if err := s.setGoalStatus(ctx, id, protocol.ThreadGoalStatusPaused); err != nil {
		return false, err
	}
	return input.Command == "" && goalExecutionAllowed(input), nil
}

// goalStateUnavailable reports goal reads that mean "nothing to read" rather
// than a failure: the goals API is absent (-32601), or Codex no longer knows
// the stored thread (wiped state). The latter must fall through to
// ensureThread's reseed path, which is the only way such a session can run
// again. Codex reports it under the generic invalid-request code, so match
// the message; every other error remains an error.
func goalStateUnavailable(err error) bool {
	var rpcErr *protocol.RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	switch rpcErr.Code {
	case -32601:
		return true
	case -32600:
		return strings.HasPrefix(rpcErr.Message, "thread not found")
	}
	return false
}

func (s *appServer) activateGoal(ctx context.Context, threadID string) error {
	if err := s.setGoalStatus(ctx, threadID, protocol.ThreadGoalStatusActive); err != nil {
		return err
	}
	if turn := s.turnForThread(threadID); turn != nil {
		turn.mu.Lock()
		turn.goalStarting = false
		turn.finishGoalIfIdle()
		turn.mu.Unlock()
	}
	return nil
}
