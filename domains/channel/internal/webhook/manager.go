package webhook

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

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/internal/config"
)

const (
	defaultMetricsAddr = "127.0.0.1:18735"

	statusDisabled = "disabled"
	statusStarting = "starting"
	statusReady    = "ready"
	statusError    = "error"
	statusStopped  = "stopped"
)

// statusSnapshot is the owner-private readiness view. Public boundary maps
// these fields explicitly onto domains/channel/webhook.Status.
type statusSnapshot struct {
	Enabled       bool
	Mode          string
	Status        string
	PublicBaseURL string
	Error         string
}

type processKiller interface {
	Kill() error
}

type Manager struct {
	log               *slog.Logger
	cfg               config.WebhookTunnelConfig
	defaultListenAddr string

	httpClient *http.Client

	mu             sync.RWMutex
	status         statusSnapshot
	managedProcess processKiller
	cmdDone        chan struct{}
	pollDone       chan struct{}
	cancel         context.CancelFunc
	started        bool
	stopped        bool
}

func NewManager(log *slog.Logger, cfg config.Config, defaultListenAddr string) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(defaultListenAddr) == "" {
		defaultListenAddr = "127.0.0.1:18734"
	}
	mode := cfg.WebhookTunnel.EffectiveMode()
	status := statusSnapshot{
		Enabled: mode != config.WebhookTunnelModeDisabled,
		Mode:    mode,
		Status:  statusDisabled,
	}
	if status.Enabled {
		status.Status = statusStarting
	}
	return &Manager{
		log:               log.With(slog.String("component", "webhook_tunnel")),
		cfg:               cfg.WebhookTunnel,
		defaultListenAddr: defaultListenAddr,
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
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return errors.New("webhook tunnel manager is stopped")
	}
	if m.started {
		m.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.cancel = cancel
	m.started = true
	m.mu.Unlock()
	switch m.cfg.EffectiveMode() {
	case config.WebhookTunnelModeDisabled:
		cancel()
		m.setCancel(nil)
		m.setStatus(statusSnapshot{Enabled: false, Mode: config.WebhookTunnelModeDisabled, Status: statusDisabled})
		return nil
	case config.WebhookTunnelModeExternal:
		m.setStatus(statusSnapshot{Enabled: true, Mode: config.WebhookTunnelModeExternal, Status: statusStarting})
		m.startPollLoop(runCtx, m.metricsURL())
		return nil
	case config.WebhookTunnelModeManaged:
		m.setStatus(statusSnapshot{Enabled: true, Mode: config.WebhookTunnelModeManaged, Status: statusStarting})
		if err := m.startManaged(runCtx); err != nil {
			cancel()
			m.setCancel(nil)
			m.setError(err)
			return nil
		}
		m.startPollLoop(runCtx, m.localMetricsURL())
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
	m.started = false
	m.stopped = true
	process := m.managedProcess
	cmdDone := m.cmdDone
	pollDone := m.pollDone
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	var errs []error
	if process != nil {
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			errs = append(errs, fmt.Errorf("stop managed webhook tunnel: %w", err))
		}
	}
	cmdWaitErr := waitForTask(ctx, cmdDone)
	if cmdWaitErr != nil {
		errs = append(errs, fmt.Errorf("wait for managed webhook tunnel: %w", cmdWaitErr))
	}
	pollWaitErr := waitForTask(ctx, pollDone)
	if pollWaitErr != nil {
		errs = append(errs, fmt.Errorf("wait for webhook tunnel polling: %w", pollWaitErr))
	}
	if cmdWaitErr == nil && pollWaitErr == nil {
		m.markStopped()
	}
	return errors.Join(errs...)
}

func waitForTask(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// Prefer a completed task when completion races the shutdown deadline.
		select {
		case <-done:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func (m *Manager) Snapshot() statusSnapshot {
	if m == nil {
		return statusSnapshot{Status: statusDisabled, Mode: config.WebhookTunnelModeDisabled}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := m.status
	if base := m.configuredPublicBaseURLLocked(); base != "" {
		status.Enabled = true
		status.PublicBaseURL = base
		status.Status = statusReady
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
	if m.status.Status != statusReady {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(m.status.PublicBaseURL), "/")
}

func (m *Manager) configuredPublicBaseURLLocked() string {
	base, err := normalizeConfiguredPublicBase(m.cfg.PublicBaseURL)
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
		m.log.InfoContext(ctx, "starting cloudflared quick tunnel",
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
	m.managedProcess = cmd.Process
	m.cmdDone = done
	m.mu.Unlock()
	go func() {
		defer close(done)
		defer func() { _ = os.RemoveAll(homeDir) }()
		err := cmd.Wait()
		stopping := ctx.Err() != nil
		m.mu.Lock()
		if m.cmdDone == done {
			m.managedProcess = nil
			current := statusSnapshot{
				Enabled: true,
				Mode:    m.cfg.EffectiveMode(),
				Status:  statusStopped,
			}
			if err != nil && !stopping {
				current.Status = statusError
				current.Error = "cloudflared exited"
			}
			m.status = current
		}
		m.mu.Unlock()
		if err != nil && !stopping && m.log != nil {
			m.log.WarnContext(ctx, "cloudflared exited", slog.Any("error", err))
		}
	}()
	return nil
}

func (m *Manager) startPollLoop(ctx context.Context, metricsURL string) {
	done := make(chan struct{})
	m.mu.Lock()
	m.pollDone = done
	m.mu.Unlock()
	go func() {
		defer close(done)
		m.pollLoop(ctx, metricsURL)
	}()
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
		listenAddr = m.defaultListenAddr
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

// normalizeConfiguredPublicBase validates an operator-provided public base URL.
// Configured public bases must be public HTTPS origins without path prefixes,
// ports, userinfo, query, or fragment.
func normalizeConfiguredPublicBase(raw string) (string, error) {
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

func (m *Manager) setReady(publicBase string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = statusSnapshot{
		Enabled:       true,
		Mode:          m.cfg.EffectiveMode(),
		Status:        statusReady,
		PublicBaseURL: publicBase,
	}
}

func (m *Manager) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil && m.log != nil {
		m.log.Debug("webhook tunnel status error", slog.Any("error", err))
	}
	m.status = statusSnapshot{
		Enabled: true,
		Mode:    m.cfg.EffectiveMode(),
		Status:  statusError,
		Error:   sanitizeError(err),
	}
}

func (m *Manager) setPollError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil && m.log != nil {
		m.log.Debug("webhook tunnel poll error", slog.Any("error", err))
	}
	if m.status.Status == statusReady && strings.TrimSpace(m.status.PublicBaseURL) != "" {
		m.status.Error = sanitizeError(err)
		return
	}
	m.status = statusSnapshot{
		Enabled: true,
		Mode:    m.cfg.EffectiveMode(),
		Status:  statusError,
		Error:   sanitizeError(err),
	}
}

func (m *Manager) setStatus(status statusSnapshot) {
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
		m.status = statusSnapshot{
			Enabled: true,
			Mode:    m.cfg.EffectiveMode(),
			Status:  statusStopped,
		}
	}
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return "webhook tunnel unavailable"
}
