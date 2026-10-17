package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
)

// ExternalAgentLoginHandler exposes the account sign-in of direct runtimes
// that sign in inside the Agent's own process (Codex): the agent obtains a
// device code, the user confirms it, and the login the agent wrote is copied
// into the encrypted Agent credential store.
type ExternalAgentLoginHandler struct {
	runtimes       external.Drivers
	agents         *botagents.Service
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

// NewExternalAgentLoginHandler constructs the sign-in handler.
func NewExternalAgentLoginHandler(log *slog.Logger, runtimes external.Drivers, agents *botagents.Service, botService *bots.Service, accountService *accounts.Service) *ExternalAgentLoginHandler {
	return &ExternalAgentLoginHandler{
		runtimes:       runtimes,
		agents:         agents,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "external_agent_login")),
	}
}

// Register registers the sign-in routes.
func (h *ExternalAgentLoginHandler) Register(e *echo.Echo) {
	for _, prefix := range []string{
		"/bots/:bot_id/agents/:id/login/device",
		// Desktop builds from before the generic path still sign Codex in
		// here, with the same requests and answers. Left out of the API docs
		// so the SDK does not grow it back; remove once those builds are gone.
		"/bots/:bot_id/agents/:id/codex/login/device",
	} {
		g := e.Group(prefix)
		g.POST("/authorize", h.AuthorizeDevice)
		g.POST("/poll", h.PollDevice)
		g.POST("/cancel", h.CancelDevice)
	}
}

// requireLogin resolves the Agent the caller may manage and the sign-in flow
// of its runtime.
func (h *ExternalAgentLoginHandler) requireLogin(c echo.Context) (external.DeviceLoginProvider, string, string, string, error) {
	botID := strings.TrimSpace(c.Param("bot_id"))
	botAgentID := strings.TrimSpace(c.Param("id"))
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return nil, "", "", "", err
	}
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, bots.PermissionManage)
	if err != nil {
		return nil, "", "", "", err
	}
	agent, err := h.agents.GetActive(c.Request().Context(), bot.ID, botAgentID)
	if err != nil {
		return nil, "", "", "", apperror.New(apperror.CodeBotAgentNotFound, nil)
	}
	login, ok := h.runtimes.DeviceLogin(agent.Runtime)
	if !ok {
		return nil, "", "", "", apperror.New(apperror.CodeBotAgentNotFound, nil)
	}
	return login, bot.ID, agent.ID, channelIdentityID, nil
}

// DeviceLoginAuthorizeResponse is a started device-code sign-in.
type DeviceLoginAuthorizeResponse struct {
	LoginID         string `json:"login_id" validate:"required"`
	UserCode        string `json:"user_code" validate:"required"`
	VerificationURL string `json:"verification_url" validate:"required"`
} // @name externalagent.DeviceLoginAuthorizeResponse

// DeviceLoginPollRequest identifies the sign-in being polled or cancelled.
type DeviceLoginPollRequest struct {
	LoginID string `json:"login_id" validate:"required"`
} // @name externalagent.DeviceLoginPollRequest

// DeviceLoginPollResponse reports the sign-in state.
type DeviceLoginPollResponse struct {
	Status string `json:"status" validate:"required" enums:"pending,success,error,unknown"`
} // @name externalagent.DeviceLoginPollResponse

// AuthorizeDevice godoc
// @Summary Start a device-code account sign-in on a direct runtime Agent
// @Description The Agent's own process obtains the device code; the user opens the URL and confirms the code there. Only runtimes that sign in this way (Codex) accept it.
// @Tags external-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Bot Agent ID"
// @Success 200 {object} DeviceLoginAuthorizeResponse
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 503 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id}/login/device/authorize [post].
func (h *ExternalAgentLoginHandler) AuthorizeDevice(c echo.Context) error {
	login, botID, botAgentID, _, err := h.requireLogin(c)
	if err != nil {
		return err
	}
	start, err := login.StartDeviceLogin(c.Request().Context(), botID, botAgentID)
	if err != nil {
		return h.runtimeError(c, err)
	}
	return c.JSON(http.StatusOK, DeviceLoginAuthorizeResponse{
		LoginID:         start.LoginID,
		UserCode:        start.UserCode,
		VerificationURL: start.VerificationURL,
	})
}

// PollDevice godoc
// @Summary Poll a pending device-code sign-in
// @Description A successful sign-in has already been stored as the Agent's credential when this answers success.
// @Tags external-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Bot Agent ID"
// @Param body body DeviceLoginPollRequest true "Sign-in reference"
// @Success 200 {object} DeviceLoginPollResponse
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id}/login/device/poll [post].
func (h *ExternalAgentLoginHandler) PollDevice(c echo.Context) error {
	login, botID, botAgentID, ownerUserID, err := h.requireLogin(c)
	if err != nil {
		return err
	}
	loginID, err := loginIDOf(c)
	if err != nil {
		return err
	}
	status, err := login.PollDeviceLogin(c.Request().Context(), ownerUserID, botID, botAgentID, loginID)
	if err != nil {
		// Storing the login that came back failed; the sign-in itself did not.
		err = application.ExternalRuntimeError(err)
		if apperror.CodeOf(err) != "" {
			return err
		}
		return apperror.Wrap(apperror.CodeAgentCredentialMaterializationFailed, err, nil)
	}
	return c.JSON(http.StatusOK, DeviceLoginPollResponse{Status: status.Status})
}

// CancelDevice godoc
// @Summary Cancel a pending device-code sign-in
// @Tags external-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Bot Agent ID"
// @Param body body DeviceLoginPollRequest true "Sign-in reference"
// @Success 204
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id}/login/device/cancel [post].
func (h *ExternalAgentLoginHandler) CancelDevice(c echo.Context) error {
	login, botID, botAgentID, _, err := h.requireLogin(c)
	if err != nil {
		return err
	}
	loginID, err := loginIDOf(c)
	if err != nil {
		return err
	}
	if err := login.CancelDeviceLogin(c.Request().Context(), botID, botAgentID, loginID); err != nil {
		return h.runtimeError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func loginIDOf(c echo.Context) (string, error) {
	var req DeviceLoginPollRequest
	if err := c.Bind(&req); err != nil {
		return "", err
	}
	if strings.TrimSpace(req.LoginID) == "" {
		return "", apperror.FieldRequired("login_id")
	}
	return strings.TrimSpace(req.LoginID), nil
}

// runtimeError answers a failure the user can act on with its own code, and
// any other with the runtime being unavailable.
func (h *ExternalAgentLoginHandler) runtimeError(c echo.Context, err error) error {
	if translated := application.ExternalAgentError(err); apperror.CodeOf(translated) != "" {
		return translated
	}
	runtime := ""
	if agent, lookupErr := h.agents.GetActive(c.Request().Context(), strings.TrimSpace(c.Param("bot_id")), strings.TrimSpace(c.Param("id"))); lookupErr == nil {
		runtime = agent.Runtime
	}
	return apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": runtime})
}
