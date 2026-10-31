package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/runtimefence"
)

type ACPRuntimeHandler struct {
	pool           acpRuntimePool
	sessionService *session.Service
	botService     *bots.Service
	accountService *accounts.Service
	botAgents      *botagents.Service
	resets         sessionResetService
}

func (h *ACPRuntimeHandler) SetBotAgents(service *botagents.Service) {
	h.botAgents = service
}

// SetRuntimeResetService installs the history reset that gives a stored
// session up without a process restoring it in between.
func (h *ACPRuntimeHandler) SetRuntimeResetService(resets sessionResetService) {
	h.resets = resets
}

type acpRuntimePool interface {
	RuntimeStatus(input acpagent.PromptInput) acpagent.RuntimeStatus
	Ensure(ctx context.Context, input acpagent.PromptInput) (acpagent.RuntimeStatus, error)
	SetModel(ctx context.Context, input acpagent.PromptInput, modelID string) (acpagent.RuntimeStatus, error)
	SetReasoning(ctx context.Context, input acpagent.PromptInput, effort string) (acpagent.RuntimeStatus, error)
	SetMode(ctx context.Context, input acpagent.PromptInput, modeID string) (acpagent.RuntimeStatus, error)
	AgentOptions(ctx context.Context, input acpagent.PromptInput) (acpagent.RuntimeStatus, error)
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
	e.GET("/bots/:bot_id/acp-options", h.GetAgentOptions)
	e.GET("/bots/:bot_id/sessions/:session_id/acp-runtime", h.GetRuntime)
	e.POST("/bots/:bot_id/sessions/:session_id/acp-runtime", h.EnsureRuntime)
	e.PATCH("/bots/:bot_id/sessions/:session_id/acp-runtime/model", h.SetModel)
	e.PATCH("/bots/:bot_id/sessions/:session_id/acp-runtime/reasoning", h.SetReasoning)
	e.PATCH("/bots/:bot_id/sessions/:session_id/acp-runtime/mode", h.SetMode)
	e.DELETE("/bots/:bot_id/sessions/:session_id/acp-runtime/native-session", h.ResetNativeSession)
}

