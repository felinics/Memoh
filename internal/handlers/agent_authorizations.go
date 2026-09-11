package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
)

type AgentAuthorizationHandler struct {
	service  *agentcredential.AuthorizationService
	agents   *botagents.Service
	bots     *bots.Service
	accounts *accounts.Service
}

func NewAgentAuthorizationHandler(service *agentcredential.AuthorizationService, agents *botagents.Service, bots *bots.Service, accounts *accounts.Service) *AgentAuthorizationHandler {
	return &AgentAuthorizationHandler{service: service, agents: agents, bots: bots, accounts: accounts}
}

func (h *AgentAuthorizationHandler) Register(e *echo.Echo) {
	g := e.Group("/agent-authorizations")
	g.POST("", h.Create)
	g.GET("/:id", h.Get)
	g.POST("/:id/poll", h.Poll)
	g.POST("/:id/exchange", h.Exchange)
	g.DELETE("/:id", h.Cancel)
	e.POST("/bots/:bot_id/agents/:id/credential/claim", h.Claim)
}

// Create godoc
// @Summary Authorize an Agent account before creating a Bot
// @Description Starts Codex device authorization or Claude browser authorization, or encrypts a supplied API key or Claude OAuth token. No Bot or workspace is created. Ready means the credential has been staged; API keys and manually supplied tokens are not probed against the provider.
// @Tags agent-authorizations
// @Accept json
// @Produce json
// @Param payload body agentcredential.AuthorizationRequest true "Authorization request"
// @Success 201 {object} agentcredential.Authorization
// @Failure 400,403,409,429,503 {object} apperror.Problem
// @Router /agent-authorizations [post].
func (h *AgentAuthorizationHandler) Create(c echo.Context) error {
	owner, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req agentcredential.AuthorizationRequest
	if err := c.Bind(&req); err != nil {
		return mapAuthorizationError(agentcredential.ErrInvalidRequest)
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 30*time.Second)
	defer cancel()
	result, err := h.service.Create(ctx, owner, req)
	if err != nil {
		return mapAuthorizationError(err)
	}
	return c.JSON(http.StatusCreated, result)
}

// Get godoc
// @Summary Read an authorization session owned by the current user
// @Tags agent-authorizations
// @Param id path string true "Authorization ID"
// @Success 200 {object} agentcredential.Authorization
// @Failure 403,410,503 {object} apperror.Problem
// @Router /agent-authorizations/{id} [get].
func (h *AgentAuthorizationHandler) Get(c echo.Context) error { return h.status(c, false) }

// Poll godoc
// @Summary Poll a pending Agent device authorization
// @Tags agent-authorizations
// @Param id path string true "Authorization ID"
// @Success 200 {object} agentcredential.Authorization
// @Failure 403,410,503 {object} apperror.Problem
// @Router /agent-authorizations/{id}/poll [post].
func (h *AgentAuthorizationHandler) Poll(c echo.Context) error { return h.status(c, true) }

type AgentAuthorizationExchangeRequest struct {
	Code string `json:"code" validate:"required"`
}

// Exchange godoc
// @Summary Complete Claude browser authorization with the returned authorization code
// @Tags agent-authorizations
// @Accept json
// @Produce json
// @Param id path string true "Authorization ID"
// @Param payload body AgentAuthorizationExchangeRequest true "Authorization code"
// @Success 200 {object} agentcredential.Authorization
// @Failure 400,403,410,503 {object} apperror.Problem
// @Router /agent-authorizations/{id}/exchange [post].
func (h *AgentAuthorizationHandler) Exchange(c echo.Context) error {
	owner, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req AgentAuthorizationExchangeRequest
	if err := c.Bind(&req); err != nil {
		return mapAuthorizationError(agentcredential.ErrAuthorizationCodeInvalid)
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 30*time.Second)
	defer cancel()
	result, err := h.service.Exchange(ctx, owner, c.Param("id"), req.Code)
	if err != nil {
		return mapAuthorizationError(err)
	}
	return c.JSON(http.StatusOK, result)
}

