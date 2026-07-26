package http

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/internal/apperror"
)

// EmailWebhookHandler handles inbound email webhooks (Mailgun).
// Modeled after the Feishu WebhookHandler pattern.
type EmailWebhookHandler struct {
	service *email.Service
	manager *email.Manager
	trigger *email.Trigger
	logger  *slog.Logger
}

func NewEmailWebhookHandler(log *slog.Logger, service *email.Service, manager *email.Manager, trigger *email.Trigger) *EmailWebhookHandler {
	return &EmailWebhookHandler{
		service: service,
		manager: manager,
		trigger: trigger,
		logger:  log.With(slog.String("handler", "email_webhook")),
	}
}

func (h *EmailWebhookHandler) Register(e *echo.Echo) {
	e.POST("/email/mailgun/webhook/:config_id", h.HandleMailgun)
}

// HandleMailgun godoc
// @Summary Mailgun inbound email webhook
// @Description Receives inbound emails from Mailgun
// @Tags email-webhook
// @Param config_id path string true "Email provider config ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /email/mailgun/webhook/{config_id} [post].
func (h *EmailWebhookHandler) HandleMailgun(c echo.Context) error {
	configID := strings.TrimSpace(c.Param("config_id"))
	if configID == "" {
		return apperror.Required("config_id")
	}

	provider, err := h.service.GetProviderInternal(c.Request().Context(), configID)
	if err != nil {
		return apperror.NotFound("get email provider", err)
	}

	if provider.Provider != string(email.ProviderMailgun) {
		return apperror.Invalid("validate email provider", nil)
	}

	mode, _ := provider.Config["inbound_mode"].(string)
	if mode != email.MailgunInboundModeWebhook {
		return apperror.Invalid("validate email inbound mode", nil)
	}

	inbound, err := h.service.HandleWebhook(c.Request().Context(), configID, c.Request())
	if err != nil {
		h.logger.Error("webhook handling failed", slog.Any("error", err))
		return apperror.Forbidden("verify mailgun webhook", err)
	}

	if err := h.trigger.HandleInbound(c.Request().Context(), configID, *inbound); err != nil {
		h.logger.Error("inbound processing failed", slog.Any("error", err))
		return apperror.Internal("process mailgun inbound", err)
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}
