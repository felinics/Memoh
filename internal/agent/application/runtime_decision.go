package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/decision"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/i18n"
)

type continuationLifecycleResult struct {
	snapshot *contextfrag.LifecycleSnapshot
	cause    error
	deferred bool
}

// ResolveRuntimeDecision reads the authoritative decision row before any live
// owner lookup. Terminal rows are returned too: the router needs to distinguish
// a known, already-decided request from an unfenced ACP/MCP request.
func (s *Service) ResolveRuntimeDecision(ctx context.Context, commandType, decisionID string) (sessionruntime.DecisionTarget, error) {
	if s == nil || s.queries == nil {
		return sessionruntime.DecisionTarget{}, errors.New("runtime decision store is not configured")
	}
	id, err := db.ParseUUID(decisionID)
	if err != nil {
		return sessionruntime.DecisionTarget{}, fmt.Errorf("%w: %w", sessionruntime.ErrDecisionNotFound, err)
	}
	switch commandType {
	case sessionruntime.CommandToolApprovalResponse:
		row, err := s.queries.GetToolApprovalRequest(ctx, id)
		if err != nil {
			return sessionruntime.DecisionTarget{}, runtimeDecisionReadError(err)
		}
		return toolApprovalDecisionTarget(row), nil
	case sessionruntime.CommandUserInputResponse:
		row, err := s.queries.GetUserInputRequest(ctx, id)
		if err != nil {
			return sessionruntime.DecisionTarget{}, runtimeDecisionReadError(err)
		}
		return userInputDecisionTarget(row), nil
	default:
		return sessionruntime.DecisionTarget{}, fmt.Errorf("unsupported runtime decision command %q", commandType)
	}
}

// PendingRuntimeDecisions resolves every durable decision that parked runID.
// It is used only by expired-owner recovery, where preserving the exact rows
// is required before advancing the run's fencing token — a turn can park on
// several approvals and user inputs at once, and dropping any of them here
// would supersede a decision the user can still answer.
func (s *Service) PendingRuntimeDecisions(ctx context.Context, runID string) ([]sessionruntime.DecisionTarget, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("runtime decision store is not configured")
	}
	id, err := db.ParseUUID(runID)
	if err != nil {
		return nil, err
	}
	approvals, err := s.queries.ListPendingToolApprovalsByRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read pending tool approvals for run: %w", err)
	}
	inputs, err := s.queries.ListPendingUserInputsByRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read pending user inputs for run: %w", err)
	}
	out := make([]sessionruntime.DecisionTarget, 0, len(approvals)+len(inputs))
	for _, approval := range approvals {
		out = append(out, toolApprovalDecisionTarget(approval))
	}
	for _, input := range inputs {
		out = append(out, userInputDecisionTarget(input))
	}
	// Recovery decides by session runtime whether the parked run is
	// resumable at all; every pending decision here shares one session.
	if len(out) > 0 && s.sessionService != nil {
		sess, sessErr := s.sessionService.Get(ctx, out[0].SessionID)
		if sessErr != nil {
			return nil, fmt.Errorf("read waiting-decision session runtime: %w", sessErr)
		}
		for i := range out {
			out[i].SessionRuntime = sess.RuntimeType
		}
	}
	return out, nil
}

func runtimeDecisionReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionruntime.ErrDecisionNotFound
	}
	return err
}

func toolApprovalDecisionTarget(row dbsqlc.ToolApprovalRequest) sessionruntime.DecisionTarget {
	return sessionruntime.DecisionTarget{
		Type: sessionruntime.CommandToolApprovalResponse, ID: decisionUUIDString(row.ID),
		BotID: decisionUUIDString(row.BotID), SessionID: decisionUUIDString(row.SessionID),
		RunID: decisionUUIDString(row.RunID), TurnID: decisionUUIDString(row.TurnID),
		Status: row.Status, FencingToken: pgInt64(row.RuntimeFencingToken),
		InlineDecision: isCapabilityManagementApproval(toolapproval.Request{ToolName: row.ToolName}),
		ControlID:      pgText(row.ResponseControlID), PayloadHash: pgText(row.ResponsePayloadHash),
	}
}

