package chat

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/bot/setting"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/iam/account"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/internal/apperror"
)

type CompactionHandler struct {
	service          *compaction.Service
	botService       *bot.Service
	accountService   *account.Service
	settingsService  *setting.Service
	modelsService    *modelcatalog.Service
	providerResolver modelcatalog.ProviderResolver
	logger           *slog.Logger
}

func NewCompactionHandler(
	log *slog.Logger,
	service *compaction.Service,
	botService *bot.Service,
	accountService *account.Service,
	settingsService *setting.Service,
	modelsService *modelcatalog.Service,
	providerResolver modelcatalog.ProviderResolver,
) *CompactionHandler {
	return &CompactionHandler{
		service:          service,
		botService:       botService,
		accountService:   accountService,
		settingsService:  settingsService,
		modelsService:    modelsService,
		providerResolver: providerResolver,
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
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/compaction/logs [get].
func (h *CompactionHandler) ListLogs(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if _, err := httpx.AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, userID, botID, bot.PermissionChat); err != nil {
		return err
	}

	limit, offset := httpx.ParseOffsetLimit(c)
	items, total, err := h.service.ListLogs(c.Request().Context(), botID, limit, offset)
	if err != nil {
		return apperror.Internal("list compaction logs", err)
	}
	return c.JSON(http.StatusOK, compaction.ListLogsResponse{Items: items, TotalCount: total})
}

// DeleteLogs godoc
// @Summary Delete compaction logs
// @Description Delete all compaction logs for a bot
// @Tags compaction
// @Param bot_id path string true "Bot ID"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/compaction/logs [delete].
func (h *CompactionHandler) DeleteLogs(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	if err := h.service.DeleteLogs(c.Request().Context(), botID); err != nil {
		return apperror.Internal("delete compaction logs", err)
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
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/compact [post].
func (h *CompactionHandler) TriggerCompact(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if _, err := httpx.AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, userID, botID, bot.PermissionChat); err != nil {
		return err
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return apperror.Required("session_id")
	}

	cfg, err := h.buildTriggerConfig(c.Request().Context(), botID, sessionID)
	if err != nil {
		if _, ok := apperror.As(err); ok {
			return err
		}
		return apperror.Invalid("build compaction config", err)
	}

	res, err := h.service.RunCompactionSync(c.Request().Context(), cfg)
	if err != nil {
		return apperror.Internal("run compaction", err)
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
	modelID := botSettings.CompactionModelID
	if modelID == "" {
		modelID = botSettings.ChatModelID
	}
	if modelID == "" {
		return compaction.TriggerConfig{}, apperror.Invalid("resolve compaction model", nil)
	}

	compactModel, err := h.modelsService.GetByID(ctx, modelID)
	if err != nil {
		return compaction.TriggerConfig{}, err
	}
	if !compactModel.Enable {
		return compaction.TriggerConfig{}, apperror.Field("compaction_model_id", apperror.FieldUnsupported)
	}
	compactProvider, err := modelcatalog.FetchProviderByID(ctx, h.providerResolver, compactModel.ProviderID)
	if err != nil {
		return compaction.TriggerConfig{}, err
	}

	cfg := compaction.TriggerConfig{
		BotID:            botID,
		SessionID:        sessionID,
		ModelID:          compactModel.ModelID,
		ClientType:       string(compactProvider.ClientType),
		APIKey:           compactProvider.APIKey,
		CodexAccountID:   compactProvider.CodexAccountID,
		BaseURL:          compactProvider.BaseURL,
		Ratio:            100,
		TotalInputTokens: 1,
	}
	return cfg, nil
}

func (h *CompactionHandler) requireUserID(c echo.Context) (string, error) {
	return httpx.RequireChannelIdentityID(c)
}

func (h *CompactionHandler) authorizeBotAccess(ctx context.Context, userID, botID string) (bot.Bot, error) {
	return httpx.AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, userID, botID, bot.PermissionManage)
}
