package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
)

// AgentCredentialHandler manages the single credential attached to a Bot
// Agent instance. There is no credential picking: PUT replaces, DELETE
// disconnects, GET reports the redacted state.
type AgentCredentialHandler struct {
	service        *agentcredential.Service
	agents         *botagents.Service
	botService     *bots.Service
	accountService *accounts.Service
	runtimes       external.Drivers
}

func NewAgentCredentialHandler(service *agentcredential.Service, agents *botagents.Service, botService *bots.Service, accountService *accounts.Service, runtimes external.Drivers) *AgentCredentialHandler {
	return &AgentCredentialHandler{service: service, agents: agents, botService: botService, accountService: accountService, runtimes: runtimes}
}

func (h *AgentCredentialHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/agents/:id/credential")
	group.GET("", h.Get)
	group.PUT("", h.Put)
	group.DELETE("", h.Delete)
}

type agentCredentialPutRequest struct {
	AuthKind string            `json:"auth_kind"`
	Secret   map[string]string `json:"secret"`
}

// Get godoc
// @Summary Get the credential attached to a Bot Agent
// @Tags agent-credentials
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Bot Agent ID"
// @Success 200 {object} agentcredential.PublicCredential
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{id}/credential [get].
func (h *AgentCredentialHandler) Get(c echo.Context) error {
	botID, botAgentID, err := h.requireBotManage(c)
	if err != nil {
		return err
	}
	credential, err := h.service.GetForBotAgent(c.Request().Context(), botID, botAgentID)
	if err != nil {
		return mapAgentCredentialError(err)
	}
	return c.JSON(http.StatusOK, credential)
}

// Put godoc
// @Summary Attach a credential to a Bot Agent, replacing any previous one
// @Description Creates an encrypted credential from the submitted secret and
// points the Agent instance at it. The replaced credential is revoked once no
// other instance references it. The Agent's warm runtimes are shut down so the
// next session starts with the new credential.
// @Tags agent-credentials
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Bot Agent ID"
// @Param payload body agentCredentialPutRequest true "Secret"
// @Success 200 {object} agentcredential.PublicCredential
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 422 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{id}/credential [put].
func (h *AgentCredentialHandler) Put(c echo.Context) error {
	botID, botAgentID, err := h.requireBotManage(c)
	if err != nil {
		return err
	}
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req agentCredentialPutRequest
	if err := c.Bind(&req); err != nil {
		return apperror.New(apperror.CodeAgentCredentialRequestInvalid, nil)
	}
	agent, err := h.agents.Get(c.Request().Context(), botID, botAgentID)
	if err != nil {
		return mapAgentCredentialError(agentcredential.ErrNotFound)
	}
	if !botagents.AcceptsCredential(agent, req.AuthKind) {
		return mapAgentCredentialError(agentcredential.ErrIncompatible)
	}
	credential, err := h.service.AttachToBotAgent(c.Request().Context(), channelIdentityID, botID, botAgentID, agentcredential.CreateRequest{
		Provider: agentcredential.ProviderForAuthKind(req.AuthKind),
		AuthKind: req.AuthKind,
		Secret:   req.Secret,
	})
	if err != nil {
		return mapAgentCredentialError(err)
	}
	h.closeRuntimes(agent.Runtime, botID, botAgentID)
	return c.JSON(http.StatusOK, credential)
}

// Delete godoc
// @Summary Disconnect a Bot Agent's credential
// @Tags agent-credentials
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Bot Agent ID"
// @Success 204
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{id}/credential [delete].
func (h *AgentCredentialHandler) Delete(c echo.Context) error {
	botID, botAgentID, err := h.requireBotManage(c)
	if err != nil {
		return err
	}
	agent, err := h.agents.Get(c.Request().Context(), botID, botAgentID)
	if err != nil {
		return mapAgentCredentialError(agentcredential.ErrNotFound)
	}
	if err := h.runtimes.PurgeBotAgentAuth(c.Request().Context(), agent.Runtime, botID, botAgentID); err != nil {
		if apperror.CodeOf(err) != "" {
			return err
		}
		return apperror.Wrap(apperror.CodeAgentCredentialMaterializationFailed, err, nil)
	}
	if err := h.service.DetachFromBotAgent(c.Request().Context(), botID, botAgentID); err != nil {
		return mapAgentCredentialError(err)
	}
	h.closeRuntimes(agent.Runtime, botID, botAgentID)
	return c.NoContent(http.StatusNoContent)
}

func (h *AgentCredentialHandler) requireBotManage(c echo.Context) (string, string, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	botAgentID := strings.TrimSpace(c.Param("id"))
	if botID == "" || botAgentID == "" {
		return "", "", apperror.New(apperror.CodeAgentCredentialNotFound, nil)
	}
	if _, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bots.PermissionManage); err != nil {
		return "", "", err
	}
	return botID, botAgentID, nil
}

func (h *AgentCredentialHandler) closeRuntimes(runtimeType, botID, botAgentID string) {
	h.runtimes.ResetBotAgent(runtimeType, botID, botAgentID)
}

func mapAgentCredentialError(err error) error {
	switch {
	case errors.Is(err, agentcredential.ErrNotFound):
		return apperror.Wrap(apperror.CodeAgentCredentialNotFound, err, nil)
	case errors.Is(err, agentcredential.ErrInvalidRequest):
		return apperror.Wrap(apperror.CodeAgentCredentialRequestInvalid, err, nil)
	case errors.Is(err, agentcredential.ErrIncompatible):
		return apperror.Wrap(apperror.CodeAgentCredentialIncompatible, err, nil)
	case errors.Is(err, agentcredential.ErrRevoked):
		return apperror.Wrap(apperror.CodeAgentCredentialRevoked, err, nil)
	case errors.Is(err, agentcredential.ErrEncryptionUnavailable):
		return apperror.Wrap(apperror.CodeAgentCredentialEncryptionUnavailable, err, nil)
	default:
		return err
	}
}
