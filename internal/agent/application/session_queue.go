package application

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
)

// SessionQueues is the application surface for user-facing queue operations.
// Items are transient and live in the configured memory or Redis runtime.
type SessionQueues struct {
	SteerSupported bool
	Steer          []sessionruntime.SteerItem
	FollowUp       []sessionruntime.FollowUpItem
}

// ErrQueueInputIncomplete reports an ingress that queued a command without
// the team, bot, or session the continuation needs. It is a wiring error at
// the ingress, not a rejected user request, so handlers surface it as
// unavailable.
var ErrQueueInputIncomplete = errors.New("queue: command is missing team, bot, or session")

func (s *Service) liveQueueRuntime() (*sessionruntime.Manager, error) {
	if s == nil || s.sessionManager == nil {
		return nil, sessionruntime.ErrLiveQueueUnavailable
	}
	return s.sessionManager, nil
}

// checkQueuedCommand admits the command an ingress queues: the turn its text
// would have started had the session been idle, built by the code that starts
// that turn. The queue stores it whole, because the run that replays it
// starts after the request is gone and, in a hosted deployment, possibly on
// another instance: nothing then can recover who sent the text, the account
// it runs for, or the conversation it answers in.
func checkQueuedCommand(invocationID string, cmd turn.StartTurnCommand) (string, error) {
	if strings.TrimSpace(cmd.TeamID) == "" || strings.TrimSpace(cmd.BotID) == "" || strings.TrimSpace(cmd.ThreadID) == "" {
		return "", ErrQueueInputIncomplete
	}
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" || strings.TrimSpace(cmd.Query) == "" {
		return "", sessionruntime.ErrQueueInvalidReference
	}
	return invocationID, nil
}

// EnqueueSteer queues text for the session's running turn. The steer joins
// that run, so only its text and sender are used; the command keeps the
// sender, who alone may edit it.
func (s *Service) EnqueueSteer(ctx context.Context, invocationID string, cmd turn.StartTurnCommand) (sessionruntime.SteerItem, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return sessionruntime.SteerItem{}, err
	}
	invocationID, err = checkQueuedCommand(invocationID, cmd)
	if err != nil {
		return sessionruntime.SteerItem{}, err
	}
	payload, err := encodeQueueCommand(cmd)
	if err != nil {
		return sessionruntime.SteerItem{}, err
	}
	return runtime.EnqueueSteer(ctx, sessionruntime.Key{BotID: cmd.BotID, SessionID: cmd.ThreadID}, uuid.NewString(), invocationID, payload)
}

// EnqueueFollowUp queues a turn to start after the session's running one.
func (s *Service) EnqueueFollowUp(ctx context.Context, invocationID string, cmd turn.StartTurnCommand) (sessionruntime.FollowUpItem, error) {
	invocationID, err := checkQueuedCommand(invocationID, cmd)
	if err != nil {
		return sessionruntime.FollowUpItem{}, err
	}
	return s.enqueueFollowUpCommand(ctx, invocationID, cmd)
}

// enqueueFollowUpCommand is the single write path into the follow-up queue.
// Text queued from the Web and complete turns deferred by a busy channel both
// arrive here as a StartTurnCommand, so the queue holds one payload shape and
// the continuation has one way to read it.
func (s *Service) enqueueFollowUpCommand(ctx context.Context, invocationID string, cmd turn.StartTurnCommand) (sessionruntime.FollowUpItem, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return sessionruntime.FollowUpItem{}, err
	}
	payload, err := encodeQueueCommand(cmd)
	if err != nil {
		return sessionruntime.FollowUpItem{}, err
	}
	item, err := runtime.EnqueueFollowUp(ctx, sessionruntime.Key{BotID: cmd.BotID, SessionID: cmd.ThreadID}, uuid.NewString(), invocationID, payload)
	if err != nil {
		return sessionruntime.FollowUpItem{}, err
	}
	if item.Status == sessionruntime.QueueAccepted {
		s.kickFollowUpIfIdle(ctx, cmd.BotID, cmd.ThreadID, item.EnqueuedDuringRunID)
	}
	return item, nil
}

