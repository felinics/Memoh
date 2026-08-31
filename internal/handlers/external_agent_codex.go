package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	codexruntime "github.com/felinics/memoh/internal/agent/runtime/codex"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
)

// codexLoginService is the slice of the codex driver the handler drives.
type codexService interface {
	StartChatGPTDeviceLogin(ctx context.Context, botID, botAgentID string) (codexruntime.DeviceLoginStart, error)
	PollDeviceLogin(botID, botAgentID, loginID string) codexruntime.DeviceLoginStatus
	CompleteChatGPTDeviceLogin(ctx context.Context, ownerUserID, botID, botAgentID, loginID string) error
	CancelDeviceLogin(ctx context.Context, botID, botAgentID, loginID string) error
}

// ExternalAgentCodexHandler exposes the direct codex runtime's login flow: the
// ChatGPT subscription device-code login runs through the app-server protocol
// and its credentials are copied into the encrypted Agent credential store.
type ExternalAgentCodexHandler struct {
	driver         codexService
	agents         *botagents.Service
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

// NewExternalAgentCodexHandler constructs the codex runtime handler.
func NewExternalAgentCodexHandler(log *slog.Logger, driver codexService, agents *botagents.Service, botService *bots.Service, accountService *accounts.Service) *ExternalAgentCodexHandler {
	return &ExternalAgentCodexHandler{
		driver:         driver,
		agents:         agents,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "external_agent_codex")),
	}
}

// Register registers codex runtime routes.
func (h *ExternalAgentCodexHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/agents/:id/codex")
	g.POST("/login/device/authorize", h.AuthorizeDevice)
	g.POST("/login/device/poll", h.PollDevice)
	g.POST("/login/device/cancel", h.CancelDevice)
}

func (h *ExternalAgentCodexHandler) requireAgentAccess(c echo.Context) (string, string, string, error) {
	botID := strings.TrimSpace(c.Param("bot_id"))
	botAgentID := strings.TrimSpace(c.Param("id"))
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", "", "", err
	}
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bots.PermissionManage)
	if err != nil {
		return "", "", "", err
	}
	agent, err := h.agents.GetActive(c.Request().Context(), bot.ID, botAgentID)
	if err != nil || agent.Runtime != botagents.RuntimeCodex {
		return "", "", "", apperror.New(apperror.CodeBotAgentNotFound, nil)
	}
	return bot.ID, agent.ID, channelIdentityID, nil
}

// CodexDeviceLoginAuthorizeResponse starts a device-code login.
type CodexDeviceLoginAuthorizeResponse struct {
	LoginID         string `json:"login_id" validate:"required"`
	UserCode        string `json:"user_code" validate:"required"`
	VerificationURL string `json:"verification_url" validate:"required"`
} // @name externalagent.CodexDeviceLoginAuthorizeResponse

// CodexDeviceLoginPollRequest identifies the login being polled or cancelled.
type CodexDeviceLoginPollRequest struct {
	LoginID string `json:"login_id" validate:"required"`
} // @name externalagent.CodexDeviceLoginPollRequest

// CodexDeviceLoginPollResponse reports the login state.
type CodexDeviceLoginPollResponse struct {
	Status string `json:"status" validate:"required" enums:"pending,success,error,unknown"`
} // @name externalagent.CodexDeviceLoginPollResponse

// AuthorizeDevice godoc
// @Summary Start a ChatGPT device-code login for the direct codex runtime
// @Tags external-agents
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} CodexDeviceLoginAuthorizeResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 503 {object} apperror.Problem
// @Param id path string true "Bot Agent ID"
// @Router /bots/{bot_id}/agents/{id}/codex/login/device/authorize [post].
func (h *ExternalAgentCodexHandler) AuthorizeDevice(c echo.Context) error {
	botID, botAgentID, _, err := h.requireAgentAccess(c)
	if err != nil {
		return err
	}
	start, err := h.driver.StartChatGPTDeviceLogin(c.Request().Context(), botID, botAgentID)
	if err != nil {
		h.logger.Error("codex device login start failed", slog.String("bot_id", botID), slog.Any("error", err))
		if apperror.CodeOf(err) != "" {
			return err
		}
		return apperror.Wrap(
			apperror.CodeExternalRuntimeUnavailable,
			err,
			map[string]string{"runtime": codexruntime.RuntimeType},
		)
	}
	return c.JSON(http.StatusOK, CodexDeviceLoginAuthorizeResponse{
		LoginID:         start.LoginID,
		UserCode:        start.UserCode,
		VerificationURL: start.VerificationURL,
	})
}

// PollDevice godoc
// @Summary Poll a pending codex device-code login
// @Tags external-agents
// @Param bot_id path string true "Bot ID"
// @Param body body CodexDeviceLoginPollRequest true "Login reference"
// @Success 200 {object} CodexDeviceLoginPollResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Param id path string true "Bot Agent ID"
// @Router /bots/{bot_id}/agents/{id}/codex/login/device/poll [post].
func (h *ExternalAgentCodexHandler) PollDevice(c echo.Context) error {
	botID, botAgentID, ownerUserID, err := h.requireAgentAccess(c)
	if err != nil {
		return err
	}
	var req CodexDeviceLoginPollRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.LoginID) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "login_id is required")
	}
	loginID := strings.TrimSpace(req.LoginID)
	status := h.driver.PollDeviceLogin(botID, botAgentID, loginID)
	if status.Status == "success" {
		if err := h.driver.CompleteChatGPTDeviceLogin(c.Request().Context(), ownerUserID, botID, botAgentID, loginID); err != nil {
			if apperror.CodeOf(err) != "" {
				return err
			}
			return apperror.Wrap(apperror.CodeAgentCredentialMaterializationFailed, err, nil)
		}
	}
	return c.JSON(http.StatusOK, CodexDeviceLoginPollResponse{Status: status.Status})
}

// CancelDevice godoc
// @Summary Cancel a pending codex device-code login
// @Tags external-agents
// @Param bot_id path string true "Bot ID"
// @Param body body CodexDeviceLoginPollRequest true "Login reference"
// @Success 204
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Param id path string true "Bot Agent ID"
// @Router /bots/{bot_id}/agents/{id}/codex/login/device/cancel [post].
func (h *ExternalAgentCodexHandler) CancelDevice(c echo.Context) error {
	botID, botAgentID, _, err := h.requireAgentAccess(c)
	if err != nil {
		return err
	}
	var req CodexDeviceLoginPollRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.LoginID) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "login_id is required")
	}
	if err := h.driver.CancelDeviceLogin(c.Request().Context(), botID, botAgentID, strings.TrimSpace(req.LoginID)); err != nil {
		return apperror.Wrap(
			apperror.CodeExternalRuntimeUnavailable,
			err,
			map[string]string{"runtime": codexruntime.RuntimeType},
		)
	}
	return c.NoContent(http.StatusNoContent)
}
