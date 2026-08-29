package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/settings"
)

type SettingsHandler struct {
	service        *settings.Service
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewSettingsHandler(log *slog.Logger, service *settings.Service, botService *bots.Service, accountService *accounts.Service) *SettingsHandler {
	return &SettingsHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "settings")),
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
// @Success 200 {object} settings.Settings
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
	if _, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bots.PermissionChat); err != nil {
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
// @Param payload body settings.UpsertRequest true "Settings payload"
// @Success 200 {object} settings.Settings
// @Failure 400 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
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
	var req settings.UpsertRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.UpsertBot(c.Request().Context(), botID, req)
	if err != nil {
		if botAgentErr := botAgentHTTPError(err); botAgentErr != nil {
			return botAgentErr
		}
		if reasoningErr := settingsReasoningHTTPError(err); reasoningErr != nil {
			return reasoningErr
		}
		if feedbackErr := acpFeedbackHTTPError(err); feedbackErr != nil {
			return feedbackErr
		}
		if errors.Is(err, settings.ErrInvalidModelRef) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, settings.ErrModelIDAmbiguous) {
			return echo.NewHTTPError(http.StatusConflict, "model_id is duplicated across providers; select by model UUID")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, resp)
}

func settingsReasoningHTTPError(err error) error {
	var invalid *settings.InvalidReasoningEffortError
	if errors.As(err, &invalid) {
		return apperror.New(apperror.CodeSettingsReasoningEffortInvalid, map[string]string{
			"effort": invalid.Effort,
		})
	}
	if errors.Is(err, settings.ErrReasoningOptionsUnavailable) {
		return apperror.Wrap(apperror.CodeSettingsReasoningUnavailable, err, nil)
	}
	return nil
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
	return RequireChannelIdentityID(c)
}

func (h *SettingsHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccess(ctx, h.botService, h.accountService, channelIdentityID, botID)
}
