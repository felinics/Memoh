package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	chatbackup "github.com/memohai/memoh/domains/agent/chat/backup"
)

// BotExclusiveLocker exclusively locks api.bots for import/compensate sequencing.
// Composition must supply an API/Bot-owned implementation; Agent must not query api.bots.
type BotExclusiveLocker interface {
	LockBotExclusive(ctx context.Context, tx pgx.Tx, botID string) error
}

type Store struct {
	pool    *pgxpool.Pool
	botLock BotExclusiveLocker
}

var (
	_ chatbackup.ExportReader  = (*Store)(nil)
	_ chatbackup.ImportWriter  = (*Store)(nil)
	_ chatbackup.SummaryReader = (*Store)(nil)
)

func New(pool *pgxpool.Pool, botLock BotExclusiveLocker) (*Store, error) {
	if pool == nil {
		return nil, errors.New("chat backup postgres store requires a pool")
	}
	if botLock == nil {
		return nil, errors.New("chat backup postgres store requires a bot exclusive locker")
	}
	return &Store{pool: pool, botLock: botLock}, nil
}

func (s *Store) Export(ctx context.Context, request chatbackup.ExportRequest) (chatbackup.Snapshot, error) {
	if request.BotID == "" {
		return chatbackup.Snapshot{}, errors.New("chat backup export requires a bot id")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return chatbackup.Snapshot{}, fmt.Errorf("begin chat backup export: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	snapshot := chatbackup.Snapshot{}
	if snapshot.Sessions, err = exportSessions(ctx, tx, request.BotID); err != nil {
		return chatbackup.Snapshot{}, err
	}
	if snapshot.Messages, err = exportMessages(ctx, tx, request.BotID); err != nil {
		return chatbackup.Snapshot{}, err
	}
	if request.IncludeAssets {
		if snapshot.Assets, err = exportAssets(ctx, tx, request.BotID); err != nil {
			return chatbackup.Snapshot{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return chatbackup.Snapshot{}, fmt.Errorf("commit chat backup export: %w", err)
	}
	return snapshot, nil
}

func (s *Store) Summary(ctx context.Context, botID string) (chatbackup.Summary, error) {
	if botID == "" {
		return chatbackup.Summary{}, errors.New("chat backup summary requires a bot id")
	}
	var summary chatbackup.Summary
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM agent.bot_sessions
			 WHERE team_id = iam.memoh_current_team_id()
			   AND bot_id = $1 AND deleted_at IS NULL),
			(SELECT count(*) FROM agent.bot_history_messages
			 WHERE team_id = iam.memoh_current_team_id()
			   AND bot_id = $1),
			(SELECT count(*) FROM agent.bot_history_message_assets asset
			 JOIN agent.bot_history_messages message
			   ON message.team_id = asset.team_id AND message.id = asset.message_id
			 WHERE asset.team_id = iam.memoh_current_team_id()
			   AND message.bot_id = $1)
	`, botID).Scan(&summary.Sessions, &summary.Messages, &summary.Assets)
	if err != nil {
		return chatbackup.Summary{}, fmt.Errorf("summarize chat backup: %w", err)
	}
	return summary, nil
}

func exportSessions(ctx context.Context, tx pgx.Tx, botID string) ([]chatbackup.Session, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			id::text, bot_id::text, route_id::text, channel_type, type,
			session_mode, runtime_type, runtime_metadata, title, metadata,
			parent_session_id::text, created_by_user_id::text,
			created_at, updated_at, deleted_at
		FROM agent.bot_sessions
		WHERE team_id = iam.memoh_current_team_id()
		  AND bot_id = $1 AND deleted_at IS NULL
		ORDER BY updated_at DESC
	`, botID)
	if err != nil {
		return nil, fmt.Errorf("export chat sessions: %w", err)
	}
	defer rows.Close()

	items := make([]chatbackup.Session, 0)
	for rows.Next() {
		var item chatbackup.Session
		if err := rows.Scan(
			&item.ID, &item.BotID, &item.RouteID, &item.ChannelType, &item.Type,
			&item.SessionMode, &item.RuntimeType, &item.RuntimeMetadata, &item.Title,
			&item.Metadata, &item.ParentSessionID, &item.CreatedByUserID,
			&item.CreatedAt, &item.UpdatedAt, &item.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan chat session backup: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export chat sessions: %w", err)
	}
	return items, nil
}

func exportMessages(ctx context.Context, tx pgx.Tx, botID string) ([]chatbackup.Message, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			message.id::text,
			message.bot_id::text,
			message.session_id::text,
			message.sender_channel_identity_id::text,
			message.sender_account_user_id::text,
			message.source_message_id,
			message.source_reply_to_message_id,
			message.role,
			message.content,
			message.metadata,
			message.usage,
			message.session_mode,
			message.runtime_type,
			message.event_id::text,
			message.display_text,
			message.created_at,
			message.turn_id::text,
			message.turn_position,
			message.turn_message_seq,
			message.turn_visible,
			message.turn_superseded_by_turn_id::text,
			message.turn_superseded_at,
			message.turn_superseded_reason,
			message.sender_display_name,
			message.sender_avatar_url,
			session.channel_type
		FROM agent.bot_history_messages message
		LEFT JOIN agent.bot_sessions session
		  ON session.team_id = message.team_id
		 AND session.id = message.session_id
		WHERE message.team_id = iam.memoh_current_team_id()
		  AND message.bot_id = $1
		ORDER BY message.created_at ASC, message.id ASC
	`, botID)
	if err != nil {
		return nil, fmt.Errorf("export chat messages: %w", err)
	}
	defer rows.Close()

	items := make([]chatbackup.Message, 0)
	for rows.Next() {
		var item chatbackup.Message
		if err := rows.Scan(
			&item.ID, &item.BotID, &item.SessionID,
			&item.SenderChannelIdentityID, &item.SenderUserID,
			&item.ExternalMessageID, &item.SourceReplyToMessageID,
			&item.Role, &item.Content, &item.Metadata, &item.Usage,
			&item.SessionMode, &item.RuntimeType, &item.EventID,
			&item.DisplayText, &item.CreatedAt, &item.TurnID,
			&item.TurnPosition, &item.TurnMessageSeq, &item.TurnVisible,
			&item.TurnSupersededByTurnID, &item.TurnSupersededAt,
			&item.TurnSupersededReason, &item.SenderDisplayName,
			&item.SenderAvatarURL, &item.Platform,
		); err != nil {
			return nil, fmt.Errorf("scan chat message backup: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export chat messages: %w", err)
	}
	return items, nil
}

func exportAssets(ctx context.Context, tx pgx.Tx, botID string) ([]chatbackup.Asset, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			asset.id::text, asset.message_id::text, asset.role, asset.ordinal,
			asset.content_hash, asset.name, asset.metadata
		FROM agent.bot_history_message_assets asset
		JOIN agent.bot_history_messages message
		  ON message.team_id = asset.team_id AND message.id = asset.message_id
		WHERE asset.team_id = iam.memoh_current_team_id()
		  AND message.bot_id = $1
		ORDER BY asset.message_id, asset.ordinal
	`, botID)
	if err != nil {
		return nil, fmt.Errorf("export chat message assets: %w", err)
	}
	defer rows.Close()

	items := make([]chatbackup.Asset, 0)
	for rows.Next() {
		var item chatbackup.Asset
		if err := rows.Scan(
			&item.RelID, &item.MessageID, &item.Role, &item.Ordinal,
			&item.ContentHash, &item.Name, &item.Metadata,
		); err != nil {
			return nil, fmt.Errorf("scan chat message asset backup: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export chat message assets: %w", err)
	}
	return items, nil
}
