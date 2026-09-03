package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

// startQueueContinuation is deliberately called only after CommitStep returns.
// The coordinator has already committed history, R0 terminalization, the
// assigned source item, and the ownerless R1 in one transaction at that point.
// Starting execution before that commit would allow a crash to expose a run
// whose input and queue claim are not durable yet.
func (s *Service) startQueueContinuation(parent context.Context, result sessionqueue.CommitStepResult) {
	if s == nil || s.queueStore == nil || s.sessionManager == nil ||
		result.Action != sessionqueue.StartContinuation || result.FollowUp == nil ||
		strings.TrimSpace(result.ContinuationRunID) == "" {
		return
	}
	item := *result.FollowUp
	go s.runQueueContinuation(parent, result.ContinuationRunID, item)
}

// recoverQueueContinuations is invoked by the elected runtime reaper. The
// query only returns ownerless accepted continuation runs, so each candidate
// still goes through AcquireContinuationRun's unique owner/fencing CAS.
func (s *Service) recoverQueueContinuations(ctx context.Context) error {
	if s == nil || s.queueStore == nil {
		return nil
	}
	runs, err := s.queueStore.ListOwnerlessContinuations(ctx, 100)
	if err != nil {
		return err
	}
	for _, run := range runs {
		item, itemErr := s.queueStore.FollowUpByID(ctx, run.SourceFollowUpItemID)
		if itemErr != nil {
			continue
		}
		go s.runQueueContinuation(ctx, run.RunID, item)
	}
	return nil
}

// recoverQueuedSteerRun resumes the same run after its live owner lease
// expired between a durable final-step commit and the next model invocation.
// The queue claim is never converted into a new item or run: ownership and the
// claim are both fenced to the replacement handle.
func (s *Service) recoverQueuedSteerRun(ctx context.Context, candidate sessionruntime.LeaseCandidate) (bool, error) {
	if s == nil || s.queueStore == nil || s.sessionManager == nil || candidate.RunID == "" {
		return false, nil
	}
	item, err := s.queueStore.GetClaimedSteerForRun(ctx, candidate.RunID)
	if errors.Is(err, sessionqueue.ErrNotPending) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	metadata, err := s.queueStore.GetRecoverableRun(ctx, candidate.RunID)
	if err != nil {
		return false, err
	}
	if metadata.FencingToken != candidate.FencingToken {
		return false, nil
	}
	generation, err := s.sessionManager.LivenessGeneration(ctx)
	if err != nil {
		return false, err
	}
	handle, won, err := s.queueStore.AcquireQueuedRun(ctx, candidate.RunID, candidate.FencingToken, s.sessionManager.OwnerID(), generation)
	if err != nil || !won {
		return false, err
	}
	_, claim, err := s.queueStore.ReclaimSteer(ctx, string(item.ID), handle)
	if err != nil {
		s.rejectLostQueuedRun(context.WithoutCancel(ctx), candidate.RunID, handle.FencingToken)
		return true, nil
	}

	runCtx, cancelCause := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelCause(nil)
	abortCh := make(chan struct{})
	injectCh := make(chan turn.InjectMessage, 16)
	started, err := s.sessionManager.StartExistingRun(
		runCtx, handle,
		func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
			return sessionruntime.RunAdmissionView{}, nil
		}, cancelCause, abortCh, func() { cancelCause(context.Canceled) }, injectCh,
	)
	if err != nil || started.RunID == "" {
		s.rejectLostQueuedRun(context.WithoutCancel(ctx), candidate.RunID, handle.FencingToken)
		return true, nil
	}
	runCtx = s.withAdmissionRuntimeFence(runCtx, sessionruntime.Admission{Handle: started, Started: true})
	if err := s.sessionManager.PublishQueueUserTurns(context.WithoutCancel(ctx), started, sessionruntime.QueueUserTurnUpdate{
		ClaimedSteerItemID: string(item.ID), ClaimedSteerText: continuationPayloadText(item.Payload),
		ClaimedSteerTimestamp: item.CreatedAt,
	}); err != nil && s.logger != nil {
		s.logger.Warn("publish recovered steer turn failed", slog.String("run_id", candidate.RunID), slog.Any("error", err))
	}

	var input struct {
		Query             string `json:"query"`
		UserVisibleText   string `json:"user_visible_text"`
		UserMessageKind   string `json:"user_message_kind"`
		ExternalMessageID string `json:"external_message_id"`
	}
	_ = json.Unmarshal(metadata.Input, &input)
	query := strings.TrimSpace(input.Query)
	if query == "" {
		query = strings.TrimSpace(input.UserVisibleText)
	}
	stepOffset, err := s.queueStore.NextQueueStepIndex(ctx, metadata.RunID)
	if err != nil {
		s.rejectLostQueuedRun(context.WithoutCancel(ctx), candidate.RunID, started.FencingToken)
		return true, nil
	}
	// The committed steer is injected before the first recovered model call. The
	// normal step committer applies the claim in the same transaction as that
	// recovered step's history.
	select {
	case injectCh <- turn.InjectMessage{Text: continuationPayloadText(item.Payload), HeaderifiedText: continuationPayloadText(item.Payload)}:
	default:
		s.rejectLostQueuedRun(context.WithoutCancel(ctx), candidate.RunID, started.FencingToken)
		return true, nil
	}
	req := ChatRequest{
		BotID: metadata.BotID, ChatID: metadata.BotID, ThreadID: metadata.SessionID,
		RunID: metadata.RunID, RunHandle: started, TurnID: metadata.TurnID,
		TurnPosition: func() *int64 { p := metadata.TurnPosition; return &p }(),
		Query:        query, RawQuery: query, UserVisibleText: input.UserVisibleText,
		UserMessageKind: input.UserMessageKind, ExternalMessageID: input.ExternalMessageID,
		UserMessagePersisted: true, InjectCh: injectCh, QueueInjectCh: injectCh,
		QueueSteerClaim: &claim, StepIndexOffset: stepOffset, PublishRuntimeEvents: true,
	}
	chunks, errs := s.StreamChat(runCtx, req)
	for range chunks {
	}
	var streamErr error
	for streamErrValue := range errs {
		if streamErrValue != nil {
			streamErr = streamErrValue
		}
	}
	status := sessionruntime.RunStatusCompleted
	if streamErr != nil {
		status = sessionruntime.RunStatusErrored
	}
	if finishErr := s.sessionManager.FinishRun(context.WithoutCancel(ctx), started, status, errorString(streamErr)); finishErr != nil && s.logger != nil {
		s.logger.Warn("recovered steer run finalization failed", slog.String("run_id", candidate.RunID), slog.Any("error", finishErr))
	}
	return true, nil
}

