package network

import "context"

// Runtime is the behavioral surface exposed by a container/runtime adapter to
// the network controller.
type Runtime interface {
	Kind() string
	Descriptor() RuntimeDescriptor
	EnsureNetwork(ctx context.Context, req RuntimeNetworkRequest) (RuntimeNetworkStatus, error)
	RemoveNetwork(ctx context.Context, req RuntimeNetworkRequest) error
	StatusNetwork(ctx context.Context, req RuntimeNetworkRequest) (RuntimeNetworkStatus, error)
}

// RuntimeDescriptor is the read-only metadata exported by a runtime adapter.
type RuntimeDescriptor struct {
	Kind         string              `json:"kind"`
	DisplayName  string              `json:"display_name"`
	Capabilities RuntimeCapabilities `json:"capabilities"`
}

// RuntimeCapabilities describes how a runtime can host or join networking.
type RuntimeCapabilities struct {
	SidecarWorker        bool `json:"sidecar_worker"`
	RuntimeNetworkSetup  bool `json:"runtime_network_setup"`
	JoinContainerNetwork bool `json:"join_container_network"`
	JoinNamespacePath    bool `json:"join_namespace_path"`
	CNI                  bool `json:"cni"`
	Devices              bool `json:"devices"`
	Capabilities         bool `json:"capabilities"`
	Privileged           bool `json:"privileged"`
}
