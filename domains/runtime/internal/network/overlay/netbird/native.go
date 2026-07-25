package netbird

import (
	"context"
	"os"
	"path/filepath"

	ctr "github.com/memohai/memoh/domains/runtime/container"
	config "github.com/memohai/memoh/domains/runtime/internal/network/overlay/config"
	"github.com/memohai/memoh/domains/runtime/internal/network/overlay/sidecar"
	netctl "github.com/memohai/memoh/domains/runtime/network"
)

type nativeDriver struct {
	config  netctl.BotOverlayConfig
	root    string
	manager sidecar.Manager
}

func newNativeDriver(cfg netctl.BotOverlayConfig, runtime sidecar.Runtime, stateRoot string) *nativeDriver {
	d := &nativeDriver{config: cfg, root: stateRoot}
	d.manager = sidecar.Manager{
		Kind:      "netbird",
		Runtime:   runtime,
		BuildSpec: d.buildSpec,
	}
	return d
}

func (*nativeDriver) Kind() string { return "netbird" }

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
	setupKey := config.String(cfg, "setup_key")
	userspace := config.Bool(cfg, "userspace")
	stateDir := filepath.Join(d.root, req.BotID, "netbird")
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return sidecar.Spec{}, err
	}
	env := []string{
		"NB_LOG_LEVEL=info",
	}
	if setupKey != "" {
		env = append(env, "NB_SETUP_KEY="+setupKey)
	}
	if hostname := config.FirstNonEmpty(config.String(cfg, "hostname"), req.BotID); hostname != "" {
		env = append(env, "NB_HOSTNAME="+hostname)
	}
	if managementURL := config.String(cfg, "management_url"); managementURL != "" {
		env = append(env, "NB_MANAGEMENT_URL="+managementURL)
	}
	if adminURL := config.String(cfg, "admin_url"); adminURL != "" {
		env = append(env, "NB_ADMIN_URL="+adminURL)
	}
	if config.Bool(cfg, "disable_dns") {
		env = append(env, "NB_DISABLE_DNS=true")
	}
	if extraArgs := config.String(cfg, "extra_args"); extraArgs != "" {
		env = append(env, "NB_EXTRA_ARGS="+extraArgs)
	}
	mounts := []ctr.MountSpec{{
		Destination: "/var/lib/netbird",
		Type:        "bind",
		Source:      stateDir,
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
		Image:     "netbirdio/netbird:latest",
		Env:       env,
		Mounts:    mounts,
		AddedCaps: addedCaps,
		Details: map[string]any{
			"userspace": userspace,
			"state_dir": stateDir,
		},
	}, nil
}
