package application

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	chatview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type runtimeSteering struct {
	manager *sessionruntime.Manager
	run     sessionruntime.RunHandle
	mu      sync.Mutex
	pending map[string]runtimeSteerClaim
}

type runtimeSteerClaim struct {
	item sessionruntime.SteerItem
	ref  sessionruntime.SteerClaimRef
}

func (s *Service) runtimeSteering(req ChatRequest) external.Steering {
	if s.sessionManager == nil || req.RunHandle.RunID == "" || req.AgentCommand != "" {
		return nil
	}
	return &runtimeSteering{manager: s.sessionManager, run: req.RunHandle, pending: make(map[string]runtimeSteerClaim)}
}

func (q *runtimeSteering) Enable(ctx context.Context) error { return q.manager.EnableSteer(ctx, q.run) }
func (q *runtimeSteering) Wake() <-chan struct{}            { return q.manager.SteerWake(q.run) }
func (q *runtimeSteering) Next(ctx context.Context) (external.SteerInput, bool, error) {
	item, ref, ok, err := q.manager.ClaimNextSteer(ctx, q.run, false)
	if err != nil || !ok {
		return external.SteerInput{}, false, err
	}
	q.mu.Lock()
	q.pending[string(item.ID)] = runtimeSteerClaim{item, ref}
	q.mu.Unlock()
	return external.SteerInput{ID: string(item.ID), Text: QueuePayloadText(item.Payload)}, true, nil
}

func (q *runtimeSteering) Accepted(ctx context.Context, id string, step int) error {
	q.mu.Lock()
	claim, ok := q.pending[id]
	q.mu.Unlock()
	if !ok {
		return sessionruntime.ErrQueueInvalidReference
	}
	// The driver's sequencing marker makes the live user bubble appear after
	// all earlier output, even when the WebSocket consumer is slower than Codex.
	if err := q.manager.PublishQueueUserTurns(ctx, q.run, sessionruntime.QueueUserTurnUpdate{
		ClaimedSteerItemID: id, ClaimedSteerText: QueuePayloadText(claim.item.Payload),
		ClaimedSteerTimestamp: claim.item.CreatedAt, AfterStepIndex: &step,
	}); err != nil {
		return err
	}
	if err := q.manager.ApplySteer(ctx, sessionruntime.Key{BotID: q.run.BotID, SessionID: q.run.SessionID}, claim.ref); err != nil {
		return err
	}
	q.mu.Lock()
	delete(q.pending, id)
	q.mu.Unlock()
	return q.manager.PublishQueueUserTurns(ctx, q.run, sessionruntime.QueueUserTurnUpdate{AppliedSteerItemID: id})
}

func (q *runtimeSteering) Close(ctx context.Context) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := q.manager.DisableSteer(cleanup, q.run); err != nil {
		return err
	}
	return q.manager.CloseSteerRun(cleanup, sessionruntime.Key{BotID: q.run.BotID, SessionID: q.run.SessionID}, q.run.RunID)
}

// Reconcile provisional steer bubbles to the identities assigned by the normal
// round transaction before publishing terminal state.
func (s *Service) publishRuntimeSteerHistory(ctx context.Context, req ChatRequest, ids []string, persisted []messagepkg.Message) {
	if len(ids) == 0 || s.sessionManager == nil {
		return
	}
	var users []chatview.UITurn
	for _, turn := range chatview.ConvertMessagesToUITurns(persisted) {
		if turn.Role == "user" {
			users = append(users, turn)
		}
	}
	if len(users) < len(ids) {
		s.logger.Error("runtime steer history identity missing", slog.String("run_id", req.RunID))
		return
	}
	users = users[len(users)-len(ids):]
	for i, id := range ids {
		if err := s.sessionManager.PublishQueueUserTurns(ctx, req.RunHandle, sessionruntime.QueueUserTurnUpdate{
			PersistedTurns: []chatview.UITurn{users[i]}, AppliedSteerItemID: id, AppliedSteerTurn: &users[i],
		}); err != nil {
			s.logger.Warn("runtime steer history projection failed", slog.Any("error", err))
			return
		}
	}
}
