package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/accounts"
	acpagent "github.com/memohai/memoh/internal/agent/runtime/acp"
	"github.com/memohai/memoh/internal/agentcredential"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/bots"
)

type agentCredentialRuntimeCloser interface {
	CloseBotAgentRuntimes(botID, agentID string) error
}

type AgentCredentialHandler struct {
	service        *agentcredential.Service
	botService     *bots.Service
	accountService *accounts.Service
	runtimes       agentCredentialRuntimeCloser
}

func NewAgentCredentialHandler(service *agentcredential.Service, botService *bots.Service, accountService *accounts.Service, runtimes *acpagent.SessionPool) *AgentCredentialHandler {
	return &AgentCredentialHandler{service: service, botService: botService, accountService: accountService, runtimes: runtimes}
}

func (h *AgentCredentialHandler) Register(e *echo.Echo) {
	e.GET("/agent-credentials", h.ListOwned)
	e.POST("/agent-credentials", h.Create)
	e.PATCH("/agent-credentials/:credential_id", h.Update)
	e.DELETE("/agent-credentials/:credential_id", h.Revoke)

	group := e.Group("/bots/:bot_id/agents/:agent_id/credentials")
	group.GET("", h.ListBindings)
	group.POST("", h.Bind)
	group.PUT("/default", h.SetDefault)
	group.DELETE("/:credential_id", h.Unbind)
}

// ListOwned godoc
// @Summary List owned Agent credentials
// @Tags agent-credentials
// @Success 200 {object} agentcredential.CredentialList
// @Failure 400 {object} apperror.Problem
// @Router /agent-credentials [get].
func (h *AgentCredentialHandler) ListOwned(c echo.Context) error {
	actorID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListOwned(c.Request().Context(), actorID)
	if err != nil {
		return mapAgentCredentialError(err)
	}
	return c.JSON(http.StatusOK, agentcredential.CredentialList{Items: items})
}

// Create godoc
// @Summary Create an encrypted Agent credential
// @Tags agent-credentials
// @Param payload body agentcredential.CreateRequest true "Credential"
// @Success 201 {object} agentcredential.PublicCredential
// @Failure 400 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /agent-credentials [post].
func (h *AgentCredentialHandler) Create(c echo.Context) error {
	actorID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req agentcredential.CreateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeAgentCredentialRequestInvalid, err, nil)
	}
	item, err := h.service.Create(c.Request().Context(), actorID, req)
	if err != nil {
		return mapAgentCredentialError(err)
	}
	return c.JSON(http.StatusCreated, item)
}

// Update godoc
// @Summary Rename an Agent credential
// @Tags agent-credentials
// @Param credential_id path string true "Credential ID"
// @Param payload body agentcredential.UpdateRequest true "Update"
// @Success 200 {object} agentcredential.PublicCredential
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /agent-credentials/{credential_id} [patch].
func (h *AgentCredentialHandler) Update(c echo.Context) error {
	actorID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req agentcredential.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeAgentCredentialRequestInvalid, err, nil)
	}
	item, err := h.service.UpdateLabel(c.Request().Context(), actorID, c.Param("credential_id"), req.Label)
	if err != nil {
		return mapAgentCredentialError(err)
	}
	return c.JSON(http.StatusOK, item)
}

// Revoke godoc
// @Summary Revoke an Agent credential
// @Tags agent-credentials
// @Param credential_id path string true "Credential ID"
// @Success 200 {object} agentcredential.PublicCredential
// @Failure 404 {object} apperror.Problem
// @Router /agent-credentials/{credential_id} [delete].
func (h *AgentCredentialHandler) Revoke(c echo.Context) error {
	actorID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	targets, err := h.service.BindingTargets(c.Request().Context(), c.Param("credential_id"))
	if err != nil {
		return mapAgentCredentialError(err)
	}
	item, err := h.service.Revoke(c.Request().Context(), actorID, c.Param("credential_id"))
	if err != nil {
		return mapAgentCredentialError(err)
	}
	for _, target := range targets {
		h.closeRuntimes(target.BotID, target.AgentID)
	}
	return c.JSON(http.StatusOK, item)
}

