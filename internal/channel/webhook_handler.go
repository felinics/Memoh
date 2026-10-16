package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/errs"
)

type webhookConfigStore interface {
	ListConfigsByType(ctx context.Context, channelType ChannelType) ([]ChannelConfig, error)
}

type webhookInboundManager interface {
	HandleInbound(ctx context.Context, cfg ChannelConfig, msg InboundMessage) error
	Registry() *Registry
}

// WebhookHandler dispatches public webhook callbacks to channel adapters.
type WebhookHandler struct {
	logger   *slog.Logger
	store    webhookConfigStore
	manager  webhookInboundManager
	registry *Registry
}

// NewWebhookServerHandler creates the generic channel webhook handler.
func NewWebhookServerHandler(log *slog.Logger, store *Store, manager *Manager) *WebhookHandler {
	if log == nil {
		log = slog.Default()
	}
	var registry *Registry
	if manager != nil {
		registry = manager.Registry()
	}
	return &WebhookHandler{
		logger:   log.With(slog.String("handler", "channel_webhook")),
		store:    store,
		manager:  manager,
		registry: registry,
	}
}

// Register registers generic channel webhook routes.
func (h *WebhookHandler) Register(e *echo.Echo) {
	e.GET("/channels/:platform/webhook/:config_id", h.Handle)
	e.POST("/channels/:platform/webhook/:config_id", h.Handle)
}

// Handle resolves the channel config and delegates the request to the adapter.
func (h *WebhookHandler) Handle(c echo.Context) error {
	if h.store == nil || h.manager == nil || h.registry == nil {
		return errs.New("channel webhook dependencies not configured")
	}
	channelType, err := h.registry.ParseChannelType(c.Param("platform"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest).WithInternal(fmt.Errorf("unknown channel platform: %w", err))
	}
	configID := strings.TrimSpace(c.Param("config_id"))
	if configID == "" {
		return echo.NewHTTPError(http.StatusBadRequest).WithInternal(errors.New("config id is required"))
	}
	cfg, err := h.findConfigByID(c.Request().Context(), channelType, configID)
	if err != nil {
		return err
	}
	if cfg.Disabled {
		desc, _ := h.registry.GetDescriptor(channelType)
		if desc.AckDisabledWebhook {
			if h.logger != nil {
				h.logger.WarnContext(c.Request().Context(),
					"channel webhook ignored for disabled channel config",
					slog.String("channel", channelType.String()),
					slog.String("config_id", configID),
				)
			}
			c.Response().WriteHeader(http.StatusOK)
			return nil
		}
		return echo.NewHTTPError(http.StatusForbidden, "channel config is disabled")
	}
	receiver, ok := h.registry.GetWebhookReceiver(channelType)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "channel webhook receiver not found")
	}
	// The access record is the one record of a failed delivery: the adapter's
	// error is returned with the channel and config, and an *echo.HTTPError it
	// chose keeps its status.
	if err := receiver.HandleWebhook(c.Request().Context(), cfg, h.manager.HandleInbound, c.Request(), c.Response()); err != nil {
		return errs.Wrap(err, "handle channel webhook",
			slog.String("channel", channelType.String()),
			slog.String("config_id", configID),
		)
	}
	return nil
}

func (h *WebhookHandler) findConfigByID(ctx context.Context, channelType ChannelType, configID string) (ChannelConfig, error) {
	items, err := h.store.ListConfigsByType(ctx, channelType)
	if err != nil {
		return ChannelConfig{}, errs.Wrap(err, "list channel configs", slog.String("channel", channelType.String()))
	}
	for _, item := range items {
		if item.ChannelType == channelType && strings.TrimSpace(item.ID) == configID {
			return item, nil
		}
	}
	return ChannelConfig{}, echo.NewHTTPError(http.StatusNotFound, "channel config not found")
}
