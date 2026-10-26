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
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/feishu"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
)

// Feishu registration poll statuses returned to the browser.
const (
	feishuRegistrationPending         = "pending"
	feishuRegistrationDenied          = "denied"
	feishuRegistrationExpired         = "expired"
	feishuRegistrationFailed          = "failed"
	feishuRegistrationConnected       = "connected"
	feishuRegistrationNeedsActivation = "needs_activation"
	feishuRegistrationSavedDisabled   = "saved_disabled"
)

type feishuRegistrationSessions interface {
	Start(ctx context.Context, botID, ownerID string, preset feishu.RegistrationAppPreset) (feishu.RegistrationSession, error)
	Poll(ctx context.Context, sessionID, botID, ownerID string) (feishu.RegistrationPollResult, error)
}

type feishuChannelConfigs interface {
	ResolveEffectiveConfig(ctx context.Context, botID string, channelType channel.ChannelType) (channel.ChannelConfig, error)
}

// FeishuRegistrationHandler creates a Feishu or Lark bot app by QR scan and
// stores its credentials as the bot's Feishu channel.
type FeishuRegistrationHandler struct {
	sessions       feishuRegistrationSessions
	configs        feishuChannelConfigs
	channelRuntime channel.Runtime
	authorize      func(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error)
	activation     func(ctx context.Context, credentials map[string]any) (feishu.BotActivation, error)
	logger         *slog.Logger
}

// NewFeishuRegistrationHandler creates the handler with in-memory sessions.
func NewFeishuRegistrationHandler(log *slog.Logger, accountService *accounts.Service, botService *bots.Service, channelStore *channel.Store, channelRuntime channel.Runtime) *FeishuRegistrationHandler {
	if log == nil {
		log = slog.Default()
	}
	return &FeishuRegistrationHandler{
		sessions:       feishu.NewRegistrationSessions(),
		configs:        channelStore,
		channelRuntime: channelRuntime,
		authorize: func(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error) {
			return AuthorizeBotAccess(ctx, botService, accountService, channelIdentityID, botID)
		},
		activation: feishu.FetchBotActivation,
		logger:     log.With(slog.String("handler", "feishu_registration")),
	}
}

// Register registers the registration routes.
func (h *FeishuRegistrationHandler) Register(e *echo.Echo) {
	e.POST("/bots/:id/channel/feishu/registration/start", h.Start)
	e.POST("/bots/:id/channel/feishu/registration/poll", h.Poll)
}

// FeishuRegistrationStartResponse is the QR code the user scans in Feishu.
// ExpiresInSeconds lets a client with a skewed clock count down from the
// moment it received the response instead of trusting ExpiresAt.
type FeishuRegistrationStartResponse struct {
	SessionID        string    `json:"session_id"`
	VerificationURL  string    `json:"verification_url"`
	ExpiresAt        time.Time `json:"expires_at"`
	ExpiresInSeconds int       `json:"expires_in_seconds"`
	IntervalSeconds  int       `json:"interval_seconds"`
}

// FeishuRegistrationPollRequest identifies the registration to advance.
type FeishuRegistrationPollRequest struct {
	SessionID string `json:"session_id"`
}

// FeishuRegistrationPollResponse is the state of a registration. A finished
// registration reports how the channel was saved: connected, saved and
// connected but waiting for the tenant to enable the app (needs_activation),
// or saved disabled because the new app could not connect yet
// (saved_disabled).
type FeishuRegistrationPollResponse struct {
	Status string `json:"status" enums:"pending,denied,expired,failed,connected,needs_activation,saved_disabled"`
}

