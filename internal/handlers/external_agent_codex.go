package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	codexruntime "github.com/felinics/memoh/internal/agent/runtime/codex"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
)

// codexService is the slice of the codex driver the handler drives. The
// ChatGPT sign-in itself is served by ExternalAgentLoginHandler.
type codexService interface {
	Account(ctx context.Context, botID, botAgentID string) (codexruntime.Account, error)
	AccountUsage(ctx context.Context, botID, botAgentID string) (codexruntime.AccountUsage, error)
}

// ExternalAgentCodexHandler exposes what is Codex's own: the ChatGPT account
// it is signed in to and that account's usage limits.
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
	e.GET("/bots/:bot_id/agents/:id/codex/usage", h.Usage)
	e.GET("/bots/:bot_id/agents/:id/codex/account", h.Account)
}

func (h *ExternalAgentCodexHandler) requireAgentPermission(c echo.Context, permission string) (string, string, error) {
	botID := strings.TrimSpace(c.Param("bot_id"))
	botAgentID := strings.TrimSpace(c.Param("id"))
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", "", err
	}
	bot, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, permission)
	if err != nil {
		return "", "", err
	}
	agent, err := h.agents.GetActive(c.Request().Context(), bot.ID, botAgentID)
	if err != nil || agent.Runtime != botagents.RuntimeCodex {
		return "", "", apperror.New(apperror.CodeBotAgentNotFound, nil)
	}
	return bot.ID, agent.ID, nil
}

// CodexUsageWindow is one rolling usage window of the ChatGPT account.
type CodexUsageWindow struct {
	UsedPercent int `json:"used_percent" validate:"required"`
	// WindowMinutes is 0 when the backend does not report the window length.
	WindowMinutes int        `json:"window_minutes" validate:"required"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
} // @name externalagent.CodexUsageWindow

// CodexUsageResponse reports the ChatGPT account's Codex usage limits.
type CodexUsageResponse struct {
	LimitReached bool               `json:"limit_reached" validate:"required"`
	Windows      []CodexUsageWindow `json:"windows" validate:"required"`
} // @name externalagent.CodexUsageResponse

// Usage godoc
// @Summary Get the Codex usage limits of the Agent's ChatGPT account
// @Description Available to anyone who can chat with the Bot, so the composer
// can warn before a turn runs into the limit.
// @Tags external-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Bot Agent ID"
// @Success 200 {object} CodexUsageResponse
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 422 {object} server.Problem
// @Failure 502 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id}/codex/usage [get].
func (h *ExternalAgentCodexHandler) Usage(c echo.Context) error {
	botID, botAgentID, err := h.requireAgentPermission(c, bots.PermissionChat)
	if err != nil {
		return err
	}
	usage, err := h.driver.AccountUsage(c.Request().Context(), botID, botAgentID)
	if err != nil {
		return codexUsageError(err)
	}
	response := CodexUsageResponse{LimitReached: usage.LimitReached, Windows: make([]CodexUsageWindow, 0, len(usage.Windows))}
	for _, window := range usage.Windows {
		response.Windows = append(response.Windows, CodexUsageWindow{
			UsedPercent:   window.UsedPercent,
			WindowMinutes: window.WindowMinutes,
			ResetsAt:      window.ResetsAt,
		})
	}
	return c.JSON(http.StatusOK, response)
}

// codexUsageError translates a failed usage read: an Agent credential or
// runtime failure by its kind, and usage Codex could not read by its own code.
func codexUsageError(err error) error {
	if errors.Is(err, codexruntime.ErrUsageUnavailable) {
		return apperror.Wrap(apperror.CodeAgentCredentialUsageUnavailable, err, nil)
	}
	return application.ExternalRuntimeError(err)
}

// CodexAccountResponse reports the ChatGPT account the Agent's Codex is
// signed in to.
type CodexAccountResponse struct {
	SignedIn bool   `json:"signed_in" validate:"required"`
	Email    string `json:"email,omitempty"`
} // @name externalagent.CodexAccountResponse

// Account godoc
// @Summary Get the ChatGPT account the Agent's Codex is signed in to
// @Description Codex keeps its own sign-in in the Agent's workspace; this asks it, starting it when it is not running.
// @Tags external-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Bot Agent ID"
// @Success 200 {object} CodexAccountResponse
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 422 {object} server.Problem
// @Failure 503 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id}/codex/account [get].
func (h *ExternalAgentCodexHandler) Account(c echo.Context) error {
	botID, botAgentID, err := h.requireAgentPermission(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	account, err := h.driver.Account(c.Request().Context(), botID, botAgentID)
	if err != nil {
		return application.ExternalRuntimeError(err)
	}
	return c.JSON(http.StatusOK, CodexAccountResponse{SignedIn: account.SignedIn, Email: account.Email})
}
