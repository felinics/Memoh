package application

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

type compactionConfigQueries struct {
	dbstore.Queries
	model    sqlc.Model
	models   map[pgtype.UUID]sqlc.Model
	provider sqlc.Provider
}

func (q *compactionConfigQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	if model, ok := q.models[id]; ok {
		return model, nil
	}
	return q.model, nil
}

func (q *compactionConfigQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func (*compactionConfigQueries) GetProviderOAuthTokenByProvider(context.Context, pgtype.UUID) (sqlc.ProviderOauthToken, error) {
	return sqlc.ProviderOauthToken{}, pgx.ErrNoRows
}

func (*compactionConfigQueries) GetLatestSessionModelID(context.Context, pgtype.UUID) (pgtype.UUID, error) {
	return pgtype.UUID{}, pgx.ErrNoRows
}

func compactionConfigUUID(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	parsed, err := db.ParseUUID(id)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", id, err)
	}
	return parsed
}

func TestBuildCompactionConfigLeavesSelectionToController(t *testing.T) {
	t.Parallel()

	const modelUUID = "00000000-0000-0000-0000-000000000401"
	const providerUUID = "00000000-0000-0000-0000-000000000402"

	queries := &compactionConfigQueries{
		model: sqlc.Model{
			ID:         compactionConfigUUID(t, modelUUID),
			ModelID:    "compact-model",
			ProviderID: compactionConfigUUID(t, providerUUID),
			Type:       "chat",
			Enable:     true,
			Config:     []byte(`{"context_window":200000,"max_output_tokens":32000}`),
		},
		provider: sqlc.Provider{
			ID:         compactionConfigUUID(t, providerUUID),
			Name:       "test-provider",
			ClientType: "openai-completions",
			Enable:     true,
			Config:     []byte(`{"api_key":"test-key"}`),
		},
	}
	r := &Service{
		logger:        slog.New(slog.DiscardHandler),
		modelsService: models.NewService(slog.New(slog.DiscardHandler), queries),
		queries:       queries,
	}

	cfg, err := r.buildCompactionConfig(context.Background(), ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000403",
		ThreadID: "00000000-0000-0000-0000-000000000404",
	}, settings.Settings{ChatModelID: modelUUID}, 150000, "")
	if err != nil {
		t.Fatalf("buildCompactionConfig: %v", err)
	}

	if cfg.TotalInputTokens != 150000 {
		t.Fatalf("TotalInputTokens = %d, want 150000", cfg.TotalInputTokens)
	}
	if cfg.SummaryWindowTokens != 200000 {
		t.Fatalf("SummaryWindowTokens = %d, want the full summarizer window", cfg.SummaryWindowTokens)
	}
	if cfg.ModelRecordID == "" {
		t.Fatal("ModelRecordID must carry the models row UUID for artifact provenance")
	}
	if cfg.MaxOutputTokens != 32000 {
		t.Fatalf("MaxOutputTokens = %d, want the model's declared output cap", cfg.MaxOutputTokens)
	}
}

func TestBuildCompactionConfigPrefersTurnModelOverChatModel(t *testing.T) {
	t.Parallel()

	const (
		turnModelUUID = "00000000-0000-0000-0000-000000000431"
		chatModelUUID = "00000000-0000-0000-0000-000000000435"
		providerUUID  = "00000000-0000-0000-0000-000000000432"
	)
	summarizer := func(id, slug string) sqlc.Model {
		return sqlc.Model{
			ID:         compactionConfigUUID(t, id),
			ModelID:    slug,
			ProviderID: compactionConfigUUID(t, providerUUID),
			Type:       "chat",
			Enable:     true,
			Config:     []byte(`{"context_window":128000}`),
		}
	}
	queries := &compactionConfigQueries{
		models: map[pgtype.UUID]sqlc.Model{
			compactionConfigUUID(t, turnModelUUID): summarizer(turnModelUUID, "turn-selected-model"),
			compactionConfigUUID(t, chatModelUUID): summarizer(chatModelUUID, "bot-chat-model"),
		},
		provider: sqlc.Provider{
			ID:         compactionConfigUUID(t, providerUUID),
			Name:       "test-provider",
			ClientType: "openai-completions",
			Enable:     true,
			Config:     []byte(`{"api_key":"test-key"}`),
		},
	}
	service := &Service{
		logger:        slog.New(slog.DiscardHandler),
		modelsService: models.NewService(slog.New(slog.DiscardHandler), queries),
		queries:       queries,
	}
	req := ChatRequest{
		BotID:    "00000000-0000-0000-0000-000000000433",
		ThreadID: "00000000-0000-0000-0000-000000000434",
	}
	botSettings := settings.Settings{ChatModelID: chatModelUUID}

	for _, tc := range []struct {
		name      string
		turnModel string
		wantID    string
		wantSlug  string
	}{
		{name: "turn model wins", turnModel: turnModelUUID, wantID: turnModelUUID, wantSlug: "turn-selected-model"},
		{name: "chat model without a turn model", wantID: chatModelUUID, wantSlug: "bot-chat-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := service.buildCompactionConfig(context.Background(), req, botSettings, 90000, tc.turnModel)
			if err != nil {
				t.Fatalf("buildCompactionConfig: %v", err)
			}
			if cfg.ModelRecordID != tc.wantID || cfg.ModelID != tc.wantSlug {
				t.Fatalf("summarizer = %q (%q), want %q (%q)", cfg.ModelRecordID, cfg.ModelID, tc.wantID, tc.wantSlug)
			}
		})
	}
}

func (*compactionConfigQueries) UpgradeModelConfig(context.Context, sqlc.UpgradeModelConfigParams) (int64, error) {
	return 0, nil
}
