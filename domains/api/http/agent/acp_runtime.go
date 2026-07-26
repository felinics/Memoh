package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/labstack/echo/v4"

	acpagent "github.com/memohai/memoh/domains/agent/acp"
	acpclient "github.com/memohai/memoh/domains/agent/acp/client"
	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

type ACPRuntimeHandler struct {
	pool           acpRuntimePool
	sessionService *session.Service
	botService     *bot.Service
	accountService *account.Service
}

type acpRuntimePool interface {
	RuntimeStatus(sessionID, agentID, projectPath string) acpagent.RuntimeStatus
	Ensure(ctx context.Context, input acpagent.PromptInput) (acpagent.RuntimeStatus, error)
	SetModel(ctx context.Context, input acpagent.PromptInput, modelID string) (acpagent.RuntimeStatus, error)
	SetReasoning(ctx context.Context, input acpagent.PromptInput, effort string) (acpagent.RuntimeStatus, error)
	CreateRuntime(ctx context.Context, input acpagent.CreateRuntimeInput) (acpagent.RuntimeStatus, error)
	RuntimeStatusByID(botID, runtimeID string) (acpagent.RuntimeStatus, error)
	SetRuntimeModel(ctx context.Context, botID, runtimeID, modelID string) (acpagent.RuntimeStatus, error)
	SetRuntimeReasoning(ctx context.Context, botID, runtimeID, effort string) (acpagent.RuntimeStatus, error)
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

func NewACPRuntimeHandler(pool *acpagent.SessionPool, sessionService *session.Service, botService *bot.Service, accountService *account.Service) *ACPRuntimeHandler {
	return NewACPRuntimeHandlerForTest(pool, sessionService, botService, accountService)
}

// NewACPRuntimeHandlerForTest constructs a handler with any pool double that
// satisfies the ACP runtime surface used by HTTP handlers.
func NewACPRuntimeHandlerForTest(pool acpRuntimePool, sessionService *session.Service, botService *bot.Service, accountService *account.Service) *ACPRuntimeHandler {
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
	e.DELETE("/bots/:bot_id/acp-runtimes/:runtime_id", h.CloseRuntime)
	e.GET("/bots/:bot_id/sessions/:session_id/acp-runtime", h.GetRuntime)
	e.POST("/bots/:bot_id/sessions/:session_id/acp-runtime", h.EnsureRuntime)
	e.PATCH("/bots/:bot_id/sessions/:session_id/acp-runtime/model", h.SetModel)
	e.PATCH("/bots/:bot_id/sessions/:session_id/acp-runtime/reasoning", h.SetReasoning)
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
// @Failure 429 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes [post].
func (h *ACPRuntimeHandler) CreateRuntime(c echo.Context) error {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	bot, err := h.authorizedACPBot(c)
	if err != nil {
		return err
	}
	var req acpRuntimeCreateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind acp runtime", err)
	}
	agentID := acpprofile.NormalizeAgentID(req.AgentID)
	if agentID == "" {
		return apperror.Required("acp_agent_id")
	}
	if err := AcpAgentSetupHTTPError(bot.Metadata, agentID); err != nil {
		return err
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
		ToolHTTPURL:           BuildACPMCPToolsURL(c, bot.ID),
	})
	if err != nil {
		if errors.Is(err, acpagent.ErrTooManyRuntimes) {
			return apperror.Exhausted("create acp runtime", err)
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
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id} [get].
func (h *ACPRuntimeHandler) GetRuntimeByID(c echo.Context) error {
	_, _, status, err := h.authorizedRuntimeByID(c)
	if err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			return runtimePoolError(err)
		}
		return err
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
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id}/model [patch].
func (h *ACPRuntimeHandler) SetRuntimeModel(c echo.Context) error {
	bot, runtimeID, _, err := h.authorizedRuntimeByID(c)
	if err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			return runtimePoolError(err)
		}
		return err
	}
	var req acpRuntimeModelRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind acp runtime model", err)
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
// @Failure 502 {object} apperror.Problem
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id}/reasoning [patch].
func (h *ACPRuntimeHandler) SetRuntimeReasoning(c echo.Context) error {
	bot, runtimeID, _, err := h.authorizedRuntimeByID(c)
	if err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			return runtimePoolError(err)
		}
		return err
	}
	var req acpRuntimeReasoningRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind acp runtime reasoning", err)
	}
	status, err := h.pool.SetRuntimeReasoning(context.WithoutCancel(c.Request().Context()), bot.ID, runtimeID, strings.TrimSpace(req.ReasoningEffort))
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
// @Router /bots/{bot_id}/acp-runtimes/{runtime_id} [delete].
func (h *ACPRuntimeHandler) CloseRuntime(c echo.Context) error {
	bot, runtimeID, _, err := h.authorizedRuntimeByID(c)
	if err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			return c.NoContent(http.StatusNoContent)
		}
		return err
	}
	if err := h.pool.CloseRuntime(bot.ID, runtimeID); err != nil {
		if errors.Is(err, acpagent.ErrRuntimeNotFound) {
			// Close is fire-and-forget on the client; a reaped runtime is fine.
			return c.NoContent(http.StatusNoContent)
		}
		return apperror.Internal("close acp runtime", err)
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
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime [get].
func (h *ACPRuntimeHandler) GetRuntime(c echo.Context) error {
	_, sessionID, sess, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	status := h.pool.RuntimeStatus(sessionID, httpx.SessionMetadataString(acpMeta, "acp_agent_id"), httpx.SessionMetadataString(acpMeta, "project_path"))
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
// @Router /bots/{bot_id}/sessions/{session_id}/acp-runtime [post].
func (h *ACPRuntimeHandler) EnsureRuntime(c echo.Context) error {
	bot, sessionID, sess, err := h.authorizedACPSession(c)
	if err != nil {
		return err
	}
	botID := bot.ID
	acpMeta := acpRuntimeSessionMetadata(sess)
	if err := AcpAgentSetupHTTPError(bot.Metadata, httpx.SessionMetadataString(acpMeta, "acp_agent_id")); err != nil {
		return err
	}
	if httpx.SessionMetadataString(acpMeta, "runtime_owner_account_id") == "" {
		return AcpFeedbackHTTPError(AcpRuntimeOwnerMissingFeedback())
	}
	status, err := h.pool.Ensure(c.Request().Context(), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               httpx.SessionMetadataString(acpMeta, "acp_agent_id"),
		ProjectPath:           httpx.SessionMetadataString(acpMeta, "project_path"),
		RuntimeOwnerAccountID: httpx.SessionMetadataString(acpMeta, "runtime_owner_account_id"),
		ToolHTTPURL:           BuildACPMCPToolsURL(c, botID),
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
		return apperror.Invalid("bind acp runtime model", err)
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		return apperror.Invalid("set acp model", nil).WithCode(apperror.CodeACPModelIDRequired, nil)
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	if httpx.SessionMetadataString(acpMeta, "runtime_owner_account_id") == "" {
		return AcpFeedbackHTTPError(AcpRuntimeOwnerMissingFeedback())
	}
	if err := AcpAgentSetupHTTPError(bot.Metadata, httpx.SessionMetadataString(acpMeta, "acp_agent_id")); err != nil {
		return err
	}
	status, err := h.pool.SetModel(context.WithoutCancel(c.Request().Context()), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               httpx.SessionMetadataString(acpMeta, "acp_agent_id"),
		ProjectPath:           httpx.SessionMetadataString(acpMeta, "project_path"),
		RuntimeOwnerAccountID: httpx.SessionMetadataString(acpMeta, "runtime_owner_account_id"),
		ToolHTTPURL:           BuildACPMCPToolsURL(c, botID),
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
		return apperror.Invalid("bind acp runtime reasoning", err)
	}
	effort := strings.TrimSpace(req.ReasoningEffort)
	if effort == "" {
		return apperror.Invalid("set acp reasoning", nil).WithCode(apperror.CodeACPReasoningEffortRequired, nil)
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	if httpx.SessionMetadataString(acpMeta, "runtime_owner_account_id") == "" {
		return AcpFeedbackHTTPError(AcpRuntimeOwnerMissingFeedback())
	}
	if err := AcpAgentSetupHTTPError(bot.Metadata, httpx.SessionMetadataString(acpMeta, "acp_agent_id")); err != nil {
		return err
	}
	status, err := h.pool.SetReasoning(context.WithoutCancel(c.Request().Context()), acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               httpx.SessionMetadataString(acpMeta, "acp_agent_id"),
		ProjectPath:           httpx.SessionMetadataString(acpMeta, "project_path"),
		RuntimeOwnerAccountID: httpx.SessionMetadataString(acpMeta, "runtime_owner_account_id"),
		ToolHTTPURL:           BuildACPMCPToolsURL(c, botID),
	}, effort)
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
	if feedbackErr := AcpFeedbackHTTPError(err); feedbackErr != nil {
		return feedbackErr
	}
	switch {
	case errors.Is(err, acpagent.ErrRuntimeNotFound):
		return apperror.NotFound("acp runtime", err).WithCode(apperror.CodeACPRuntimeNotFound, nil)
	case errors.Is(err, acpagent.ErrRuntimeConfigUpdateFailed):
		return apperror.Unavailable("update acp runtime config", err).WithCode(apperror.CodeACPConfigUpdateFailed, nil)
	case errors.Is(err, acpclient.ErrModelSelectionUnsupported):
		return apperror.Invalid("set acp model", err).WithCode(apperror.CodeACPModelSelectionUnsupported, nil)
	case errors.Is(err, acpclient.ErrModelIDRequired):
		return apperror.Invalid("set acp model", err).WithCode(apperror.CodeACPModelIDRequired, nil)
	case errors.Is(err, acpclient.ErrModelUnavailable):
		return apperror.Invalid("set acp model", err).WithCode(apperror.CodeACPModelUnavailable, nil)
	case errors.Is(err, acpclient.ErrReasoningSelectionUnsupported):
		return apperror.Invalid("set acp reasoning", err).WithCode(apperror.CodeACPReasoningUnsupported, nil)
	case errors.Is(err, acpclient.ErrReasoningEffortRequired):
		return apperror.Invalid("set acp reasoning", err).WithCode(apperror.CodeACPReasoningEffortRequired, nil)
	case errors.Is(err, acpclient.ErrReasoningEffortUnavailable):
		return apperror.Invalid("set acp reasoning", err).WithCode(apperror.CodeACPReasoningUnavailable, nil)
	case errors.Is(err, acpclient.ErrSessionNotInitialized),
		errors.Is(err, acpclient.ErrSessionClosed):
		return apperror.Conflict("acp runtime session", err)
	default:
		return AcpFeedbackHTTPError(acpRuntimeStartFailedFeedback(err.Error()))
	}
}

func requiredRuntimeID(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("runtime_id"))
	if id == "" {
		return "", apperror.Required("runtime_id")
	}
	return id, nil
}

func (h *ACPRuntimeHandler) authorizedACPBot(c echo.Context) (bot.Bot, error) {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return bot.Bot{}, err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bot.Bot{}, apperror.Required("bot_id")
	}
	record, err := httpx.AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bot.PermissionWorkspaceExec)
	if err != nil {
		if IsHTTPStatus(err, http.StatusForbidden) {
			feedback := AcpNoWorkspaceExecFeedback("missing_workspace_exec", "You do not have permission to run workspace commands for this bot.")
			return bot.Bot{}, AcpFeedbackHTTPError(feedback)
		}
		return bot.Bot{}, err
	}
	return record, nil
}