// Start godoc
// @Summary Start Feishu bot creation by QR scan
// @Description Begin creating a Feishu or Lark bot app for a bot that has no Feishu channel yet. Returns the URL to render as a QR code.
// @Tags bots
// @Param id path string true "Bot ID"
// @Success 200 {object} FeishuRegistrationStartResponse
// @Failure 403 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Failure 429 {object} server.Problem
// @Failure 502 {object} server.Problem
// @Router /bots/{id}/channel/feishu/registration/start [post].
func (h *FeishuRegistrationHandler) Start(c echo.Context) error {
	ctx := c.Request().Context()
	channelIdentityID, botID, bot, err := h.requireManagedBot(c)
	if err != nil {
		return err
	}
	if err := h.requireUnconfigured(ctx, botID); err != nil {
		return err
	}
	session, err := h.sessions.Start(ctx, botID, channelIdentityID, feishu.RegistrationAppPreset{Name: feishuAppName(bot)})
	if err != nil {
		return translateFeishuRegistrationError(err)
	}
	return c.JSON(http.StatusOK, FeishuRegistrationStartResponse{
		SessionID:        session.ID,
		VerificationURL:  session.VerificationURL,
		ExpiresAt:        session.ExpiresAt.UTC(),
		ExpiresInSeconds: max(0, int(time.Until(session.ExpiresAt)/time.Second)),
		IntervalSeconds:  int(session.Interval / time.Second),
	})
}

// Poll godoc
// @Summary Poll Feishu bot creation
// @Description Advance a Feishu bot creation. When the user confirms in Feishu, the new app's credentials are saved as the bot's Feishu channel and never returned.
// @Tags bots
// @Param id path string true "Bot ID"
// @Param payload body FeishuRegistrationPollRequest true "Registration session"
// @Success 200 {object} FeishuRegistrationPollResponse
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Router /bots/{id}/channel/feishu/registration/poll [post].
func (h *FeishuRegistrationHandler) Poll(c echo.Context) error {
	ctx := c.Request().Context()
	channelIdentityID, botID, _, err := h.requireManagedBot(c)
	if err != nil {
		return err
	}
	var req FeishuRegistrationPollRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return apperror.FieldRequired("session_id")
	}
	result, err := h.sessions.Poll(ctx, sessionID, botID, channelIdentityID)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		h.logRegistration(ctx, "poll", errs.Wrap(err, "poll feishu registration", slog.String("bot_id", botID)))
		if errors.Is(err, feishu.ErrRegistrationFailed) {
			return c.JSON(http.StatusOK, FeishuRegistrationPollResponse{Status: feishuRegistrationFailed})
		}
		// A transient transport failure keeps the session; the next poll retries.
		return c.JSON(http.StatusOK, FeishuRegistrationPollResponse{Status: feishuRegistrationPending})
	}
	switch result.Status {
	case feishu.RegistrationSuccess:
		status, err := h.saveRegisteredApp(ctx, botID, result)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, FeishuRegistrationPollResponse{Status: status})
	case feishu.RegistrationDenied:
		return c.JSON(http.StatusOK, FeishuRegistrationPollResponse{Status: feishuRegistrationDenied})
	case feishu.RegistrationExpired:
		return c.JSON(http.StatusOK, FeishuRegistrationPollResponse{Status: feishuRegistrationExpired})
	default:
		return c.JSON(http.StatusOK, FeishuRegistrationPollResponse{Status: feishuRegistrationPending})
	}
}

// saveRegisteredApp stores the new app as the bot's Feishu channel. Feishu
// shows the secret only once, so a channel that cannot be enabled yet, for
// example while an administrator still has to approve the app, is saved
// disabled rather than dropped.
func (h *FeishuRegistrationHandler) saveRegisteredApp(ctx context.Context, botID string, result feishu.RegistrationPollResult) (string, error) {
	if err := h.requireUnconfigured(ctx, botID); err != nil {
		if apperror.CodeOf(err) == apperror.CodeChannelAlreadyConfigured {
			// The new app exists in Feishu but is not saved; the user deletes
			// it there or enters it manually after removing the other channel.
			h.logger.WarnContext(ctx, "feishu registration finished after the channel was configured", slog.String("bot_id", botID))
		}
		return "", err
	}
	if h.channelRuntime == nil {
		return "", echo.NewHTTPError(http.StatusInternalServerError, "channel lifecycle not configured")
	}
	credentials := map[string]any{
		"appId":       result.AppID,
		"appSecret":   result.AppSecret,
		"region":      result.Region,
		"inboundMode": "websocket",
		"setupMode":   feishu.SetupModeRegistration,
	}
	enabled := false
	_, err := h.channelRuntime.UpsertBotChannelConfig(ctx, botID, feishu.Type, channel.UpsertConfigRequest{Credentials: credentials, Disabled: &enabled})
	if err == nil {
		return h.activationStatus(ctx, botID, credentials), nil
	}
	if !errors.Is(err, channel.ErrChannelDiscoveryFailed) && !errors.Is(err, channel.ErrEnableChannelFailed) {
		return "", translateFeishuChannelSaveError(err)
	}
	h.logRegistration(ctx, "enable", errs.Wrap(err, "enable registered feishu app", slog.String("bot_id", botID)))
	disabled := true
	if _, err := h.channelRuntime.UpsertBotChannelConfig(ctx, botID, feishu.Type, channel.UpsertConfigRequest{Credentials: credentials, Disabled: &disabled}); err != nil {
		return "", translateFeishuChannelSaveError(err)
	}
	return feishuRegistrationSavedDisabled, nil
}

