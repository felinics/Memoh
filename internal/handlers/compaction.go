package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/settings"
)

type CompactionHandler struct {
	service          *compaction.Service
	botService       *bots.Service
	accountService   *accounts.Service
	settingsService  *settings.Service
	modelsService    *models.Service
	queries          dbstore.Queries
	providersService *providers.Service
	logger           *slog.Logger
}

func NewCompactionHandler(
	log *slog.Logger,
	service *compaction.Service,
	botService *bots.Service,
	accountService *accounts.Service,
	settingsService *settings.Service,
	modelsService *models.Service,
	queries dbstore.Queries,
	providersService *providers.Service,
) *CompactionHandler {
	return &CompactionHandler{
		service:          service,
		botService:       botService,
		accountService:   accountService,
		settingsService:  settingsService,
		modelsService:    modelsService,
		queries:          queries,
		providersService: providersService,
		logger:           log.With(slog.String("handler", "compaction")),
	}
}

func (h *CompactionHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/compaction")
	group.GET("/logs", h.ListLogs)
	group.DELETE("/logs", h.DeleteLogs)
	e.POST("/bots/:bot_id/sessions/:session_id/compact", h.TriggerCompact)
}

// ListLogs godoc
// @Summary List compaction logs
// @Description List compaction logs for a bot
// @Tags compaction
// @Param bot_id path string true "Bot ID"
// @Param limit query int false "Limit" default(50)
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} compaction.ListLogsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/compaction/logs [get].
func (h *CompactionHandler) ListLogs(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, userID, botID, bots.PermissionChat); err != nil {
		return err
	}

	limit, offset := parseOffsetLimit(c)
	items, total, err := h.service.ListLogs(c.Request().Context(), botID, limit, offset)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, compaction.ListLogsResponse{Items: items, TotalCount: total})
}

// DeleteLogs godoc
// @Summary Delete compaction logs
// @Description Delete all compaction logs for a bot
// @Tags compaction
// @Param bot_id path string true "Bot ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/compaction/logs [delete].
func (h *CompactionHandler) DeleteLogs(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	if err := h.service.DeleteLogs(c.Request().Context(), botID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// TriggerCompactResponse is the API response for triggering compaction.
type TriggerCompactResponse struct {
	Status       string `json:"status"`
	Summary      string `json:"summary,omitempty"`
	MessageCount int    `json:"message_count"`
}

// TriggerCompact godoc
// @Summary Trigger immediate context compaction
// @Description Run context compaction synchronously for a session
// @Tags compaction
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} TriggerCompactResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/sessions/{session_id}/compact [post].
func (h *CompactionHandler) TriggerCompact(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, userID, botID, bots.PermissionChat); err != nil {
		return err
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session id is required")
	}

	cfg, err := h.buildTriggerConfig(c.Request().Context(), botID, sessionID)
	if err != nil {
		if apperror.CodeOf(err) != "" {
			return err
		}
		h.logger.Error("compaction: build trigger config failed",
			slog.String("bot_id", botID), slog.String("session_id", sessionID), slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "compaction failed")
	}

	res, err := h.service.RunCompactionSync(c.Request().Context(), cfg)
	if err != nil {
		mapped := compactionRunFailure(err)
		if apperror.CodeOf(mapped) == "" {
			h.logger.Error("compaction: manual run failed",
				slog.String("bot_id", botID), slog.String("session_id", sessionID), slog.Any("error", err))
		}
		return mapped
	}
	return c.JSON(http.StatusOK, TriggerCompactResponse{
		Status:       res.Status,
		Summary:      res.Summary,
		MessageCount: res.MessageCount,
	})
}

