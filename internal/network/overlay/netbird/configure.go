package netbird

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/configutil"
	"github.com/felinics/memoh/internal/network/overlay/netbird/daemonpb"
)

// defaultManagementURL is what the daemon uses when none is configured.
const defaultManagementURL = "https://api.netbird.io:443"

// ssoLoginTTL bounds how long Memoh shows a device-login URL and waits for
// the user to finish it. The daemon does not report the IdP's expiry.
const ssoLoginTTL = 10 * time.Minute

// ssoLogin is a pending device login, kept next to the bot's state so that
// the status API can show the URL and a later attach does not replace it.
type ssoLogin struct {
	URL           string    `json:"url"`
	UserCode      string    `json:"user_code"`
	ManagementURL string    `json:"management_url"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// ssoWaiters holds the bots with a goroutine blocked in WaitSSOLogin, so
// that repeated attaches start at most one.
var ssoWaiters sync.Map

func (d *nativeDriver) managementURL() string {
	raw := configutil.String(d.config.Config, "management_url")
	if raw == "" {
		raw = defaultManagementURL
	}
	return normalizeManagementURL(raw)
}

// normalizeManagementURL makes a configured URL comparable with the one the
// daemon stores, which carries an explicit port.
func normalizeManagementURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimRight(strings.TrimSpace(raw), "/")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	return u.Scheme + "://" + host + strings.TrimRight(u.Path, "/")
}

func (d *nativeDriver) hostname(req netctl.AttachmentRequest) string {
	return configutil.FirstNonEmpty(configutil.String(d.config.Config, "hostname"), req.BotID)
}

// configure brings the daemon in line with the bot network config.
func (d *nativeDriver) configure(ctx context.Context, req netctl.AttachmentRequest) error {
	c := d.client(req)
	if err := c.waitReady(ctx, daemonWait); err != nil {
		return err
	}
	st, err := c.status(ctx)
	if err != nil {
		return err
	}
	switch st.GetStatus() {
	case daemonNeedsLogin, daemonLoginFailed, daemonSessionExpired:
		return d.login(ctx, req, c)
	}
	cfg, err := c.config(ctx)
	if err != nil {
		return err
	}
	wantURL := d.managementURL()
	if normalizeManagementURL(cfg.GetManagementUrl()) != wantURL {
		// A peer belongs to one management server; moving it means a new
		// login there.
		if err := c.logout(ctx); err != nil {
			return err
		}
		return d.login(ctx, req, c)
	}
	wantDisableDNS := configutil.Bool(d.config.Config, "disable_dns")
	if cfg.GetDisableDns() != wantDisableDNS {
		// SetConfig only persists; the engine reads it on the next Up.
		if err := c.setDisableDNS(ctx, wantURL, wantDisableDNS); err != nil {
			return err
		}
		if err := c.down(ctx); err != nil {
			return err
		}
		return c.up(ctx)
	}
	if st.GetStatus() == daemonIdle {
		return c.up(ctx)
	}
	return nil
}

func (d *nativeDriver) login(ctx context.Context, req netctl.AttachmentRequest, c daemonClient) error {
	setupKey := configutil.String(d.config.Config, "setup_key")
	if setupKey == "" {
		if pending, ok := d.pendingSSO(req); ok && pending.ManagementURL == d.managementURL() {
			// Keep the URL the user may have open; only make sure someone
			// is waiting for it.
			d.waitForSSO(ctx, req, c, pending)
			return nil
		}
	}
	disableDNS := configutil.Bool(d.config.Config, "disable_dns")
	resp, err := c.login(ctx, &daemonpb.LoginRequest{
		SetupKey:      setupKey,
		ManagementUrl: d.managementURL(),
		AdminURL:      configutil.String(d.config.Config, "admin_url"),
		Hostname:      d.hostname(req),
		DisableDns:    &disableDNS,
	})
	if err != nil {
		if rejected(err) {
			d.saveLoginError(req, status.Convert(err).Message())
		}
		return err
	}
	d.clearLoginError(req)
	if !resp.GetNeedsSSOLogin() {
		d.clearSSO(req)
		return c.up(ctx)
	}
	pending := ssoLogin{
		URL:           configutil.FirstNonEmpty(resp.GetVerificationURIComplete(), resp.GetVerificationURI()),
		UserCode:      resp.GetUserCode(),
		ManagementURL: d.managementURL(),
		ExpiresAt:     time.Now().Add(ssoLoginTTL),
	}
	if err := d.saveSSO(req, pending); err != nil {
		return err
	}
	d.waitForSSO(ctx, req, c, pending)
	return nil
}

// waitForSSO blocks in WaitSSOLogin in the background until the user
// finishes the device login, then brings the peer up. The wait outlives the
// request that started it.
func (d *nativeDriver) waitForSSO(ctx context.Context, req netctl.AttachmentRequest, c daemonClient, pending ssoLogin) {
	key := req.BotID + "/" + pending.UserCode
	if _, running := ssoWaiters.LoadOrStore(key, struct{}{}); running {
		return
	}
	hostname := d.hostname(req)
	go func() {
		defer ssoWaiters.Delete(key)
		ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), pending.ExpiresAt)
		defer cancel()
		err := c.waitSSOLogin(ctx, pending.UserCode, hostname)
		if err == nil {
			err = c.up(ctx)
		}
		d.clearSSO(req)
		if err != nil {
			d.logger.WarnContext(ctx, "netbird sso login did not complete",
				slog.String("bot_id", req.BotID), slog.Any("error", err))
		}
	}()
}

func (d *nativeDriver) ssoPath(req netctl.AttachmentRequest) string {
	return filepath.Join(d.root, req.BotID, "netbird-sso.json")
}

func (d *nativeDriver) pendingSSO(req netctl.AttachmentRequest) (ssoLogin, bool) {
	data, err := os.ReadFile(d.ssoPath(req))
	if err != nil {
		return ssoLogin{}, false
	}
	var pending ssoLogin
	if json.Unmarshal(data, &pending) != nil || pending.URL == "" || time.Now().After(pending.ExpiresAt) {
		return ssoLogin{}, false
	}
	return pending, true
}

func (d *nativeDriver) saveSSO(req netctl.AttachmentRequest, pending ssoLogin) error {
	data, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return os.WriteFile(d.ssoPath(req), data, 0o600)
}

func (d *nativeDriver) clearSSO(req netctl.AttachmentRequest) {
	if err := os.Remove(d.ssoPath(req)); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.logger.Warn("remove netbird sso state failed", slog.String("bot_id", req.BotID), slog.Any("error", err))
	}
}

// rejected reports whether the management server refused the credentials,
// as opposed to being unreachable.
func rejected(err error) bool {
	switch status.Code(err) {
	case codes.PermissionDenied, codes.Unauthenticated, codes.InvalidArgument:
		return true
	default:
		return false
	}
}

func (d *nativeDriver) loginErrorPath(req netctl.AttachmentRequest) string {
	return filepath.Join(d.root, req.BotID, "netbird-login-error")
}

// loginError returns why the last login was refused. The daemon itself only
// reports NeedsLogin, which cannot tell a bad setup key from a fresh peer.
func (d *nativeDriver) loginError(req netctl.AttachmentRequest) string {
	data, err := os.ReadFile(d.loginErrorPath(req))
	if err != nil {
		return ""
	}
	return string(data)
}

func (d *nativeDriver) saveLoginError(req netctl.AttachmentRequest, message string) {
	if err := os.WriteFile(d.loginErrorPath(req), []byte(message), 0o600); err != nil {
		d.logger.Warn("save netbird login error failed", slog.String("bot_id", req.BotID), slog.Any("error", err))
	}
}

func (d *nativeDriver) clearLoginError(req netctl.AttachmentRequest) {
	if err := os.Remove(d.loginErrorPath(req)); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.logger.Warn("remove netbird login error failed", slog.String("bot_id", req.BotID), slog.Any("error", err))
	}
}
