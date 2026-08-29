package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/settings"
)

type compactionCapabilityQueries struct {
	dbstore.Queries
	bot          sqlc.GetBotByIDRow
	model        sqlc.Model
	chatModel    sqlc.Model
	provider     sqlc.Provider
	settingsErr  error
	chatModelErr error
}

func (q *compactionCapabilityQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *compactionCapabilityQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	if q.settingsErr != nil {
		return sqlc.GetSettingsByBotIDRow{}, q.settingsErr
	}
	return sqlc.GetSettingsByBotIDRow{
		Language:                settings.DefaultLanguage,
		ReasoningEffort:         settings.DefaultReasoningEffort,
		CompactionTargetPercent: pgtype.Int4{},
		ChatModelID:             q.chatModel.ID,
		CompactionModelID:       q.model.ID,
		CommandUiLanguage:       settings.DefaultCommandUILanguage,
		ChatAcpProjectPath:      settings.DefaultACPProjectPath,
		ChatAcpProjectMode:      settings.DefaultACPProjectMode,
	}, nil
}

func (q *compactionCapabilityQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	if id == q.chatModel.ID {
		if q.chatModelErr != nil {
			return sqlc.Model{}, q.chatModelErr
		}
		return q.chatModel, nil
	}
	return q.model, nil
}

func (q *compactionCapabilityQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func (*compactionCapabilityQueries) GetProviderOAuthTokenByProvider(context.Context, pgtype.UUID) (sqlc.ProviderOauthToken, error) {
	return sqlc.ProviderOauthToken{}, pgx.ErrNoRows
}

func triggerCompactError(t *testing.T, queries *compactionCapabilityQueries) error {
	t.Helper()

	botID := "00000000-0000-0000-0000-000000000423"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	logger := slog.New(slog.DiscardHandler)
	handler := NewCompactionHandler(
		logger,
		nil,
		bots.NewService(logger, queries),
		newTestAdminAccountService("admin"),
		settings.NewService(logger, queries, nil, nil),
		models.NewService(logger, queries),
		queries,
		providers.NewService(logger, queries, ""),
	)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/"+botID+"/sessions/00000000-0000-0000-0000-000000000424/compact", nil)
	recorder := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, recorder, userID)
	echoCtx.SetPath("/bots/:bot_id/sessions/:session_id/compact")
	echoCtx.SetParamNames("bot_id", "session_id")
	echoCtx.SetParamValues(botID, "00000000-0000-0000-0000-000000000424")

	return handler.TriggerCompact(echoCtx)
}

func compactionCodexQueries(botID string) *compactionCapabilityQueries {
	modelID := testUUID("00000000-0000-0000-0000-000000000421")
	providerID := testUUID("00000000-0000-0000-0000-000000000422")
	return &compactionCapabilityQueries{
		bot: testBotRow(botID, map[string]any{}),
		model: sqlc.Model{
			ID:         modelID,
			ModelID:    "codex-compact-model",
			ProviderID: providerID,
			Type:       string(models.ModelTypeChat),
			Enable:     true,
		},
		provider: sqlc.Provider{
			ID:         providerID,
			Name:       "codex-provider",
			ClientType: string(models.ClientTypeOpenAICodex),
			Enable:     true,
		},
	}
}