func (s *Service) runQueueContinuation(parent context.Context, runID string, item sessionqueue.FollowUpItem) {
	// The reaper rescans ownerless continuations every tick and CommitStep also
	// starts one directly. Only one goroutine per continuation may wait on the
	// session slot; the durable owner CAS still decides who executes.
	if _, loaded := s.queueContinuationsInFlight.LoadOrStore(runID, struct{}{}); loaded {
		return
	}
	defer s.queueContinuationsInFlight.Delete(runID)
	ctx := context.WithoutCancel(parent)
	metadata, err := s.queueStore.GetContinuationRun(ctx, runID)
	if err != nil {
		return
	}
	// EnqueuedDuringRunID is admission provenance only. The run that handed this
	// continuation off may be a later run in the same session, so wait on the
	// session's live slot rather than on a named parent.
	if err := s.sessionManager.WaitSessionSlotFor(ctx, metadata.BotID, metadata.SessionID, runID); err != nil {
		if s.logger != nil && !errors.Is(err, sessionruntime.ErrManagerClosed) {
			s.logger.Warn("continuation slot wait failed",
				slog.String("run_id", runID),
				slog.Any("error", err))
		}
		return
	}
	ownerID := s.sessionManager.OwnerID()
	if ownerID == "" {
		return
	}
	liveGeneration, err := s.sessionManager.LivenessGeneration(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("continuation liveness generation lookup failed", slog.String("run_id", runID), slog.Any("error", err))
		}
		return
	}
	handle, won, err := s.queueStore.AcquireContinuationRun(ctx, runID, ownerID, liveGeneration)
	if err != nil || !won {
		if err != nil && s.logger != nil {
			s.logger.Warn("continuation acquisition failed", slog.String("run_id", runID), slog.Any("error", err))
		}
		return
	}
	// Claim with the acquired owner/fence before starting the local runtime.
	// Admission can then publish the follow-up prompt immediately, without a
	// started run whose source item has not been claimed yet.
	claimed, err := s.queueStore.ClaimAssignedFollowUp(ctx, string(item.ID), handle)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("continuation follow-up claim failed",
				slog.String("run_id", runID),
				slog.String("follow_up_item_id", string(item.ID)),
				slog.Any("error", err))
		}
		s.rejectLostContinuation(context.WithoutCancel(ctx), runID, handle.FencingToken)
		return
	}
	text := continuationPayloadText(claimed.Payload)
	requestUserTurn := chatview.UITurn{
		TurnID:    metadata.TurnID,
		Role:      "user",
		Text:      text,
		Timestamp: claimed.CreatedAt,
	}

	runCtx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(nil)
	abortCh := make(chan struct{})
	injectCh := make(chan turn.InjectMessage, 16)
	started, err := s.sessionManager.StartExistingRun(
		runCtx,
		handle,
		func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
			return sessionruntime.RunAdmissionView{
				RequestUserTurn:      &requestUserTurn,
				SourceFollowUpItemID: string(item.ID),
			}, nil
		},
		cancelCause,
		abortCh,
		func() { cancelCause(context.Canceled) },
		injectCh,
	)
	if err != nil || started.RunID == "" {
		if s.logger != nil {
			s.logger.Warn("continuation runtime start failed",
				slog.String("run_id", runID),
				slog.Any("error", err))
		}
		s.rejectLostContinuation(context.WithoutCancel(ctx), runID, handle.FencingToken)
		return
	}
	runCtx = s.withAdmissionRuntimeFence(runCtx, sessionruntime.Admission{Handle: started, Started: true})

	req := ChatRequest{
		BotID: metadata.BotID, ChatID: metadata.BotID, ThreadID: metadata.SessionID,
		RunID: metadata.RunID, RunHandle: started, TurnID: metadata.TurnID,
		TurnPosition: func() *int64 { p := metadata.TurnPosition; return &p }(),
		Query:        text, RawQuery: text, SessionType: "chat",
		InjectCh: injectCh, QueueInjectCh: injectCh, QueueFollowUpClaim: claimed.Claim,
		PublishRuntimeEvents: true,
	}
	chunks, errs := s.StreamChat(runCtx, req)
	for range chunks {
		// Continuations are server-owned. Their events are persisted and
		// projected by the normal stream path; no client channel is attached.
	}
	var streamErr error
	for err := range errs {
		if err != nil {
			streamErr = err
		}
	}
	status := sessionruntime.RunStatusCompleted
	if streamErr != nil {
		status = sessionruntime.RunStatusErrored
	}
	if err := s.sessionManager.FinishRun(context.WithoutCancel(ctx), started, status, errorString(streamErr)); err != nil && s.logger != nil {
		s.logger.Warn("continuation finalization failed", slog.String("run_id", runID), slog.Any("error", err))
	}
}

