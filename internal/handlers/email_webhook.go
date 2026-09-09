package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/email"
	emailmailgun "github.com/felinics/memoh/internal/email/adapters/mailgun"
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
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /email/mailgun/webhook/{config_id} [post].
func (h *EmailWebhookHandler) HandleMailgun(c echo.Context) error {
	configID := strings.TrimSpace(c.Param("config_id"))
	if configID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "config_id is required")
	}

	provider, err := h.service.GetProviderInternal(c.Request().Context(), configID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "provider not found")
	}

	if provider.Provider != string(emailmailgun.ProviderName) {
		return echo.NewHTTPError(http.StatusBadRequest, "provider is not mailgun")
	}

	mode, _ := provider.Config["inbound_mode"].(string)
	if mode != emailmailgun.InboundModeWebhook {
		return echo.NewHTTPError(http.StatusBadRequest, "provider is not in webhook mode")
	}

	adapter, err := h.service.Registry().Get(emailmailgun.ProviderName)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "mailgun adapter not available")
	}
	webhookReceiver, ok := adapter.(email.WebhookReceiver)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "mailgun adapter does not support webhooks")
	}

	var configMap map[string]any
	configBytes, _ := json.Marshal(provider.Config)
	_ = json.Unmarshal(configBytes, &configMap)

	inbound, err := webhookReceiver.HandleWebhook(c.Request().Context(), configMap, c.Request())
	if err != nil {
		h.logger.Error("webhook handling failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	}

	if err := h.trigger.HandleInbound(c.Request().Context(), configID, *inbound); err != nil {
		h.logger.Error("inbound processing failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "processing failed")
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}
