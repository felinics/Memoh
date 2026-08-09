package handlers

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/accounts"
	"github.com/memohai/memoh/internal/bots"
)

// BotPeerGrantListResponse wraps the list of bot-to-bot access grants for a bot.
type BotPeerGrantListResponse struct {
	Items []bots.PeerGrant `json:"items"`
}

// BotPeerCandidate is a bot eligible to be granted access to another bot.
type BotPeerCandidate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// BotPeerCandidateListResponse wraps the list of grantable peer bots.
type BotPeerCandidateListResponse struct {
	Items []BotPeerCandidate `json:"items"`
}

// BotPeerAccessHandler exposes CRUD for bot-to-bot access grants on a bot.
type BotPeerAccessHandler struct {
	botService     *bots.Service
	accountService *accounts.Service
}

// NewBotPeerAccessHandler constructs a BotPeerAccessHandler.
func NewBotPeerAccessHandler(botService *bots.Service, accountService *accounts.Service) *BotPeerAccessHandler {
	return &BotPeerAccessHandler{
		botService:     botService,
		accountService: accountService,
	}
}

func (h *BotPeerAccessHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/bot-access")
	group.GET("", h.ListGrants)
	group.POST("", h.CreateGrant)
	group.PUT("/:grant_id", h.UpdateGrant)
	group.DELETE("/:grant_id", h.DeleteGrant)
	group.GET("/candidates", h.ListCandidates)
}

// ListGrants godoc
// @Summary List bot peer access grants
// @Description List the bot-to-bot access grants configured on a bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} BotPeerGrantListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/bot-access [get].
func (h *BotPeerAccessHandler) ListGrants(c echo.Context) error {
	botID, _, err := h.requireCalleeManage(c)
	if err != nil {
		return err
	}
	items, err := h.botService.ListPeerGrants(c.Request().Context(), botID)
	if err != nil {
		return h.mapGrantError(err)
	}
	return c.JSON(http.StatusOK, BotPeerGrantListResponse{Items: items})
}

// CreateGrant godoc
// @Summary Create bot peer access grant
// @Description Let another bot (or any bot in the team) reach this bot with a peer permission set
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param payload body bots.CreatePeerGrantRequest true "Grant payload"
// @Success 201 {object} bots.PeerGrant
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/bot-access [post].
func (h *BotPeerAccessHandler) CreateGrant(c echo.Context) error {
	botID, actorID, err := h.requireCalleeManage(c)
	if err != nil {
		return err
	}
	var req bots.CreatePeerGrantRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.requireCallerSideAuthority(c.Request().Context(), actorID, req.SubjectType, req.SubjectBotID); err != nil {
		return err
	}
	item, err := h.botService.CreatePeerGrant(c.Request().Context(), botID, actorID, req)
	if err != nil {
		return h.mapGrantError(err)
	}
	return c.JSON(http.StatusCreated, item)
}

// UpdateGrant godoc
// @Summary Update bot peer access grant
// @Description Update the peer permission set of a bot access grant
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param grant_id path string true "Grant ID"
// @Param payload body bots.UpdatePeerGrantRequest true "Grant payload"
// @Success 200 {object} bots.PeerGrant
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/bot-access/{grant_id} [put].
func (h *BotPeerAccessHandler) UpdateGrant(c echo.Context) error {
	botID, actorID, err := h.requireCalleeManage(c)
	if err != nil {
		return err
	}
	grantID := strings.TrimSpace(c.Param("grant_id"))
	if grantID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "grant_id is required")
	}
	var req bots.UpdatePeerGrantRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	// An update replaces the scope set and can therefore widen it, so it needs
	// the same caller-side authority a create does. The subject comes from the
	// stored row, not the request, so it cannot be spoofed.
	subjectBotID, err := h.botService.SubjectBotIDForPeerGrant(c.Request().Context(), botID, grantID)
	if err != nil {
		return h.mapGrantError(err)
	}
	subjectType := bots.PeerGrantSubjectBot
	if subjectBotID == "" {
		subjectType = bots.PeerGrantSubjectAnyBot
	}
	if err := h.requireCallerSideAuthority(c.Request().Context(), actorID, subjectType, subjectBotID); err != nil {
		return err
	}
	item, err := h.botService.UpdatePeerGrant(c.Request().Context(), botID, grantID, req)
	if err != nil {
		return h.mapGrantError(err)
	}
	return c.JSON(http.StatusOK, item)
}