func userInputDecisionTarget(row dbsqlc.UserInputRequest) sessionruntime.DecisionTarget {
	return sessionruntime.DecisionTarget{
		Type: sessionruntime.CommandUserInputResponse, ID: decisionUUIDString(row.ID),
		BotID: decisionUUIDString(row.BotID), SessionID: decisionUUIDString(row.SessionID),
		RunID: decisionUUIDString(row.RunID), TurnID: decisionUUIDString(row.TurnID),
		Status: row.Status, FencingToken: pgInt64(row.RuntimeFencingToken),
		ControlID: pgText(row.ResponseControlID), PayloadHash: pgText(row.ResponsePayloadHash),
	}
}

func decisionUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func pgInt64(value pgtype.Int8) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}

func pgText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func (s *Service) hasPendingDecisionOutputForRow(ctx context.Context, commandType, botID, decisionID, responseControlID string) bool {
	if s == nil || s.decisionRuntime == nil || strings.TrimSpace(decisionID) == "" {
		return false
	}
	ctrl := firstNonEmpty(strings.TrimSpace(responseControlID), implicitDecisionControlID(commandType, decisionID, true))
	return s.decisionRuntime.HasPendingDecisionOutput(ctx, commandType, botID, decisionID, ctrl)
}

func isPendingUserInputRow(row dbsqlc.UserInputRequest, now time.Time) bool {
	return strings.TrimSpace(row.Status) == userinput.StatusPending && (!row.ExpiresAt.Valid || row.ExpiresAt.Time.After(now))
}

type runtimeDecisionCandidate struct {
	id, promptMessageID, controlID string
	shortID                        int
	pending, decided               bool
}

func matchesDecisionResponseControlID(commandType, botID, decisionID, stored, input string) bool {
	stored, input = strings.TrimSpace(stored), strings.TrimSpace(input)
	return stored != "" && input != "" && (stored == input || stored == sessionruntime.DecisionControlCommandID(commandType, botID, decisionID, input))
}

func (s *Service) selectRuntimeDecisionIndex(ctx context.Context, commandType, botID, controlID, rawID, replyID string, items []runtimeDecisionCandidate) (int, bool) {
	controlID, rawID, replyID = strings.TrimSpace(controlID), strings.TrimSpace(rawID), strings.TrimSpace(replyID)
	find := func(pred func(runtimeDecisionCandidate) bool) (int, bool) {
		for i := len(items) - 1; i >= 0; i-- {
			if pred(items[i]) {
				return i, true
			}
		}
		return -1, false
	}
	pickDecided := func(match func(runtimeDecisionCandidate) bool) (int, bool) {
		fallback := -1
		for i := len(items) - 1; i >= 0; i-- {
			if match(items[i]) && items[i].decided {
				if s.hasPendingDecisionOutputForRow(ctx, commandType, botID, items[i].id, items[i].controlID) {
					return i, true
				}
				if fallback < 0 {
					fallback = i
				}
			}
		}
		return fallback, fallback >= 0
	}
	matchCtrl := func(c runtimeDecisionCandidate) bool {
		return controlID != "" && matchesDecisionResponseControlID(commandType, botID, c.id, c.controlID, controlID)
	}
	if rawID == "" {
		if i, ok := find(matchCtrl); ok {
			return i, true
		}
	} else {
		shortID, err := strconv.Atoi(rawID)
		if err != nil {
			return -1, false
		}
		if i, ok := find(func(c runtimeDecisionCandidate) bool { return c.shortID == shortID && (matchCtrl(c) || c.pending) }); ok {
			return i, true
		}
		return pickDecided(func(c runtimeDecisionCandidate) bool { return c.shortID == shortID })
	}
	if replyID != "" {
		if i, ok := find(func(c runtimeDecisionCandidate) bool { return c.promptMessageID == replyID && c.pending }); ok {
			return i, true
		}
		if i, ok := pickDecided(func(c runtimeDecisionCandidate) bool { return c.promptMessageID == replyID }); ok {
			return i, true
		}
	}
	if i, ok := find(func(c runtimeDecisionCandidate) bool { return c.pending }); ok {
		return i, true
	}
	if n := len(items); n > 0 && (replyID == "" || items[n-1].promptMessageID == "") && items[n-1].decided &&
		s.hasPendingDecisionOutputForRow(ctx, commandType, botID, items[n-1].id, items[n-1].controlID) {
		return n - 1, true
	}
	return -1, false
}