func continuationPayloadText(payload []byte) string {
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(payload, &body) == nil && strings.TrimSpace(body.Text) != "" {
		return body.Text
	}
	return strings.TrimSpace(string(payload))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Service) rejectLostContinuation(ctx context.Context, runID string, fencingToken int64) {
	if rejector, ok := s.queueCoordinator.(sessionqueue.LostContinuationRejector); ok {
		if err := rejector.RejectLostContinuation(ctx, runID, fencingToken); err != nil && s.logger != nil {
			s.logger.Warn("continuation rejection failed", slog.String("run_id", runID), slog.Any("error", err))
		}
	}
}

func (s *Service) rejectLostQueuedRun(ctx context.Context, runID string, fencingToken int64) {
	if rejector, ok := s.queueCoordinator.(sessionqueue.LostQueuedRunRejector); ok {
		if err := rejector.RejectLostQueuedRun(ctx, runID, fencingToken); err != nil && s.logger != nil {
			s.logger.Warn("queued run rejection failed", slog.String("run_id", runID), slog.Any("error", err))
		}
	}
}

func (s *Service) reconcileLostQueueContinuation(ctx context.Context, terminal sessionruntime.TerminalRun) {
	if s == nil || terminal.State != sessionruntime.RunStatusLost || s.queueStore == nil {
		return
	}
	metadata, err := s.queueStore.GetContinuationRun(ctx, terminal.RunID)
	if err != nil || metadata.RunID == "" {
		return
	}
	s.rejectLostContinuation(context.WithoutCancel(ctx), terminal.RunID, terminal.FencingToken)
}

func (s *Service) reconcileTerminalQueue(ctx context.Context, terminal sessionruntime.TerminalRun) {
	if s == nil || s.queueCoordinator == nil {
		return
	}
	reconciler, ok := s.queueCoordinator.(sessionqueue.TerminalReconciler)
	if !ok {
		return
	}
	if err := reconciler.ReconcileTerminalRun(context.WithoutCancel(ctx), terminal); err != nil && s.logger != nil {
		s.logger.Warn("terminal queue reconciliation failed", slog.String("run_id", terminal.RunID), slog.Any("error", err))
	}
}
