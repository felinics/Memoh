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

	"github.com/memohai/memoh/internal/agent/context/compaction"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/providers"
	"github.com/memohai/memoh/internal/settings"
)

type compactionCapabilityQueries struct {
	dbstore.Queries
	bot         sqlc.GetBotByIDRow
	model       sqlc.Model
	provider    sqlc.Provider
	settingsErr error
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
		HeartbeatInterval:       settings.DefaultHeartbeatInterval,
		CompactionTargetPercent: pgtype.Int4{},
		CompactionModelID:       q.model.ID,
		CommandUiLanguage:       settings.DefaultCommandUILanguage,
		ChatAcpProjectPath:      settings.DefaultACPProjectPath,
		ChatAcpProjectMode:      settings.DefaultACPProjectMode,
	}, nil
}

func (q *compactionCapabilityQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
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
