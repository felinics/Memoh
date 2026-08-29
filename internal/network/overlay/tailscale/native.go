package tailscale

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	ctr "github.com/felinics/memoh/internal/container"
	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/configutil"
	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
)

type nativeDriver struct {
	config  netctl.BotOverlayConfig
	root    string
	manager sidecar.Manager
}

func newNativeDriver(cfg netctl.BotOverlayConfig, runtime sidecar.Runtime, stateRoot string) *nativeDriver {
	d := &nativeDriver{config: cfg, root: stateRoot}
	d.manager = sidecar.Manager{
		Kind:         "tailscale",
		Runtime:      runtime,
		BuildSpec:    d.buildSpec,
		EnrichStatus: d.enrichStatus,
	}
	return d
}

func (*nativeDriver) Kind() string { return "tailscale" }

func (d *nativeDriver) EnsureAttached(ctx context.Context, req netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	if !d.config.Enabled {
		return netctl.OverlayStatus{Provider: d.Kind(), State: "disabled"}, nil
	}
	return d.manager.EnsureAttached(ctx, req)
}

func (d *nativeDriver) Detach(ctx context.Context, req netctl.AttachmentRequest) error {
	return d.manager.Detach(ctx, req)
}

func (d *nativeDriver) Status(ctx context.Context, req netctl.AttachmentRequest) (netctl.OverlayStatus, error) {
	if !d.config.Enabled {
		return netctl.OverlayStatus{Provider: d.Kind(), State: "disabled"}, nil
	}
	return d.manager.Status(ctx, req)
}

func (d *nativeDriver) buildSpec(req netctl.AttachmentRequest) (sidecar.Spec, error) {
	cfg := d.config.Config
	authKey := configutil.String(cfg, "auth_key")
	userspace := configutil.Bool(cfg, "userspace")
	stateDir := d.stateDir(req)
	socketDir := d.socketDir(req)
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return sidecar.Spec{}, err
	}
	if err := os.MkdirAll(socketDir, 0o750); err != nil {
		return sidecar.Spec{}, err
	}
	proxyAddress := ""
	env := []string{
		"TS_STATE_DIR=/var/lib/tailscale",
		"TS_SOCKET=/var/run/tailscale/tailscaled.sock",
		"TS_USERSPACE=" + strconv.FormatBool(userspace),
		"TS_ENABLE_HEALTH_CHECK=true",
		"TS_LOCAL_ADDR_PORT=127.0.0.1:9002",
		"TS_AUTH_ONCE=true",
	}
	if authKey != "" {
		env = append(env, "TS_AUTHKEY="+authKey)
	}
	if hostname := configutil.FirstNonEmpty(configutil.String(cfg, "hostname"), req.BotID); hostname != "" {
		env = append(env, "TS_HOSTNAME="+hostname)
	}
	if routes := configutil.String(cfg, "advertise_routes"); routes != "" {
		env = append(env, "TS_ROUTES="+routes)
	}
	if configutil.Bool(cfg, "accept_dns", true) {
		env = append(env, "TS_ACCEPT_DNS=true")
	}
	extraArgs := make([]string, 0, 4)
	if controlURL := configutil.String(cfg, "control_url"); controlURL != "" {
		extraArgs = append(extraArgs, "--login-server="+controlURL)
	}
	extraArgs = append(extraArgs, "--accept-routes="+strconv.FormatBool(configutil.Bool(cfg, "accept_routes")))
	exitNode := configutil.String(req.Overlay.Config, "exit_node")
	if exitNode != "" {
		extraArgs = append(extraArgs, "--exit-node="+exitNode)
	}
	if more := strings.TrimSpace(configutil.String(cfg, "extra_args")); more != "" {
		extraArgs = append(extraArgs, more)
	}
	if len(extraArgs) > 0 {
		env = append(env, "TS_EXTRA_ARGS="+strings.Join(extraArgs, " "))
	}
	tailscaledExtraArgs := make([]string, 0, 4)
	if userspace {
		tailscaledExtraArgs = append(tailscaledExtraArgs, "--tun=userspace-networking")
	}
	if configutil.Bool(cfg, "socks5_enabled") {
		port := configutil.Int(cfg, "socks5_port", 1055)
		env = append(env, "TS_SOCKS5_SERVER=:"+strconv.Itoa(port))
		proxyAddress = "socks5://127.0.0.1:" + strconv.Itoa(port)
	}
	if configutil.Bool(cfg, "http_proxy_enabled") {
		port := configutil.Int(cfg, "http_proxy_port", 1056)
		env = append(env, "TS_OUTBOUND_HTTP_PROXY_LISTEN=:"+strconv.Itoa(port))
		if proxyAddress == "" {
			proxyAddress = "http://127.0.0.1:" + strconv.Itoa(port)
		}
	}
	if len(tailscaledExtraArgs) > 0 {
		env = append(env, "TS_TAILSCALED_EXTRA_ARGS="+strings.Join(tailscaledExtraArgs, " "))
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
		Image:        "tailscale/tailscale:stable",
		Env:          env,
		Mounts:       mounts,
		AddedCaps:    addedCaps,
		ProxyAddress: proxyAddress,
		Details: map[string]any{
			"userspace":                 userspace,
			"state_dir":                 stateDir,
			"localapi_socket_host_path": d.socketPath(req),
			"healthcheck_url":           "http://127.0.0.1:9002/healthz",
			"configured_exit_node":      exitNode,
		},
	}, nil
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
