package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type TokenUsageHandler struct {
	queries        dbstore.Queries
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewTokenUsageHandler(log *slog.Logger, queries dbstore.Queries, botService *bots.Service, accountService *accounts.Service) *TokenUsageHandler {
	return &TokenUsageHandler{
		queries:        queries,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "token_usage")),
	}
}

func (h *TokenUsageHandler) Register(e *echo.Echo) {
	e.GET("/bots/:bot_id/token-usage", h.GetTokenUsage)
	e.GET("/bots/:bot_id/token-usage/records", h.ListTokenUsageRecords)
}

// DailyTokenUsage represents aggregated token usage for a single day.
type DailyTokenUsage struct {
	Day             string `json:"day"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CacheReadTokens int64  `json:"cache_read_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
}

// ModelTokenUsage represents aggregated token usage for a single model.
type ModelTokenUsage struct {
	ModelID      string `json:"model_id"`
	ModelSlug    string `json:"model_slug"`
	ModelName    string `json:"model_name"`
	ProviderName string `json:"provider_name"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// TokenUsageResponse is the response body for GET /bots/:bot_id/token-usage.
type TokenUsageResponse struct {
	Chat     []DailyTokenUsage `json:"chat"`
	Discuss  []DailyTokenUsage `json:"discuss"`
	ACPAgent []DailyTokenUsage `json:"acp_agent"`
	Schedule []DailyTokenUsage `json:"schedule"`
	ByModel  []ModelTokenUsage `json:"by_model"`
}

// TokenUsageRecord represents a single LLM call (one assistant message row) with its token usage.
type TokenUsageRecord struct {
	ID              string `json:"id"`
	CreatedAt       string `json:"created_at"`
	SessionID       string `json:"session_id"`
	SessionType     string `json:"session_type"`
	ModelID         string `json:"model_id"`
	ModelSlug       string `json:"model_slug"`
	ModelName       string `json:"model_name"`
	ProviderName    string `json:"provider_name"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CacheReadTokens int64  `json:"cache_read_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
}

// TokenUsageRecordsResponse is the response body for GET /bots/:bot_id/token-usage/records.
type TokenUsageRecordsResponse struct {
	Items []TokenUsageRecord `json:"items"`
	Total int64              `json:"total"`
}

// GetTokenUsage godoc
// @Summary Get token usage statistics
// @Description Get daily aggregated token usage for a bot, split by chat, discuss, and schedule session types, with optional model filter and per-model breakdown
// @Tags token-usage
// @Param bot_id path string true "Bot ID"
// @Param from query string true "Start date (YYYY-MM-DD)"
// @Param to query string true "End date exclusive (YYYY-MM-DD)"
// @Param model_id query string false "Optional model UUID to filter by"
// @Param session_type query string false "Optional session type: chat, discuss, schedule, or acp_agent. acp_agent filters by runtime."
// @Success 200 {object} TokenUsageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/token-usage [get].
func (h *TokenUsageHandler) GetTokenUsage(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID); err != nil {
		return err
	}

	fromStr := strings.TrimSpace(c.QueryParam("from"))
	toStr := strings.TrimSpace(c.QueryParam("to"))
	if fromStr == "" || toStr == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "from and to query parameters are required (YYYY-MM-DD)")
	}
	fromDate, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid from date format, expected YYYY-MM-DD")
	}
	toDate, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid to date format, expected YYYY-MM-DD")
	}
	if !toDate.After(fromDate) {
		return echo.NewHTTPError(http.StatusBadRequest, "to must be after from")
	}

	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid bot id")
	}

	var pgModelID pgtype.UUID
	if modelIDStr := strings.TrimSpace(c.QueryParam("model_id")); modelIDStr != "" {
		pgModelID, err = db.ParseUUID(modelIDStr)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid model_id")
		}
	}
	pgSessionType, err := parseTokenUsageSessionType(c)
	if err != nil {
		return err
	}

	fromTS := pgtype.Timestamptz{Time: fromDate, Valid: true}
	toTS := pgtype.Timestamptz{Time: toDate, Valid: true}

	ctx := c.Request().Context()

	chat, discuss, acpAgent, schedule, err := h.fetchUsageByDay(ctx, pgBotID, fromTS, toTS, pgModelID, pgSessionType)
	if err != nil {
		h.logger.Error("fetch token usage failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch token usage")
	}

	byModel, err := h.fetchUsageByModel(ctx, pgBotID, fromTS, toTS, pgSessionType)
	if err != nil {
		h.logger.Error("fetch token usage by model failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch token usage by model")
	}

	resp := TokenUsageResponse{
		Chat:     chat,
		Discuss:  discuss,
		ACPAgent: acpAgent,
		Schedule: schedule,
		ByModel:  byModel,
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *TokenUsageHandler) fetchUsageByDay(ctx context.Context, botID pgtype.UUID, from, to pgtype.Timestamptz, modelID pgtype.UUID, sessionType pgtype.Text) (chat, discuss, acpAgent, schedule []DailyTokenUsage, err error) {
	rows, err := h.queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{
		BotID:       botID,
		FromTime:    from,
		ToTime:      to,
		ModelID:     modelID,
		SessionType: sessionType,
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}

	for _, r := range rows {
		d := DailyTokenUsage{
			Day:             formatPgDate(r.Day),
			InputTokens:     r.InputTokens,
			OutputTokens:    r.OutputTokens,
			CacheReadTokens: r.CacheReadTokens,
			ReasoningTokens: r.ReasoningTokens,
		}
		switch r.SessionType {
		case session.TypeDiscuss:
			discuss = append(discuss, d)
		case session.TypeACPAgent:
			acpAgent = append(acpAgent, d)
		case "schedule":
			schedule = append(schedule, d)
		default:
			chat = append(chat, d)
		}
	}
	return chat, discuss, acpAgent, schedule, nil
}

func (h *TokenUsageHandler) fetchUsageByModel(ctx context.Context, botID pgtype.UUID, from, to pgtype.Timestamptz, sessionType pgtype.Text) ([]ModelTokenUsage, error) {
	rows, err := h.queries.GetTokenUsageByModel(ctx, sqlc.GetTokenUsageByModelParams{
		BotID:       botID,
		FromTime:    from,
		ToTime:      to,
		SessionType: sessionType,
	})
	if err != nil {
		return nil, err
	}

	result := make([]ModelTokenUsage, 0, len(rows))
	for _, r := range rows {
		result = append(result, ModelTokenUsage{
			ModelID:      formatOptionalUUID(r.ModelID),
			ModelSlug:    r.ModelSlug,
			ModelName:    r.ModelName,
			ProviderName: r.ProviderName,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
		})
	}
	return result, nil
}

func formatPgDate(d pgtype.Date) string {
	if !d.Valid {
		return ""
	}
	return d.Time.Format("2006-01-02")
}

func formatOptionalUUID(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

const (
	tokenUsageRecordsDefaultLimit = 20
	tokenUsageRecordsMaxLimit     = 100
)

// ListTokenUsageRecords godoc
// @Summary List per-call token usage records
// @Description Paginated list of individual LLM call records (assistant messages with usage) for a bot, with optional model and session type filters
// @Tags token-usage
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param from query string true "Start date (YYYY-MM-DD)"
// @Param to query string true "End date exclusive (YYYY-MM-DD)"
// @Param model_id query string false "Optional model UUID to filter by"
// @Param session_type query string false "Optional session type: chat, discuss, schedule, or acp_agent. acp_agent filters by runtime."
// @Param limit query int false "Page size (default 20, max 100)"
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} TokenUsageRecordsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/token-usage/records [get].
func (h *TokenUsageHandler) ListTokenUsageRecords(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID); err != nil {
		return err
	}

	fromStr := strings.TrimSpace(c.QueryParam("from"))
	toStr := strings.TrimSpace(c.QueryParam("to"))
	if fromStr == "" || toStr == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "from and to query parameters are required (YYYY-MM-DD)")
	}
	fromDate, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid from date format, expected YYYY-MM-DD")
	}
	toDate, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid to date format, expected YYYY-MM-DD")
	}
	if !toDate.After(fromDate) {
		return echo.NewHTTPError(http.StatusBadRequest, "to must be after from")
	}

	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid bot id")
	}

	var pgModelID pgtype.UUID
	if modelIDStr := strings.TrimSpace(c.QueryParam("model_id")); modelIDStr != "" {
		pgModelID, err = db.ParseUUID(modelIDStr)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid model_id")
		}
	}

	pgSessionType, err := parseTokenUsageSessionType(c)
	if err != nil {
		return err
	}

	limit, err := parseInt32Query(c.QueryParam("limit"), tokenUsageRecordsDefaultLimit)
	if err != nil {
		return err
	}
	if limit <= 0 {
		limit = tokenUsageRecordsDefaultLimit
	}
	if limit > tokenUsageRecordsMaxLimit {
		limit = tokenUsageRecordsMaxLimit
	}
	offset, err := parseInt32Query(c.QueryParam("offset"), 0)
	if err != nil {
		return err
	}

	fromTS := pgtype.Timestamptz{Time: fromDate, Valid: true}
	toTS := pgtype.Timestamptz{Time: toDate, Valid: true}

	ctx := c.Request().Context()

	rows, err := h.queries.ListTokenUsageRecords(ctx, sqlc.ListTokenUsageRecordsParams{
		BotID:       pgBotID,
		FromTime:    fromTS,
		ToTime:      toTS,
		ModelID:     pgModelID,
		SessionType: pgSessionType,
		PageOffset:  offset,
		PageLimit:   limit,
	})
	if err != nil {
		h.logger.Error("list token usage records failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list token usage records")
	}

	total, err := h.queries.CountTokenUsageRecords(ctx, sqlc.CountTokenUsageRecordsParams{
		BotID:       pgBotID,
		FromTime:    fromTS,
		ToTime:      toTS,
		ModelID:     pgModelID,
		SessionType: pgSessionType,
	})
	if err != nil {
		h.logger.Error("count token usage records failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count token usage records")
	}

	items := make([]TokenUsageRecord, 0, len(rows))
	for _, r := range rows {
		items = append(items, TokenUsageRecord{
			ID:              formatOptionalUUID(r.ID),
			CreatedAt:       formatPgTime(r.CreatedAt),
			SessionID:       formatOptionalUUID(r.SessionID),
			SessionType:     r.SessionType,
			ModelID:         formatOptionalUUID(r.ModelID),
			ModelSlug:       r.ModelSlug,
			ModelName:       r.ModelName,
			ProviderName:    r.ProviderName,
			InputTokens:     r.InputTokens,
			OutputTokens:    r.OutputTokens,
			CacheReadTokens: r.CacheReadTokens,
			ReasoningTokens: r.ReasoningTokens,
		})
	}

	return c.JSON(http.StatusOK, TokenUsageRecordsResponse{
		Items: items,
		Total: total,
	})
}

func parseTokenUsageSessionType(c echo.Context) (pgtype.Text, error) {
	switch sessionType := strings.TrimSpace(c.QueryParam("session_type")); sessionType {
	case "":
		return pgtype.Text{}, nil
	case session.TypeChat, session.TypeDiscuss, session.TypeSchedule, session.TypeACPAgent:
		return pgtype.Text{String: sessionType, Valid: true}, nil
	default:
		return pgtype.Text{}, echo.NewHTTPError(http.StatusBadRequest, "invalid session_type, expected one of: chat, discuss, schedule, acp_agent")
	}
}

func formatPgTime(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}