func (h *AgentAuthorizationHandler) status(c echo.Context, poll bool) error {
	owner, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 30*time.Second)
	defer cancel()
	result, err := h.service.Get(ctx, owner, c.Param("id"), poll)
	if err != nil {
		return mapAuthorizationError(err)
	}
	return c.JSON(http.StatusOK, result)
}

// Cancel godoc
// @Summary Discard a temporary authorization without disconnecting an already-created Agent
// @Tags agent-authorizations
// @Param id path string true "Authorization ID"
// @Success 204
// @Failure 400,403,503 {object} apperror.Problem
// @Router /agent-authorizations/{id} [delete].
func (h *AgentAuthorizationHandler) Cancel(c echo.Context) error {
	owner, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	if err := h.service.Cancel(c.Request().Context(), owner, c.Param("id")); err != nil {
		return mapAuthorizationError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

type AgentAuthorizationClaimRequest struct {
	AuthorizationID string `json:"authorization_id" validate:"required"`
}

// Claim godoc
// @Summary Bind a completed temporary authorization to a Bot Agent
// @Description Requires Bot management access and ownership of the authorization. Retrying for the same Agent is safe; an authorization cannot be bound to another Agent. Credentials are attached atomically and removed from the temporary session.
// @Tags agent-authorizations
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Param payload body AgentAuthorizationClaimRequest true "Authorization reference"
// @Success 200 {object} agentcredential.PublicCredential
// @Failure 400,403,404,409,410,503 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{id}/credential/claim [post].
func (h *AgentAuthorizationHandler) Claim(c echo.Context) error {
	owner, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.bots, h.accounts, owner, c.Param("bot_id"), bots.PermissionManage)
	if err != nil {
		return err
	}
	agent, err := h.agents.Get(c.Request().Context(), bot.ID, c.Param("id"))
	if err != nil {
		return mapAuthorizationError(agentcredential.ErrNotFound)
	}
	var req AgentAuthorizationClaimRequest
	if err := c.Bind(&req); err != nil {
		return mapAuthorizationError(agentcredential.ErrInvalidRequest)
	}
	session, err := h.service.Get(c.Request().Context(), owner, req.AuthorizationID, false)
	if err != nil {
		return mapAuthorizationError(err)
	}
	// A completed claim can be retried after enabling. New bindings require a
	// disabled Agent; running Agents use the credential replacement API.
	if agent.Enabled && session.Status != "claimed" {
		return apperror.New(apperror.CodeAgentCredentialRuntimeBusy, nil)
	}
	if !botagents.AcceptsCredential(agent, session.AuthKind) {
		return mapAuthorizationError(agentcredential.ErrIncompatible)
	}
	result, err := h.service.Claim(c.Request().Context(), owner, req.AuthorizationID, bot.ID, agent.ID)
	if err != nil {
		return mapAuthorizationError(err)
	}
	return c.JSON(http.StatusOK, result)
}

func mapAuthorizationError(err error) error {
	switch {
	case errors.Is(err, agentcredential.ErrAuthorizationCodeInvalid):
		return apperror.Wrap(apperror.CodeAgentAuthorizationCodeInvalid, err, nil)
	case errors.Is(err, agentcredential.ErrAuthorizationExpired):
		return apperror.Wrap(apperror.CodeAgentAuthorizationExpired, err, nil)
	case errors.Is(err, agentcredential.ErrAuthorizationNotReady):
		return apperror.Wrap(apperror.CodeAgentAuthorizationNotReady, err, nil)
	case errors.Is(err, agentcredential.ErrAuthorizationLimit):
		return apperror.Wrap(apperror.CodeAgentAuthorizationLimit, err, nil)
	case errors.Is(err, agentcredential.ErrAuthorizationFailed):
		return apperror.Wrap(apperror.CodeAgentAuthorizationFailed, err, nil)
	default:
		mapped := mapAgentCredentialError(err)
		if apperror.CodeOf(mapped) != "" {
			return mapped
		}
		return apperror.Wrap(apperror.CodeAgentAuthorizationFailed, err, nil)
	}
}
