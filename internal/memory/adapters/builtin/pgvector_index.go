package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/felinics/memoh/internal/db"
	pgvectordb "github.com/felinics/memoh/internal/db/pgvector"
	pgvectorsqlc "github.com/felinics/memoh/internal/db/pgvector/sqlc"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/team"
)

const (
	semanticEmbedTimeout = models.DefaultProviderRequestTimeout
	maxPgvectorInt32     = int64(1<<31 - 1)
)

type pgvectorIndex struct {
	store       *pgvectordb.Store
	lookup      dbstore.Queries
	embedModel  *sdk.EmbeddingModel
	model       embeddingModelSpec
	modelRef    string
	resolveTeam adapters.TeamIDResolver
	logger      *slog.Logger
}

type embeddingModelSpec struct {
	uuid       pgtype.UUID
	modelID    string
	clientType string
	baseURL    string
	apiKey     string
	dimensions int
}

func newPGVectorIndex(ctx context.Context, logger *slog.Logger, providerConfig map[string]any, queries dbstore.Queries, vectorStore *pgvectordb.Store, resolver adapters.TeamIDResolver) (*pgvectorIndex, error) {
	modelRef := strings.TrimSpace(adapters.StringFromConfig(providerConfig, "embedding_model_id"))
	if modelRef == "" {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if vectorStore == nil || vectorStore.Queries() == nil {
		logger.Debug("graph: pgvector semantic index unavailable", slog.String("embedding_model_id", modelRef))
		return nil, nil
	}
	if queries == nil {
		logger.Debug("graph: pgvector semantic index disabled without relational query store", slog.String("embedding_model_id", modelRef))
		return nil, nil
	}
	if resolver == nil {
		resolver = adapters.FixedTeamIDResolver(team.DefaultTeamID)
	}
	spec, err := resolveEmbeddingModel(ctx, queries, modelRef)
	if err != nil {
		return nil, err
	}
	index := &pgvectorIndex{
		store:       vectorStore,
		lookup:      queries,
		embedModel:  models.NewSDKEmbeddingModel(spec.clientType, spec.baseURL, spec.apiKey, spec.modelID, semanticEmbedTimeout, nil),
		model:       spec,
		modelRef:    modelRef,
		resolveTeam: resolver,
		logger:      logger,
	}
	return index, nil
}

func (r *pgvectorIndex) Name() string {
	if r == nil {
		return ""
	}
	return "pgvector"
}

func (r *pgvectorIndex) ensureEmbeddingEnabled(ctx context.Context) error {
	if r == nil || r.modelRef == "" {
		return nil
	}
	modelRef := r.modelRef
	var row dbsqlc.Model
	if parsed, err := db.ParseUUID(modelRef); err == nil {
		if dbModel, err := r.lookupQueries().GetModelByID(ctx, parsed); err == nil {
			row = dbModel
		}
	}
	if !row.ID.Valid {
		rows, err := r.lookupQueries().ListModelsByModelID(ctx, modelRef)
		if err != nil || len(rows) == 0 {
			return fmt.Errorf("pgvector semantic index: embedding model not found: %s", modelRef)
		}
		row = rows[0]
	}
	if !row.Enable {
		return fmt.Errorf("pgvector semantic index: embedding model %s is disabled", modelRef)
	}
	return nil
}

func (r *pgvectorIndex) lookupQueries() dbstore.Queries {
	return r.lookup
}

func (r *pgvectorIndex) teamUUID(ctx context.Context) (pgtype.UUID, error) {
	if r == nil || r.resolveTeam == nil {
		return pgtype.UUID{}, errors.New("pgvector semantic index: team resolver is not configured")
	}
	teamID, err := r.resolveTeam(ctx)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("pgvector semantic index: resolve team: %w", err)
	}
	teamUUID, err := db.ParseUUID(strings.TrimSpace(teamID))
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("pgvector semantic index: invalid team id: %w", err)
	}
	return teamUUID, nil
}

// withTeamTx binds the RLS context transaction-locally. Queries also include
// team_id explicitly, so the GUC is a second isolation boundary rather than
// the only one.
func (r *pgvectorIndex) withTeamTx(ctx context.Context, fn func(*pgvectorsqlc.Queries, pgtype.UUID) error) error {
	if r == nil || r.store == nil {
		return nil
	}
	teamUUID, err := r.teamUUID(ctx)
	if err != nil {
		return err
	}
	tx, err := r.store.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgvector semantic index: begin team transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", teamUUID.String()); err != nil {
		return fmt.Errorf("pgvector semantic index: bind team: %w", err)
	}
	if err := fn(r.store.Queries().WithTx(tx), teamUUID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgvector semantic index: commit team transaction: %w", err)
	}
	return nil
}

