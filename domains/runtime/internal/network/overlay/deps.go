package overlay

import (
	"github.com/memohai/memoh/domains/runtime/internal/network/overlay/sidecar"
	netctl "github.com/memohai/memoh/domains/runtime/network"
)

type ProviderDeps struct {
	SidecarRuntime sidecar.Runtime
	Runtime        netctl.RuntimeDescriptor
	StateRoot      string
}
