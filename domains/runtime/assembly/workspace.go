package assembly

import (
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	userruntime "github.com/memohai/memoh/domains/runtime/client"
	"github.com/memohai/memoh/domains/runtime/container"
	runtimesqlc "github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	internalworkspace "github.com/memohai/memoh/domains/runtime/internal/workspace"
	workspacepostgres "github.com/memohai/memoh/domains/runtime/internal/workspace/postgres"
	workspacetarget "github.com/memohai/memoh/domains/runtime/internal/workspace/target"
	"github.com/memohai/memoh/domains/runtime/network"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/config"
)

// WorkspaceDeps are the explicit public inputs required to assemble the
// workspace Service and remote-mount Service.
type WorkspaceDeps struct {
	Log             *slog.Logger
	Container       container.Service
	Network         network.Controller
	Config          config.WorkspaceConfig
	Namespace       string
	Profiles        runtimeworkspace.BotProfileStore
	BotOwners       runtimeworkspace.BotOwnerReader
	RuntimeSettings runtimeworkspace.BotRuntimeSettingsReader
	Pool            *pgxpool.Pool
	UserRuntime     *userruntime.Service
	AppConfig       *config.Config // optional; enables Bridge TLS material when set
	Persistence     *runtimeworkspace.Persistence
	RemoteMounts    runtimeworkspace.RemoteMountStore

	// AllowNilContainer permits constructing a Service without a container
	// backend. Production composition must leave this false.
	AllowNilContainer bool
}

// Workspace is the assembled workspace manager + remote mount surface.
type Workspace struct {
	Service runtimeworkspace.Service
	Remote  runtimeworkspace.RemoteService
}

// NewContainerStore constructs Runtime-owned container persistence.
func NewContainerStore(pool *pgxpool.Pool) runtimeworkspace.ContainerStore {
	return workspacepostgres.NewStore(runtimesqlc.New(pool), pool)
}

// NewWorkspace constructs the public workspace Service backed by the private
// Manager and postgres adapters. The returned cleanup is reserved for future
// shutdown hooks and is safe to call today.
func NewWorkspace(deps WorkspaceDeps) (*Workspace, func(), error) {
	if deps.Container == nil && !deps.AllowNilContainer {
		return nil, nil, errors.New("container service is required")
	}
	if deps.Pool == nil {
		return nil, nil, errors.New("postgres pool is required")
	}
	if deps.Profiles == nil {
		return nil, nil, errors.New("bot profile store is required")
	}
	if deps.BotOwners == nil {
		return nil, nil, errors.New("bot owner reader is required")
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}

	customPersistence := deps.Persistence != nil
	persistence := deps.Persistence
	if persistence == nil {
		store := workspacepostgres.NewStore(runtimesqlc.New(deps.Pool), deps.Pool)
		persistence = &runtimeworkspace.Persistence{
			Profiles:       deps.Profiles,
			Settings:       deps.RuntimeSettings,
			Containers:     store,
			ResourceLimits: store,
			Versions:       store,
		}
	} else {
		cloned := *persistence
		persistence = &cloned
		if persistence.Profiles == nil {
			persistence.Profiles = deps.Profiles
		}
		if persistence.Settings == nil {
			persistence.Settings = deps.RuntimeSettings
		}
	}
	var remote runtimeworkspace.RemoteService
	if !customPersistence || deps.RemoteMounts != nil {
		remoteMounts := deps.RemoteMounts
		if remoteMounts == nil {
			remoteMounts = workspacepostgres.NewRemoteMountStore(deps.Pool)
		}
		remote = workspacetarget.NewRemoteWorkspaceService(
			remoteMounts,
			deps.UserRuntime,
			deps.BotOwners,
		)
	}

	mgr := internalworkspace.NewManager(
		log,
		deps.Container,
		deps.Network,
		deps.Config,
		deps.Namespace,
		persistence,
	)
	if remote != nil {
		mgr.SetRemoteWorkspaceService(remote)
	}

	if deps.AppConfig != nil {
		tlsOpts, err := internalworkspace.BridgeTLSRuntimeOptionsFromConfig(*deps.AppConfig)
		if err != nil {
			return nil, nil, err
		}
		if tlsOpts != nil {
			mgr.SetBridgeTLS(tlsOpts)
		}
	}

	return &Workspace{Service: mgr, Remote: remote}, func() {}, nil
}
