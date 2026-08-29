package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channelaccess"
	identitypkg "github.com/felinics/memoh/internal/identity"
)

// ChannelAccessHandler exposes the per-bot Manage capability (Channel Access
// managers) and the global account-binding flow (Connected Accounts).
type ChannelAccessHandler struct {
	service        *channelaccess.Service
	botService     *bots.Service
	accountService *accounts.Service
}

// NewChannelAccessHandler constructs a ChannelAccessHandler.
func NewChannelAccessHandler(service *channelaccess.Service, botService *bots.Service, accountService *accounts.Service) *ChannelAccessHandler {
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
// @Success 200 {object} channelaccess.ListManagersResponse
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
	return c.JSON(http.StatusOK, channelaccess.ListManagersResponse{Items: items})
}

// SetManager godoc
// @Summary Set a channel manage override
// @Description Force the Manage capability ON or OFF for a channel identity on a bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param payload body channelaccess.SetManagerRequest true "Override payload"
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
	var req channelaccess.SetManagerRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	channelIdentityID := strings.TrimSpace(req.ChannelIdentityID)
	if err := identitypkg.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.service.SetManager(c.Request().Context(), botID, channelIdentityID, req.Granted, actorID); err != nil {
		if errors.Is(err, channelaccess.ErrInvalidInput) || errors.Is(err, acl.ErrInvalidRuleSubject) {
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
// @Param payload body channelaccess.IssueLinkCodeRequest false "Link code options"
// @Success 201 {object} channelaccess.LinkCode
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/channel-links [post].
func (h *ChannelAccessHandler) IssueLinkCode(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req channelaccess.IssueLinkCodeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	code, err := h.service.IssueLinkCode(c.Request().Context(), userID, strings.TrimSpace(req.ChannelType))
	if err != nil {
		if errors.Is(err, channelaccess.ErrInvalidInput) {
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
// @Success 200 {object} channelaccess.ListBindingsResponse
// @Failure 500 {object} ErrorResponse
// @Router /users/me/channel-identities [get].
func (h *ChannelAccessHandler) ListBindings(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListUserBindings(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, channelaccess.ListBindingsResponse{Items: items})
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
	userID, err := RequireChannelIdentityID(c)
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
	actorID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, actorID, botID); err != nil {
		return "", "", err
	}
	return botID, actorID, nil
}