func (s *Service) resolveRuntimeToolApprovalID(ctx context.Context, input ToolApprovalResponseInput) (string, bool) {
	rawID := strings.TrimSpace(firstNonEmpty(input.ExplicitID, input.ApprovalID))
	if _, err := db.ParseUUID(rawID); rawID != "" && err == nil {
		return rawID, true
	}
	botID, err1 := db.ParseUUID(input.BotID)
	sessionID, err2 := db.ParseUUID(input.ThreadID)
	if s.queries == nil || err1 != nil || err2 != nil {
		if s.toolApproval != nil {
			t, err := s.toolApproval.ResolveTarget(ctx, toolapproval.ResolveInput{BotID: input.BotID, SessionID: input.ThreadID, ExplicitID: rawID, ReplyExternalMessageID: input.ReplyExternalMessageID})
			return strings.TrimSpace(t.ID), err == nil && strings.TrimSpace(t.ID) != ""
		}
		return "", false
	}
	rows, err := s.queries.ListToolApprovalsBySession(ctx, dbsqlc.ListToolApprovalsBySessionParams{BotID: botID, SessionID: sessionID})
	items := make([]runtimeDecisionCandidate, len(rows))
	for i, r := range rows {
		st := strings.ToLower(strings.TrimSpace(r.Status))
		items[i] = runtimeDecisionCandidate{id: decisionUUIDString(r.ID), shortID: int(r.ShortID), promptMessageID: strings.TrimSpace(r.PromptExternalMessageID), controlID: pgText(r.ResponseControlID), pending: st == "pending", decided: st == "approved" || st == "rejected"}
	}
	if idx, ok := s.selectRuntimeDecisionIndex(ctx, sessionruntime.CommandToolApprovalResponse, input.BotID, input.ControlID, rawID, input.ReplyExternalMessageID, items); err == nil && ok {
		return items[idx].id, true
	}
	return "", false
}

func (s *Service) resolveRuntimeUserInputID(ctx context.Context, input UserInputResponseInput) (string, *userinput.UIPayload, bool) {
	rawID := strings.TrimSpace(firstNonEmpty(input.ExplicitID, input.UserInputID))
	if parsed, err := db.ParseUUID(rawID); rawID != "" && err == nil {
		if len(input.Answers) == 0 && strings.TrimSpace(input.TextAnswer) != "" && s.queries != nil {
			if row, getErr := s.queries.GetUserInputRequest(ctx, parsed); getErr == nil {
				p := userinput.RequestFromSQLRow(row).UIPayload
				return rawID, &p, true
			}
		}
		return rawID, nil, true
	}
	rows, ok := s.listSessionUserInputs(ctx, input.BotID, input.ThreadID)
	if !ok {
		if s.queries == nil && s.userInput != nil {
			t, err := s.userInput.ResolveTarget(ctx, userinput.ResolveInput{BotID: input.BotID, SessionID: input.ThreadID, ExplicitID: rawID, ReplyExternalMessageID: input.ReplyExternalMessageID})
			return strings.TrimSpace(t.ID), &t.UIPayload, err == nil && strings.TrimSpace(t.ID) != ""
		}
		return "", nil, false
	}
	now, items := time.Now(), make([]runtimeDecisionCandidate, len(rows))
	for i, r := range rows {
		st := strings.TrimSpace(r.Status)
		items[i] = runtimeDecisionCandidate{id: decisionUUIDString(r.ID), shortID: int(r.ShortID), promptMessageID: strings.TrimSpace(r.PromptExternalMessageID), controlID: pgText(r.ResponseControlID), pending: isPendingUserInputRow(r, now), decided: st == userinput.StatusSubmitted || st == userinput.StatusCanceled}
	}
	if idx, found := s.selectRuntimeDecisionIndex(ctx, sessionruntime.CommandUserInputResponse, input.BotID, input.ControlID, rawID, input.ReplyExternalMessageID, items); found {
		p := userinput.RequestFromSQLRow(rows[idx]).UIPayload
		return items[idx].id, &p, true
	}
	return "", nil, false
}

