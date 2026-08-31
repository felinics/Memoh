package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// ErrExternalForkUnsupported reports a fork request against an external
// runtime that cannot fork.
var ErrExternalForkUnsupported = errors.New("runtime does not support forking")

// ErrExternalForkAnchorMissing reports a fork anchored at a turn that
// recorded no runtime turn id, so the runtime-side cut cannot match the
// visible history.
var ErrExternalForkAnchorMissing = errors.New("fork source turn has no runtime turn anchor")

// PrepareExternalFork forks the runtime-side conversation behind an external
// session and returns the complete runtime metadata the forked Memoh session
// must carry. The anchor turn's recorded runtime turn id bounds the
// runtime-side fork to the same cut as the visible history.
func (s *Service) PrepareExternalFork(ctx context.Context, botID, sessionID, turnID string) (map[string]any, error) {
	if s == nil || s.sessionService == nil {
		return nil, errors.New("session service not configured")
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := validateSessionBot(botID, sessionID, sess.BotID); err != nil {
		return nil, err
	}
	if !session.IsDirectRuntime(sess) {
		return nil, fmt.Errorf("session %s is not on an external runtime", sessionID)
	}
	runtimeType := strings.TrimSpace(sess.RuntimeType)
	driver, ok := s.externalDrivers[runtimeType]
	if !ok {
		return nil, apperror.New(apperror.CodeExternalRuntimeUnavailable, map[string]string{"runtime": runtimeType})
	}
	forker, ok := driver.(external.ThreadForker)
	if !ok {
		return nil, ErrExternalForkUnsupported
	}
	lastTurnID, err := s.runtimeTurnIDForTurn(ctx, botID, sessionID, turnID)
	if err != nil {
		return nil, err
	}
	delta, err := forker.ForkThread(ctx, botID, sess.BotAgentID, runtimeSessionMeta(sess), lastTurnID)
	if err != nil {
		return nil, err
	}
	override := make(map[string]any, len(sess.RuntimeMetadata)+len(delta))
	for key, value := range sess.RuntimeMetadata {
		override[key] = value
	}
	for key, value := range delta {
		override[key] = value
	}
	return override, nil
}

// runtimeTurnIDForTurn reads the runtime turn id recorded on the anchor
// turn's run. Every external round records one in its round transaction, so
// a missing anchor is an error rather than a silent fork at the runtime
// head.
func (s *Service) runtimeTurnIDForTurn(ctx context.Context, botID, sessionID, turnID string) (string, error) {
	if s.queries == nil {
		return "", errors.New("queries not configured")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return "", fmt.Errorf("invalid bot id: %w", err)
	}
	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return "", fmt.Errorf("invalid session id: %w", err)
	}
	pgTurnID, err := db.ParseUUID(turnID)
	if err != nil {
		return "", fmt.Errorf("invalid turn id: %w", err)
	}
	runtimeTurnID, err := s.queries.GetTurnAgentTurnID(ctx, sqlc.GetTurnAgentTurnIDParams{
		BotID:     pgBotID,
		SessionID: pgSessionID,
		TurnID:    pgTurnID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrExternalForkAnchorMissing
		}
		return "", fmt.Errorf("load fork anchor: %w", err)
	}
	if runtimeTurnID = strings.TrimSpace(runtimeTurnID); runtimeTurnID == "" {
		return "", ErrExternalForkAnchorMissing
	}
	return runtimeTurnID, nil
}
