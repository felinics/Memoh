package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
)

// BotUserGrantListResponse wraps the list of workspace user access grants for a bot.
type BotUserGrantListResponse struct {
	Items []bots.UserGrant `json:"items"`
}

// BotUserCandidate is a workspace member eligible to be granted bot access.
type BotUserCandidate struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// BotUserCandidateListResponse wraps the list of grantable workspace members.
type BotUserCandidateListResponse struct {
	Items []BotUserCandidate `json:"items"`
}

// BotUserAccessHandler exposes CRUD for workspace user access grants on a bot.
type BotUserAccessHandler struct {
	botService     *bots.Service
	accountService *accounts.Service
}

// NewBotUserAccessHandler constructs a BotUserAccessHandler.
func NewBotUserAccessHandler(botService *bots.Service, accountService *accounts.Service) *BotUserAccessHandler {
	return &BotUserAccessHandler{
		botService:     botService,
		accountService: accountService,
	}
}

func (h *BotUserAccessHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/user-access")
	group.GET("", h.ListGrants)
	group.POST("", h.CreateGrant)
	group.PUT("/:grant_id", h.UpdateGrant)
	group.DELETE("/:grant_id", h.DeleteGrant)
	group.GET("/candidates", h.ListCandidates)
}

// ListGrants godoc
// @Summary List bot user access grants
// @Description List workspace user access grants for a bot, including the owner entry
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} BotUserGrantListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/user-access [get].
func (h *BotUserAccessHandler) ListGrants(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	items, err := h.botService.ListUserGrants(c.Request().Context(), botID)
	if err != nil {
		return h.mapGrantError(err)
	}
	return c.JSON(http.StatusOK, BotUserGrantListResponse{Items: items})
}

// CreateGrant godoc
// @Summary Create bot user access grant
// @Description Grant a workspace user (or everyone) access to a bot with a permission set
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param payload body bots.CreateUserGrantRequest true "Grant payload"
// @Success 201 {object} bots.UserGrant
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/user-access [post].
func (h *BotUserAccessHandler) CreateGrant(c echo.Context) error {
	botID, actorID, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	var req bots.CreateUserGrantRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	item, err := h.botService.CreateUserGrant(c.Request().Context(), botID, actorID, req)
	if err != nil {
		return h.mapGrantError(err)
	}
	return c.JSON(http.StatusCreated, item)
}

// UpdateGrant godoc
// @Summary Update bot user access grant
// @Description Update the permission set of a grant
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param grant_id path string true "Grant ID"
// @Param payload body bots.UpdateUserGrantRequest true "Grant payload"
// @Success 200 {object} bots.UserGrant
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/user-access/{grant_id} [put].
func (h *BotUserAccessHandler) UpdateGrant(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	grantID := strings.TrimSpace(c.Param("grant_id"))
	if grantID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "grant_id is required")
	}
	var req bots.UpdateUserGrantRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	item, err := h.botService.UpdateUserGrant(c.Request().Context(), botID, grantID, req)
	if err != nil {
		return h.mapGrantError(err)
	}
	return c.JSON(http.StatusOK, item)
}

// DeleteGrant godoc
// @Summary Delete bot user access grant
// @Description Remove a workspace user access grant from a bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param grant_id path string true "Grant ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/user-access/{grant_id} [delete].
func (h *BotUserAccessHandler) DeleteGrant(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	grantID := strings.TrimSpace(c.Param("grant_id"))
	if grantID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "grant_id is required")
	}
	if err := h.botService.DeleteUserGrant(c.Request().Context(), botID, grantID); err != nil {
		return h.mapGrantError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListCandidates godoc
// @Summary List grantable workspace members
// @Description List workspace members that can be granted access to a bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param q query string false "Search query"
// @Param limit query int false "Max results"
// @Success 200 {object} BotUserCandidateListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/user-access/candidates [get].
func (h *BotUserAccessHandler) ListCandidates(c echo.Context) error {
	if _, _, err := h.requireManageAccess(c); err != nil {
		return err
	}
	accountsList, err := h.accountService.SearchAccounts(c.Request().Context(), strings.TrimSpace(c.QueryParam("q")), parseLimit(c.QueryParam("limit")))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	items := make([]BotUserCandidate, 0, len(accountsList))
	for _, account := range accountsList {
		if !account.IsActive {
			continue
		}
		items = append(items, BotUserCandidate{
			ID:          account.ID,
			Username:    account.Username,
			DisplayName: account.DisplayName,
			AvatarURL:   account.AvatarURL,
		})
	}
	return c.JSON(http.StatusOK, BotUserCandidateListResponse{Items: items})
}

func (h *BotUserAccessHandler) requireManageAccess(c echo.Context) (string, string, error) {
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

func (*BotUserAccessHandler) mapGrantError(err error) error {
	switch {
	case errors.Is(err, bots.ErrGrantNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, bots.ErrOwnerUserNotFound):
		return echo.NewHTTPError(http.StatusBadRequest, "user not found")
	case errors.Is(err, bots.ErrInvalidPermission),
		errors.Is(err, bots.ErrInvalidGrantSubject),
		errors.Is(err, bots.ErrGrantUserRequired),
		errors.Is(err, bots.ErrGrantOwnerConflict):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, bots.ErrGrantExists):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, bots.ErrBotNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "bot not found")
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
}
