package webhook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/memohai/memoh/domains/channel/gateway"
	privatewebhook "github.com/memohai/memoh/domains/channel/internal/webhook"
	"github.com/memohai/memoh/internal/config"
)

// manager is the process-boundary public implementation wrapping the
// Channel-owned private tunnel process manager.
type manager struct {
	inner *privatewebhook.Manager
}

// NewManager constructs the Channel-owned webhook tunnel manager.
func NewManager(log *slog.Logger, cfg config.Config) Manager {
	return &manager{inner: privatewebhook.NewManager(log, cfg, DefaultListenAddr)}
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

func (m *manager) Status() Status {
	if m == nil || m.inner == nil {
		return Status{Enabled: false, Mode: "disabled", Status: StatusDisabled}
	}
	snap := m.inner.Snapshot()
	return Status{
		Enabled:       snap.Enabled,
		Mode:          snap.Mode,
		Status:        snap.Status,
		PublicBaseURL: snap.PublicBaseURL,
		Error:         snap.Error,
	}
}

func (m *manager) PublicBaseURL() string {
	if m == nil || m.inner == nil {
		return ""
	}
	return m.inner.PublicBaseURL()
}

// NormalizeConfiguredPublicBase validates an operator-provided public base URL.
// Configured public bases must be public HTTPS origins without path prefixes,
// ports, userinfo, query, or fragment.
func NormalizeConfiguredPublicBase(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("public base url is empty")
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse public base url: %w", err)
	}
	if u.Scheme != "https" || strings.TrimSpace(u.Host) == "" {
		return "", errors.New("public base url must be HTTPS")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("public base url must not include userinfo, query, or fragment")
	}
	if path := strings.TrimSpace(u.EscapedPath()); path != "" && path != "/" {
		return "", errors.New("public base url must not include a path")
	}
	if u.Port() != "" {
		return "", errors.New("public base url must not include a port")
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u.Hostname()), "."))
	if !gateway.IsPublicHost(host) {
		return "", errors.New("public base url host must be public")
	}
	return "https://" + host, nil
}