func compactionWindowQueries(chatConfig []byte, chatModelErr error) *compactionCapabilityQueries {
	compactionModelID := testUUID("00000000-0000-0000-0000-000000000431")
	chatModelID := testUUID("00000000-0000-0000-0000-000000000432")
	providerID := testUUID("00000000-0000-0000-0000-000000000433")
	return &compactionCapabilityQueries{
		model: sqlc.Model{
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
}

func compactionConfigHandler(queries *compactionCapabilityQueries) *CompactionHandler {
	logger := slog.New(slog.DiscardHandler)
	return NewCompactionHandler(
		logger,
		nil,
		nil,
		nil,
		settings.NewService(logger, queries, nil, nil),
		models.NewService(logger, queries),
		queries,
		providers.NewService(logger, queries, ""),
	)
}

func TestBuildTriggerConfigUsesChatModelContextWindow(t *testing.T) {
	t.Parallel()

	handler := compactionConfigHandler(compactionWindowQueries([]byte(`{"context_window":16000}`), nil))
	cfg, err := handler.buildTriggerConfig(
		context.Background(),
		"00000000-0000-0000-0000-000000000430",
		"00000000-0000-0000-0000-000000000434",
	)
	if err != nil {
		t.Fatalf("buildTriggerConfig: %v", err)
	}
	if cfg.SummaryWindowTokens != 32000 {
		t.Fatalf("SummaryWindowTokens = %d, want summarizer window 32000", cfg.SummaryWindowTokens)
	}
	if cfg.ContextWindowTokens != 16000 {
		t.Fatalf("ContextWindowTokens = %d, want chat window 16000", cfg.ContextWindowTokens)
	}
	if cfg.Ratio != 100 || cfg.TotalInputTokens != 1 || !cfg.Manual || !cfg.AllowFrontierFusion {
		t.Fatalf("manual config semantics changed: %#v", cfg)
	}
}

func TestBuildTriggerConfigFallsBackWhenChatWindowIsUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		chatConfig   []byte
		chatModelErr error
	}{
		{name: "model lookup fails", chatConfig: []byte(`{"context_window":16000}`), chatModelErr: pgx.ErrNoRows},
		{name: "window missing", chatConfig: []byte(`{}`)},
		{name: "window nonpositive", chatConfig: []byte(`{"context_window":0}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := compactionConfigHandler(compactionWindowQueries(tt.chatConfig, tt.chatModelErr))
			cfg, err := handler.buildTriggerConfig(
				context.Background(),
				"00000000-0000-0000-0000-000000000430",
				"00000000-0000-0000-0000-000000000434",
			)
			if err != nil {
				t.Fatalf("buildTriggerConfig: %v", err)
			}
			if cfg.ContextWindowTokens != 0 {
				t.Fatalf("ContextWindowTokens = %d, want unknown-window fallback", cfg.ContextWindowTokens)
			}
		})
	}
}

func TestTriggerCompactRejectsProviderWithoutOutputLimitBeforeService(t *testing.T) {
	t.Parallel()

	err := triggerCompactError(t, compactionCodexQueries("00000000-0000-0000-0000-000000000423"))
	if got := apperror.CodeOf(err); got != apperror.CodeCompactionModelUnavailable {
		t.Fatalf("TriggerCompact() code = %q, want %q (err=%v)", got, apperror.CodeCompactionModelUnavailable, err)
	}
	problem, ok := apperror.ProblemFrom(err, "req-test")
	if !ok || problem.Status != http.StatusBadRequest {
		t.Fatalf("TriggerCompact() problem = %+v ok=%t, want a 400 problem shape", problem, ok)
	}
	body, marshalErr := json.Marshal(problem)
	if marshalErr != nil {
		t.Fatalf("marshal problem: %v", marshalErr)
	}
	for _, fragment := range []string{`"code":"compaction.model_unavailable"`, `"reason":"output_limit_unsupported"`} {
		if !strings.Contains(string(body), fragment) {
			t.Fatalf("problem body %s missing %s", body, fragment)
		}
	}
}

func TestTriggerCompactInfraFailureReturnsGeneric500(t *testing.T) {
	t.Parallel()

	queries := compactionCodexQueries("00000000-0000-0000-0000-000000000423")
	queries.settingsErr = errors.New("pq: connection refused to db-internal-host")

	err := triggerCompactError(t, queries)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusInternalServerError {
		t.Fatalf("TriggerCompact() infra failure = %v, want a plain 500", err)
	}
	if message, _ := httpErr.Message.(string); strings.Contains(message, "pq:") || strings.Contains(message, "db-internal-host") {
		t.Fatalf("infra failure leaked diagnostics: %q", message)
	}
}

func TestCompactionRunFailureShapes(t *testing.T) {
	t.Parallel()

	err := compactionRunFailure(fmt.Errorf("wrap: %w", compaction.ErrSummaryWindowTooSmall))
	if got := apperror.CodeOf(err); got != apperror.CodeCompactionModelUnavailable {
		t.Fatalf("window-too-small code = %q, want %q", got, apperror.CodeCompactionModelUnavailable)
	}
	problem, ok := apperror.ProblemFrom(err, "req")
	if !ok || problem.Status != http.StatusBadRequest {
		t.Fatalf("window-too-small problem = %+v ok=%t, want a 400 problem shape", problem, ok)
	}
	body, marshalErr := json.Marshal(problem)
	if marshalErr != nil {
		t.Fatalf("marshal problem: %v", marshalErr)
	}
	if !strings.Contains(string(body), `"reason":"window_too_small"`) {
		t.Fatalf("problem body %s missing the window_too_small reason", body)
	}

	generic := compactionRunFailure(errors.New("window=512 output_reserve=51 fixed_prompt=180"))
	var httpErr *echo.HTTPError
	if !errors.As(generic, &httpErr) || httpErr.Code != http.StatusInternalServerError {
		t.Fatalf("generic failure = %v, want a plain 500", generic)
	}
	if message, _ := httpErr.Message.(string); strings.Contains(message, "window=") {
		t.Fatalf("generic failure leaked diagnostics: %q", message)
	}
}
