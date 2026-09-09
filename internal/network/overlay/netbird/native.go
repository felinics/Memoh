package netbird

import (
	"context"
	"os"
	"path/filepath"

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
	setupKey := configutil.String(cfg, "setup_key")
	userspace := configutil.Bool(cfg, "userspace")
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
	if hostname := configutil.FirstNonEmpty(configutil.String(cfg, "hostname"), req.BotID); hostname != "" {
		env = append(env, "NB_HOSTNAME="+hostname)
	}
	if managementURL := configutil.String(cfg, "management_url"); managementURL != "" {
		env = append(env, "NB_MANAGEMENT_URL="+managementURL)
	}
	if adminURL := configutil.String(cfg, "admin_url"); adminURL != "" {
		env = append(env, "NB_ADMIN_URL="+adminURL)
	}
	if configutil.Bool(cfg, "disable_dns") {
		env = append(env, "NB_DISABLE_DNS=true")
	}
	if extraArgs := configutil.String(cfg, "extra_args"); extraArgs != "" {
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
