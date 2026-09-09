package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel/identities"
	identitypkg "github.com/felinics/memoh/internal/identity"
)

type ACLHandler struct {
	service         *acl.Service
	botService      *bots.Service
	accountService  *accounts.Service
	identityService *identities.Service
}

func NewACLHandler(service *acl.Service, botService *bots.Service, accountService *accounts.Service, identityService *identities.Service) *ACLHandler {
	return &ACLHandler{
		service:         service,
		botService:      botService,
		accountService:  accountService,
		identityService: identityService,
	}
}

func (h *ACLHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/acl")
	group.GET("/rules", h.ListRules)
	group.POST("/rules", h.CreateRule)
	group.PUT("/rules/:rule_id", h.UpdateRule)
	group.DELETE("/rules/:rule_id", h.DeleteRule)
	group.GET("/default-effect", h.GetDefaultEffect)
	group.PUT("/default-effect", h.SetDefaultEffect)
	group.GET("/channel-identities", h.SearchChannelIdentities)
	group.GET("/channel-identities/:channel_identity_id/conversations", h.ListObservedConversations)
	group.GET("/channel-types/:channel_type/conversations", h.ListObservedConversationsByChannelType)
}

// ListRules godoc
// @Summary List bot ACL rules
// @Description List all ACL rules for a bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} acl.ListRulesResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/rules [get].
func (h *ACLHandler) ListRules(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListRules(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, acl.ListRulesResponse{Items: items})
}

// CreateRule godoc
// @Summary Create ACL rule
// @Description Create a new ACL rule for chat.trigger
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param payload body acl.CreateRuleRequest true "Rule payload"
// @Success 201 {object} acl.Rule
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/rules [post].
func (h *ACLHandler) CreateRule(c echo.Context) error {
	botID, actorID, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	var req acl.CreateRuleRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	item, err := h.service.CreateRule(c.Request().Context(), botID, actorID, req)
	if err != nil {
		return h.mapRuleError(err)
	}
	return c.JSON(http.StatusCreated, item)
}

// UpdateRule godoc
// @Summary Update ACL rule
// @Description Update an existing ACL rule
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param rule_id path string true "Rule ID"
// @Param payload body acl.UpdateRuleRequest true "Rule payload"
// @Success 200 {object} acl.Rule
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/rules/{rule_id} [put].
func (h *ACLHandler) UpdateRule(c echo.Context) error {
	if _, _, err := h.requireManageAccess(c); err != nil {
		return err
	}
	ruleID := strings.TrimSpace(c.Param("rule_id"))
	if ruleID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "rule_id is required")
	}
	var req acl.UpdateRuleRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	item, err := h.service.UpdateRule(c.Request().Context(), ruleID, req)
	if err != nil {
		return h.mapRuleError(err)
	}
	return c.JSON(http.StatusOK, item)
}

// DeleteRule godoc
// @Summary Delete ACL rule
// @Description Delete an ACL rule by ID
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param rule_id path string true "Rule ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/rules/{rule_id} [delete].
func (h *ACLHandler) DeleteRule(c echo.Context) error {
	if _, _, err := h.requireManageAccess(c); err != nil {
		return err
	}
	ruleID := strings.TrimSpace(c.Param("rule_id"))
	if ruleID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "rule_id is required")
	}
	if err := h.service.DeleteRule(c.Request().Context(), ruleID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// GetDefaultEffect godoc
// @Summary Get bot ACL default effect
// @Description Get the fallback effect when no rule matches
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} acl.DefaultEffectResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/default-effect [get].
func (h *ACLHandler) GetDefaultEffect(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	effect, err := h.service.GetDefaultEffect(c.Request().Context(), botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, acl.DefaultEffectResponse{DefaultEffect: effect})
}

// SetDefaultEffect godoc
// @Summary Set bot ACL default effect
// @Description Set the fallback effect when no rule matches (allow or deny)
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param payload body acl.DefaultEffectResponse true "Default effect payload"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/default-effect [put].
func (h *ACLHandler) SetDefaultEffect(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	var req acl.DefaultEffectResponse
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.service.SetDefaultEffect(c.Request().Context(), botID, req.DefaultEffect); err != nil {
		if errors.Is(err, acl.ErrInvalidEffect) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// SearchChannelIdentities godoc
// @Summary Search ACL channel identity candidates
// @Description Search locally observed channel identities for building ACL rules
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param q query string false "Search query"
// @Param limit query int false "Max results"
// @Success 200 {object} acl.ChannelIdentityCandidateListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/channel-identities [get].
func (h *ACLHandler) SearchChannelIdentities(c echo.Context) error {
	if _, _, err := h.requireManageAccess(c); err != nil {
		return err
	}
	items, err := h.identityService.Search(c.Request().Context(), strings.TrimSpace(c.QueryParam("q")), parseLimit(c.QueryParam("limit")))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	result := make([]acl.ChannelIdentityCandidate, 0, len(items))
	for _, item := range items {
		result = append(result, acl.ChannelIdentityCandidate{
			ID:               item.ID,
			Channel:          item.Channel,
			ChannelSubjectID: item.ChannelSubjectID,
			DisplayName:      item.DisplayName,
			AvatarURL:        item.AvatarURL,
		})
	}
	return c.JSON(http.StatusOK, acl.ChannelIdentityCandidateListResponse{Items: result})
}

// ListObservedConversations godoc
// @Summary List observed conversations for a channel identity
// @Description List previously observed conversation candidates for a channel identity, for scoped rule building
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param channel_identity_id path string true "Channel Identity ID"
// @Success 200 {object} acl.ObservedConversationCandidateListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/channel-identities/{channel_identity_id}/conversations [get].
func (h *ACLHandler) ListObservedConversations(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	channelIdentityID := strings.TrimSpace(c.Param("channel_identity_id"))
	if err := identitypkg.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	items, err := h.service.ListObservedConversationsByChannelIdentity(c.Request().Context(), botID, channelIdentityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, acl.ObservedConversationCandidateListResponse{Items: items})
}

// ListObservedConversationsByChannelType godoc
// @Summary List observed conversations for a platform type
// @Description List previously observed group/thread conversation candidates for a channel type under this bot
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param channel_type path string true "Channel type (e.g. telegram, discord)"
// @Success 200 {object} acl.ObservedConversationCandidateListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/acl/channel-types/{channel_type}/conversations [get].
func (h *ACLHandler) ListObservedConversationsByChannelType(c echo.Context) error {
	botID, _, err := h.requireManageAccess(c)
	if err != nil {
		return err
	}
	channelType := strings.TrimSpace(c.Param("channel_type"))
	if channelType == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "channel_type is required")
	}
	items, err := h.service.ListObservedConversationsByChannelType(c.Request().Context(), botID, channelType)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, acl.ObservedConversationCandidateListResponse{Items: items})
}

func (h *ACLHandler) requireManageAccess(c echo.Context) (string, string, error) {
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

func (*ACLHandler) mapRuleError(err error) error {
	if errors.Is(err, acl.ErrInvalidRuleSubject) ||
		errors.Is(err, acl.ErrInvalidSourceScope) ||
		errors.Is(err, acl.ErrInvalidEffect) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
}

func parseLimit(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 50
	}
	if value > 200 {
		return 200
	}
	return value
}
