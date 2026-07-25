package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	channelbackup "github.com/memohai/memoh/domains/channel/backup"
)

func (s *Store) Import(ctx context.Context, request channelbackup.ImportRequest) (channelbackup.ImportResult, error) {
	if _, err := uuid.Parse(request.BotID); err != nil {
		return channelbackup.ImportResult{}, fmt.Errorf("invalid channel import bot id: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return channelbackup.ImportResult{}, fmt.Errorf("begin channel observations import: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.botLock.LockBotExclusive(ctx, tx, request.BotID); err != nil {
		return channelbackup.ImportResult{}, fmt.Errorf("lock channel import bot: %w", err)
	}
	if request.Replace {
		if err := clearObservations(ctx, tx, request.BotID); err != nil {
			return channelbackup.ImportResult{}, err
		}
	}

	cursorKeys, err := importDiscussCursors(ctx, tx, request)
	if err != nil {
		return channelbackup.ImportResult{}, err
	}
	eventIDs, ownedEventIDs, err := importSessionEvents(ctx, tx, request)
	if err != nil {
		return channelbackup.ImportResult{}, err
	}
	routeBindings, err := importRouteBindings(ctx, tx, request)
	if err != nil {
		return channelbackup.ImportResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return channelbackup.ImportResult{}, fmt.Errorf("commit channel observations import: %w", err)
	}

	receipt := channelbackup.ImportReceipt{
		BotID:         request.BotID,
		EventIDs:      ownedEventIDs,
		Cursors:       cursorKeys,
		RouteBindings: routeBindings,
		Replace:       request.Replace,
	}
	return channelbackup.ImportResult{EventIDs: eventIDs, Receipt: receipt}, nil
}

func (s *Store) Compensate(ctx context.Context, receipt channelbackup.ImportReceipt) error {
	if _, err := uuid.Parse(receipt.BotID); err != nil {
		return fmt.Errorf("invalid channel compensation bot id: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin channel import compensation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.botLock.LockBotExclusive(ctx, tx, receipt.BotID); err != nil {
		return fmt.Errorf("lock channel compensation bot: %w", err)
	}
	for _, binding := range receipt.RouteBindings {
		if _, err := tx.Exec(ctx, `
			UPDATE channel.bot_channel_routes
			SET active_session_id = NULL, updated_at = now()
			WHERE team_id = iam.memoh_current_team_id()
			  AND bot_id = $1 AND id = $2 AND active_session_id = $3
		`, receipt.BotID, binding.RouteID, binding.SessionID); err != nil {
			return fmt.Errorf("compensate route active session: %w", err)
		}
	}
	if len(receipt.EventIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM channel.bot_session_events
			WHERE team_id = iam.memoh_current_team_id()
			  AND bot_id = $1
			  AND id = ANY($2::uuid[])
		`, receipt.BotID, receipt.EventIDs); err != nil {
			return fmt.Errorf("compensate session events: %w", err)
		}
	}
	for _, key := range receipt.Cursors {
		if _, err := tx.Exec(ctx, `
			DELETE FROM channel.bot_session_discuss_cursors
			WHERE team_id = iam.memoh_current_team_id()
			  AND bot_id = $1
			  AND session_id = $2
			  AND scope_key = $3
		`, receipt.BotID, key.SessionID, key.ScopeKey); err != nil {
			return fmt.Errorf("compensate discuss cursor: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit channel import compensation: %w", err)
	}
	return nil
}

func clearObservations(ctx context.Context, tx pgx.Tx, botID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE channel.bot_channel_routes
		SET active_session_id = NULL, updated_at = now()
		WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1
	`, botID); err != nil {
		return fmt.Errorf("clear route active sessions: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM channel.bot_session_discuss_cursors
		WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1
	`, botID); err != nil {
		return fmt.Errorf("clear discuss cursors: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM channel.bot_session_events
		WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1
	`, botID); err != nil {
		return fmt.Errorf("clear session events: %w", err)
	}
	return nil
}

func importDiscussCursors(
	ctx context.Context,
	tx pgx.Tx,
	request channelbackup.ImportRequest,
) ([]channelbackup.CursorKey, error) {
	keys := make([]channelbackup.CursorKey, 0, len(request.DiscussCursors))
	for _, item := range request.DiscussCursors {
		sessionID := request.SessionIDs[item.SessionID]
		if sessionID == "" || strings.TrimSpace(item.ScopeKey) == "" {
			continue
		}
		routeID := mappedPointer(item.RouteID, request.RouteIDs)
		updatedAt := optionalTime(item.UpdatedAt)
		if _, err := tx.Exec(ctx, `
			INSERT INTO channel.bot_session_discuss_cursors (
				session_id, bot_id, scope_key, route_id, source, consumed_cursor, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, COALESCE($7::timestamptz, now())
			)
			ON CONFLICT (team_id, session_id, scope_key) DO UPDATE SET
				bot_id = EXCLUDED.bot_id,
				route_id = EXCLUDED.route_id,
				source = EXCLUDED.source,
				consumed_cursor = EXCLUDED.consumed_cursor,
				updated_at = EXCLUDED.updated_at
		`, sessionID, request.BotID, item.ScopeKey, routeID, item.Source, item.ConsumedCursor, updatedAt); err != nil {
			return nil, fmt.Errorf("restore discuss cursor: %w", err)
		}
		keys = append(keys, channelbackup.CursorKey{SessionID: sessionID, ScopeKey: item.ScopeKey})
	}
	return keys, nil
}

func importSessionEvents(
	ctx context.Context,
	tx pgx.Tx,
	request channelbackup.ImportRequest,
) (map[string]string, []string, error) {
	eventIDs := make(map[string]string, len(request.SessionEvents))
	ownedEventIDs := make([]string, 0, len(request.SessionEvents))
	for _, item := range request.SessionEvents {
		sessionID := request.SessionIDs[item.SessionID]
		if sessionID == "" || item.ID == "" {
			continue
		}
		if len(bytes.TrimSpace(item.EventData)) == 0 {
			return nil, nil, fmt.Errorf("restore session event %s: event data is empty", item.ID)
		}
		newID := deterministicID(request.BotID, "event", item.ID)
		senderID := mappedPointer(item.SenderChannelIdentityID, request.ChannelIdentityIDs)
		createdAt := optionalTime(item.CreatedAt)
		eventData := sanitizeRestoredEventData(item.EventData)
		tag, err := tx.Exec(ctx, `
			INSERT INTO channel.bot_session_events (
				id, bot_id, session_id, event_kind, event_data,
				external_message_id, sender_channel_identity_id,
				received_at_ms, created_at
			) VALUES (
				$1, $2, $3, $4, $5::jsonb, $6, $7, $8,
				COALESCE($9::timestamptz, now())
			)
			ON CONFLICT DO NOTHING
		`, newID, request.BotID, sessionID, item.EventKind, eventData,
			item.ExternalMessageID, senderID, item.ReceivedAtMS, createdAt)
		if err != nil {
			return nil, nil, fmt.Errorf("restore session event: %w", err)
		}
		owned := tag.RowsAffected() == 1
		if tag.RowsAffected() == 0 {
			resolvedID, resolvedOwned, err := resolveExistingEvent(ctx, tx, newID, sessionID, item)
			if err != nil {
				return nil, nil, err
			}
			newID = resolvedID
			owned = resolvedOwned
		}
		eventIDs[item.ID] = newID
		if owned {
			ownedEventIDs = append(ownedEventIDs, newID)
		}
	}
	sort.Strings(ownedEventIDs)
	return eventIDs, ownedEventIDs, nil
}

func resolveExistingEvent(
	ctx context.Context,
	tx pgx.Tx,
	deterministicID string,
	sessionID string,
	item channelbackup.SessionEvent,
) (string, bool, error) {
	var resolved string
	err := tx.QueryRow(ctx, `
		SELECT id::text
		FROM channel.bot_session_events
		WHERE team_id = iam.memoh_current_team_id()
		  AND (
			id = $1
			OR (
				session_id = $2
				AND event_kind = $3
				AND external_message_id IS NOT DISTINCT FROM $4::text
			)
		  )
		ORDER BY (id = $1) DESC
		LIMIT 1
	`, deterministicID, sessionID, item.EventKind, item.ExternalMessageID).Scan(&resolved)
	if err != nil {
		return "", false, fmt.Errorf("resolve idempotent session event: %w", err)
	}
	return resolved, resolved == deterministicID, nil
}

func importRouteBindings(
	ctx context.Context,
	tx pgx.Tx,
	request channelbackup.ImportRequest,
) ([]channelbackup.RouteBinding, error) {
	bindings := make([]channelbackup.RouteBinding, 0, len(request.RouteSessions))
	for _, item := range request.RouteSessions {
		routeID := request.RouteIDs[item.RouteID]
		sessionID := request.SessionIDs[item.SessionID]
		if routeID == "" || sessionID == "" {
			continue
		}
		tag, err := tx.Exec(ctx, `
			UPDATE channel.bot_channel_routes
			SET active_session_id = $3, updated_at = now()
			WHERE team_id = iam.memoh_current_team_id()
			  AND bot_id = $1
			  AND id = $2
		`, request.BotID, routeID, sessionID)
		if err != nil {
			return nil, fmt.Errorf("restore route active session: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil, errors.New("restore route active session: mapped route or session not found")
		}
		bindings = append(bindings, channelbackup.RouteBinding{RouteID: routeID, SessionID: sessionID})
	}
	return bindings, nil
}

func deterministicID(botID, kind, oldID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(kind+"\x00"+botID+"\x00"+oldID)).String()
}

func mappedPointer(oldID *string, ids map[string]string) *string {
	if oldID == nil {
		return nil
	}
	newID := ids[*oldID]
	if newID == "" {
		return nil
	}
	return &newID
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

// sanitizeRestoredEventData strips instance-local event cursors from restored
// payloads. Cursors are coordinates in the source deployment's sequence and
// cannot be compared against this one's, so keeping them would poison local
// discuss watermarks. Cursor-less events gate correctly in the source-time
// domain, which is what a restored history falls back to.
func sanitizeRestoredEventData(eventData []byte) []byte {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(eventData, &payload); err != nil {
		return eventData
	}
	if _, ok := payload["event_cursor"]; !ok {
		return eventData
	}
	delete(payload, "event_cursor")
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return eventData
	}
	return sanitized
}