func (s *Service) routeToolApprovalResponse(ctx context.Context, input ToolApprovalResponseInput, eventCh chan<- WSStreamEvent) (bool, error) {
	if s == nil || s.decisionRuntime == nil {
		return false, nil
	}
	if id, ok := s.resolveRuntimeToolApprovalID(ctx, input); ok && id != "" {
		p := input
		p.ControlID, p.ApprovalID, p.ExplicitID, p.ReplyExternalMessageID = "", id, id, ""
		return s.marshalAndStreamDecision(ctx, sessionruntime.DecisionResponse{ControlID: strings.TrimSpace(input.ControlID), DeferRelease: input.DeferRelease, ReleaseOutput: input.ReleaseOutput, Type: sessionruntime.CommandToolApprovalResponse, DecisionID: id, BotID: input.BotID, SessionID: input.ThreadID}, p, eventCh, toolapproval.ErrAlreadyDecided)
	}
	return false, nil
}

func (s *Service) routeUserInputResponse(ctx context.Context, input UserInputResponseInput, eventCh chan<- WSStreamEvent) (bool, error) {
	if s == nil || s.decisionRuntime == nil {
		return false, nil
	}
	id, uiPayload, ok := s.resolveRuntimeUserInputID(ctx, input)
	if !ok || id == "" {
		return false, nil
	}
	p := input
	p.ControlID, p.UserInputID, p.ExplicitID, p.ReplyExternalMessageID = "", id, id, ""
	if !p.Canceled && len(p.Answers) == 0 && strings.TrimSpace(p.TextAnswer) != "" && uiPayload != nil {
		if answers, err := userInputAnswersFromText(uiPayload.Localized(i18n.New(p.UILanguage)), p.TextAnswer); err == nil && len(answers) > 0 {
			p.Answers = answers
		}
	}
	if len(p.Answers) > 0 {
		p.TextAnswer, p.UILanguage = "", ""
	}
	return s.marshalAndStreamDecision(ctx, sessionruntime.DecisionResponse{ControlID: strings.TrimSpace(input.ControlID), DeferRelease: input.DeferRelease, ReleaseOutput: input.ReleaseOutput, Type: sessionruntime.CommandUserInputResponse, DecisionID: id, BotID: input.BotID, SessionID: input.ThreadID}, p, eventCh, userinput.ErrAlreadyDecided)
}

func (s *Service) marshalAndStreamDecision(ctx context.Context, resp sessionruntime.DecisionResponse, payloadInput any, eventCh chan<- WSStreamEvent, alreadyDecidedErr error) (bool, error) {
	payload, err := json.Marshal(payloadInput)
	if err != nil {
		return true, err
	}
	resp.Payload = payload
	if resp.ControlID == "" {
		resp.ControlID = implicitDecisionControlID(resp.Type, resp.DecisionID, eventCh != nil)
	}
	result, err := s.decisionRuntime.StreamDecisionResponse(ctx, resp, eventCh)
	switch {
	case errors.Is(err, sessionruntime.ErrDecisionNotFound):
		return false, nil
	case errors.Is(err, sessionruntime.ErrDecisionOutputBusy):
		return true, fmt.Errorf("%w: %w", turn.ErrDecisionOutputBusy, err)
	case errors.Is(err, sessionruntime.ErrDecisionOutputInterrupted):
		if s.logger != nil {
			s.logger.WarnContext(ctx, "accepted decision output stream interrupted", slog.Any("error", err))
		}
		return true, fmt.Errorf("%w: %w", turn.ErrDecisionOutputInterrupted, err)
	case err != nil && result.Applied && eventCh != nil:
		if s.logger != nil {
			s.logger.WarnContext(ctx, "accepted decision output interrupted", slog.Any("error", err))
		}
		raw, _ := json.Marshal(agentFailureStreamEvent(err))
		select {
		case eventCh <- raw:
			return true, nil
		case <-ctx.Done():
			return true, ctx.Err()
		}
	case err != nil:
		return true, err
	case !result.Handled:
		return false, nil
	case !result.Applied:
		return true, alreadyDecidedErr
	default:
		return true, nil
	}
}

func implicitDecisionControlID(commandType, decisionID string, streamOutput bool) string {
	if streamOutput {
		return "implicit-stream:" + commandType + ":" + decisionID
	}
	return "implicit:" + commandType + ":" + decisionID
}

