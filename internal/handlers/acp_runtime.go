package handlers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
)

type ACPRuntimeHandler struct {
	pool           acpRuntimePool
	sessionService *session.Service
	botService     *bots.Service
	accountService *accounts.Service
}

type acpRuntimePool interface {
	RuntimeStatus(sessionID, agentID, projectPath string) acpagent.RuntimeStatus
	Ensure(ctx context.Context, input acpagent.PromptInput) (acpagent.RuntimeStatus, error)
	SetModel(ctx context.Context, input acpagent.PromptInput, modelID string) (acpagent.RuntimeStatus, error)
	SetReasoning(ctx context.Context, input acpagent.PromptInput, effort string) (acpagent.RuntimeStatus, error)
	SetMode(ctx context.Context, input acpagent.PromptInput, modeID string) (acpagent.RuntimeStatus, error)
	CreateRuntime(ctx context.Context, input acpagent.CreateRuntimeInput) (acpagent.RuntimeStatus, error)
	RuntimeStatusByID(botID, runtimeID string) (acpagent.RuntimeStatus, error)
	SetRuntimeModel(ctx context.Context, botID, runtimeID, modelID string) (acpagent.RuntimeStatus, error)
	SetRuntimeReasoning(ctx context.Context, botID, runtimeID, effort string) (acpagent.RuntimeStatus, error)
	SetRuntimeMode(ctx context.Context, botID, runtimeID, modeID string) (acpagent.RuntimeStatus, error)
	CloseRuntime(botID, runtimeID string) error
}

type acpRuntimeCreateRequest struct {
	AgentID     string `json:"acp_agent_id"`
	ProjectPath string `json:"project_path,omitempty"`
}

type acpRuntimeModelRequest struct {
	ModelID string `json:"model_id"`
}

type acpRuntimeReasoningRequest struct {
	ReasoningEffort string `json:"reasoning_effort"`
}

type acpRuntimeModeRequest struct {
	ModeID string `json:"mode_id" validate:"required"`
}

func NewACPRuntimeHandler(pool *acpagent.SessionPool, sessionService *session.Service, botService *bots.Service, accountService *accounts.Service) *ACPRuntimeHandler {
	return newACPRuntimeHandler(pool, sessionService, botService, accountService)
}

func newACPRuntimeHandler(pool acpRuntimePool, sessionService *session.Service, botService *bots.Service, accountService *accounts.Service) *ACPRuntimeHandler {
	return &ACPRuntimeHandler{
		pool:           pool,
		sessionService: sessionService,
		botService:     botService,
		accountService: accountService,
	}
}

func (h *ACPRuntimeHandler) Register(e *echo.Echo) {
	e.POST("/bots/:bot_id/acp-runtimes", h.CreateRuntime)
	e.GET("/bots/:bot_id/acp-runtimes/:runtime_id", h.GetRuntimeByID)
	e.PATCH("/bots/:bot_id/acp-runtimes/:runtime_id/model", h.SetRuntimeModel)
	e.PATCH("/bots/:bot_id/acp-runtimes/:runtime_id/reasoning", h.SetRuntimeReasoning)
	e.PATCH("/bots/:bot_id/acp-runtimes/:runtime_id/mode", h.SetRuntimeMode)
	e.DELETE("/bots/:bot_id/acp-runtimes/:runtime_id", h.CloseRuntime)
	e.GET("/bots/:bot_id/sessions/:session_id/acp-runtime", h.GetRuntime)
	e.POST("/bots/:bot_id/sessions/:session_id/acp-runtime", h.EnsureRuntime)
	e.PATCH("/bots/:bot_id/sessions/:session_id/acp-runtime/model", h.SetModel)
	e.PATCH("/bots/:bot_id/sessions/:session_id/acp-runtime/reasoning", h.SetReasoning)
	e.PATCH("/bots/:bot_id/sessions/:session_id/acp-runtime/mode", h.SetMode)
}

