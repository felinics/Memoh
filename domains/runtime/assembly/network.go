package assembly

import (
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/runtime/container"
	netoverlay "github.com/memohai/memoh/domains/runtime/internal/network/overlay"
	networkpostgres "github.com/memohai/memoh/domains/runtime/internal/postgres/network"
	runtimenetwork "github.com/memohai/memoh/domains/runtime/network"
)

// NetworkDeps are the explicit public inputs required to assemble the network
// Service and Controller.
type NetworkDeps struct {
	Log          *slog.Logger
	Container    container.Service
	Backend      string
	ConfigReader runtimenetwork.ConfigReader
	Pool         *pgxpool.Pool
	CNIBinaryDir string
	CNIConfigDir string
	DataRoot     string
}

// Network is the assembled overlay + runtime network surface.
type Network struct {
	Service    *runtimenetwork.Service
	Controller runtimenetwork.Controller
}

// NewNetwork constructs the public network Service and Controller in one step,
// registers builtin overlay providers, and wires Service as BindingResolver.
func NewNetwork(deps NetworkDeps) (*Network, error) {
	if deps.Container == nil {
		return nil, errors.New("container service is required")
	}
	if deps.ConfigReader == nil {
		return nil, errors.New("config reader is required")
	}
	if deps.Pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}

	registry := runtimenetwork.NewRegistry()
	overlayRuntime := runtimenetwork.NewContainerRuntimeFromBackend(deps.Backend, deps.Container)
	if err := netoverlay.RegisterBuiltinProviders(registry, netoverlay.ProviderDeps{
		SidecarRuntime: deps.Container,
		Runtime:        overlayRuntime.Descriptor(),
		StateRoot:      deps.DataRoot,
	}); err != nil {
		return nil, err
	}

	workspaces := networkpostgres.NewStore(deps.Pool)
	svc, ctrl := runtimenetwork.New(
		log,
		runtimenetwork.Persistence{
			Config:     deps.ConfigReader,
			Workspaces: workspaces,
		},
		registry,
		overlayRuntime,
		deps.Container,
		deps.Backend,
		deps.CNIBinaryDir,
		deps.CNIConfigDir,
		deps.DataRoot,
	)
	return &Network{Service: svc, Controller: ctrl}, nil
}