func (h *ACPRuntimeHandler) authorizedRuntimeByID(c echo.Context) (bot.Bot, string, acpagent.RuntimeStatus, error) {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return bot.Bot{}, "", acpagent.RuntimeStatus{}, err
	}
	record, err := h.authorizedACPBot(c)
	if err != nil {
		return bot.Bot{}, "", acpagent.RuntimeStatus{}, err
	}
	runtimeID, err := requiredRuntimeID(c)
	if err != nil {
		return bot.Bot{}, "", acpagent.RuntimeStatus{}, err
	}
	status, err := h.pool.RuntimeStatusByID(record.ID, runtimeID)
	if err != nil {
		return bot.Bot{}, "", acpagent.RuntimeStatus{}, err
	}
	perms, err := h.resolveCurrentUserPermissions(c, channelIdentityID, record.ID)
	if err != nil {
		return bot.Bot{}, "", acpagent.RuntimeStatus{}, err
	}
	if err := AuthorizeACPRuntimeSessionAccess(channelIdentityID, perms, status.RuntimeOwnerAccountID); err != nil {
		return bot.Bot{}, "", acpagent.RuntimeStatus{}, err
	}
	return record, runtimeID, status, nil
}

func (h *ACPRuntimeHandler) authorizedACPSession(c echo.Context) (bot.Bot, string, session.Thread, error) {
	channelIdentityID, err := httpx.RequireChannelIdentityID(c)
	if err != nil {
		return bot.Bot{}, "", session.Thread{}, err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bot.Bot{}, "", session.Thread{}, apperror.Required("bot_id")
	}
	record, err := httpx.AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bot.PermissionWorkspaceExec)
	if err != nil {
		if IsHTTPStatus(err, http.StatusForbidden) {
			feedback := AcpNoWorkspaceExecFeedback("missing_workspace_exec", "You do not have permission to run workspace commands for this bot.")
			return bot.Bot{}, "", session.Thread{}, AcpFeedbackHTTPError(feedback)
		}
		return bot.Bot{}, "", session.Thread{}, err
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return bot.Bot{}, "", session.Thread{}, apperror.Required("session_id")
	}
	sess, err := h.sessionService.Get(c.Request().Context(), sessionID)
	if err != nil || sess.BotID != record.ID {
		return bot.Bot{}, "", session.Thread{}, apperror.NotFound("get acp session", nil)
	}
	if !session.IsACPRuntime(sess) {
		return bot.Bot{}, "", session.Thread{}, apperror.Invalid("validate acp session", nil)
	}
	perms, err := h.resolveCurrentUserPermissions(c, channelIdentityID, record.ID)
	if err != nil {
		return bot.Bot{}, "", session.Thread{}, err
	}
	acpMeta := acpRuntimeSessionMetadata(sess)
	if err := AuthorizeACPRuntimeSessionAccess(channelIdentityID, perms, httpx.SessionMetadataString(acpMeta, "runtime_owner_account_id")); err != nil {
		return bot.Bot{}, "", session.Thread{}, err
	}
	return record, sessionID, sess, nil
}

func (h *ACPRuntimeHandler) resolveCurrentUserPermissions(c echo.Context, channelIdentityID, botID string) ([]string, error) {
	if h.botService == nil || h.accountService == nil {
		return nil, apperror.Internal("resolve bot permissions", nil)
	}
	isAdmin, err := h.accountService.IsAdmin(c.Request().Context(), channelIdentityID)
	if err != nil {
		return nil, apperror.Internal("resolve bot permissions", err)
	}
	perms, err := h.botService.ResolveUserPermissions(c.Request().Context(), botID, channelIdentityID, isAdmin)
	if err != nil {
		return nil, apperror.Internal("resolve bot permissions", err)
	}
	return perms, nil
}

func BuildACPMCPToolsURL(c echo.Context, botID string) string {
	if c == nil {
		return ""
	}
	return BuildACPMCPToolsURLFromRequest(c.Request(), botID)
}

func BuildACPMCPToolsURLFromRequest(req *http.Request, botID string) string {
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
