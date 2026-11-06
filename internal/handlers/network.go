package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
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

// ListMeta godoc
// @Summary List network providers
// @Description List the network providers a bot can use, with their config schema, capabilities and actions
// @Tags network
// @Success 200 {array} network.ProviderDescriptor
// @Failure 401 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /network/meta [get].
func (h *NetworkHandler) ListMeta(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h.service.ListMeta(c.Request().Context()))
}

// Status godoc
// @Summary Get bot network status
// @Description Get the state of the bot's network overlay and of the workspace it attaches to
// @Tags network
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} network.BotStatus
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/network/status [get].
func (h *NetworkHandler) Status(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	status, err := h.service.StatusBot(c.Request().Context(), botID)
	if err != nil {
		return networkHTTPError(err, "network status")
	}
	return c.JSON(http.StatusOK, status)
}

// ListNodes godoc
// @Summary List bot network nodes
// @Description List the peers the bot can use as exit nodes
// @Tags network
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} network.NodeListResponse
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/network/nodes [get].
func (h *NetworkHandler) ListNodes(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	resp, err := h.service.ListBotNodes(c.Request().Context(), botID)
	if err != nil {
		return networkHTTPError(err, "list network nodes")
	}
	return c.JSON(http.StatusOK, resp)
}

// ExecuteAction godoc
// @Summary Run a bot network action
// @Description Run a provider action such as test_connection or logout
// @Tags network
// @Param bot_id path string true "Bot ID"
// @Param action_id path string true "Action ID"
// @Param payload body network.BotActionRequest true "Action input"
// @Success 200 {object} network.ProviderActionExecution
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/network/actions/{action_id} [post].
func (h *NetworkHandler) ExecuteAction(c echo.Context) error {
	botID, err := h.authorize(c)
	if err != nil {
		return err
	}
	actionID, err := httpx.RequiredParam(c, "action_id")
	if err != nil {
		return err
	}
	var req netctl.BotActionRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	resp, err := h.service.ExecuteActionBot(c.Request().Context(), botID, actionID, req.Input)
	if err != nil {
		return networkHTTPError(err, "execute network action")
	}
	return c.JSON(http.StatusOK, resp)
}

// networkHTTPError answers the failures a user can act on and wraps the rest
// as internal faults.
func networkHTTPError(err error, op string) error {
	switch {
	case errors.Is(err, netctl.ErrProviderNotConfigured):
		return apperror.Wrap(apperror.CodeNetworkProviderNotConfigured, err, nil)
	case errors.Is(err, netctl.ErrUnsupportedAction):
		return apperror.FieldInvalid("action_id", err)
	default:
		return errs.Wrap(err, op)
	}
}

func (h *NetworkHandler) authorize(c echo.Context) (string, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return "", err
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID); err != nil {
		return "", err
	}
	return botID, nil
}