// handleRuntimeDecisionCommand commits on the routed-command deadline, then
// continues independently on the owning run. The command result therefore
// means "the decision was durably accepted", not "the model finished".
//
//nolint:contextcheck // the continuation is rooted in the owning run, not the acknowledgement request.
func (s *Service) handleRuntimeDecisionCommand(ctx context.Context, command sessionruntime.Command) error {
	if s == nil || s.decisionRuntime == nil {
		return errors.New("runtime decision handler is not configured")
	}
	runCtx, runCancel, runHandle, err := s.decisionRuntime.DecisionContinuationContext(command)
	if err != nil {
		return err
	}
	switch command.Type {
	case sessionruntime.CommandUserInputResponse:
		var input UserInputResponseInput
		if err := json.Unmarshal(command.Payload, &input); err != nil {
			runCancel()
			return err
		}
		input.BotID = command.BotID
		input.ThreadID = command.SessionID
		input.UserInputID = command.TargetID
		input.ExplicitID = command.TargetID
		input.ReplyExternalMessageID = ""
		input.SuppressActivePromptAttach = true
		ctx = decision.WithResponseIdentity(ctx, decision.ResponseIdentity{
			ControlID: firstNonEmpty(input.ControlID, command.ID), PayloadHash: command.PayloadHash,
		})

		committed, err := s.CommitUserInputResponse(ctx, input)
		if err != nil {
			runCancel()
			return err
		}
		committed.runID = command.RunID
		committed.runHandle = runHandle
		s.publishCommittedRuntimeDecision(runCtx, command, native.StreamEvent{
			Type:        native.EventUserInputRequest,
			ToolName:    committed.request.ToolName,
			ToolCallID:  committed.request.ToolCallID,
			UserInputID: committed.request.ID,
			ShortID:     committed.request.ShortID,
			Status:      committed.request.Status,
			Input:       committed.request.Input,
			Metadata:    userinput.DeferredMetadata(committed.request),
		})
		if committed.isExternalAgent || committed.ackOnly {
			defer runCancel()
			s.ackInlineRuntimeDecision(runCtx, command)
			return nil
		}
		go func() {
			defer runCancel()
			s.continueRuntimeDecision(runCtx, command, func(
				continuationCtx context.Context,
				lifecycle *continuationLifecycleResult,
				eventCh chan<- WSStreamEvent,
			) error {
				return s.continueCommittedUserInputResponse(continuationCtx, committed, lifecycle, eventCh)
			})
		}()
		return nil
	case sessionruntime.CommandToolApprovalResponse:
		var input ToolApprovalResponseInput
		if err := json.Unmarshal(command.Payload, &input); err != nil {
			runCancel()
			return err
		}
		input.BotID = command.BotID
		input.ThreadID = command.SessionID
		input.ApprovalID = command.TargetID
		input.ExplicitID = command.TargetID
		input.ReplyExternalMessageID = ""
		input.SuppressActivePromptAttach = true
		ctx = decision.WithResponseIdentity(ctx, decision.ResponseIdentity{
			ControlID: firstNonEmpty(input.ControlID, command.ID), PayloadHash: command.PayloadHash,
		})

		committed, err := s.CommitToolApprovalResponse(ctx, input)
		if err != nil {
			runCancel()
			return err
		}
		committed.runID = command.RunID
		committed.runHandle = runHandle
		s.publishCommittedRuntimeDecision(runCtx, command, native.StreamEvent{
			Type:           native.EventToolApprovalRequest,
			ToolName:       committed.request.ToolName,
			InlineDecision: committed.usesDecisionWaiter,
			ToolCallID:     committed.request.ToolCallID,
			ApprovalID:     committed.request.ID,
			ShortID:        committed.request.ShortID,
			Status:         committed.request.Status,
			Input:          committed.request.ToolInput,
			Metadata:       approvalResultMetadata(committed.request),
		})
		if committed.usesDecisionWaiter || committed.ackOnly {
			defer runCancel()
			s.ackInlineRuntimeDecision(runCtx, command)
			return nil
		}
		go func() {
			defer runCancel()
			s.continueRuntimeDecision(runCtx, command, func(
				continuationCtx context.Context,
				lifecycle *continuationLifecycleResult,
				eventCh chan<- WSStreamEvent,
			) error {
				return s.continueCommittedToolApprovalResponse(continuationCtx, committed, lifecycle, eventCh)
			})
		}()
		return nil
	default:
		runCancel()
		return errors.New("unsupported runtime decision command")
	}
}