// ListBindings godoc
// @Summary List credentials bound to a Bot Agent
// @Tags agent-credentials
// @Param bot_id path string true "Bot ID"
// @Param agent_id path string true "Agent ID"
// @Success 200 {object} agentcredential.CredentialList
// @Failure 403 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{agent_id}/credentials [get].
func (h *AgentCredentialHandler) ListBindings(c echo.Context) error {
	botID, agentID, _, err := h.requireBotManage(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListBindings(c.Request().Context(), botID, agentID)
	if err != nil {
		return mapAgentCredentialError(err)
	}
	return c.JSON(http.StatusOK, agentcredential.CredentialList{Items: items})
}

// Bind godoc
// @Summary Bind a credential to a Bot Agent
// @Tags agent-credentials
// @Param bot_id path string true "Bot ID"
// @Param agent_id path string true "Agent ID"
// @Param payload body agentcredential.BindRequest true "Binding"
// @Success 201 {object} agentcredential.PublicCredential
// @Failure 404 {object} apperror.Problem
// @Failure 422 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{agent_id}/credentials [post].
func (h *AgentCredentialHandler) Bind(c echo.Context) error {
	botID, agentID, actorID, err := h.requireBotManage(c)
	if err != nil {
		return err
	}
	var req agentcredential.BindRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeAgentCredentialRequestInvalid, err, nil)
	}
	item, err := h.service.Bind(c.Request().Context(), actorID, botID, agentID, req.CredentialID, req.MakeDefault)
	if err != nil {
		return mapAgentCredentialError(err)
	}
	h.closeRuntimes(botID, agentID)
	return c.JSON(http.StatusCreated, item)
}

// SetDefault godoc
// @Summary Set the default credential for a Bot Agent
// @Tags agent-credentials
// @Param bot_id path string true "Bot ID"
// @Param agent_id path string true "Agent ID"
// @Param payload body agentcredential.SetDefaultRequest true "Default credential"
// @Success 204
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{agent_id}/credentials/default [put].
func (h *AgentCredentialHandler) SetDefault(c echo.Context) error {
	botID, agentID, _, err := h.requireBotManage(c)
	if err != nil {
		return err
	}
	var req agentcredential.SetDefaultRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeAgentCredentialRequestInvalid, err, nil)
	}
	if err := h.service.SetDefault(c.Request().Context(), botID, agentID, req.CredentialID); err != nil {
		return mapAgentCredentialError(err)
	}
	h.closeRuntimes(botID, agentID)
	return c.NoContent(http.StatusNoContent)
}

// Unbind godoc
// @Summary Unbind a credential from a Bot Agent
// @Tags agent-credentials
// @Param bot_id path string true "Bot ID"
// @Param agent_id path string true "Agent ID"
// @Param credential_id path string true "Credential ID"
// @Success 204
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{agent_id}/credentials/{credential_id} [delete].
func (h *AgentCredentialHandler) Unbind(c echo.Context) error {
	botID, agentID, _, err := h.requireBotManage(c)
	if err != nil {
		return err
	}
	if err := h.service.Unbind(c.Request().Context(), botID, agentID, c.Param("credential_id")); err != nil {
		return mapAgentCredentialError(err)
	}
	h.closeRuntimes(botID, agentID)
	return c.NoContent(http.StatusNoContent)
}

func (h *AgentCredentialHandler) requireBotManage(c echo.Context) (string, string, string, error) {
	actorID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", "", "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	agentID := strings.ToLower(strings.TrimSpace(c.Param("agent_id")))
	if botID == "" || agentID == "" {
		return "", "", "", echo.NewHTTPError(http.StatusBadRequest, "bot_id and agent_id are required")
	}
	if _, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, actorID, botID); err != nil {
		return "", "", "", err
	}
	return botID, agentID, actorID, nil
}

func (h *AgentCredentialHandler) closeRuntimes(botID, agentID string) {
	if h.runtimes != nil {
		_ = h.runtimes.CloseBotAgentRuntimes(botID, agentID)
	}
}

func mapAgentCredentialError(err error) error {
	switch {
	case errors.Is(err, agentcredential.ErrNotFound):
		return apperror.New(apperror.CodeAgentCredentialNotFound, nil)
	case errors.Is(err, agentcredential.ErrForbidden):
		return apperror.New(apperror.CodeAgentCredentialForbidden, nil)
	case errors.Is(err, agentcredential.ErrIncompatible):
		return apperror.New(apperror.CodeAgentCredentialIncompatible, nil)
	case errors.Is(err, agentcredential.ErrRevoked):
		return apperror.New(apperror.CodeAgentCredentialRevoked, nil)
	case errors.Is(err, agentcredential.ErrEncryptionUnavailable):
		return apperror.Wrap(apperror.CodeAgentCredentialEncryptionUnavailable, err, nil)
	case errors.Is(err, agentcredential.ErrInvalidRequest):
		return apperror.New(apperror.CodeAgentCredentialRequestInvalid, nil)
	default:
		return apperror.Wrap(apperror.CodeAgentCredentialMaterializationFailed, err, nil)
	}
}
