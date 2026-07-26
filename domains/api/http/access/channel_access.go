package access

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
	botaccess "github.com/memohai/memoh/domains/api/bot/access"
	"github.com/memohai/memoh/domains/api/bot/access/acl"
	httpx "github.com/memohai/memoh/domains/api/http"
	identitypkg "github.com/memohai/memoh/domains/api/identity"
	identitylink "github.com/memohai/memoh/domains/api/identity/link"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

// ChannelAccessHandler exposes the per-bot Manage capability (Channel Access
// managers) and the global account-binding flow (Connected Accounts).
type ChannelAccessHandler struct {
	managerService *botaccess.Service
	linkService    *identitylink.Service
	botService     *bot.Service
	accountService *account.Service
}

// NewChannelAccessHandler constructs a ChannelAccessHandler.
func NewChannelAccessHandler(managerService *botaccess.Service, linkService *identitylink.Service, botService *bot.Service, accountService *account.Service) *ChannelAccessHandler {
	return &ChannelAccessHandler{
		managerService: managerService,
		linkService:    linkService,
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
// @Success 200 {object} botaccess.ListManagersResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/channel-managers [get].
func (h *ChannelAccessHandler) ListManagers(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	items, err := h.managerService.ListManagers(c.Request().Context(), botID)
	if err != nil {
		return apperror.Internal("list channel managers", err)
	}
	return c.JSON(http.StatusOK, botaccess.ListManagersResponse{Items: items})
}

// SetManager godoc
// @Summary Set a channel manage override
// @Description Force the Manage capability ON or OFF for a channel identity on a bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param payload body botaccess.SetManagerRequest true "Override payload"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/channel-managers [post].
func (h *ChannelAccessHandler) SetManager(c echo.Context) error {
	botID, actorID, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	var req botaccess.SetManagerRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind channel manager", err)
	}
	channelIdentityID := strings.TrimSpace(req.ChannelIdentityID)
	if err := identitypkg.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return apperror.Invalid("validate channel identity", err)
	}
	if err := h.managerService.SetManager(c.Request().Context(), botID, channelIdentityID, req.Granted, actorID); err != nil {
		if errors.Is(err, acl.ErrInvalidRuleSubject) {
			return apperror.Invalid("set channel manager", err)
		}
		return apperror.Internal("set channel manager", err)
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
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/channel-managers/{channel_identity_id} [delete].
func (h *ChannelAccessHandler) ClearManagerOverride(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	channelIdentityID := strings.TrimSpace(c.Param("channel_identity_id"))
	if err := identitypkg.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return apperror.Invalid("validate channel identity", err)
	}
	if err := h.managerService.ClearManagerOverride(c.Request().Context(), botID, channelIdentityID); err != nil {
		return apperror.Internal("clear channel manager", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// IssueLinkCode godoc
// @Summary Issue an account link code
// @Description Generate a one-time code to send as /link <code> in IM to bind that channel identity to your account
// @Tags users
// @Param payload body identitylink.IssueLinkCodeRequest false "Link code options"
// @Success 201 {object} identitylink.LinkCode
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /users/me/channel-links [post].
func (h *ChannelAccessHandler) IssueLinkCode(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req identitylink.IssueLinkCodeRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind link code", err)
	}
	code, err := h.linkService.IssueLinkCode(c.Request().Context(), userID, strings.TrimSpace(req.ChannelType))
	if err != nil {
		if errors.Is(err, identitylink.ErrInvalidInput) {
			return apperror.Invalid("issue link code", err)
		}
		return apperror.Internal("issue link code", err)
	}
	return c.JSON(http.StatusCreated, code)
}

// ListBindings godoc
// @Summary List connected channel identities
// @Description List the IM channel identities bound to the current user's account
// @Tags users
// @Success 200 {object} identitylink.ListBindingsResponse
// @Failure 500 {object} apperror.Problem
// @Router /users/me/channel-identities [get].
func (h *ChannelAccessHandler) ListBindings(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	items, err := h.linkService.ListUserBindings(c.Request().Context(), userID)
	if err != nil {
		return apperror.Internal("list channel bindings", err)
	}
	return c.JSON(http.StatusOK, identitylink.ListBindingsResponse{Items: items})
}

// Unbind godoc
// @Summary Disconnect a channel identity
// @Description Remove a channel identity binding from the current user's account
// @Tags users
// @Param channel_identity_id path string true "Channel Identity ID"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /users/me/channel-identities/{channel_identity_id} [delete].
func (h *ChannelAccessHandler) Unbind(c echo.Context) error {
	userID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	channelIdentityID := strings.TrimSpace(c.Param("channel_identity_id"))
	if err := identitypkg.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return apperror.Invalid("validate channel identity", err)
	}
	if err := h.linkService.Unbind(c.Request().Context(), userID, channelIdentityID); err != nil {
		return apperror.Internal("unbind channel identity", err)
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
		return "", "", apperror.Required("bot_id")
	}
	if _, err := httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, actorID, botID); err != nil {
		return "", "", err
	}
	return botID, actorID, nil
}
