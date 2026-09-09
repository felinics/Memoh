package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	netctl "github.com/felinics/memoh/internal/network"
)

type NetworkHandler struct {
	service        *netctl.Service
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewNetworkHandler(log *slog.Logger, service *netctl.Service, botService *bots.Service, accountService *accounts.Service) *NetworkHandler {
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
	if _, err := RequireChannelIdentityID(c); err != nil {
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
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
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
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
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
		return echo.NewHTTPError(http.StatusBadRequest, "action_id is required")
	}
	var req netctl.BotActionRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.service.ExecuteActionBot(c.Request().Context(), botID, actionID, req.Input)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *NetworkHandler) authorize(c echo.Context) (string, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID); err != nil {
		return "", err
	}
	return botID, nil
}
