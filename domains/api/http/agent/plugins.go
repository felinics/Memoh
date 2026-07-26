package agent

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	pluginspkg "github.com/memohai/memoh/domains/agent/extension/plugins"
	pluginspersistence "github.com/memohai/memoh/domains/agent/extension/plugins/persistence"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

type PluginsHandler struct {
	service        *pluginspkg.Service
	botService     *bot.Service
	accountService *account.Service
	logger         *slog.Logger
}

func NewPluginsHandler(log *slog.Logger, service *pluginspkg.Service, botService *bot.Service, accountService *account.Service) *PluginsHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PluginsHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "plugins")),
	}
}

func (h *PluginsHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/plugins")
	group.GET("", h.List)
	group.GET("/:id", h.Get)
	group.POST("/:id/enable", h.Enable)
	group.POST("/:id/disable", h.Disable)
	group.POST("/:id/uninstall", h.Uninstall)
	group.DELETE("/:id", h.Purge)
	group.POST("/:id/oauth/authorize", h.StartOAuth)
	group.GET("/:id/oauth/status", h.RefreshOAuthStatus)
}

func (h *PluginsHandler) requireBotAccess(c echo.Context) (string, error) {
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

func pluginIDParam(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return "", apperror.Required("id")
	}
	return id, nil
}

// List godoc
// @Summary List bot plugins
// @Tags plugins
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} plugins.ListResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/plugins [get].
func (h *PluginsHandler) List(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	items, err := h.service.List(c.Request().Context(), botID)
	if err != nil {
		return apperror.Internal("list plugins", err)
	}
	return c.JSON(http.StatusOK, pluginspkg.ListResponse{Items: items})
}

// Get godoc
// @Summary Get bot plugin installation
// @Tags plugins
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Plugin installation ID"
// @Success 200 {object} plugins.Installation
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/plugins/{id} [get].
func (h *PluginsHandler) Get(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	id, err := pluginIDParam(c)
	if err != nil {
		return err
	}
	resp, err := h.service.Get(c.Request().Context(), botID, id)
	if err != nil {
		return pluginServiceError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

// Enable godoc
// @Summary Enable bot plugin
// @Tags plugins
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Plugin installation ID"
// @Success 200 {object} plugins.Installation
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/plugins/{id}/enable [post].
func (h *PluginsHandler) Enable(c echo.Context) error {
	return h.setEnabled(c, true)
}

// Disable godoc
// @Summary Disable bot plugin
// @Tags plugins
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Plugin installation ID"
// @Success 200 {object} plugins.Installation
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/plugins/{id}/disable [post].
func (h *PluginsHandler) Disable(c echo.Context) error {
	return h.setEnabled(c, false)
}

func (h *PluginsHandler) setEnabled(c echo.Context, enabled bool) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	id, err := pluginIDParam(c)
	if err != nil {
		return err
	}
	resp, err := h.service.SetEnabled(c.Request().Context(), botID, id, enabled)
	if err != nil {
		return pluginServiceError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

// Uninstall godoc
// @Summary Uninstall bot plugin
// @Tags plugins
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Plugin installation ID"
// @Success 200 {object} plugins.Installation
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/plugins/{id}/uninstall [post].
func (h *PluginsHandler) Uninstall(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	id, err := pluginIDParam(c)
	if err != nil {
		return err
	}
	resp, err := h.service.Uninstall(c.Request().Context(), botID, id)
	if err != nil {
		return pluginServiceError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

// Purge godoc
// @Summary Purge bot plugin installation
// @Tags plugins
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Plugin installation ID"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/plugins/{id} [delete].
func (h *PluginsHandler) Purge(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	id, err := pluginIDParam(c)
	if err != nil {
		return err
	}
	if err := h.service.Purge(c.Request().Context(), botID, id); err != nil {
		return pluginServiceError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// StartOAuth godoc
// @Summary Start managed OAuth for a bot plugin
// @Tags plugins
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Plugin installation ID"
// @Param payload body plugins.OAuthAuthorizeRequest false "OAuth authorize request"
// @Success 200 {object} mcp.AuthorizeResult
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/plugins/{id}/oauth/authorize [post].
func (h *PluginsHandler) StartOAuth(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	id, err := pluginIDParam(c)
	if err != nil {
		return err
	}
	var req pluginspkg.OAuthAuthorizeRequest
	_ = c.Bind(&req)
	resp, err := h.service.StartOAuth(c.Request().Context(), botID, id, req.CallbackURL)
	if err != nil {
		return pluginServiceError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

// RefreshOAuthStatus godoc
// @Summary Refresh managed OAuth status for a bot plugin
// @Tags plugins
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Plugin installation ID"
// @Success 200 {object} plugins.Installation
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/plugins/{id}/oauth/status [get].
func (h *PluginsHandler) RefreshOAuthStatus(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	id, err := pluginIDParam(c)
	if err != nil {
		return err
	}
	resp, err := h.service.RefreshOAuthStatus(c.Request().Context(), botID, id)
	if err != nil {
		return pluginServiceError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

func pluginServiceError(err error) error {
	if errors.Is(err, pluginspersistence.ErrNotFound) {
		return apperror.NotFound("get plugin installation", err)
	}
	return apperror.Invalid("plugin service", err)
}