// GetAgentOptions godoc
// @Summary Get what a new ACP conversation could choose
// @Description Reports the models, reasoning efforts, modes and commands the agent offers before a conversation exists. Choices travel in the new session's runtime_metadata (acp_model_id, acp_reasoning_effort, permission_mode).
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param acp_agent_id query string true "ACP agent ID"
// @Param bot_agent_id query string false "Agent instance ID"
// @Param project_path query string false "Project the conversation will run in; /data when omitted"
// @Param model_id query string false "Model whose reasoning efforts to report; the agent's current model when omitted"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Failure 502 {object} server.Problem
// @Router /bots/{bot_id}/acp-options [get].
func (h *ACPRuntimeHandler) GetAgentOptions(c echo.Context) error {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return acpRuntimeHTTPError(err)
	}
	bot, err := h.authorizedACPBot(c)
	if err != nil {
		return err
	}
	agentID := acpprofile.NormalizeAgentID(c.QueryParam("acp_agent_id"))
	if agentID == "" {
		return apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	botAgentID := strings.TrimSpace(c.QueryParam("bot_agent_id"))
	if botAgentID != "" && h.botAgents != nil {
		if _, err := h.botAgents.GetActiveACP(c.Request().Context(), bot.ID, botAgentID, agentID); err != nil {
			if publicErr := botAgentHTTPError(err); publicErr != nil {
				return publicErr
			}
			return acpRuntimeHTTPError(err)
		}
	}
	if err := acpAgentSetupError(c.Request().Context(), h.botAgents, bot, botAgentID, agentID); err != nil {
		return acpRuntimeHTTPError(err)
	}
	// An agent offers commands and settings by its working directory: read
	// them where the conversation will run.
	projectPath := strings.TrimSpace(c.QueryParam("project_path"))
	if projectPath == "" {
		projectPath = session.DefaultACPProjectPath
	}
	status, err := h.pool.AgentOptions(c.Request().Context(), acpagent.PromptInput{
		BotID:                 bot.ID,
		BotAgentID:            botAgentID,
		AgentID:               agentID,
		ProjectPath:           projectPath,
		ModelID:               strings.TrimSpace(c.QueryParam("model_id")),
		RuntimeOwnerAccountID: channelIdentityID,
	})
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

// ResetNativeSession godoc
// @Summary Start the agent over on a new ACP session
// @Description Forgets the agent session that keeps failing to restore and keeps the messages. The next message opens a new agent session, which does not remember them.
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 204
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime/native-session [delete].
func (h *ACPRuntimeHandler) ResetNativeSession(c echo.Context) error {
	bot, sessionID, _, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	// The reset closes the process and holds every start until the stored
	// session is gone, so no process restores it in between.
	if h.resets == nil {
		return historyResetError(sessionruntime.ErrHistoryResetUnavailable)
	}
	ctx, release, err := h.resets.BeginSessionHistoryReset(c.Request().Context(), bot.ID, sessionID)
	if err != nil {
		return historyResetError(err)
	}
	defer release()
	if _, err := h.sessionService.MergeRuntimeMetadata(ctx, sessionID, session.RuntimeACPAgent, map[string]any{acpagent.NativeSessionKey: nil}); err != nil {
		if leaseErr := runtimefence.ResetLeaseFailure(ctx, err); leaseErr != nil {
			return apperror.Wrap(apperror.CodeSessionResetConflict, leaseErr, nil)
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
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime [get].
func (h *ACPRuntimeHandler) GetRuntime(c echo.Context) error {
	bot, sessionID, identity, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	status := h.pool.RuntimeStatus(acpagent.PromptInput{
		BotID:       bot.ID,
		BotAgentID:  identity.BotAgentID,
		SessionID:   sessionID,
		AgentID:     identity.ACPAgentID,
		ProjectPath: identity.ProjectPath,
	})
	return c.JSON(http.StatusOK, status)
}

// EnsureRuntime godoc
// @Summary Ensure ACP session runtime is started
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Success 200 {object} acpagent.RuntimeStatus
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Failure 502 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime [post].
func (h *ACPRuntimeHandler) EnsureRuntime(c echo.Context) error {
	bot, sessionID, identity, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	botID := bot.ID
	if err := acpAgentSetupError(c.Request().Context(), h.botAgents, bot, identity.BotAgentID, identity.ACPAgentID); err != nil {
		return acpRuntimeHTTPError(err)
	}
	if identity.RuntimeOwnerAccountID == "" {
		return apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	status, err := h.pool.Ensure(c.Request().Context(), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               identity.ACPAgentID,
		ProjectPath:           identity.ProjectPath,
		RuntimeOwnerAccountID: identity.RuntimeOwnerAccountID,
		ToolHTTPURL:           buildExternalAgentToolsURL(c, botID),
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
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Failure 502 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime/model [patch].
func (h *ACPRuntimeHandler) SetModel(c echo.Context) error {
	bot, sessionID, identity, err := h.authorizedACPSession(c)
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
	if identity.RuntimeOwnerAccountID == "" {
		return apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	if err := acpAgentSetupError(c.Request().Context(), h.botAgents, bot, identity.BotAgentID, identity.ACPAgentID); err != nil {
		return acpRuntimeHTTPError(err)
	}
	status, err := h.pool.SetModel(context.WithoutCancel(c.Request().Context()), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               identity.ACPAgentID,
		ProjectPath:           identity.ProjectPath,
		RuntimeOwnerAccountID: identity.RuntimeOwnerAccountID,
		ToolHTTPURL:           buildExternalAgentToolsURL(c, botID),
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
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Failure 502 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime/reasoning [patch].
func (h *ACPRuntimeHandler) SetReasoning(c echo.Context) error {
	bot, sessionID, identity, err := h.authorizedACPSession(c)
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
	if identity.RuntimeOwnerAccountID == "" {
		return apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	if err := acpAgentSetupError(c.Request().Context(), h.botAgents, bot, identity.BotAgentID, identity.ACPAgentID); err != nil {
		return acpRuntimeHTTPError(err)
	}
	status, err := h.pool.SetReasoning(context.WithoutCancel(c.Request().Context()), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               identity.ACPAgentID,
		ProjectPath:           identity.ProjectPath,
		RuntimeOwnerAccountID: identity.RuntimeOwnerAccountID,
		ToolHTTPURL:           buildExternalAgentToolsURL(c, botID),
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
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Failure 502 {object} server.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime/mode [patch].
func (h *ACPRuntimeHandler) SetMode(c echo.Context) error {
	bot, sessionID, identity, err := h.authorizedACPSession(c)
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
	if identity.RuntimeOwnerAccountID == "" {
		return apperror.New(apperror.CodeACPRuntimeConflict, nil)
	}
	if err := acpAgentSetupError(c.Request().Context(), h.botAgents, bot, identity.BotAgentID, identity.ACPAgentID); err != nil {
		return acpRuntimeHTTPError(err)
	}
	status, err := h.pool.SetMode(context.WithoutCancel(c.Request().Context()), acpagent.PromptInput{
		BotID:                 bot.ID,
		SessionID:             sessionID,
		AgentID:               identity.ACPAgentID,
		ProjectPath:           identity.ProjectPath,
		RuntimeOwnerAccountID: identity.RuntimeOwnerAccountID,
		ToolHTTPURL:           buildExternalAgentToolsURL(c, bot.ID),
	}, modeID)
	if err != nil {
		return runtimePoolError(err)
	}
	return c.JSON(http.StatusOK, status)
}

func runtimePoolError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	if translated := application.ExternalAgentError(err); apperror.CodeOf(translated) != "" {
		return translated
	}
	var commandNotFound *acpclient.CommandNotFoundError
	switch {
	case errors.As(err, &commandNotFound):
		return apperror.Wrap(apperror.CodeACPCommandNotFound, err, map[string]string{"command": commandNotFound.Command})
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

func (h *ACPRuntimeHandler) authorizedACPSession(c echo.Context) (bots.Bot, string, session.ExternalIdentity, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return bots.Bot{}, "", session.ExternalIdentity{}, acpRuntimeHTTPError(err)
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bots.Bot{}, "", session.ExternalIdentity{}, apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return bots.Bot{}, "", session.ExternalIdentity{}, apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	if _, err := db.ParseUUID(sessionID); err != nil {
		return bots.Bot{}, "", session.ExternalIdentity{}, apperror.Wrap(apperror.CodeACPRequestInvalid, err, nil)
	}
	sess, err := h.sessionService.Get(c.Request().Context(), sessionID)
	if err != nil {
		return bots.Bot{}, "", session.ExternalIdentity{}, acpRuntimeControlError(err)
	}
	if sess.BotID != botID {
		return bots.Bot{}, "", session.ExternalIdentity{}, apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	}
	if !session.IsACPRuntime(sess) {
		return bots.Bot{}, "", session.ExternalIdentity{}, apperror.New(apperror.CodeACPRequestInvalid, nil)
	}
	identity, err := h.sessionService.ExternalIdentity(c.Request().Context(), sess)
	if err != nil {
		return bots.Bot{}, "", session.ExternalIdentity{}, acpRuntimeControlError(err)
	}
	bot, err := h.authorizedRuntimeControlBot(c, channelIdentityID, botID, identity.RuntimeOwnerAccountID)
	if err != nil {
		return bots.Bot{}, "", session.ExternalIdentity{}, err
	}
	return bot, sessionID, identity, nil
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
	if err == nil {
		return nil
	}
	if isBotOrSessionNotFound(err) {
		return apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	}
	if apperror.CodeOf(err) != "" {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, bots.ErrBotNotFound) || isHTTPStatus(err, http.StatusNotFound) {
		return apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	}
	return acpRuntimeHTTPError(err)
}

func acpRuntimeHTTPError(err error) error {
	if err == nil {
		return nil
	}
	if isBotOrSessionNotFound(err) {
		return apperror.New(apperror.CodeACPRuntimeNotFound, nil)
	}
	if apperror.CodeOf(err) != "" {
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
		}
	}
	return apperror.Wrap(apperror.CodeACPOperationFailed, err, nil)
}
