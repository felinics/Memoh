package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	sdk "github.com/memohai/twilight-ai/sdk"

	team "github.com/memohai/memoh/domains/iam/team"
	memport "github.com/memohai/memoh/domains/memory/internal/port"
	memreg "github.com/memohai/memoh/domains/memory/registry"
	modelexecution "github.com/memohai/memoh/domains/model/execution"
)

const (
	semanticEmbedTimeout = modelexecution.DefaultProviderRequestTimeout
	maxPgvectorInt32     = int64(1<<31 - 1)
)

type pgvectorIndex struct {
	store         memport.SemanticEmbeddingStore
	modelResolver EmbeddingModelResolver
	embedModel    *sdk.EmbeddingModel
	modelID       string
	dimensions    int
	modelRef      string
	resolveTeam   memreg.TeamIDResolver
	logger        *slog.Logger
}

func newPGVectorIndex(ctx context.Context, logger *slog.Logger, providerConfig map[string]any, modelResolver EmbeddingModelResolver, vectorStore memport.SemanticEmbeddingStore, resolver memreg.TeamIDResolver) (*pgvectorIndex, error) {
	modelRef := strings.TrimSpace(memport.StringFromConfig(providerConfig, "embedding_model_id"))
	if modelRef == "" {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if vectorStore == nil {
		logger.DebugContext(ctx, "graph: pgvector semantic index unavailable", slog.String("embedding_model_id", modelRef))
		return nil, nil
	}
	if modelResolver == nil {
		logger.DebugContext(ctx, "graph: pgvector semantic index disabled without embedding model resolver", slog.String("embedding_model_id", modelRef))
		return nil, nil
	}
	if resolver == nil {
		resolver = memreg.FixedTeamIDResolver(team.DefaultTeamID)
	}
	spec, err := resolveEmbeddingModel(ctx, modelResolver, modelRef)
	if err != nil {
		return nil, err
	}
	modelID, err := uuid.Parse(spec.ID)
	if err != nil {
		return nil, fmt.Errorf("pgvector semantic index: invalid embedding model id %q: %w", spec.ID, err)
	}
	index := &pgvectorIndex{
		store:         vectorStore,
		modelResolver: modelResolver,
		embedModel:    modelexecution.NewSDKEmbeddingModel(spec.ClientType, spec.BaseURL, spec.APIKey, spec.ModelID, semanticEmbedTimeout, nil),
		modelID:       modelID.String(),
		dimensions:    spec.Dimensions,
		modelRef:      modelRef,
		resolveTeam:   resolver,
		logger:        logger,
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
	if r.modelResolver == nil {
		return errors.New("pgvector semantic index: embedding model resolver is not configured")
	}
	enabled, err := r.modelResolver.EmbeddingModelEnabled(ctx, r.modelRef)
	if err != nil {
		return fmt.Errorf("pgvector semantic index: resolve embedding model status: %w", err)
	}
	if !enabled {
		return fmt.Errorf("pgvector semantic index: embedding model %s is disabled", r.modelRef)
	}
	return nil
}

func (r *pgvectorIndex) teamID(ctx context.Context) (string, error) {
	if r == nil || r.resolveTeam == nil {
		return "", errors.New("pgvector semantic index: team resolver is not configured")
	}
	teamID, err := r.resolveTeam(ctx)
	if err != nil {
		return "", fmt.Errorf("pgvector semantic index: resolve team: %w", err)
	}
	teamUUID, err := uuid.Parse(strings.TrimSpace(teamID))
	if err != nil {
		return "", fmt.Errorf("pgvector semantic index: invalid team id: %w", err)
	}
	return teamUUID.String(), nil
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
	if r.dimensions > 0 && len(out) != r.dimensions {
		return nil, fmt.Errorf("pgvector semantic index: embedding dimensions = %d, want %d", len(out), r.dimensions)
	}
	return out, nil
}

func (r *pgvectorIndex) Upsert(ctx context.Context, botID, nodeID, body, hash string) error {
	if r == nil || r.store == nil || strings.TrimSpace(body) == "" {
		return nil
	}
	teamID, err := r.teamID(ctx)
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
	err = r.store.UpsertEmbedding(ctx, memport.SemanticEmbeddingUpsert{
		TeamID: teamID, BotID: strings.TrimSpace(botID), NodeID: strings.TrimSpace(nodeID),
		ModelID: r.modelID, Dimensions: dimensions, BodyHash: strings.TrimSpace(hash), Embedding: vec,
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
	teamID, err := r.teamID(ctx)
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
	rows, err := r.store.SearchEmbeddings(ctx, memport.SemanticEmbeddingSearch{
		TeamID: teamID, BotID: strings.TrimSpace(botID), ModelID: r.modelID, Embedding: vec, Limit: rowLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("pgvector semantic index: search: %w", err)
	}
	seeds := make(map[string]float64, len(rows))
	for _, row := range rows {
		nodeID := strings.TrimSpace(row.NodeID)
		if nodeID != "" {
			seeds[nodeID] = row.Score
		}
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
	teamID, err := r.teamID(ctx)
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
	err = r.store.DeleteEmbeddings(ctx, memport.SemanticEmbeddingDelete{
		TeamID: teamID, BotID: strings.TrimSpace(botID), NodeIDs: ids,
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
	teamID, err := r.teamID(ctx)
	if err != nil {
		return err
	}
	err = r.store.DeleteBotEmbeddings(ctx, teamID, strings.TrimSpace(botID))
	if err != nil {
		return fmt.Errorf("pgvector semantic index: delete bot: %w", err)
	}
	return nil
}

func (r *pgvectorIndex) Count(ctx context.Context, botID string) (int, error) {
	if r == nil || r.store == nil {
		return 0, nil
	}
	teamID, err := r.teamID(ctx)
	if err != nil {
		return 0, err
	}
	count, err := r.store.CountEmbeddings(ctx, memport.SemanticEmbeddingKey{
		TeamID: teamID, BotID: strings.TrimSpace(botID), ModelID: r.modelID,
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
	teamID, err := r.teamID(ctx)
	if err != nil {
		return err
	}
	if err := r.store.CheckEmbeddings(ctx, teamID); err != nil {
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

func resolveEmbeddingModel(ctx context.Context, resolver EmbeddingModelResolver, modelRef string) (EmbeddingModelSpec, error) {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return EmbeddingModelSpec{}, errors.New("pgvector semantic index: embedding_model_id is required")
	}
	if resolver == nil {
		return EmbeddingModelSpec{}, errors.New("pgvector semantic index: embedding model resolver is required")
	}
	spec, err := resolver.ResolveEmbeddingModel(ctx, modelRef)
	if err != nil {
		return EmbeddingModelSpec{}, fmt.Errorf("pgvector semantic index: resolve embedding model: %w", err)
	}
	spec.ID = strings.TrimSpace(spec.ID)
	spec.ModelID = strings.TrimSpace(spec.ModelID)
	spec.Type = strings.TrimSpace(spec.Type)
	spec.ProviderID = strings.TrimSpace(spec.ProviderID)
	spec.ClientType = strings.TrimSpace(spec.ClientType)
	spec.BaseURL = strings.TrimSpace(spec.BaseURL)
	spec.APIKey = strings.TrimSpace(spec.APIKey)
	if _, err := uuid.Parse(spec.ID); err != nil {
		return EmbeddingModelSpec{}, fmt.Errorf("pgvector semantic index: invalid embedding model id %q: %w", spec.ID, err)
	}
	if spec.Type != "embedding" {
		return EmbeddingModelSpec{}, fmt.Errorf("pgvector semantic index: model %s is not an embedding model", modelRef)
	}
	if !spec.Enabled {
		return EmbeddingModelSpec{}, fmt.Errorf("pgvector semantic index: embedding model %s is disabled", modelRef)
	}
	if spec.ProviderID == "" {
		return EmbeddingModelSpec{}, fmt.Errorf("pgvector semantic index: model %s has no provider", modelRef)
	}
	if spec.Dimensions <= 0 {
		return EmbeddingModelSpec{}, fmt.Errorf("pgvector semantic index: embedding model %s missing dimensions", modelRef)
	}
	return spec, nil
}
