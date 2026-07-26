package chat

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	session "github.com/memohai/memoh/domains/agent/chat/thread"
	usagepersistence "github.com/memohai/memoh/domains/agent/chat/usage/persistence"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

type TokenUsageHandler struct {
	reader         usagepersistence.Reader
	botService     *bot.Service
	accountService *account.Service
	logger         *slog.Logger
}

func NewTokenUsageHandler(log *slog.Logger, reader usagepersistence.Reader, botService *bot.Service, accountService *account.Service) *TokenUsageHandler {
	return &TokenUsageHandler{
		reader:         reader,
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
	Chat      []DailyTokenUsage `json:"chat"`
	Discuss   []DailyTokenUsage `json:"discuss"`
	ACPAgent  []DailyTokenUsage `json:"acp_agent"`
	Heartbeat []DailyTokenUsage `json:"heartbeat"`
	Schedule  []DailyTokenUsage `json:"schedule"`
	ByModel   []ModelTokenUsage `json:"by_model"`
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
// @Description Get daily aggregated token usage for a bot, split by chat, discuss, heartbeat, and schedule session types, with optional model filter and per-model breakdown
// @Tags token-usage
// @Param bot_id path string true "Bot ID"
// @Param from query string true "Start date (YYYY-MM-DD)"
// @Param to query string true "End date exclusive (YYYY-MM-DD)"
// @Param model_id query string false "Optional model UUID to filter by"
// @Param session_type query string false "Optional session type: chat, discuss, heartbeat, schedule, or acp_agent. acp_agent filters by runtime."
// @Success 200 {object} TokenUsageResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/token-usage [get].
func (h *TokenUsageHandler) GetTokenUsage(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if _, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID); err != nil {
		return err
	}

	fromStr := strings.TrimSpace(c.QueryParam("from"))
	toStr := strings.TrimSpace(c.QueryParam("to"))
	if fromStr == "" || toStr == "" {
		return apperror.Required("from")
	}
	fromDate, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return apperror.Field("from", apperror.FieldInvalid)
	}
	toDate, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return apperror.Field("to", apperror.FieldInvalid)
	}
	if !toDate.After(fromDate) {
		return apperror.Field("to", apperror.FieldOutOfRange)
	}

	if _, err := uuid.Parse(botID); err != nil {
		return apperror.Field("bot_id", apperror.FieldInvalid)
	}

	modelID := strings.TrimSpace(c.QueryParam("model_id"))
	if modelID != "" {
		if _, err := uuid.Parse(modelID); err != nil {
			return apperror.Field("model_id", apperror.FieldInvalid)
		}
	}
	sessionType, err := parseTokenUsageSessionType(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	filter := usagepersistence.Filter{
		BotID:       botID,
		From:        fromDate,
		To:          toDate,
		ModelID:     modelID,
		SessionType: sessionType,
	}

	chat, discuss, acpAgent, heartbeat, schedule, err := h.fetchUsageByDay(ctx, filter)
	if err != nil {
		h.logger.Error("fetch token usage failed", slog.Any("error", err))
		return apperror.Internal("fetch token usage", err)
	}

	byModel, err := h.fetchUsageByModel(ctx, filter)
	if err != nil {
		h.logger.Error("fetch token usage by model failed", slog.Any("error", err))
		return apperror.Internal("fetch token usage by model", err)
	}

	resp := TokenUsageResponse{
		Chat:      chat,
		Discuss:   discuss,
		ACPAgent:  acpAgent,
		Heartbeat: heartbeat,
		Schedule:  schedule,
		ByModel:   byModel,
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *TokenUsageHandler) fetchUsageByDay(ctx context.Context, filter usagepersistence.Filter) (chat, discuss, acpAgent, heartbeat, schedule []DailyTokenUsage, err error) {
	rows, err := h.reader.GetDaily(ctx, filter)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	for _, r := range rows {
		d := DailyTokenUsage{
			Day:             formatUsageDate(r.Day),
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
		case "heartbeat":
			heartbeat = append(heartbeat, d)
		case "schedule":
			schedule = append(schedule, d)
		default:
			chat = append(chat, d)
		}
	}
	return chat, discuss, acpAgent, heartbeat, schedule, nil
}

func (h *TokenUsageHandler) fetchUsageByModel(ctx context.Context, filter usagepersistence.Filter) ([]ModelTokenUsage, error) {
	rows, err := h.reader.GetByModel(ctx, filter)
	if err != nil {
		return nil, err
	}

	result := make([]ModelTokenUsage, 0, len(rows))
	for _, r := range rows {
		result = append(result, ModelTokenUsage{
			ModelID:      r.ModelID,
			ModelSlug:    r.ModelSlug,
			ModelName:    r.ModelName,
			ProviderName: r.ProviderName,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
		})
	}
	return result, nil
}

func formatUsageDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02")
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
// @Param session_type query string false "Optional session type: chat, discuss, heartbeat, schedule, or acp_agent. acp_agent filters by runtime."
// @Param limit query int false "Page size (default 20, max 100)"
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} TokenUsageRecordsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/token-usage/records [get].
func (h *TokenUsageHandler) ListTokenUsageRecords(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if _, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID); err != nil {
		return err
	}

	fromStr := strings.TrimSpace(c.QueryParam("from"))
	toStr := strings.TrimSpace(c.QueryParam("to"))
	if fromStr == "" || toStr == "" {
		return apperror.Required("from")
	}
	fromDate, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return apperror.Field("from", apperror.FieldInvalid)
	}
	toDate, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return apperror.Field("to", apperror.FieldInvalid)
	}
	if !toDate.After(fromDate) {
		return apperror.Field("to", apperror.FieldOutOfRange)
	}

	if _, err := uuid.Parse(botID); err != nil {
		return apperror.Field("bot_id", apperror.FieldInvalid)
	}

	modelID := strings.TrimSpace(c.QueryParam("model_id"))
	if modelID != "" {
		if _, err := uuid.Parse(modelID); err != nil {
			return apperror.Field("model_id", apperror.FieldInvalid)
		}
	}

	sessionType, err := parseTokenUsageSessionType(c)
	if err != nil {
		return err
	}

	limit, err := httpx.ParseInt32Query(c.QueryParam("limit"), tokenUsageRecordsDefaultLimit)
	if err != nil {
		return err
	}
	if limit <= 0 {
		limit = tokenUsageRecordsDefaultLimit
	}
	if limit > tokenUsageRecordsMaxLimit {
		limit = tokenUsageRecordsMaxLimit
	}
	offset, err := httpx.ParseInt32Query(c.QueryParam("offset"), 0)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	page, err := h.reader.ListRecords(ctx, usagepersistence.Filter{
		BotID:       botID,
		From:        fromDate,
		To:          toDate,
		ModelID:     modelID,
		SessionType: sessionType,
	}, usagepersistence.Pagination{
		Limit:  int(limit),
		Offset: int(offset),
	})
	if err != nil {
		if errors.Is(err, usagepersistence.ErrCountRecords) {
			h.logger.Error("count token usage records failed", slog.Any("error", err))
			return apperror.Internal("count token usage records", err)
		}
		h.logger.Error("list token usage records failed", slog.Any("error", err))
		return apperror.Internal("list token usage records", err)
	}

	items := make([]TokenUsageRecord, 0, len(page.Items))
	for _, r := range page.Items {
		items = append(items, TokenUsageRecord{
			ID:              r.ID,
			CreatedAt:       formatUsageTime(r.CreatedAt),
			SessionID:       r.SessionID,
			SessionType:     r.SessionType,
			ModelID:         r.ModelID,
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
		Total: page.Total,
	})
}

func parseTokenUsageSessionType(c echo.Context) (string, error) {
	switch sessionType := strings.TrimSpace(c.QueryParam("session_type")); sessionType {
	case "":
		return "", nil
	case session.TypeChat, session.TypeDiscuss, session.TypeHeartbeat, session.TypeSchedule, session.TypeACPAgent:
		return sessionType, nil
	default:
		return "", apperror.Field("session_type", apperror.FieldUnsupported)
	}
}

func formatUsageTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
