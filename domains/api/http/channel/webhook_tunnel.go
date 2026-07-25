package channel

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/channel/webhook"
)

type WebhookTunnelHandler struct {
	manager webhook.Service
}

func NewWebhookTunnelHandler(manager webhook.Service) *WebhookTunnelHandler {
	return &WebhookTunnelHandler{manager: manager}
}

func (h *WebhookTunnelHandler) Register(e *echo.Echo) {
	e.GET("/webhook-tunnel/status", h.Status)
}

// Status godoc
// @Summary Get webhook tunnel status
// @Tags system
// @Success 200 {object} webhook.Status
// @Router /webhook-tunnel/status [get].
func (h *WebhookTunnelHandler) Status(c echo.Context) error {
	if h == nil || h.manager == nil {
		return c.JSON(http.StatusOK, webhook.Status{
			Enabled: false,
			Mode:    "disabled",
			Status:  webhook.StatusDisabled,
		})
	}
	return c.JSON(http.StatusOK, h.manager.Status())
}