// CreateRuntime godoc
// @Summary Create an unbound ACP runtime (pre-session model picker)
// @Description Starts an agent runtime before any session exists. The runtime ID is server generated; bind it to a session at creation time via acp_runtime_id.
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param body body acpRuntimeCreateRequest true "Runtime spec"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 429 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes [post].
func (h *ACPRuntimeHandler) CreateRuntime(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return acpRuntimeHTTPError(err)
	}
	bot, err := h.authorizedACPBot(c)
	if err != nil {
		return err
	}
	var req acpRuntimeCreateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	agentID := acpprofile.NormalizeAgentID(req.AgentID)
	if agentID == "" {
		return apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	if err := acpAgentSetupHTTPError(bot.Metadata, agentID); err != nil {
		return acpRuntimeHTTPError(err)
	}
	projectPath := strings.TrimSpace(req.ProjectPath)
	if projectPath == "" {
		projectPath = session.DefaultACPProjectPath
	}
	status, err := h.pool.CreateRuntime(c.Request().Context(), acpagent.CreateRuntimeInput{
		BotID:                 bot.ID,
		AgentID:               agentID,
		ProjectPath:           projectPath,
		RuntimeOwnerAccountID: channelIdentityID,
		ToolHTTPURL:           buildACPMCPToolsURL(c, bot.ID),
	})
	if err != nil {
		if errors.Is(err, acpagent.ErrTooManyRuntimes) {
			return apperror.Wrap(apperror.CodeACPRuntimeLimitReached, err, nil)
		}
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// GetRuntimeByID godoc
// @Summary Get ACP runtime state by runtime ID
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param runtime_id path string true "Runtime ID"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id} [get].
func (h *ACPRuntimeHandler) GetRuntimeByID(c echo.Context) error {
	_, _, status, err := h.authorizedRuntimeByID(c)
	if err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			return runtimePoolError(err)
		}
		return acpRuntimeHTTPError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// SetRuntimeModel godoc
// @Summary Set (or reset) an ACP runtime's model
// @Description An empty model_id resets the runtime to the agent default model.
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param runtime_id path string true "Runtime ID"
// @Param body body acpRuntimeModelRequest true "Model selection"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id}/model [patch].
func (h *ACPRuntimeHandler) SetRuntimeModel(c echo.Context) error {
	bot, runtimeID, _, err := h.authorizedRuntimeByID(c)
	if err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			return runtimePoolError(err)
		}
		return acpRuntimeHTTPError(err)
	}
	var req acpRuntimeModelRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	status, err := h.pool.SetRuntimeModel(context.WithoutCancel(c.Request().Context()), bot.ID, runtimeID, strings.TrimSpace(req.ModelID))
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// SetRuntimeReasoning godoc
// @Summary Set an ACP runtime's reasoning effort
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param runtime_id path string true "Runtime ID"
// @Param body body acpRuntimeReasoningRequest true "Reasoning effort selection"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id}/reasoning [patch].
func (h *ACPRuntimeHandler) SetRuntimeReasoning(c echo.Context) error {
	bot, runtimeID, _, err := h.authorizedRuntimeByID(c)
	if err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			return runtimePoolError(err)
		}
		return acpRuntimeHTTPError(err)
	}
	var req acpRuntimeReasoningRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	status, err := h.pool.SetRuntimeReasoning(context.WithoutCancel(c.Request().Context()), bot.ID, runtimeID, strings.TrimSpace(req.ReasoningEffort))
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// SetRuntimeMode godoc
// @Summary Set an unbound ACP runtime's mode
// @Description Sends the selected agent-declared mode ID unchanged to session/set_mode before the first chat message binds this runtime to a Session.
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param runtime_id path string true "Runtime ID"
// @Param body body acpRuntimeModeRequest true "Mode selection"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id}/mode [patch].
func (h *ACPRuntimeHandler) SetRuntimeMode(c echo.Context) error {
	bot, runtimeID, _, err := h.authorizedRuntimeByID(c)
	if err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			return runtimePoolError(err)
		}
		return acpRuntimeHTTPError(err)
	}
	var req acpRuntimeModeRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	if strings.TrimSpace(req.ModeID) == "" {
		return apperror.New(apperror.CodeACPModeIDRequired, nil)
	}
	status, err := h.pool.SetRuntimeMode(context.WithoutCancel(c.Request().Context()), bot.ID, runtimeID, req.ModeID)
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// CloseRuntime godoc
// @Summary Close an ACP runtime
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param runtime_id path string true "Runtime ID"
// @Success 204
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id} [delete].
func (h *ACPRuntimeHandler) CloseRuntime(c echo.Context) error {
	bot, runtimeID, _, err := h.authorizedRuntimeByID(c)
	if err != nil {
		// Close is fire-and-forget on the client; a reaped runtime is fine.
		// authorizedRuntimeByID wraps ErrRuntimeNotFound into an apperror, so
		// match the stable code alongside the raw sentinel.
		if errors.Is(err, acpagent.ErrRuntimeNotFound) || apperror.CodeOf(err) == apperror.CodeACPRuntimeNotFound {
			return c.NoContent(http.StatusNoContent)
		}
		return acpRuntimeHTTPError(err)
	}
	if err := h.pool.CloseRuntime(bot.ID, runtimeID); err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			// Close is fire-and-forget on the client; a reaped runtime is fine.
			return c.NoContent(http.StatusNoContent)
		}
		return apperror.Wrap(apperror.CodeACPOperationFailed, err, nil)
	}
	return c.NoContent(http.StatusNoContent)
}

