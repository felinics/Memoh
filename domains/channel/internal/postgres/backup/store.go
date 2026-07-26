package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	channelbackup "github.com/memohai/memoh/domains/channel/backup"
)

// BotExclusiveLocker exclusively locks api.bots for import/compensate sequencing.
// Composition must supply an API/Bot-owned implementation; Channel must not query api.bots.
type BotExclusiveLocker interface {
	LockBotExclusive(ctx context.Context, tx pgx.Tx, botID string) error
}

type Store struct {
	pool    *pgxpool.Pool
	botLock BotExclusiveLocker
}

var (
	_ channelbackup.ExportReader = (*Store)(nil)
	_ channelbackup.ImportWriter = (*Store)(nil)
)

func New(pool *pgxpool.Pool, botLock BotExclusiveLocker) (*Store, error) {
	if pool == nil {
		return nil, errors.New("channel backup postgres store requires a pool")
	}
	if botLock == nil {
		return nil, errors.New("channel backup postgres store requires a bot exclusive locker")
	}
	return &Store{pool: pool, botLock: botLock}, nil
}

func (s *Store) Export(ctx context.Context, botID string) (channelbackup.Snapshot, error) {
	if botID == "" {
		return channelbackup.Snapshot{}, errors.New("channel backup export requires a bot id")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return channelbackup.Snapshot{}, fmt.Errorf("begin channel backup export: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	snapshot := channelbackup.Snapshot{}
	if snapshot.DiscussCursors, err = exportDiscussCursors(ctx, tx, botID); err != nil {
		return channelbackup.Snapshot{}, err
	}
	if snapshot.SessionEvents, err = exportSessionEvents(ctx, tx, botID); err != nil {
		return channelbackup.Snapshot{}, err
	}
	if snapshot.RouteActiveSessions, err = exportRouteActiveSessions(ctx, tx, botID); err != nil {
		return channelbackup.Snapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return channelbackup.Snapshot{}, fmt.Errorf("commit channel backup export: %w", err)
	}
	return snapshot, nil
}

func exportDiscussCursors(ctx context.Context, tx pgx.Tx, botID string) ([]channelbackup.DiscussCursor, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			session_id::text, scope_key, route_id::text,
			source, consumed_cursor, updated_at, team_id::text
		FROM channel.bot_session_discuss_cursors
		WHERE team_id = iam.memoh_current_team_id()
		  AND bot_id = $1
		ORDER BY updated_at, session_id, scope_key
	`, botID)
	if err != nil {
		return nil, fmt.Errorf("export channel discuss cursors: %w", err)
	}
	defer rows.Close()

	items := make([]channelbackup.DiscussCursor, 0)
	for rows.Next() {
		var item channelbackup.DiscussCursor
		if err := rows.Scan(
			&item.SessionID, &item.ScopeKey, &item.RouteID, &item.Source,
			&item.ConsumedCursor, &item.UpdatedAt, &item.TeamID,
		); err != nil {
			return nil, fmt.Errorf("scan channel discuss cursor backup: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export channel discuss cursors: %w", err)
	}
	return items, nil
}

func exportSessionEvents(ctx context.Context, tx pgx.Tx, botID string) ([]channelbackup.SessionEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			id::text, bot_id::text, session_id::text, event_kind, event_data,
			external_message_id, sender_channel_identity_id::text,
			received_at_ms, created_at, team_id::text
		FROM channel.bot_session_events
		WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1
		ORDER BY received_at_ms, id
	`, botID)
	if err != nil {
		return nil, fmt.Errorf("export channel session events: %w", err)
	}
	defer rows.Close()

	items := make([]channelbackup.SessionEvent, 0)
	for rows.Next() {
		var item channelbackup.SessionEvent
		if err := rows.Scan(
			&item.ID, &item.BotID, &item.SessionID, &item.EventKind,
			&item.EventData, &item.ExternalMessageID,
			&item.SenderChannelIdentityID, &item.ReceivedAtMS,
			&item.CreatedAt, &item.TeamID,
		); err != nil {
			return nil, fmt.Errorf("scan channel session event backup: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export channel session events: %w", err)
	}
	return items, nil
}

func exportRouteActiveSessions(ctx context.Context, tx pgx.Tx, botID string) ([]channelbackup.RouteActiveSession, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, active_session_id::text
		FROM channel.bot_channel_routes
		WHERE team_id = iam.memoh_current_team_id()
		  AND bot_id = $1
		  AND active_session_id IS NOT NULL
		ORDER BY created_at, id
	`, botID)
	if err != nil {
		return nil, fmt.Errorf("export route active sessions: %w", err)
	}
	defer rows.Close()

	items := make([]channelbackup.RouteActiveSession, 0)
	for rows.Next() {
		var item channelbackup.RouteActiveSession
		if err := rows.Scan(&item.RouteID, &item.SessionID); err != nil {
			return nil, fmt.Errorf("scan route active session backup: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export route active sessions: %w", err)
	}
	return items, nil
}
