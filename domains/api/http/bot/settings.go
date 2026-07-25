package bot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat"
	"github.com/memohai/memoh/domains/api/bot"
	agenthttp "github.com/memohai/memoh/domains/api/http/agent"
	httpx "github.com/memohai/memoh/domains/api/http/httpx"
	"github.com/memohai/memoh/domains/api/setting"
	"github.com/memohai/memoh/domains/iam/account"
)

type SettingsHandler struct {
	service          *setting.Service
	botService       *bot.Service
	accountService   *account.Service
	heartbeatService *heartbeat.Service
	logger           *slog.Logger
}

func NewSettingsHandler(log *slog.Logger, service *setting.Service, botService *bot.Service, accountService *account.Service, heartbeatService *heartbeat.Service) *SettingsHandler {
	return &SettingsHandler{
		service:          service,
		botService:       botService,
		accountService:   accountService,
		heartbeatService: heartbeatService,
		logger:           log.With(slog.String("handler", "settings")),
	}
}

func (h *SettingsHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/settings")
	group.GET("", h.Get)
	group.POST("", h.Upsert)
	group.PUT("", h.Upsert)
	group.DELETE("", h.Delete)
}

// Get godoc
// @Summary Get user settings
// @Description Get agent settings for current user
// @Tags settings
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} setting.Settings
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/settings [get].
func (h *SettingsHandler) Get(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	// Reading settings is part of the chat experience (the chat UI needs model
	// capabilities, etc.), so allow chat-level members. Writes stay manage-only.
	if _, err := httpx.AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bot.PermissionChat); err != nil {
		return err
	}
	resp, err := h.service.GetBot(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Upsert godoc
// @Summary Update user settings
// @Description Update or create agent settings for current user
// @Tags settings
// @Param bot_id path string true "Bot ID"
// @Param payload body setting.UpsertRequest true "Settings payload"
// @Success 200 {object} setting.Settings
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/settings [put]
// @Router /bots/{bot_id}/settings [post].
func (h *SettingsHandler) Upsert(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	var req setting.UpsertRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.UpsertBot(c.Request().Context(), botID, req)
	if err != nil {
		if feedbackErr := agenthttp.AcpFeedbackHTTPError(err); feedbackErr != nil {
			return feedbackErr
		}
		if errors.Is(err, setting.ErrInvalidModelRef) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, setting.ErrModelIDAmbiguous) {
			return echo.NewHTTPError(http.StatusConflict, "model_id is duplicated across providers; select by model UUID")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if req.HeartbeatEnabled != nil || req.HeartbeatInterval != nil {
		if err := h.heartbeatService.Reschedule(c.Request().Context(), botID); err != nil {
			h.logger.Error("failed to reschedule heartbeat", slog.String("bot_id", botID), slog.Any("error", err))
		}
	}

	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete user settings
// @Description Remove agent settings for current user
// @Tags settings
// @Param bot_id path string true "Bot ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/settings [delete].
func (h *SettingsHandler) Delete(c echo.Context) error {
	channelIdentityID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), channelIdentityID, botID); err != nil {
		return err
	}
	if err := h.service.Delete(c.Request().Context(), botID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

func (*SettingsHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return httpx.RequireChannelIdentityID(c)
}

func (h *SettingsHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bot.Bot, error) {
	return httpx.AuthorizeBotAccess(ctx, h.botService, h.accountService, channelIdentityID, botID)
}