// GetRuntime godoc
// @Summary Get ACP session runtime state
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime [get].
func (h *ACPRuntimeHandler) GetRuntime(c echo.Context) error {
	_, sessionID, sess, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	status := h.pool.RuntimeStatus(sessionID, sessionMetadataString(acpMeta, "acp_agent_id"), sessionMetadataString(acpMeta, "project_path"))
	return c.JSON(http.StatusOK, status)
}

// EnsureRuntime godoc
// @Summary Ensure ACP session runtime is started
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime [post].
func (h *ACPRuntimeHandler) EnsureRuntime(c echo.Context) error {
	bot, sessionID, sess, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	botID := bot.ID
	acpMeta := acpRuntimeSessionMetadata(sess)
	if err := acpAgentSetupHTTPError(bot.Metadata, sessionMetadataString(acpMeta, "acp_agent_id")); err != nil {
		return acpRuntimeHTTPError(err)
	}
	if sessionMetadataString(acpMeta, "runtime_owner_account_id") == "" {
		return apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	status, err := h.pool.Ensure(c.Request().Context(), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               sessionMetadataString(acpMeta, "acp_agent_id"),
		ProjectPath:           sessionMetadataString(acpMeta, "project_path"),
		RuntimeOwnerAccountID: sessionMetadataString(acpMeta, "runtime_owner_account_id"),
		ToolHTTPURL:           buildACPMCPToolsURL(c, botID),
	})
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// SetModel godoc
// @Summary Set ACP session runtime model
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param body body acpRuntimeModelRequest true "ACP model selection"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime/model [patch].
func (h *ACPRuntimeHandler) SetModel(c echo.Context) error {
	bot, sessionID, sess, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	botID := bot.ID
	var req acpRuntimeModelRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		return apperror.New(apperror.CodeACPModelIDRequired, nil)
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	if sessionMetadataString(acpMeta, "runtime_owner_account_id") == "" {
		return apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	if err := acpAgentSetupHTTPError(bot.Metadata, sessionMetadataString(acpMeta, "acp_agent_id")); err != nil {
		return acpRuntimeHTTPError(err)
	}
	status, err := h.pool.SetModel(context.WithoutCancel(c.Request().Context()), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               sessionMetadataString(acpMeta, "acp_agent_id"),
		ProjectPath:           sessionMetadataString(acpMeta, "project_path"),
		RuntimeOwnerAccountID: sessionMetadataString(acpMeta, "runtime_owner_account_id"),
		ToolHTTPURL:           buildACPMCPToolsURL(c, botID),
	}, modelID)
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// SetReasoning godoc
// @Summary Set ACP session runtime reasoning effort
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param body body acpRuntimeReasoningRequest true "Reasoning effort selection"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime/reasoning [patch].
func (h *ACPRuntimeHandler) SetReasoning(c echo.Context) error {
	bot, sessionID, sess, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	botID := bot.ID
	var req acpRuntimeReasoningRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	effort := strings.TrimSpace(req.ReasoningEffort)
	if effort == "" {
		return apperror.New(apperror.CodeACPReasoningEffortRequired, nil)
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	if sessionMetadataString(acpMeta, "runtime_owner_account_id") == "" {
		return apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	if err := acpAgentSetupHTTPError(bot.Metadata, sessionMetadataString(acpMeta, "acp_agent_id")); err != nil {
		return acpRuntimeHTTPError(err)
	}
	status, err := h.pool.SetReasoning(context.WithoutCancel(c.Request().Context()), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               sessionMetadataString(acpMeta, "acp_agent_id"),
		ProjectPath:           sessionMetadataString(acpMeta, "project_path"),
		RuntimeOwnerAccountID: sessionMetadataString(acpMeta, "runtime_owner_account_id"),
		ToolHTTPURL:           buildACPMCPToolsURL(c, botID),
	}, effort)
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// SetMode godoc
// @Summary Set ACP session runtime mode
// @Description Sends the selected agent-declared mode ID unchanged to session/set_mode. The selection applies only to this live session.
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param body body acpRuntimeModeRequest true "ACP session mode selection"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime/mode [patch].
func (h *ACPRuntimeHandler) SetMode(c echo.Context) error {
	bot, sessionID, sess, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	var req acpRuntimeModeRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	modeID := req.ModeID
	if strings.TrimSpace(modeID) == "" {
		return apperror.New(apperror.CodeACPModeIDRequired, nil)
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	if sessionMetadataString(acpMeta, "runtime_owner_account_id") == "" {
		return apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	if err := acpAgentSetupHTTPError(bot.Metadata, sessionMetadataString(acpMeta, "acp_agent_id")); err != nil {
		return acpRuntimeHTTPError(err)
	}
	status, err := h.pool.SetMode(context.WithoutCancel(c.Request().Context()), acpagent.PromptInput{
		BotID:                 bot.ID,
		SessionID:             sessionID,
		AgentID:               sessionMetadataString(acpMeta, "acp_agent_id"),
		ProjectPath:           sessionMetadataString(acpMeta, "project_path"),
		RuntimeOwnerAccountID: sessionMetadataString(acpMeta, "runtime_owner_account_id"),
		ToolHTTPURL:           buildACPMCPToolsURL(c, bot.ID),
	}, modeID)
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

func acpRuntimeSessionMetadata(sess session.Thread) map[string]any {
	out := make(map[string]any, len(sess.Metadata)+len(sess.RuntimeMetadata))
	for key, value := range sess.Metadata {
		out[key] = value
	}
	for _, key := range []string{"acp_agent_id", "project_path", "acp_project_mode", "runtime_owner_account_id"} {
		if value, ok := sess.RuntimeMetadata[key]; ok {
			out[key] = value
		}
	}
	return out
}

func runtimePoolError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	if feedbackErr := acpFeedbackHTTPError(err); feedbackErr != nil {
		return acpRuntimeHTTPError(feedbackErr)
	}
	switch {
	case errors.Is(err, acpagent.ErrRuntimeNotFound):
		return apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	case errors.Is(err, acpagent.ErrRuntimeConfigUpdateFailed):
		return apperror.Wrap(apperror.CodeACPConfigUpdateFailed, err, nil)
	case errors.Is(err, acpclient.ErrModelSelectionUnsupported):
		return apperror.New(apperror.CodeACPModelSelectionUnsupported, nil)
	case errors.Is(err, acpclient.ErrModelIDRequired):
		return apperror.New(apperror.CodeACPModelIDRequired, nil)
	case errors.Is(err, acpclient.ErrModelUnavailable):
		return apperror.New(apperror.CodeACPModelUnavailable, nil)
	case errors.Is(err, acpclient.ErrReasoningSelectionUnsupported):
		return apperror.New(apperror.CodeACPReasoningUnsupported, nil)
	case errors.Is(err, acpclient.ErrReasoningEffortRequired):
		return apperror.New(apperror.CodeACPReasoningEffortRequired, nil)
	case errors.Is(err, acpclient.ErrReasoningEffortUnavailable):
		return apperror.New(apperror.CodeACPReasoningUnavailable, nil)
	case errors.Is(err, acpclient.ErrModeSelectionUnsupported):
		return apperror.New(apperror.CodeACPModeSelectionUnsupported, nil)
	case errors.Is(err, acpclient.ErrModeIDRequired):
		return apperror.New(apperror.CodeACPModeIDRequired, nil)
	case errors.Is(err, acpclient.ErrModeUnavailable):
		return apperror.New(apperror.CodeACPModeUnavailable, nil)
	case errors.Is(err, acpclient.ErrSessionNotInitialized),
		errors.Is(err, acpclient.ErrSessionClosed):
		return apperror.Wrap(apperror.CodeACPRuntimeConflict, err, nil)
	default:
		return apperror.Wrap(apperror.CodeACPOperationFailed, err, nil)
	}
}

func requiredRuntimeID(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("runtime_id"))
	if id == "" {
		return "", apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	return id, nil
}

func (h *ACPRuntimeHandler) authorizedACPBot(c echo.Context) (bots.Bot, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return bots.Bot{}, acpRuntimeHTTPError(err)
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bots.Bot{}, apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bots.PermissionWorkspaceExec)
	if err != nil {
		return bots.Bot{}, acpRuntimeHTTPError(err)
	}
	return bot, nil
}

func (h *ACPRuntimeHandler) authorizedRuntimeByID(c echo.Context) (bots.Bot, string, acpagent.RuntimeStatus, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return bots.Bot{}, "", acpagent.RuntimeStatus{}, acpRuntimeHTTPError(err)
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bots.Bot{}, "", acpagent.RuntimeStatus{}, apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	runtimeID, err := requiredRuntimeID(c)
	if err != nil {
		return bots.Bot{}, "", acpagent.RuntimeStatus{}, err
	}
	// Authorize before consulting the pool so callers without workspace_exec
	// cannot probe runtime liveness through 403/404 response differences.
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bots.PermissionWorkspaceExec)
	if err != nil {
		return bots.Bot{}, "", acpagent.RuntimeStatus{}, acpRuntimeControlError(err)
	}
	status, err := h.pool.RuntimeStatusByID(botID, runtimeID)
	if err != nil {
		return bots.Bot{}, "", acpagent.RuntimeStatus{}, runtimePoolError(err)
	}
	if strings.TrimSpace(status.RuntimeOwnerAccountID) == "" {
		return bots.Bot{}, "", acpagent.RuntimeStatus{}, apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	return bot, runtimeID, status, nil
}

func (h *ACPRuntimeHandler) authorizedACPSession(c echo.Context) (bots.Bot, string, session.Thread, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return bots.Bot{}, "", session.Thread{}, acpRuntimeHTTPError(err)
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bots.Bot{}, "", session.Thread{}, apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return bots.Bot{}, "", session.Thread{}, apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	if _, err := db.ParseUUID(sessionID); err != nil {
		return bots.Bot{}, "", session.Thread{}, apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	sess, err := h.sessionService.Get(c.Request().Context(), sessionID)
	if err != nil {
		return bots.Bot{}, "", session.Thread{}, acpRuntimeControlError(err)
	}
	if sess.BotID != botID {
		return bots.Bot{}, "", session.Thread{}, apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	}
	if !session.IsACPRuntime(sess) {
		return bots.Bot{}, "", session.Thread{}, apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	bot, err := h.authorizedRuntimeControlBot(c, channelIdentityID, botID, sessionMetadataString(acpMeta, "runtime_owner_account_id"))
	if err != nil {
		return bots.Bot{}, "", session.Thread{}, err
	}
	return bot, sessionID, sess, nil
}

func (h *ACPRuntimeHandler) authorizedRuntimeControlBot(c echo.Context, actorID, botID, runtimeOwnerID string) (bots.Bot, error) {
	runtimeOwnerID = strings.TrimSpace(runtimeOwnerID)
	if runtimeOwnerID == "" {
		return bots.Bot{}, apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	// The runtime owner has no standing beyond their live grants: a revoked
	// or offboarded owner must lose runtime control, so every actor — owner
	// included — passes the workspace_exec check.
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, actorID, botID, bots.PermissionWorkspaceExec)
	if err != nil {
		return bots.Bot{}, acpRuntimeControlError(err)
	}
	return bot, nil
}

func acpRuntimeControlError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, bots.ErrBotNotFound) || isHTTPStatus(err, http.StatusNotFound) {
		return apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	}
	return acpRuntimeHTTPError(err)
}

func acpRuntimeHTTPError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Code {
		case http.StatusBadRequest:
			return apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
		case http.StatusUnauthorized:
			// Authentication belongs to the shared auth contract rather than the
			// ACP runtime business-error surface.
			return err
		case http.StatusForbidden:
			return apperror.Wrap(apperror.CodeACPAccessForbidden, err, nil)
		case http.StatusNotFound:
			return apperror.New(apperror.CodeACPRuntimeNotFound, nil)
		case http.StatusConflict:
			return apperror.Wrap(apperror.CodeACPRuntimeConflict, err, nil)
		case http.StatusTooManyRequests:
			return apperror.Wrap(apperror.CodeACPRuntimeLimitReached, err, nil)
		}
	}
	return apperror.Wrap(apperror.CodeACPOperationFailed, err, nil)
}

func buildACPMCPToolsURL(c echo.Context, botID string) string {
	if c == nil {
		return ""
	}
	return buildACPMCPToolsURLFromRequest(c.Request(), botID)
}

func buildACPMCPToolsURLFromRequest(req *http.Request, botID string) string {
	if raw := strings.TrimSpace(os.Getenv("MEMOH_ACP_MCP_HTTP_URL")); raw != "" {
		if strings.Contains(raw, "{bot_id}") {
			return strings.ReplaceAll(raw, "{bot_id}", url.PathEscape(strings.TrimSpace(botID)))
		}
		return raw
	}
	base := strings.TrimSpace(os.Getenv("MEMOH_ACP_MCP_HTTP_BASE_URL"))
	if base == "" {
		base = localRequestBaseURL(req)
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + "/bots/" + url.PathEscape(strings.TrimSpace(botID)) + "/tools"
}

func localRequestBaseURL(req *http.Request) string {
	if req == nil {
		return ""
	}
	proto := "http"
	if req.TLS != nil {
		proto = "https"
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		return ""
	}
	if !isLoopbackRequestHost(host) {
		return ""
	}
	return proto + "://" + host
}

func isLoopbackRequestHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || strings.Contains(host, "/") {
		return false
	}
	name := host
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		name = splitHost
	}
	name = strings.Trim(strings.TrimSpace(name), "[]")
	if strings.EqualFold(name, "localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}
