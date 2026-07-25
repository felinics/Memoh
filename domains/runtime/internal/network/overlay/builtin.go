package overlay

import (
	"github.com/memohai/memoh/domains/runtime/internal/network/overlay/netbird"
	"github.com/memohai/memoh/domains/runtime/internal/network/overlay/tailscale"
	netctl "github.com/memohai/memoh/domains/runtime/network"
)

func RegisterBuiltinProviders(registry *netctl.Registry, deps ProviderDeps) error {
	if registry == nil {
		return nil
	}
	providers := []netctl.Provider{
		tailscale.NewProvider(tailscale.Deps{
			SidecarRuntime: deps.SidecarRuntime,
			Runtime:        deps.Runtime,
			StateRoot:      deps.StateRoot,
		}),
		netbird.NewProvider(netbird.Deps{
			SidecarRuntime: deps.SidecarRuntime,
			Runtime:        deps.Runtime,
			StateRoot:      deps.StateRoot,
		}),
	}
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			return err
		}
	}
	return nil
}