func (r *pgvectorIndex) embedText(ctx context.Context, text string) ([]float32, error) {
	if err := r.ensureEmbeddingEnabled(ctx); err != nil {
		return nil, err
	}
	client := sdk.NewClient()
	vec, err := client.Embed(ctx, text, sdk.WithEmbeddingModel(r.embedModel))
	if err != nil {
		return nil, fmt.Errorf("pgvector semantic embed: %w", err)
	}
	out := float64sToFloat32s(vec)
	if r.model.dimensions > 0 && len(out) != r.model.dimensions {
		return nil, fmt.Errorf("pgvector semantic index: embedding dimensions = %d, want %d", len(out), r.model.dimensions)
	}
	return out, nil
}

func (r *pgvectorIndex) Upsert(ctx context.Context, botID, nodeID, body, hash string) error {
	if r == nil || r.store == nil || strings.TrimSpace(body) == "" {
		return nil
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	vec, err := r.embedText(ctx, body)
	if err != nil {
		return err
	}
	dimensions, err := checkedPgvectorInt32("dimensions", len(vec))
	if err != nil {
		return err
	}
	err = r.withTeamTx(ctx, func(teamQueries *pgvectorsqlc.Queries, teamUUID pgtype.UUID) error {
		return teamQueries.UpsertMemoryNodeEmbedding(ctx, pgvectorsqlc.UpsertMemoryNodeEmbeddingParams{
			TeamID:     teamUUID,
			BotID:      botUUID,
			NodeID:     strings.TrimSpace(nodeID),
			ModelID:    r.model.uuid,
			Dimensions: dimensions,
			BodyHash:   strings.TrimSpace(hash),
			Embedding:  pgvector.NewVector(vec),
		})
	})
	if err != nil {
		return fmt.Errorf("pgvector semantic index: upsert: %w", err)
	}
	return nil
}

func (r *pgvectorIndex) SearchSeeds(ctx context.Context, botID, query string, limit int) (map[string]float64, error) {
	if r == nil || r.store == nil || strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	vec, err := r.embedText(ctx, query)
	if err != nil {
		return nil, err
	}
	rowLimit, err := checkedPgvectorInt32("row_limit", limit)
	if err != nil {
		return nil, err
	}
	seeds := map[string]float64{}
	err = r.withTeamTx(ctx, func(teamQueries *pgvectorsqlc.Queries, teamUUID pgtype.UUID) error {
		rows, queryErr := teamQueries.SearchMemoryNodeEmbeddings(ctx, pgvectorsqlc.SearchMemoryNodeEmbeddingsParams{
			Embedding: pgvector.NewVector(vec),
			TeamID:    teamUUID,
			BotID:     botUUID,
			ModelID:   r.model.uuid,
			RowLimit:  rowLimit,
		})
		if queryErr != nil {
			return queryErr
		}
		for _, row := range rows {
			nodeID := strings.TrimSpace(row.NodeID)
			if nodeID != "" {
				seeds[nodeID] = row.Score
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pgvector semantic index: search: %w", err)
	}

	return seeds, nil
}

func checkedPgvectorInt32(name string, n int) (int32, error) {
	if n < 0 || int64(n) > maxPgvectorInt32 {
		return 0, fmt.Errorf("pgvector semantic index: %s out of int32 range: %d", name, n)
	}
	return int32(n), nil //nolint:gosec // guarded above.
}

func (r *pgvectorIndex) DeleteNodes(ctx context.Context, botID string, nodeIDs []string) error {
	if r == nil || r.store == nil || len(nodeIDs) == 0 {
		return nil
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		nodeID = strings.TrimSpace(nodeID)
		if nodeID != "" {
			ids = append(ids, nodeID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	err = r.withTeamTx(ctx, func(teamQueries *pgvectorsqlc.Queries, teamUUID pgtype.UUID) error {
		return teamQueries.DeleteMemoryNodeEmbeddings(ctx, pgvectorsqlc.DeleteMemoryNodeEmbeddingsParams{
			TeamID:  teamUUID,
			BotID:   botUUID,
			NodeIds: ids,
		})
	})
	if err != nil {
		return fmt.Errorf("pgvector semantic index: delete nodes: %w", err)
	}
	return nil
}

func (r *pgvectorIndex) DeleteBot(ctx context.Context, botID string) error {
	if r == nil || r.store == nil {
		return nil
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	err = r.withTeamTx(ctx, func(teamQueries *pgvectorsqlc.Queries, teamUUID pgtype.UUID) error {
		return teamQueries.DeleteBotMemoryNodeEmbeddings(ctx, pgvectorsqlc.DeleteBotMemoryNodeEmbeddingsParams{
			TeamID: teamUUID,
			BotID:  botUUID,
		})
	})
	if err != nil {
		return fmt.Errorf("pgvector semantic index: delete bot: %w", err)
	}
	return nil
}

func (r *pgvectorIndex) Count(ctx context.Context, botID string) (int, error) {
	if r == nil || r.store == nil {
		return 0, nil
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return 0, err
	}
	var count int64
	err = r.withTeamTx(ctx, func(teamQueries *pgvectorsqlc.Queries, teamUUID pgtype.UUID) error {
		var queryErr error
		count, queryErr = teamQueries.CountMemoryNodeEmbeddings(ctx, pgvectorsqlc.CountMemoryNodeEmbeddingsParams{
			TeamID:  teamUUID,
			BotID:   botUUID,
			ModelID: r.model.uuid,
		})
		return queryErr
	})
	if err != nil {
		return 0, fmt.Errorf("pgvector semantic index: count: %w", err)
	}
	if count > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("pgvector semantic index: count overflow: %d", count)
	}
	return int(count), nil
}

func (r *pgvectorIndex) Health(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	if err := r.ensureEmbeddingEnabled(ctx); err != nil {
		return err
	}
	if err := r.withTeamTx(ctx, func(teamQueries *pgvectorsqlc.Queries, teamUUID pgtype.UUID) error {
		_, queryErr := teamQueries.MemoryNodeEmbeddingsExist(ctx, teamUUID)
		return queryErr
	}); err != nil {
		return fmt.Errorf("pgvector semantic index: health: %w", err)
	}
	return nil
}

func float64sToFloat32s(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

func resolveEmbeddingModel(ctx context.Context, queries dbstore.Queries, modelRef string) (embeddingModelSpec, error) {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return embeddingModelSpec{}, errors.New("pgvector semantic index: embedding_model_id is required")
	}
	if queries == nil {
		return embeddingModelSpec{}, errors.New("pgvector semantic index: queries are required")
	}
	var row dbsqlc.Model
	if parsed, err := db.ParseUUID(modelRef); err == nil {
		dbModel, err := queries.GetModelByID(ctx, parsed)
		if err == nil {
			row = dbModel
		}
	}
	if !row.ID.Valid {
		rows, err := queries.ListModelsByModelID(ctx, modelRef)
		if err != nil || len(rows) == 0 {
			return embeddingModelSpec{}, fmt.Errorf("pgvector semantic index: embedding model not found: %s", modelRef)
		}
		row = rows[0]
	}
	if row.Type != "embedding" {
		return embeddingModelSpec{}, fmt.Errorf("pgvector semantic index: model %s is not an embedding model", modelRef)
	}
	if !row.Enable {
		return embeddingModelSpec{}, fmt.Errorf("pgvector semantic index: embedding model %s is disabled", modelRef)
	}
	if !row.ProviderID.Valid {
		return embeddingModelSpec{}, fmt.Errorf("pgvector semantic index: model %s has no provider", modelRef)
	}
	provider, err := queries.GetProviderByID(ctx, row.ProviderID)
	if err != nil {
		return embeddingModelSpec{}, fmt.Errorf("pgvector semantic index: get embedding provider: %w", err)
	}
	var modelCfg struct {
		Dimensions *int `json:"dimensions"`
	}
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &modelCfg)
	}
	if modelCfg.Dimensions == nil || *modelCfg.Dimensions <= 0 {
		return embeddingModelSpec{}, fmt.Errorf("pgvector semantic index: embedding model %s missing dimensions", modelRef)
	}
	var providerCfg map[string]any
	if len(provider.Config) > 0 {
		_ = json.Unmarshal(provider.Config, &providerCfg)
	}
	baseURL, _ := providerCfg["base_url"].(string)
	apiKey, _ := providerCfg["api_key"].(string)
	return embeddingModelSpec{
		uuid:       row.ID,
		modelID:    strings.TrimSpace(row.ModelID),
		clientType: strings.TrimSpace(provider.ClientType),
		baseURL:    strings.TrimSpace(baseURL),
		apiKey:     strings.TrimSpace(apiKey),
		dimensions: *modelCfg.Dimensions,
	}, nil
}