// activationStatus reports whether a connected app can already serve users.
// An unknown or unreadable status counts as connected: the channel works, and
// the status only refines the message.
func (h *FeishuRegistrationHandler) activationStatus(ctx context.Context, botID string, credentials map[string]any) string {
	if h.activation == nil {
		return feishuRegistrationConnected
	}
	activation, err := h.activation(ctx, credentials)
	if err != nil {
		h.logRegistration(ctx, "activation", errs.Wrap(err, "check feishu bot activation", slog.String("bot_id", botID)))
		return feishuRegistrationConnected
	}
	if activation.Known && !activation.Active {
		return feishuRegistrationNeedsActivation
	}
	return feishuRegistrationConnected
}

func (h *FeishuRegistrationHandler) requireManagedBot(c echo.Context) (string, string, bots.Bot, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", "", bots.Bot{}, err
	}
	botID, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return "", "", bots.Bot{}, err
	}
	bot, err := h.authorize(c.Request().Context(), channelIdentityID, botID)
	if err != nil {
		return "", "", bots.Bot{}, err
	}
	return channelIdentityID, botID, bot, nil
}

// requireUnconfigured refuses a bot that already has a Feishu channel. A new
// app gives every user a new open_id, so replacing the app would orphan the
// bot's existing Feishu bindings.
func (h *FeishuRegistrationHandler) requireUnconfigured(ctx context.Context, botID string) error {
	if h.configs == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "channel store not configured")
	}
	_, err := h.configs.ResolveEffectiveConfig(ctx, botID, feishu.Type)
	if err == nil {
		return apperror.New(apperror.CodeChannelAlreadyConfigured, nil)
	}
	if errors.Is(err, channel.ErrChannelConfigNotFound) {
		return nil
	}
	return errs.Wrap(err, "resolve feishu channel config", slog.String("bot_id", botID))
}

// logRegistration records a failed registration step. The step names which
// one: poll, enable (the channel was saved disabled instead) or activation.
func (h *FeishuRegistrationHandler) logRegistration(ctx context.Context, step string, err error) {
	result := errlog.Event(ctx, "channel.feishu.registration", err, errlog.Options{})
	attrs := append(result.Attrs(), slog.String("step", step))
	h.logger.LogAttrs(ctx, result.Level, "feishu registration step failed", attrs...)
}

// translateFeishuRegistrationError maps a registration failure to the
// public error of the start request.
func translateFeishuRegistrationError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, feishu.ErrRegistrationRateLimited) {
		return apperror.Wrap(apperror.CodeChannelRegistrationRateLimited, err, nil)
	}
	return apperror.Wrap(apperror.CodeChannelRegistrationUnavailable, err, nil)
}

func translateFeishuChannelSaveError(err error) error {
	if mapped := mapChannelRuntimeError(err); mapped != nil {
		return mapped
	}
	if errors.Is(err, channel.ErrEnableChannelFailed) {
		return apperror.Wrap(apperror.CodeChannelEnableFailed, err, nil)
	}
	return errs.Wrap(err, "save registered feishu app")
}

func feishuAppName(bot bots.Bot) string {
	if name := strings.TrimSpace(bot.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(bot.Name)
}
