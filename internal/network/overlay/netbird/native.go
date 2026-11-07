package netbird

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

// daemonWait bounds how long attach waits for a freshly started daemon to
// open its socket.
const daemonWait = 15 * time.Second

// netstackSocksPort is the SOCKS5 port NetBird's userspace (netstack) mode
// listens on by default.
const netstackSocksPort = 1080

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
		Kind:         "netbird",
		Runtime:      runtime,
		BuildSpec:    d.buildSpec,
		EnrichStatus: d.enrichStatus,
		DataDir:      stateRoot,
	}
	return d
}

func (*nativeDriver) Kind() string { return "netbird" }

func (d *nativeDriver) EnsureAttached(ctx context.Context, req netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	if !d.config.Enabled {
		return netctl.OverlayStatus{Provider: d.Kind(), State: netctl.OverlayStateDisabled}, nil
	}
	status, err := d.manager.EnsureAttached(ctx, req)
	if err != nil || !status.Attached {
		return status, err
	}
	// An unreachable management server must not fail the workspace start;
	// the daemon keeps retrying and the status shows why.
	if err := d.configure(ctx, req); err != nil {
		d.logger.WarnContext(ctx, "configure netbird sidecar failed",
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

// buildSpec describes the sidecar process. Login and settings go through the
// daemon API in configure; only the networking mode is fixed at startup.
func (d *nativeDriver) buildSpec(req netctl.AttachmentRequest) (sidecar.Spec, error) {
	userspace := configutil.Bool(d.config.Config, "userspace")
	stateDir := d.stateDir(req)
	socketDir := d.socketDir(req)
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return sidecar.Spec{}, err
	}
	if err := os.MkdirAll(socketDir, 0o750); err != nil {
		return sidecar.Spec{}, err
	}
	env := []string{
		// The image sets this too; the sidecar needs the socket in a
		// directory the host can reach.
		"NB_DAEMON_ADDR=unix:///var/run/netbird/netbird.sock",
	}
	mounts := []ctr.MountSpec{{
		Destination: "/var/lib/netbird",
		Type:        "bind",
		Source:      stateDir,
		Options:     []string{"rbind", "rw"},
	}, {
		Destination: "/var/run/netbird",
		Type:        "bind",
		Source:      socketDir,
		Options:     []string{"rbind", "rw"},
	}}
	addedCaps := []string(nil)
	proxyAddress := ""
	if userspace {
		env = append(env, "NB_USE_NETSTACK_MODE=true")
		proxyAddress = "socks5://127.0.0.1:" + strconv.Itoa(netstackSocksPort)
	} else {
		mounts = append(mounts, ctr.MountSpec{
			Destination: "/dev/net/tun",
			Type:        "bind",
			Source:      "/dev/net/tun",
			Options:     []string{"rbind", "rw"},
		})
		addedCaps = append(addedCaps, "CAP_NET_ADMIN")
	}
	return sidecar.Spec{
		Image:          "netbirdio/netbird:latest",
		Cmd:            []string{"netbird", "service", "run"},
		Env:            env,
		Mounts:         mounts,
		AddedCaps:      addedCaps,
		ResolvConfPath: filepath.Join(d.root, req.BotID, "netbird-resolv.conf"),
		ProxyAddress:   proxyAddress,
		Details: map[string]any{
			"userspace":               userspace,
			"state_dir":               stateDir,
			"daemon_socket_host_path": d.socketPath(req),
		},
	}, nil
}

func (d *nativeDriver) client(req netctl.AttachmentRequest) daemonClient {
	return daemonClient{socketPath: d.socketPath(req)}
}

func (d *nativeDriver) stateDir(req netctl.AttachmentRequest) string {
	return filepath.Join(d.root, req.BotID, "netbird")
}

func (d *nativeDriver) socketDir(req netctl.AttachmentRequest) string {
	return filepath.Join(d.stateDir(req), "run")
}

func (d *nativeDriver) socketPath(req netctl.AttachmentRequest) string {
	return filepath.Join(d.socketDir(req), "netbird.sock")
}
