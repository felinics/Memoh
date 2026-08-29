package overlay

import (
	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
)

type ProviderDeps struct {
	SidecarRuntime sidecar.Runtime
	Runtime        netctl.RuntimeDescriptor
	StateRoot      string
}