func (s *Service) ListSessionQueues(ctx context.Context, botID, sessionID string) (SessionQueues, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return SessionQueues{}, err
	}
	steers, followUps, err := runtime.PendingQueues(ctx, sessionruntime.Key{BotID: botID, SessionID: sessionID}, 0)
	if err != nil {
		return SessionQueues{}, err
	}
	snapshot, err := runtime.Snapshot(ctx, botID, sessionID)
	if err != nil {
		return SessionQueues{}, err
	}
	return SessionQueues{Steer: steers, FollowUp: followUps, SteerSupported: sessionruntime.SteerRunAvailable(snapshot.CurrentRunView)}, nil
}

func (s *Service) ReorderSteer(ctx context.Context, botID, sessionID string, item, before sessionruntime.SteerPendingRef) ([]sessionruntime.SteerItem, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return nil, err
	}
	return runtime.ReorderSteer(ctx, sessionruntime.Key{BotID: botID, SessionID: sessionID}, item, before)
}

func (s *Service) ReorderFollowUp(ctx context.Context, botID, sessionID string, item, before sessionruntime.FollowUpPendingRef) ([]sessionruntime.FollowUpItem, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return nil, err
	}
	return runtime.ReorderFollowUp(ctx, sessionruntime.Key{BotID: botID, SessionID: sessionID}, item, before)
}

// UpdateSteer replaces the text of an accepted steer on behalf of editorUserID,
// who must be the sender the item recorded. Sender, routing, and attachment
// metadata are immutable for a queued command; only the text is rewritten, and
// the backend rechecks accepted status atomically with the edit.
func (s *Service) UpdateSteer(ctx context.Context, botID, sessionID, itemID, editorUserID, text string) (sessionruntime.SteerItem, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return sessionruntime.SteerItem{}, err
	}
	key := sessionruntime.Key{BotID: botID, SessionID: sessionID}
	items, _, err := runtime.PendingQueues(ctx, key, 0)
	if err != nil {
		return sessionruntime.SteerItem{}, err
	}
	for _, item := range items {
		if string(item.ID) != itemID {
			continue
		}
		payload, err := rewriteQueuePayloadText(item.Payload, editorUserID, text)
		if err != nil {
			return sessionruntime.SteerItem{}, err
		}
		return runtime.UpdateSteer(ctx, key, item.ID, payload)
	}
	return sessionruntime.SteerItem{}, sessionruntime.ErrQueueNotPending
}

// UpdateFollowUp replaces the text of an accepted follow-up. See UpdateSteer.
func (s *Service) UpdateFollowUp(ctx context.Context, botID, sessionID, itemID, editorUserID, text string) (sessionruntime.FollowUpItem, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return sessionruntime.FollowUpItem{}, err
	}
	key := sessionruntime.Key{BotID: botID, SessionID: sessionID}
	_, items, err := runtime.PendingQueues(ctx, key, 0)
	if err != nil {
		return sessionruntime.FollowUpItem{}, err
	}
	for _, item := range items {
		if string(item.ID) != itemID {
			continue
		}
		payload, err := rewriteQueuePayloadText(item.Payload, editorUserID, text)
		if err != nil {
			return sessionruntime.FollowUpItem{}, err
		}
		return runtime.UpdateFollowUp(ctx, key, item.ID, payload)
	}
	return sessionruntime.FollowUpItem{}, sessionruntime.ErrQueueNotPending
}

func (s *Service) CancelSteer(ctx context.Context, botID, sessionID, itemID string) error {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return err
	}
	return runtime.CancelSteer(ctx, sessionruntime.Key{BotID: botID, SessionID: sessionID}, sessionruntime.SteerItemID(itemID))
}

func (s *Service) CancelFollowUp(ctx context.Context, botID, sessionID, itemID string) error {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return err
	}
	return runtime.CancelFollowUp(ctx, sessionruntime.Key{BotID: botID, SessionID: sessionID}, sessionruntime.FollowUpItemID(itemID))
}

func (s *Service) PromoteFollowUpToSteer(ctx context.Context, botID, sessionID string, followUp sessionruntime.FollowUpPendingRef) (sessionruntime.PromoteFollowUpResult, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return sessionruntime.PromoteFollowUpResult{}, err
	}
	return runtime.PromoteFollowUpToSteer(ctx, sessionruntime.Key{BotID: botID, SessionID: sessionID}, followUp)
}
