package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/settings"
)

type compactConfigQueries struct {
	dbstore.Queries
	compactionModel sqlc.Model
	chatModel       sqlc.Model
	provider        sqlc.Provider
	chatModelErr    error
}

func (q *compactConfigQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{
		Language:                settings.DefaultLanguage,
		ReasoningEffort:         settings.DefaultReasoningEffort,
		CompactionTargetPercent: pgtype.Int4{},
		ChatModelID:             q.chatModel.ID,
		CompactionModelID:       q.compactionModel.ID,
		CommandUiLanguage:       settings.DefaultCommandUILanguage,
		ChatAcpProjectPath:      settings.DefaultACPProjectPath,
		ChatAcpProjectMode:      settings.DefaultACPProjectMode,
	}, nil
}

func (q *compactConfigQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	if id == q.chatModel.ID {
		if q.chatModelErr != nil {
			return sqlc.Model{}, q.chatModelErr
		}
		return q.chatModel, nil
	}
	return q.compactionModel, nil
}

func (q *compactConfigQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func compactConfigHarness(chatConfig []byte, chatModelErr error) (*Handler, CommandContext) {
	compactionModelID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000511"), Valid: true}
	chatModelID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000512"), Valid: true}
	providerID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000513"), Valid: true}
	queries := &compactConfigQueries{
		compactionModel: sqlc.Model{
			ID:         compactionModelID,
			ModelID:    "summary-model",
			ProviderID: providerID,
			Type:       string(models.ModelTypeChat),
			Enable:     true,
			Config:     []byte(`{"context_window":32000}`),
		},
		chatModel: sqlc.Model{
			ID:         chatModelID,
			ModelID:    "chat-model",
			ProviderID: providerID,
			Type:       string(models.ModelTypeChat),
			Enable:     true,
			Config:     chatConfig,
		},
		provider: sqlc.Provider{
			ID:         providerID,
			Name:       "test-provider",
			ClientType: string(models.ClientTypeOpenAICompletions),
			Enable:     true,
		},
		chatModelErr: chatModelErr,
	}
	logger := slog.New(slog.DiscardHandler)
	handler := &Handler{
		settingsService:  settings.NewService(logger, queries, nil, nil),
		modelsService:    models.NewService(logger, queries),
		providersService: providers.NewService(logger, queries, ""),
		sqlcQueries:      queries,
	}
	return handler, CommandContext{
		Ctx:   context.Background(),
		BotID: "00000000-0000-0000-0000-000000000510",
	}
}

func TestCompactRunErrorKeepsDiagnosticsOutOfChat(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	cc := CommandContext{L: i18n.New("en")}

	tooSmall := h.compactRunError(cc, fmt.Errorf("compaction: %w: window=512 output_reserve=51 fixed_prompt=180", compaction.ErrSummaryWindowTooSmall))
	if tooSmall == "" || strings.Contains(tooSmall, "window=") {
		t.Fatalf("window-too-small message leaked diagnostics: %q", tooSmall)
	}

	generic := h.compactRunError(cc, errors.New("dial tcp 10.0.0.1:443: i/o timeout"))
	if generic == "" || strings.Contains(generic, "dial tcp") {
		t.Fatalf("generic run failure leaked diagnostics: %q", generic)
	}

	if tooSmall == generic {
		t.Fatal("window-too-small must surface its own actionable message, not the generic failure")
	}
}

func TestBuildCompactConfigUsesChatModelContextWindow(t *testing.T) {
	t.Parallel()

	handler, commandContext := compactConfigHarness([]byte(`{"context_window":200000}`), nil)
	cfg, err := handler.buildCompactConfig(commandContext, "00000000-0000-0000-0000-000000000514")
	if err != nil {
		t.Fatalf("buildCompactConfig: %v", err)
	}
	if cfg.SummaryWindowTokens != 32000 {
		t.Fatalf("SummaryWindowTokens = %d, want summarizer window 32000", cfg.SummaryWindowTokens)
	}
	if cfg.ContextWindowTokens != 200000 {
		t.Fatalf("ContextWindowTokens = %d, want chat window 200000", cfg.ContextWindowTokens)
	}
	if cfg.Ratio != 100 || cfg.TotalInputTokens != 1 || !cfg.Manual || !cfg.AllowFrontierFusion {
		t.Fatalf("manual config semantics changed: %#v", cfg)
	}
}

func TestBuildCompactConfigFallsBackWhenChatWindowIsUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		chatConfig   []byte
		chatModelErr error
	}{
		{name: "model lookup fails", chatConfig: []byte(`{"context_window":200000}`), chatModelErr: pgx.ErrNoRows},
		{name: "window missing", chatConfig: []byte(`{}`)},
		{name: "window nonpositive", chatConfig: []byte(`{"context_window":0}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, commandContext := compactConfigHarness(tt.chatConfig, tt.chatModelErr)
			cfg, err := handler.buildCompactConfig(commandContext, "00000000-0000-0000-0000-000000000514")
			if err != nil {
				t.Fatalf("buildCompactConfig: %v", err)
			}
			if cfg.ContextWindowTokens != 0 {
				t.Fatalf("ContextWindowTokens = %d, want unknown-window fallback", cfg.ContextWindowTokens)
			}
		})
	}
}