// Inline runtimes consume the committed answer inside their original run.
// Close only this command's output; synthetic agent start/end events or a
// deferred continuation would race the original producer's lifecycle.
func (s *Service) ackInlineRuntimeDecision(ctx context.Context, command sessionruntime.Command) {
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.decisionRuntime.PublishDecisionOutput(ackCtx, command, 1, nil); err != nil && s.logger != nil {
		// The answer is already durable. An output transport failure must not
		// fail the independently running producer or undo the accepted answer.
		s.logger.WarnContext(ctx, "close inline decision output failed", slog.Any("error", err),
			slog.String("run_id", command.RunID), slog.String("decision_id", command.TargetID))
	}
}

// publishCommittedRuntimeDecision replaces the pending decision in the live
// projection before the command is acknowledged. PostgreSQL remains the
// correctness boundary: once Commit*Response succeeds the user's answer is
// accepted even if publishing the derived live view fails, so publication
// errors are logged and the continuation still runs.
func (s *Service) publishCommittedRuntimeDecision(ctx context.Context, command sessionruntime.Command, event native.StreamEvent) {
	if s == nil || s.decisionRuntime == nil {
		return
	}
	handle := sessionruntime.RunHandle{
		BotID:      command.BotID,
		SessionID:  command.SessionID,
		RunID:      command.RunID,
		Generation: command.Generation,
	}
	if _, err := s.decisionRuntime.HandleAgentEvent(ctx, handle, event); err != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "publish committed runtime decision failed",
			slog.Any("error", err),
			slog.String("run_id", command.RunID),
			slog.String("decision_id", command.TargetID),
			slog.String("command_type", command.Type))
	}
}

func (s *Service) continueRuntimeDecision(
	ctx context.Context,
	command sessionruntime.Command,
	continueRun func(context.Context, *continuationLifecycleResult, chan<- WSStreamEvent) error,
) {
	handle := sessionruntime.RunHandle{
		BotID:      command.BotID,
		SessionID:  command.SessionID,
		RunID:      command.RunID,
		Generation: command.Generation,
	}
	var outputSeq int64
	var outputCause error
	// failurePublished records that the continuation's own failure event
	// reached the output, so the run's failure is not appended a second time.
	var failurePublished bool
	defer func() {
		if outputCause != nil && !failurePublished {
			raw, _ := json.Marshal(agentFailureStreamEvent(outputCause))
			if err := s.decisionRuntime.PublishDecisionOutput(context.WithoutCancel(ctx), command, outputSeq+1, raw); err == nil {
				outputSeq++
			}
		}
		if err := s.decisionRuntime.PublishDecisionOutput(context.WithoutCancel(ctx), command, outputSeq+1, nil); err != nil {
			if s.logger != nil {
				s.logger.WarnContext(ctx, "close decision output failed", slog.Any("error", err))
			}
			// A failed checkpoint write must not leave a parked run owning an output
			// subscription that can never finish. Use the normal run failure lifecycle.
			s.finishRuntimeDecision(context.WithoutCancel(ctx), handle, err)
		}
	}()

	if err := s.decisionRuntime.WaitDecisionContinuationReady(ctx, command); err != nil {
		outputCause = err
		s.recoverContextLifecycleFromAssistantMetadata(ctx, command.RunID, command.BotID, command.SessionID, err)
		s.finishRuntimeDecision(ctx, handle, err)
		return
	}
	eventCh := make(chan WSStreamEvent, 64)
	runDone := make(chan error, 1)
	lifecycle := &continuationLifecycleResult{}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		runDone <- continueRun(runCtx, lifecycle, eventCh)
		close(eventCh)
	}()

	var (
		publishErr        error
		eventCause        error
		lifecycleDeferred bool
	)
	for raw := range eventCh {
		// Cancellation may leave already-buffered events. Drain them so the runner
		// can return and finish through the same lifecycle as other stream failures.
		if publishErr != nil {
			continue
		}
		var event native.StreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			continue
		}
		eventErr := agentStreamFailure(event)
		if eventErr != nil && eventCause == nil {
			eventCause = eventErr
		}
		if event.IsTerminal() {
			lifecycleDeferred = pendingContinuationDecision(event)
			if !lifecycleDeferred {
				switch event.Type {
				case native.EventAgentEnd:
					eventCause = nil
				case native.EventAgentAbort:
					if context.Cause(ctx) != nil || eventCause == nil {
						eventCause = agentAbortCause(ctx)
					}
				}
			}
		}
		if _, err := s.decisionRuntime.HandleAgentEvent(runCtx, handle, event); err != nil {
			publishErr = err
			cancel()
			continue
		}
		if err := s.decisionRuntime.PublishDecisionOutput(runCtx, command, outputSeq+1, raw); err != nil {
			publishErr = err
			cancel()
			continue
		}
		outputSeq++
		failurePublished = failurePublished || eventErr != nil
	}
	runErr := <-runDone
	lifecycleDeferred = lifecycleDeferred || lifecycle.deferred
	lifecycleCause := firstLifecycleCause(runErr, eventCause, lifecycle.cause)
	if publishErr != nil {
		runErr = publishErr
		lifecycleCause = publishErr
		lifecycleDeferred = false
	}
	if runErr != nil {
		outputCause = runErr
		s.persistRuntimeDecisionLifecycle(ctx, command, lifecycle, lifecycleCause)
		s.finishRuntimeDecision(ctx, handle, runErr)
		return
	}
	if lifecycleDeferred {
		_, _ = s.decisionRuntime.FinishRun(context.WithoutCancel(ctx), handle, "")
		return
	}
	s.persistRuntimeDecisionLifecycle(ctx, command, lifecycle, lifecycleCause)
	s.finishRuntimeDecision(ctx, handle, lifecycleCause)
}

