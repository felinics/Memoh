package tailscale

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	ctr "github.com/felinics/memoh/internal/container"
	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/configutil"
	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
)

// localAPIWait bounds how long attach waits for a freshly started tailscaled
// to open its LocalAPI socket.
const localAPIWait = 15 * time.Second

type nativeDriver struct {
	config  netctl.BotOverlayConfig
	root    string
	logger  *slog.Logger
	manager sidecar.Manager
}

func newNativeDriver(cfg netctl.BotOverlayConfig, runtime sidecar.Runtime, stateRoot string, logger *slog.Logger) *nativeDriver {
	if logger == nil {
		logger = slog.Default()
	}
	d := &nativeDriver{config: cfg, root: stateRoot, logger: logger}
	d.manager = sidecar.Manager{
		Kind:         "tailscale",
		Runtime:      runtime,
		BuildSpec:    d.buildSpec,
		EnrichStatus: d.enrichStatus,
		DataDir:      stateRoot,
	}
	return d
}

func (*nativeDriver) Kind() string { return "tailscale" }

func (d *nativeDriver) EnsureAttached(ctx context.Context, req netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	if !d.config.Enabled {
		return netctl.OverlayStatus{Provider: d.Kind(), State: netctl.OverlayStateDisabled}, nil
	}
	status, err := d.manager.EnsureAttached(ctx, req)
	if err != nil || !status.Attached {
		return status, err
	}
	// A control server that is briefly unreachable must not fail the
	// workspace start; tailscaled keeps retrying and the status shows why.
	if err := d.configure(ctx, req); err != nil {
		d.logger.WarnContext(ctx, "configure tailscale sidecar failed",
			slog.String("bot_id", req.BotID), slog.Any("error", err))
	}
	return d.manager.Status(ctx, req)
}

func (d *nativeDriver) Detach(ctx context.Context, req netctl.AttachmentRequest) error {
	return d.manager.Detach(ctx, req)
}

func (d *nativeDriver) Status(ctx context.Context, req netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	if !d.config.Enabled {
		return netctl.OverlayStatus{Provider: d.Kind(), State: netctl.OverlayStateDisabled}, nil
	}
	return d.manager.Status(ctx, req)
}

// buildSpec describes the sidecar process. Login and preferences go through
// the LocalAPI in configure, so only settings that tailscaled reads at
// startup appear here; changing them recreates the sidecar.
func (d *nativeDriver) buildSpec(req netctl.AttachmentRequest) (sidecar.Spec, error) {
	cfg := d.config.Config
	userspace := configutil.Bool(cfg, "userspace")
	stateDir := d.stateDir(req)
	socketDir := d.socketDir(req)
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return sidecar.Spec{}, err
	}
	if err := os.MkdirAll(socketDir, 0o750); err != nil {
		return sidecar.Spec{}, err
	}
	cmd := []string{
		"tailscaled",
		"--statedir=/var/lib/tailscale",
		"--socket=/var/run/tailscale/tailscaled.sock",
	}
	if userspace {
		cmd = append(cmd, "--tun=userspace-networking")
	}
	proxyAddress := ""
	if configutil.Bool(cfg, "socks5_enabled") {
		port := strconv.Itoa(configutil.Int(cfg, "socks5_port", 1055))
		cmd = append(cmd, "--socks5-server=:"+port)
		proxyAddress = "socks5://127.0.0.1:" + port
	}
	if configutil.Bool(cfg, "http_proxy_enabled") {
		port := strconv.Itoa(configutil.Int(cfg, "http_proxy_port", 1056))
		cmd = append(cmd, "--outbound-http-proxy-listen=:"+port)
		if proxyAddress == "" {
			proxyAddress = "http://127.0.0.1:" + port
		}
	}
	mounts := []ctr.MountSpec{{
		Destination: "/var/lib/tailscale",
		Type:        "bind",
		Source:      stateDir,
		Options:     []string{"rbind", "rw"},
	}, {
		Destination: "/var/run/tailscale",
		Type:        "bind",
		Source:      socketDir,
		Options:     []string{"rbind", "rw"},
	}}
	addedCaps := []string(nil)
	if !userspace {
		mounts = append(mounts, ctr.MountSpec{
			Destination: "/dev/net/tun",
			Type:        "bind",
			Source:      "/dev/net/tun",
			Options:     []string{"rbind", "rw"},
		})
		addedCaps = append(addedCaps, "CAP_NET_ADMIN")
	}
	return sidecar.Spec{
		Image:          "tailscale/tailscale:stable",
		Cmd:            cmd,
		Mounts:         mounts,
		AddedCaps:      addedCaps,
		ResolvConfPath: filepath.Join(d.root, req.BotID, "tailscale-resolv.conf"),
		ProxyAddress:   proxyAddress,
		Details: map[string]any{
			"userspace":                 userspace,
			"state_dir":                 stateDir,
			"localapi_socket_host_path": d.socketPath(req),
			"configured_exit_node":      configutil.String(cfg, "exit_node"),
		},
	}, nil
}

func (d *nativeDriver) client(req netctl.AttachmentRequest) localClient {
	return localClient{socketPath: d.socketPath(req)}
}

func (d *nativeDriver) stateDir(req netctl.AttachmentRequest) string {
	return filepath.Join(d.root, req.BotID, "tailscale")
}

func (d *nativeDriver) socketDir(req netctl.AttachmentRequest) string {
	return filepath.Join(d.stateDir(req), "run")
}

func (d *nativeDriver) socketPath(req netctl.AttachmentRequest) string {
	return filepath.Join(d.socketDir(req), "tailscaled.sock")
}
