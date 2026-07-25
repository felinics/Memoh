// Package vector implements the Memory semantic embedding persistence port.
package vector

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	pgvector "github.com/pgvector/pgvector-go"

	memport "github.com/memohai/memoh/domains/memory/internal/port"
	"github.com/memohai/memoh/internal/db"
	pgvectordb "github.com/memohai/memoh/internal/db/pgvector"
	pgvectorsqlc "github.com/memohai/memoh/internal/db/pgvector/sqlc"
)

type Store struct {
	legacy *pgvectordb.Store
}

var _ memport.SemanticEmbeddingStore = (*Store)(nil)

func NewStore(legacy *pgvectordb.Store) *Store {
	return &Store{legacy: legacy}
}

func (s *Store) UpsertEmbedding(ctx context.Context, value memport.SemanticEmbeddingUpsert) error {
	botID, modelID, err := parseBotAndModel(value.BotID, value.ModelID)
	if err != nil {
		return err
	}
	return s.withTeamTx(ctx, value.TeamID, func(queries *pgvectorsqlc.Queries, teamID pgtype.UUID) error {
		return queries.UpsertMemoryNodeEmbedding(ctx, pgvectorsqlc.UpsertMemoryNodeEmbeddingParams{
			TeamID: teamID, BotID: botID, NodeID: value.NodeID, ModelID: modelID,
			Dimensions: value.Dimensions, BodyHash: value.BodyHash,
			Embedding: pgvector.NewVector(append([]float32(nil), value.Embedding...)),
		})
	})
}

func (s *Store) SearchEmbeddings(ctx context.Context, value memport.SemanticEmbeddingSearch) ([]memport.SemanticEmbeddingSeed, error) {
	botID, modelID, err := parseBotAndModel(value.BotID, value.ModelID)
	if err != nil {
		return nil, err
	}
	var result []memport.SemanticEmbeddingSeed
	err = s.withTeamTx(ctx, value.TeamID, func(queries *pgvectorsqlc.Queries, teamID pgtype.UUID) error {
		rows, queryErr := queries.SearchMemoryNodeEmbeddings(ctx, pgvectorsqlc.SearchMemoryNodeEmbeddingsParams{
			Embedding: pgvector.NewVector(append([]float32(nil), value.Embedding...)),
			TeamID:    teamID, BotID: botID, ModelID: modelID, RowLimit: value.Limit,
		})
		if queryErr != nil {
			return queryErr
		}
		result = make([]memport.SemanticEmbeddingSeed, 0, len(rows))
		for _, row := range rows {
			result = append(result, memport.SemanticEmbeddingSeed{NodeID: row.NodeID, Score: row.Score})
		}
		return nil
	})
	return result, err
}

func (s *Store) DeleteEmbeddings(ctx context.Context, value memport.SemanticEmbeddingDelete) error {
	botID, err := db.ParseUUID(value.BotID)
	if err != nil {
		return fmt.Errorf("memory vector bot id: %w", err)
	}
	return s.withTeamTx(ctx, value.TeamID, func(queries *pgvectorsqlc.Queries, teamID pgtype.UUID) error {
		return queries.DeleteMemoryNodeEmbeddings(ctx, pgvectorsqlc.DeleteMemoryNodeEmbeddingsParams{
			TeamID: teamID, BotID: botID, NodeIds: append([]string(nil), value.NodeIDs...),
		})
	})
}

func (s *Store) DeleteBotEmbeddings(ctx context.Context, teamIDValue, botIDValue string) error {
	botID, err := db.ParseUUID(botIDValue)
	if err != nil {
		return fmt.Errorf("memory vector bot id: %w", err)
	}
	return s.withTeamTx(ctx, teamIDValue, func(queries *pgvectorsqlc.Queries, teamID pgtype.UUID) error {
		return queries.DeleteBotMemoryNodeEmbeddings(ctx, pgvectorsqlc.DeleteBotMemoryNodeEmbeddingsParams{TeamID: teamID, BotID: botID})
	})
}

func (s *Store) CountEmbeddings(ctx context.Context, value memport.SemanticEmbeddingKey) (int64, error) {
	botID, modelID, err := parseBotAndModel(value.BotID, value.ModelID)
	if err != nil {
		return 0, err
	}
	var count int64
	err = s.withTeamTx(ctx, value.TeamID, func(queries *pgvectorsqlc.Queries, teamID pgtype.UUID) error {
		var queryErr error
		count, queryErr = queries.CountMemoryNodeEmbeddings(ctx, pgvectorsqlc.CountMemoryNodeEmbeddingsParams{TeamID: teamID, BotID: botID, ModelID: modelID})
		return queryErr
	})
	return count, err
}

func (s *Store) CheckEmbeddings(ctx context.Context, teamIDValue string) error {
	return s.withTeamTx(ctx, teamIDValue, func(queries *pgvectorsqlc.Queries, teamID pgtype.UUID) error {
		_, err := queries.MemoryNodeEmbeddingsExist(ctx, teamID)
		return err
	})
}

func (s *Store) withTeamTx(ctx context.Context, teamIDValue string, fn func(*pgvectorsqlc.Queries, pgtype.UUID) error) error {
	if s == nil || s.legacy == nil || s.legacy.Queries() == nil {
		return errors.New("memory vector store is not open")
	}
	teamID, err := db.ParseUUID(teamIDValue)
	if err != nil {
		return fmt.Errorf("memory vector team id: %w", err)
	}
	tx, err := s.legacy.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memory vector begin team transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", teamID.String()); err != nil {
		return fmt.Errorf("memory vector bind team: %w", err)
	}
	if err := fn(s.legacy.Queries().WithTx(tx), teamID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("memory vector commit team transaction: %w", err)
	}
	return nil
}

func parseBotAndModel(botIDValue, modelIDValue string) (pgtype.UUID, pgtype.UUID, error) {
	botID, err := db.ParseUUID(botIDValue)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("memory vector bot id: %w", err)
	}
	modelID, err := db.ParseUUID(modelIDValue)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("memory vector model id: %w", err)
	}
	return botID, modelID, nil
}
