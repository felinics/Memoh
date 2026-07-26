package runtime

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/iam/account"
	netctl "github.com/memohai/memoh/domains/runtime/network"
	"github.com/memohai/memoh/internal/apperror"
)

type NetworkHandler struct {
	service        *netctl.Service
	botService     *bot.Service
	accountService *account.Service
	logger         *slog.Logger
}

func NewNetworkHandler(log *slog.Logger, service *netctl.Service, botService *bot.Service, accountService *account.Service) *NetworkHandler {
	return &NetworkHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "network")),
	}
}

func (h *NetworkHandler) Register(e *echo.Echo) {
	metaGroup := e.Group("/network")
	metaGroup.GET("/meta", h.ListMeta)

	group := e.Group("/bots/:bot_id/network")
	group.GET("/status", h.Status)
	group.GET("/nodes", h.ListNodes)
	group.POST("/actions/:action_id", h.ExecuteAction)
}

func (h *NetworkHandler) ListMeta(c echo.Context) error {
	if _, err := httpx.RequireChannelIdentityID(c); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h.service.ListMeta(c.Request().Context()))
}

func (h *NetworkHandler) Status(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	status, err := h.service.StatusBot(c.Request().Context(), botID)
	if err != nil {
		return apperror.Invalid("get network status", err)
	}
	return c.JSON(http.StatusOK, status)
}

func (h *NetworkHandler) ListNodes(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	resp, err := h.service.ListBotNodes(c.Request().Context(), botID)
	if err != nil {
		return apperror.Invalid("list network nodes", err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *NetworkHandler) ExecuteAction(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	actionID := strings.TrimSpace(c.Param("action_id"))
	if actionID == "" {
		return apperror.Required("action_id")
	}
	var req netctl.BotActionRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind network action", err)
	}
	resp, err := h.service.ExecuteActionBot(c.Request().Context(), botID, actionID, req.Input)
	if err != nil {
		return apperror.Invalid("execute network action", err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *NetworkHandler) authorize(c echo.Context) (string, error) {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", apperror.Required("bot_id")
	}
	if _, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID); err != nil {
		return "", err
	}
	return botID, nil
}
