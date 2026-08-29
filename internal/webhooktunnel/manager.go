package webhooktunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/config"
)

const (
	DefaultListenAddr  = "127.0.0.1:18734"
	defaultMetricsAddr = "127.0.0.1:18735"

	StatusDisabled = "disabled"
	StatusStarting = "starting"
	StatusReady    = "ready"
	StatusError    = "error"
	StatusStopped  = "stopped"
)

type Status struct {
	Enabled       bool   `json:"enabled"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	PublicBaseURL string `json:"public_base_url,omitempty"`
	Error         string `json:"error,omitempty"`
}

type Manager struct {
	log *slog.Logger
	cfg config.WebhookTunnelConfig

	httpClient *http.Client

	mu      sync.RWMutex
	status  Status
	cmd     *exec.Cmd
	cmdDone chan struct{}
	cancel  context.CancelFunc
}

func NewManager(log *slog.Logger, cfg config.Config) *Manager {
	if log == nil {
		log = slog.Default()
	}
	mode := cfg.WebhookTunnel.EffectiveMode()
	status := Status{
		Enabled: mode != config.WebhookTunnelModeDisabled,
		Mode:    mode,
		Status:  StatusDisabled,
	}
	if status.Enabled {
		status.Status = StatusStarting
	}
	return &Manager{
		log: log.With(slog.String("component", "webhook_tunnel")),
		cfg: cfg.WebhookTunnel,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		status: status,
	}
}

func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.setCancel(cancel)
	switch m.cfg.EffectiveMode() {
	case config.WebhookTunnelModeDisabled:
		cancel()
		m.setCancel(nil)
		m.setStatus(Status{Enabled: false, Mode: config.WebhookTunnelModeDisabled, Status: StatusDisabled})
		return nil
	case config.WebhookTunnelModeExternal:
		m.setStatus(Status{Enabled: true, Mode: config.WebhookTunnelModeExternal, Status: StatusStarting})
		go m.pollLoop(runCtx, m.metricsURL())
		return nil
	case config.WebhookTunnelModeManaged:
		m.setStatus(Status{Enabled: true, Mode: config.WebhookTunnelModeManaged, Status: StatusStarting})
		if err := m.startManaged(runCtx); err != nil {
			cancel()
			m.setCancel(nil)
			m.setError(err)
			return nil
		}
		go m.pollLoop(runCtx, m.localMetricsURL())
		return nil
	default:
		cancel()
		m.setCancel(nil)
		err := fmt.Errorf("unsupported webhook tunnel mode %q", m.cfg.Mode)
		m.setError(err)
		return nil
	}
}

func (m *Manager) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	cmd := m.cmd
	done := m.cmdDone
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cmd == nil || cmd.Process == nil {
		m.markStopped()
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	if done == nil {
		m.markStopped()
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		m.markStopped()
		return nil
	}
}

func (m *Manager) Status() Status {
	if m == nil {
		return Status{Status: StatusDisabled, Mode: config.WebhookTunnelModeDisabled}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := m.status
	if base := m.configuredPublicBaseURLLocked(); base != "" {
		status.Enabled = true
		status.PublicBaseURL = base
		status.Status = StatusReady
		status.Error = ""
	}
	return status
}

func (m *Manager) PublicBaseURL() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if base := m.configuredPublicBaseURLLocked(); base != "" {
		return base
	}
	if m.status.Status != StatusReady {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(m.status.PublicBaseURL), "/")
}

func (m *Manager) configuredPublicBaseURLLocked() string {
	base, err := NormalizeConfiguredPublicBase(m.cfg.PublicBaseURL)
	if err != nil {
		return ""
	}
	return base
}

func (m *Manager) startManaged(ctx context.Context) error {
	bin := strings.TrimSpace(m.cfg.CloudflaredPath)
	if bin == "" {
		found, err := exec.LookPath("cloudflared")
		if err != nil {
			return errors.New("cloudflared binary not found; set MEMOH_CLOUDFLARED_BIN or [webhook_tunnel].cloudflared_path")
		}
		bin = found
	}
	targetURL, err := m.targetURL()
	if err != nil {
		return err
	}
	metricsAddr := strings.TrimSpace(m.cfg.MetricsAddr)
	if metricsAddr == "" {
		metricsAddr = defaultMetricsAddr
	}
	homeDir, err := os.MkdirTemp("", "memoh-cloudflared-*")
	if err != nil {
		return fmt.Errorf("prepare isolated cloudflared home: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, //nolint:gosec // G204: cloudflared binary is operator-configured / resolved via exec.LookPath, not user input
		"tunnel",
		"--no-autoupdate",
		"--url", targetURL,
		"--metrics", metricsAddr,
	)
	cmd.Env = append(os.Environ(), "HOME="+homeDir)
	if m.log != nil {
		m.log.Info("starting cloudflared quick tunnel",
			slog.String("target_url", targetURL),
			slog.String("metrics_addr", metricsAddr),
		)
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(homeDir)
		return fmt.Errorf("start cloudflared: %w", err)
	}
	done := make(chan struct{})
	m.mu.Lock()
	m.cmd = cmd
	m.cmdDone = done
	m.mu.Unlock()
	go func() {
		defer close(done)
		defer func() { _ = os.RemoveAll(homeDir) }()
		err := cmd.Wait()
		m.mu.Lock()
		if m.cmd == cmd {
			m.cmd = nil
			m.cmdDone = nil
			current := Status{
				Enabled: true,
				Mode:    m.cfg.EffectiveMode(),
				Status:  StatusStopped,
			}
			if err != nil {
				current.Status = StatusError
				current.Error = "cloudflared exited"
			}
			m.status = current
		}
		m.mu.Unlock()
		if err != nil && m.log != nil {
			m.log.Warn("cloudflared exited", slog.Any("error", err))
		}
	}()
	return nil
}

func (m *Manager) pollLoop(ctx context.Context, metricsURL string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if metricsURL == "" {
			m.setError(errors.New("webhook tunnel metrics url is not configured"))
			return
		}
		if base, err := m.fetchQuickTunnel(ctx, metricsURL); err == nil && base != "" {
			if ctx.Err() != nil {
				return
			}
			m.setReady(base)
		} else if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.setPollError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) fetchQuickTunnel(ctx context.Context, metricsURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(metricsURL, "/")+"/quicktunnel", nil)
	if err != nil {
		return "", err
	}
	resp, err := m.httpClient.Do(req) //nolint:gosec // G704: metrics URL targets the locally-managed cloudflared endpoint, not user-controlled
	if err != nil {
		return "", fmt.Errorf("read cloudflared quick tunnel status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cloudflared quick tunnel status returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode cloudflared quick tunnel status: %w", err)
	}
	return normalizePublicBase(body.Hostname)
}

func (m *Manager) metricsURL() string {
	if raw := strings.TrimSpace(m.cfg.MetricsURL); raw != "" {
		return strings.TrimRight(raw, "/")
	}
	return m.localMetricsURL()
}

func (m *Manager) localMetricsURL() string {
	addr := strings.TrimSpace(m.cfg.MetricsAddr)
	if addr == "" {
		addr = defaultMetricsAddr
	}
	return "http://" + addr
}

func (m *Manager) targetURL() (string, error) {
	if raw := strings.TrimSpace(m.cfg.TargetURL); raw != "" {
		return raw, nil
	}
	listenAddr := strings.TrimSpace(m.cfg.ListenAddr)
	if listenAddr == "" {
		listenAddr = DefaultListenAddr
	}
	host, port, err := splitListenAddr(listenAddr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func splitListenAddr(addr string) (string, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = config.DefaultHTTPAddr
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1", strings.TrimPrefix(addr, ":"), nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", fmt.Errorf("derive webhook tunnel target from server addr %q: %w", addr, err)
	}
	return host, port, nil
}

func normalizePublicBase(hostname string) (string, error) {
	raw := strings.TrimSpace(hostname)
	if raw == "" {
		return "", errors.New("cloudflared quick tunnel hostname is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse cloudflared quick tunnel hostname: %w", err)
	}
	if u.Scheme != "https" || strings.TrimSpace(u.Host) == "" {
		return "", errors.New("cloudflared quick tunnel hostname is not a public HTTPS URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("cloudflared quick tunnel hostname must not include userinfo, query, or fragment")
	}
	if path := strings.TrimSpace(u.EscapedPath()); path != "" && path != "/" {
		return "", errors.New("cloudflared quick tunnel hostname must not include a path")
	}
	if u.Port() != "" {
		return "", errors.New("cloudflared quick tunnel hostname must not include a port")
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsValid() {
		return "", errors.New("cloudflared quick tunnel hostname must be a trycloudflare.com hostname")
	}
	if host == "trycloudflare.com" || !strings.HasSuffix(host, ".trycloudflare.com") {
		return "", errors.New("cloudflared quick tunnel hostname must end with .trycloudflare.com")
	}
	return "https://" + host, nil
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
	if !channel.IsPublicHost(host) {
		return "", errors.New("public base url host must be public")
	}
	return "https://" + host, nil
}

func (m *Manager) setReady(publicBase string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = Status{
		Enabled:       true,
		Mode:          m.cfg.EffectiveMode(),
		Status:        StatusReady,
		PublicBaseURL: publicBase,
	}
}

func (m *Manager) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil && m.log != nil {
		m.log.Debug("webhook tunnel status error", slog.Any("error", err))
	}
	m.status = Status{
		Enabled: true,
		Mode:    m.cfg.EffectiveMode(),
		Status:  StatusError,
		Error:   sanitizeError(err),
	}
}

func (m *Manager) setPollError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil && m.log != nil {
		m.log.Debug("webhook tunnel poll error", slog.Any("error", err))
	}
	if m.status.Status == StatusReady && strings.TrimSpace(m.status.PublicBaseURL) != "" {
		m.status.Error = sanitizeError(err)
		return
	}
	m.status = Status{
		Enabled: true,
		Mode:    m.cfg.EffectiveMode(),
		Status:  StatusError,
		Error:   sanitizeError(err),
	}
}

func (m *Manager) setStatus(status Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = status
}

func (m *Manager) setCancel(cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = cancel
}

func (m *Manager) markStopped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Enabled {
		m.status = Status{
			Enabled: true,
			Mode:    m.cfg.EffectiveMode(),
			Status:  StatusStopped,
		}
	}
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return "webhook tunnel unavailable"
}
