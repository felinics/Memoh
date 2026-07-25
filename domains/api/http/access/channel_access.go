package access

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/access"
	"github.com/memohai/memoh/domains/api/access/acl"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http/httpx"
	identitypkg "github.com/memohai/memoh/domains/api/identity"
	"github.com/memohai/memoh/domains/iam/account"
)

// ChannelAccessHandler exposes the per-bot Manage capability (Channel Access
// managers) and the global account-binding flow (Connected Accounts).
type ChannelAccessHandler struct {
	service        *access.Service
	botService     *bot.Service
	accountService *account.Service
}

// NewChannelAccessHandler constructs a ChannelAccessHandler.
func NewChannelAccessHandler(service *access.Service, botService *bot.Service, accountService *account.Service) *ChannelAccessHandler {
	return &ChannelAccessHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
	}
}

func (h *ChannelAccessHandler) Register(e *echo.Echo) {
	managers := e.Group("/bots/:bot_id/channel-managers")
	managers.GET("", h.ListManagers)
	managers.POST("", h.SetManager)
	managers.DELETE("/:channel_identity_id", h.ClearManagerOverride)

	links := e.Group("/users/me/channel-links")
	links.POST("", h.IssueLinkCode)

	identities := e.Group("/users/me/channel-identities")
	identities.GET("", h.ListBindings)
	identities.DELETE("/:channel_identity_id", h.Unbind)
}

// ListManagers godoc
// @Summary List channel managers
// @Description List effective Manage state per channel identity on a bot (inherited + local overrides)
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} access.ListManagersResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/channel-managers [get].
func (h *ChannelAccessHandler) ListManagers(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListManagers(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, access.ListManagersResponse{Items: items})
}

// SetManager godoc
// @Summary Set a channel manage override
// @Description Force the Manage capability ON or OFF for a channel identity on a bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param payload body access.SetManagerRequest true "Override payload"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/channel-managers [post].
func (h *ChannelAccessHandler) SetManager(c echo.Context) error {
	botID, actorID, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	var req access.SetManagerRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	channelIdentityID := strings.TrimSpace(req.ChannelIdentityID)
	if err := identitypkg.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.service.SetManager(c.Request().Context(), botID, channelIdentityID, req.Granted, actorID); err != nil {
		if errors.Is(err, access.ErrInvalidInput) || errors.Is(err, acl.ErrInvalidRuleSubject) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// ClearManagerOverride godoc
// @Summary Clear a channel manage override
// @Description Remove the local Manage override so the channel identity falls back to inheritance
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param channel_identity_id path string true "Channel Identity ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/channel-managers/{channel_identity_id} [delete].
func (h *ChannelAccessHandler) ClearManagerOverride(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	channelIdentityID := strings.TrimSpace(c.Param("channel_identity_id"))
	if err := identitypkg.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.service.ClearManagerOverride(c.Request().Context(), botID, channelIdentityID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// IssueLinkCode godoc
// @Summary Issue an account link code
// @Description Generate a one-time code to send as /link <code> in IM to bind that channel identity to your account
// @Tags users
// @Param payload body access.IssueLinkCodeRequest false "Link code options"
// @Success 201 {object} access.LinkCode
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/channel-links [post].
func (h *ChannelAccessHandler) IssueLinkCode(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req access.IssueLinkCodeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	code, err := h.service.IssueLinkCode(c.Request().Context(), userID, strings.TrimSpace(req.ChannelType))
	if err != nil {
		if errors.Is(err, access.ErrInvalidInput) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusCreated, code)
}

// ListBindings godoc
// @Summary List connected channel identities
// @Description List the IM channel identities bound to the current user's account
// @Tags users
// @Success 200 {object} access.ListBindingsResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/channel-identities [get].
func (h *ChannelAccessHandler) ListBindings(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListUserBindings(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, access.ListBindingsResponse{Items: items})
}

// Unbind godoc
// @Summary Disconnect a channel identity
// @Description Remove a channel identity binding from the current user's account
// @Tags users
// @Param channel_identity_id path string true "Channel Identity ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/channel-identities/{channel_identity_id} [delete].
func (h *ChannelAccessHandler) Unbind(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	channelIdentityID := strings.TrimSpace(c.Param("channel_identity_id"))
	if err := identitypkg.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.service.Unbind(c.Request().Context(), userID, channelIdentityID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *ChannelAccessHandler) requireManageAccess(c echo.Context) (string, string, error) {
	actorID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return "", "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	if _, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, actorID, botID); err != nil {
		return "", "", err
	}
	return botID, actorID, nil
}
