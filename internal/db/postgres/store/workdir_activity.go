package postgresstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (s *Store) ActiveWorkdirs(ctx context.Context, botID string) ([]dbstore.BotWorkdirRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	return activeWorkdirs(ctx, s.queries, id)
}

func (s *Store) WithWorkdirMutation(ctx context.Context, botID string, fn func([]dbstore.BotWorkdirRecord) error) error {
	if s.pool == nil {
		return errors.New("workdir mutation requires a transaction pool")
	}
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if _, err := q.LockBotForSessionRunClaim(ctx, id); err != nil {
		return err
	}
	active, err := activeWorkdirs(ctx, q, id)
	if err != nil {
		return err
	}
	if err := fn(active); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func activeWorkdirs(ctx context.Context, q *dbsqlc.Queries, botID pgtype.UUID) ([]dbstore.BotWorkdirRecord, error) {
	runs, err := q.ListActiveSessionRunsByBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	result := make([]dbstore.BotWorkdirRecord, 0, len(runs))
	for _, run := range runs {
		sess, err := q.GetSessionByID(ctx, run.SessionID)
		if err != nil {
			return nil, err
		}
		if sess.WorkdirID.Valid {
			row, err := q.GetBotWorkdir(ctx, dbsqlc.GetBotWorkdirParams{BotID: botID, WorkdirID: sess.WorkdirID})
			if err != nil {
				return nil, err
			}
			result = append(result, botWorkdirRecord(row))
			continue
		}
		// Unbound threads use the default workspace, including older external
		// sessions whose directory was stored directly in runtime metadata.
		meta := struct {
			ProjectPath string `json:"project_path"`
		}{}
		if len(sess.RuntimeMetadata) > 0 {
			if err := json.Unmarshal(sess.RuntimeMetadata, &meta); err != nil {
				return nil, err
			}
		}
		if meta.ProjectPath == "" {
			meta.ProjectPath = "/data"
		}
		result = append(result, dbstore.BotWorkdirRecord{TargetKind: "native", Path: meta.ProjectPath})
	}
	return result, nil
}