func (h *CompactionHandler) buildTriggerConfig(ctx context.Context, botID, sessionID string) (compaction.TriggerConfig, error) {
	botSettings, err := h.settingsService.GetBot(ctx, botID)
	if err != nil {
		return compaction.TriggerConfig{}, err
	}
	sessionModelID := ""
	if strings.TrimSpace(botSettings.CompactionModelID) == "" {
		sessionModelID = models.LatestSessionModelID(ctx, h.queries, sessionID)
	}
	resolution, err := models.ResolveCompactionModel(
		ctx,
		h.modelsService,
		h.queries,
		botSettings.CompactionModelID,
		sessionModelID,
		botSettings.ChatModelID,
	)
	if models.IsCompactionModelUnavailable(err) {
		return compaction.TriggerConfig{}, apperror.New(apperror.CodeCompactionModelUnavailable, map[string]string{
			"reason": compactionUnavailableReason(err),
		})
	}
	if err != nil {
		return compaction.TriggerConfig{}, err
	}
	creds, err := h.providersService.ResolveModelCredentials(ctx, resolution.Provider)
	if err != nil {
		return compaction.TriggerConfig{}, err
	}
	cfg := compaction.NewTriggerConfig(compaction.TriggerModel{
		Slug:                  resolution.Model.ModelID,
		RecordID:              resolution.Model.ID,
		ClientType:            resolution.Provider.ClientType,
		APIKey:                creds.APIKey,
		CodexAccountID:        creds.CodexAccountID,
		BaseURL:               providers.ProviderConfigString(resolution.Provider, "base_url"),
		ChatCompletionsCompat: providers.ProviderConfigString(resolution.Provider, models.ChatCompletionsCompatConfigKey),
		PromptCacheTTL:        providers.ProviderConfigString(resolution.Provider, "prompt_cache_ttl"),
		WindowTokens:          resolution.WindowTokens,
	})
	if chatModelID := strings.TrimSpace(botSettings.ChatModelID); chatModelID != "" {
		chatModel, chatErr := h.modelsService.GetByID(ctx, chatModelID)
		if chatErr == nil && chatModel.Config.ContextWindow != nil && *chatModel.Config.ContextWindow > 0 {
			cfg.ContextWindowTokens = *chatModel.Config.ContextWindow
		}
	}
	cfg.BotID = botID
	cfg.SessionID = sessionID
	cfg.Ratio = 100
	cfg.TotalInputTokens = 1
	cfg.Manual = true
	cfg.AllowFrontierFusion = true
	return cfg, nil
}

// compactionUnavailableReason maps resolution sentinels to the stable reason
// args of compaction.model_unavailable.
func compactionUnavailableReason(err error) string {
	switch {
	case errors.Is(err, models.ErrCompactionModelNotConfigured):
		return "not_configured"
	case errors.Is(err, models.ErrCompactionModelNotChat):
		return "not_chat"
	case errors.Is(err, models.ErrCompactionModelDisabled):
		return "model_disabled"
	case errors.Is(err, models.ErrCompactionProviderDisabled):
		return "provider_disabled"
	case errors.Is(err, models.ErrCompactionOutputLimitUnsupported):
		return "output_limit_unsupported"
	case errors.Is(err, models.ErrCompactionWindowUnknown):
		return "window_unknown"
	default:
		return "unavailable"
	}
}

func (*CompactionHandler) requireUserID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *CompactionHandler) authorizeBotAccess(ctx context.Context, userID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccess(ctx, h.botService, h.accountService, userID, botID)
}

// compactionRunFailure maps a summarizer run failure to its public shape: a
// too-small summarizer window is a stable capability condition the user can
// fix by picking another model, while any other failure surfaces as a generic
// error — the diagnostic detail (windows, budgets, provider responses) stays
// out of the response body.
func compactionRunFailure(err error) error {
	if errors.Is(err, compaction.ErrSummaryWindowTooSmall) {
		return apperror.New(apperror.CodeCompactionModelUnavailable, map[string]string{
			"reason": "window_too_small",
		})
	}
	return echo.NewHTTPError(http.StatusInternalServerError, "compaction failed")
}