// logContinuationStreamError records the cause of a native error event
// observed while a decision continuation streams. publicAgentStreamEvent
// replaces the event with its code before it leaves the application, so this
// is the only place the cause is retained.
func (s *Service) logContinuationStreamError(runID string, event native.StreamEvent) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Error("decision continuation stream error",
		slog.String("run_id", strings.TrimSpace(runID)),
		slog.String("event_type", string(event.Type)),
		slog.String("code", strings.TrimSpace(event.Code)),
		slog.Any("error", event.Cause),
	)
}

func firstLifecycleCause(causes ...error) error {
	for _, cause := range causes {
		if cause != nil {
			return cause
		}
	}
	return nil
}

func pendingContinuationDecision(event native.StreamEvent) bool {
	if !event.IsTerminal() ||
		(strings.TrimSpace(event.ApprovalID) == "" && strings.TrimSpace(event.UserInputID) == "") {
		return false
	}
	status := strings.TrimSpace(event.Status)
	return status == "" || strings.EqualFold(status, "pending")
}

func (s *Service) persistRuntimeDecisionLifecycle(
	ctx context.Context,
	command sessionruntime.Command,
	result *continuationLifecycleResult,
	cause error,
) {
	if result != nil && result.snapshot != nil {
		s.persistContextLifecycleSnapshot(
			ctx,
			command.RunID,
			command.BotID,
			command.SessionID,
			result.snapshot,
			cause,
			true,
		)
		return
	}
	s.recoverContextLifecycleFromAssistantMetadata(
		ctx,
		command.RunID,
		command.BotID,
		command.SessionID,
		cause,
	)
}

func (s *Service) finishRuntimeDecision(ctx context.Context, handle sessionruntime.RunHandle, cause error) {
	outcome := runtimeDecisionTerminal(ctx, cause)
	lifecycleCtx := frozenContextCause(ctx)
	minimal := minimalContextLifecycleSnapshot()
	staged := s.stageContextLifecycleCandidate(
		lifecycleCtx,
		handle.RunID,
		handle.BotID,
		handle.SessionID,
		&minimal,
		cause,
		contextLifecycleCandidateMinimal,
	)
	finishCtx := WithRunOutcome(context.WithoutCancel(nonNilContext(ctx)), handle.RunID, outcome)
	if _, err := s.decisionRuntime.FinishRunWithErrorCode(finishCtx, handle, outcome.Status, outcome.ErrorCode()); err == nil && !staged {
		s.EnsureTerminalContextLifecycle(
			lifecycleCtx,
			handle.RunID,
			handle.BotID,
			handle.SessionID,
			cause,
		)
	}
}