// DeleteGrant godoc
// @Summary Delete bot peer access grant
// @Description Remove a bot-to-bot access grant from a bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param grant_id path string true "Grant ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/bot-access/{grant_id} [delete].
func (h *BotPeerAccessHandler) DeleteGrant(c echo.Context) error {
	botID, _, err := h.requireCalleeManage(c)
	if err != nil {
		return err
	}
	grantID := strings.TrimSpace(c.Param("grant_id"))
	if grantID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "grant_id is required")
	}
	// Revocation is de-escalation, so it deliberately requires only the callee
	// side. Demanding the caller side too would mean a manager could be unable
	// to withdraw access their own bot granted.
	if err := h.botService.DeletePeerGrant(c.Request().Context(), botID, grantID); err != nil {
		return h.mapGrantError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListCandidates godoc
// @Summary List grantable peer bots
// @Description List bots that can be granted access to this bot: bots the caller manages, minus this bot and the ones already granted
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param q query string false "Search query"
// @Param limit query int false "Max results"
// @Success 200 {object} BotPeerCandidateListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/bot-access/candidates [get].
func (h *BotPeerAccessHandler) ListCandidates(c echo.Context) error {
	botID, actorID, err := h.requireCalleeManage(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	// Only bots the caller manages are offered, which keeps the bidirectional
	// authority rule invisible in the happy path: every candidate shown is one
	// the subsequent create call will accept.
	manageable, err := h.listManageableBots(ctx, actorID)
	if err != nil {
		return err
	}
	granted, err := h.botService.ListPeerGrants(ctx, botID)
	if err != nil {
		return h.mapGrantError(err)
	}
	taken := make(map[string]bool, len(granted))
	for _, grant := range granted {
		if grant.SubjectBotID != "" {
			taken[grant.SubjectBotID] = true
		}
	}

	query := strings.ToLower(strings.TrimSpace(c.QueryParam("q")))
	limit := parseLimit(c.QueryParam("limit"))
	items := make([]BotPeerCandidate, 0, len(manageable))
	for _, bot := range manageable {
		if bot.ID == botID || taken[bot.ID] {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(bot.Name), query) &&
			!strings.Contains(strings.ToLower(bot.DisplayName), query) {
			continue
		}
		items = append(items, BotPeerCandidate{
			ID:          bot.ID,
			Name:        bot.Name,
			DisplayName: bot.DisplayName,
			AvatarURL:   bot.AvatarURL,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return c.JSON(http.StatusOK, BotPeerCandidateListResponse{Items: items})
}

// listManageableBots returns the accessible bots on which the actor holds manage.
func (h *BotPeerAccessHandler) listManageableBots(ctx context.Context, actorID string) ([]bots.Bot, error) {
	if h.botService == nil || h.accountService == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	isAdmin, err := h.accountService.IsAdmin(ctx, actorID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	accessible, err := h.botService.ListAccessible(ctx, actorID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	items := make([]bots.Bot, 0, len(accessible))
	for _, bot := range accessible {
		perms, err := h.botService.ResolveUserPermissionsForBot(ctx, bot, actorID, isAdmin)
		if err != nil {
			return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if bots.HasPermission(perms, bots.PermissionManage) {
			items = append(items, bot)
		}
	}
	return items, nil
}

// requireCallerSideAuthority enforces the caller half of the bidirectional rule.
//
// The callee's manager consenting to be reached is only half the decision: the
// grant also hands the subject bot a capability it did not have, and the subject
// bot's context can leave through it. So a directed grant additionally requires
// manage on the subject bot.
//
// A blanket 'any_bot' grant has no single subject whose manager could consent —
// it reaches bots the actor may not manage at all — so it requires workspace
// admin instead. Both branches fail closed: a permission lookup that errors
// denies rather than allows.
func (h *BotPeerAccessHandler) requireCallerSideAuthority(ctx context.Context, actorID, subjectType, subjectBotID string) error {
	switch strings.ToLower(strings.TrimSpace(subjectType)) {
	case bots.PeerGrantSubjectAnyBot:
		if h.accountService == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
		}
		isAdmin, err := h.accountService.IsAdmin(ctx, actorID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if !isAdmin {
			return echo.NewHTTPError(http.StatusForbidden, "admin role required to grant access to every bot")
		}
		return nil
	case bots.PeerGrantSubjectBot:
		subjectID := strings.TrimSpace(subjectBotID)
		if subjectID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, bots.ErrPeerGrantBotRequired.Error())
		}
		if _, err := AuthorizeBotAccess(ctx, h.botService, h.accountService, actorID, subjectID); err != nil {
			return err
		}
		return nil
	default:
		return echo.NewHTTPError(http.StatusBadRequest, bots.ErrInvalidGrantSubject.Error())
	}
}

func (h *BotPeerAccessHandler) requireCalleeManage(c echo.Context) (string, string, error) {
	actorID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	bot, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, actorID, botID)
	if err != nil {
		return "", "", err
	}
	// Grants are stored against the canonical UUID, but bot_id in the URL may be
	// a name slug.
	return bot.ID, actorID, nil
}

func (*BotPeerAccessHandler) mapGrantError(err error) error {
	switch {
	case errors.Is(err, bots.ErrPeerGrantNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, bots.ErrBotNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "bot not found")
	case errors.Is(err, bots.ErrPeerGrantSubjectNotFound):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, bots.ErrInvalidPermission),
		errors.Is(err, bots.ErrInvalidGrantSubject),
		errors.Is(err, bots.ErrPeerGrantBotRequired),
		errors.Is(err, bots.ErrPeerGrantSelfConflict):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, bots.ErrGrantExists):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
}
