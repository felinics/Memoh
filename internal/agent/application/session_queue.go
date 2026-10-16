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

// QueueInput is one text input an ingress admits into a session queue. It
// carries the identity the ingress authenticated, because the continuation
// that eventually runs the input happens after the request is gone and, in a
// hosted deployment, possibly on another instance: nothing at that point can
// recover which team or user the text belonged to.
type QueueInput struct {
	TeamID       string
	BotID        string
	SessionID    string
	InvocationID string
	// UserID and SourceChannelIdentityID name the sender the way the
	// ingress's ordinary turns do. Empty values are allowed for a channel
	// that has no platform account for the sender.
	UserID                  string
	SourceChannelIdentityID string
	Text                    string
}

// ErrQueueInputIncomplete reports an ingress that admitted queue input without
// the identity the continuation needs. It is a wiring error at the ingress,
// not a rejected user request, so handlers surface it as unavailable.
var ErrQueueInputIncomplete = errors.New("queue: input is missing team, bot, or session identity")

func (s *Service) liveQueueRuntime() (*sessionruntime.Manager, error) {
	if s == nil || s.sessionManager == nil {
		return nil, sessionruntime.ErrLiveQueueUnavailable
	}
	return s.sessionManager, nil
}

// queueTurnCommand rebuilds the ordinary chat turn a queued text would have
// started had the session been idle. Every queue item stores this shape, so
// the continuation replays a command instead of inferring one.
func queueTurnCommand(input QueueInput) (turn.StartTurnCommand, error) {
	teamID, botID, sessionID := strings.TrimSpace(input.TeamID), strings.TrimSpace(input.BotID), strings.TrimSpace(input.SessionID)
	if teamID == "" || botID == "" || sessionID == "" {
		return turn.StartTurnCommand{}, ErrQueueInputIncomplete
	}
	text := strings.TrimSpace(input.Text)
	if text == "" || strings.TrimSpace(input.InvocationID) == "" {
		return turn.StartTurnCommand{}, sessionruntime.ErrQueueInvalidReference
	}
	return turn.StartTurnCommand{
		SchemaVersion:           1,
		TeamID:                  teamID,
		Mode:                    turn.ModeChat,
		BotID:                   botID,
		ChatID:                  botID,
		ThreadID:                sessionID,
		UserID:                  strings.TrimSpace(input.UserID),
		SourceChannelIdentityID: strings.TrimSpace(input.SourceChannelIdentityID),
		Query:                   text,
		UserVisibleText:         text,
	}, nil
}

func (s *Service) EnqueueSteer(ctx context.Context, input QueueInput) (sessionruntime.SteerItem, error) {
	runtime, err := s.liveQueueRuntime()
	if err != nil {
		return sessionruntime.SteerItem{}, err
	}
	cmd, err := queueTurnCommand(input)
	if err != nil {
		return sessionruntime.SteerItem{}, err
	}
	payload, err := encodeQueueCommand(cmd)
	if err != nil {
		return sessionruntime.SteerItem{}, err
	}
	return runtime.EnqueueSteer(ctx, sessionruntime.Key{BotID: cmd.BotID, SessionID: cmd.ThreadID}, uuid.NewString(), strings.TrimSpace(input.InvocationID), payload)
}

func (s *Service) EnqueueFollowUp(ctx context.Context, input QueueInput) (sessionruntime.FollowUpItem, error) {
	cmd, err := queueTurnCommand(input)
	if err != nil {
		return sessionruntime.FollowUpItem{}, err
	}
	return s.enqueueFollowUpCommand(ctx, strings.TrimSpace(input.InvocationID), cmd)
}

// enqueueFollowUpCommand is the single write path into the follow-up queue.
// Text inputs from the queue panel and complete turns deferred by a busy
// channel both arrive here as a StartTurnCommand, so the queue holds one
// payload shape and the continuation has one way to read it.
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
