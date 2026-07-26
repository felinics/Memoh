package agent

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

type HeartbeatHandler struct {
	service        *heartbeat.Service
	botService     *bot.Service
	accountService *account.Service
	logger         *slog.Logger
}

func NewHeartbeatHandler(log *slog.Logger, service *heartbeat.Service, botService *bot.Service, accountService *account.Service) *HeartbeatHandler {
	return &HeartbeatHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "heartbeat")),
	}
}

func (h *HeartbeatHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/heartbeat")
	group.GET("/logs", h.ListLogs)
	group.DELETE("/logs", h.DeleteLogs)
}

// ListLogs godoc
// @Summary List heartbeat logs
// @Description List heartbeat execution logs for a bot
// @Tags heartbeat
// @Param bot_id path string true "Bot ID"
// @Param limit query int false "Limit" default(50)
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} heartbeat.ListLogsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/heartbeat/logs [get].
func (h *HeartbeatHandler) ListLogs(c echo.Context) error {
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

	limit, offset := httpx.ParseOffsetLimit(c)
	items, total, err := h.service.ListLogs(c.Request().Context(), botID, limit, offset)
	if err != nil {
		return apperror.Internal("list heartbeat logs", err)
	}
	return c.JSON(http.StatusOK, heartbeat.ListLogsResponse{Items: items, TotalCount: total})
}

// DeleteLogs godoc
// @Summary Delete heartbeat logs
// @Description Delete all heartbeat execution logs for a bot
// @Tags heartbeat
// @Param bot_id path string true "Bot ID"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/heartbeat/logs [delete].
func (h *HeartbeatHandler) DeleteLogs(c echo.Context) error {
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
		return apperror.Internal("delete heartbeat logs", err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (*HeartbeatHandler) requireUserID(c echo.Context) (string, error) {
	return httpx.RequireChannelIdentityID(c)
}

func (h *HeartbeatHandler) authorizeBotAccess(ctx context.Context, userID, botID string) (bot.Bot, error) {
	return httpx.AuthorizeBotAccess(ctx, h.botService, h.accountService, userID, botID)
}