func frozenContextCause(ctx context.Context) context.Context {
	ctx = nonNilContext(ctx)
	frozen := context.WithoutCancel(ctx)
	cause := context.Cause(ctx)
	if cause == nil {
		return frozen
	}
	frozen, cancel := context.WithCancelCause(frozen)
	cancel(cause)
	return frozen
}

// runtimeDecisionTerminal is the outcome a decision continuation reports: a
// failure unless the continuation was explicitly canceled, in which case the
// session runtime resolves it from the abort intent recorded against the run.
func runtimeDecisionTerminal(ctx context.Context, cause error) RunOutcome {
	explicitlyCanceled := ctx != nil &&
		errors.Is(cause, context.Canceled) &&
		errors.Is(context.Cause(ctx), context.Canceled)
	if cause != nil && !explicitlyCanceled {
		return RunOutcome{Status: sessionruntime.RunStatusErrored, Cause: cause}
	}
	return RunOutcome{}
}

// parkedRunRequestResolver finds the request message a parked run's view
// starts from. It is reached through a type assertion, so the production
// message service is pinned to it at compile time: without it a recovered
// waiting-decision run would show no decision card.
type parkedRunRequestResolver interface {
	GetVisibleHistoryTurnRequestMessageIDByTurn(context.Context, string, string) (string, error)
}

var _ parkedRunRequestResolver = (*messagepkg.DBService)(nil)

// ParkedRunMessages rebuilds the assistant messages a parked run persisted, from
// its request turn on, with each decision card as the history page shows it. A
// recovered run is shown from them when the live backend kept no view of it.
// Every turn after the request belongs to the run: it is the session's active
// run, and the turns a steer opens inside it carry its later steps.
func (s *Service) ParkedRunMessages(ctx context.Context, run ledger.Run) ([]chatview.UIMessage, error) {
	if s == nil || s.messageService == nil || s.sessionService == nil {
		return nil, nil
	}
	resolver, ok := s.messageService.(parkedRunRequestResolver)
	if !ok {
		return nil, nil
	}
	requestID, err := resolver.GetVisibleHistoryTurnRequestMessageIDByTurn(ctx, run.SessionID, run.TurnID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve parked run request: %w", err)
	}
	if strings.TrimSpace(requestID) == "" {
		return nil, nil
	}
	rows, err := s.messageService.ListVisibleFromBySession(ctx, run.SessionID, requestID)
	if err != nil {
		return nil, fmt.Errorf("list parked run messages: %w", err)
	}
	sess, err := s.sessionService.Get(ctx, run.SessionID)
	if err != nil {
		return nil, fmt.Errorf("read parked run session: %w", err)
	}
	turns := chatview.ConvertSessionMessagesToUITurns(rows, sess.RuntimeType, sess.RuntimeMetadata)
	var toolCallIDs []string
	for _, turn := range turns {
		for _, message := range turn.Messages {
			if message.Type == chatview.UIMessageTool && strings.TrimSpace(message.ToolCallID) != "" {
				toolCallIDs = append(toolCallIDs, strings.TrimSpace(message.ToolCallID))
			}
		}
	}
	if len(toolCallIDs) > 0 {
		if s.toolApproval != nil {
			approvals, err := s.toolApproval.ListBySessionToolCalls(ctx, run.BotID, run.SessionID, toolCallIDs)
			if err != nil {
				return nil, fmt.Errorf("list parked run approvals: %w", err)
			}
			chatview.ApplyToolApprovals(turns, approvals, nil)
		}
		if lister, ok := s.userInput.(interface {
			ListBySessionToolCalls(context.Context, string, string, []string) ([]userinput.Request, error)
		}); ok {
			requests, err := lister.ListBySessionToolCalls(ctx, run.BotID, run.SessionID, toolCallIDs)
			if err != nil {
				return nil, fmt.Errorf("list parked run questions: %w", err)
			}
			chatview.ApplyUserInputs(turns, requests, nil)
		}
	}
	var messages []chatview.UIMessage
	for _, turn := range turns {
		if turn.Role != "assistant" {
			continue
		}
		for _, message := range turn.Messages {
			message.ID = len(messages)
			messages = append(messages, message)
		}
	}
	return messages, nil
}
