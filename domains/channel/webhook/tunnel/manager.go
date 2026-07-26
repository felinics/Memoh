// Package tunnel owns the concrete webhook tunnel process adapter.
package tunnel

import (
	"context"
	"log/slog"

	privatewebhook "github.com/memohai/memoh/domains/channel/internal/webhook"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/internal/config"
)

type manager struct {
	inner *privatewebhook.Manager
}

// NewManager constructs the Channel-owned webhook tunnel manager.
func NewManager(log *slog.Logger, cfg config.Config) webhook.Manager {
	return &manager{inner: privatewebhook.NewManager(log, cfg, webhook.DefaultListenAddr)}
}

func (m *manager) Start(ctx context.Context) error {
	if m == nil || m.inner == nil {
		return nil
	}
	return m.inner.Start(ctx)
}

func (m *manager) Stop(ctx context.Context) error {
	if m == nil || m.inner == nil {
		return nil
	}
	return m.inner.Stop(ctx)
}

func (m *manager) Status() webhook.Status {
	if m == nil || m.inner == nil {
		return webhook.Status{Enabled: false, Mode: "disabled", Status: webhook.StatusDisabled}
	}
	snapshot := m.inner.Snapshot()
	return webhook.Status{
		Enabled:       snapshot.Enabled,
		Mode:          snapshot.Mode,
		Status:        snapshot.Status,
		PublicBaseURL: snapshot.PublicBaseURL,
		Error:         snapshot.Error,
	}
}

func (m *manager) PublicBaseURL() string {
	if m == nil || m.inner == nil {
		return ""
	}
	return m.inner.PublicBaseURL()
}
