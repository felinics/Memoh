package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/webhooktunnel"
)

type WebhookTunnelHandler struct {
	manager interface{ Status() webhooktunnel.Status }
}

func NewWebhookTunnelHandler(manager interface{ Status() webhooktunnel.Status }) *WebhookTunnelHandler {
	return &WebhookTunnelHandler{manager: manager}
}

func (h *WebhookTunnelHandler) Register(e *echo.Echo) {
	e.GET("/webhook-tunnel/status", h.Status)
}

// Status godoc
// @Summary Get webhook tunnel status
// @Tags system
// @Success 200 {object} webhooktunnel.Status
// @Router /webhook-tunnel/status [get].
func (h *WebhookTunnelHandler) Status(c echo.Context) error {
	if h == nil || h.manager == nil {
		return c.JSON(http.StatusOK, webhooktunnel.Status{
			Enabled: false,
			Mode:    "disabled",
			Status:  webhooktunnel.StatusDisabled,
		})
	}
	return c.JSON(http.StatusOK, h.manager.Status())
}
