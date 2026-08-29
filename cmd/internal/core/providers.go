// Package core assembles the shared command-side domain and agent runtime
// providers used by the Memoh binaries (all-in-one, channel).
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	stdpath "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"golang.org/x/crypto/bcrypt"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/acl"
	acpprofileadapter "github.com/felinics/memoh/internal/agent/adapter/acpprofile"
	acpsessionadapter "github.com/felinics/memoh/internal/agent/adapter/acpsession"
	channelcontactadapter "github.com/felinics/memoh/internal/agent/adapter/channelcontact"
	channelidentityadapter "github.com/felinics/memoh/internal/agent/adapter/channelidentity"
	channelmessagingadapter "github.com/felinics/memoh/internal/agent/adapter/channelmessaging"
	channelthreadadapter "github.com/felinics/memoh/internal/agent/adapter/channelthread"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	agentpayload "github.com/felinics/memoh/internal/agent/event/payload"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/turn"
	audiopkg "github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/boot"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/botbackup"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/connectors"
	ctr "github.com/felinics/memoh/internal/container"
	containerprovider "github.com/felinics/memoh/internal/container/provider"
	"github.com/felinics/memoh/internal/contextview"
	"github.com/felinics/memoh/internal/db"
	pgvectordb "github.com/felinics/memoh/internal/db/pgvector"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	emailpkg "github.com/felinics/memoh/internal/email"
	"github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/handlers"
	hookspkg "github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/mcp"
	mcpfederation "github.com/felinics/memoh/internal/mcp/sources/federation"
	"github.com/felinics/memoh/internal/media"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	membuiltin "github.com/felinics/memoh/internal/memory/adapters/builtin"
	memmem0 "github.com/felinics/memoh/internal/memory/adapters/mem0"
	memopenviking "github.com/felinics/memoh/internal/memory/adapters/openviking"
	"github.com/felinics/memoh/internal/memory/memllm"
	storefs "github.com/felinics/memoh/internal/memory/storefs"
	"github.com/felinics/memoh/internal/memory/wikistore"
	"github.com/felinics/memoh/internal/messaging"
	"github.com/felinics/memoh/internal/models"
	netctl "github.com/felinics/memoh/internal/network"
	netoverlay "github.com/felinics/memoh/internal/network/overlay"
	"github.com/felinics/memoh/internal/policy"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/providertemplates"
	"github.com/felinics/memoh/internal/registry"
	"github.com/felinics/memoh/internal/schedule"
	"github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/storage/providers/containerfs"
	"github.com/felinics/memoh/internal/storage/providers/fallback"
	"github.com/felinics/memoh/internal/storage/providers/localfs"
	"github.com/felinics/memoh/internal/team"
	"github.com/felinics/memoh/internal/userruntime"
	videopkg "github.com/felinics/memoh/internal/video"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func provideLogger(cfg config.Config) *slog.Logger {
	logger.Init(cfg.Log.Level, cfg.Log.Format)
	return logger.L
}

func provideContainerService(lc fx.Lifecycle, log *slog.Logger, cfg config.Config, rc *boot.RuntimeConfig) (ctr.Service, error) {
	svc, cleanup, err := containerprovider.ProvideService(context.Background(), log, cfg, rc.ContainerBackend)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			cleanup()
			return nil
		},
	})
	return svc, nil
}

func provideNetworkController(service ctr.Service, rc *boot.RuntimeConfig, networkService *netctl.Service, registry *netctl.Registry) netctl.Controller {
	runtime := netctl.NewContainerRuntimeFromBackend(rc.ContainerBackend, service)
	ctrl := netctl.NewController(runtime, networkService, registry)
	networkService.SetController(ctrl)
	return ctrl
}

func provideDBConn(lc fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	conn, err := db.Open(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	if conn == nil {
		return nil, nil
	}
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			conn.Close()
			return nil
		},
	})
	return conn, nil
}

func providePGVectorStore(lc fx.Lifecycle, log *slog.Logger, cfg config.Config) (*pgvectordb.Store, error) {
	if !cfg.PGVector.Enabled {
		return nil, nil
	}
	store, err := pgvectordb.Open(context.Background(), log, cfg.PGVector)
	if err != nil {
		log.Warn("pgvector store unavailable; semantic memory index disabled", slog.Any("error", err))
		return nil, nil
	}
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			store.Close()
			return nil
		},
	})
	return store, nil
}

func providePostgresStore(conn *pgxpool.Pool) (*postgresstore.Store, error) {
	if conn == nil {
		return nil, nil
	}
	return postgresstore.New(conn)
}

func provideOverlayProviderRegistry(service ctr.Service, cfg config.Config, rc *boot.RuntimeConfig) *netctl.Registry {
	registry := netctl.NewRegistry()
	runtime := netctl.NewContainerRuntimeFromBackend(rc.ContainerBackend, service)
	if err := netoverlay.RegisterBuiltinProviders(registry, netoverlay.ProviderDeps{
		SidecarRuntime: service,
		Runtime:        runtime.Descriptor(),
		StateRoot:      cfg.Workspace.DataRoot,
	}); err != nil {
		panic(err)
	}
	return registry
}

func provideNetworkService(log *slog.Logger, queries dbstore.Queries, registry *netctl.Registry, service ctr.Service, rc *boot.RuntimeConfig, cfg config.Config) *netctl.Service {
	return netctl.NewService(log, queries, registry, service, rc.ContainerBackend, cfg.Workspace.CNIBinaryDir, cfg.Workspace.CNIConfigDir, cfg.Workspace.DataRoot)
}

func provideDBQueries(postgresStore *postgresstore.Store) (dbstore.Queries, error) {
	if postgresStore == nil {
		return nil, errors.New("postgres store not configured")
	}
	return postgresstore.NewQueriesWithPool(postgresStore.Pool(), postgresStore.SQLC()), nil
}

func provideAccountStore(postgresStore *postgresstore.Store) (dbstore.AccountStore, error) {
	if postgresStore == nil {
		return nil, errors.New("postgres account store not configured")
	}
	return postgresStore, nil
}

func provideUserRuntimeStore(postgresStore *postgresstore.Store) (dbstore.UserRuntimeStore, error) {
	if postgresStore == nil {
		return nil, errors.New("postgres user runtime store not configured")
	}
	return postgresStore, nil
}

func provideBotRemoteRuntimeBindingStore(postgresStore *postgresstore.Store) (dbstore.BotRemoteRuntimeBindingStore, error) {
	if postgresStore == nil {
		return nil, errors.New("postgres bot remote runtime binding store not configured")
	}
	return postgresStore, nil
}

func provideBotWorkdirStore(postgresStore *postgresstore.Store) (dbstore.BotWorkdirStore, error) {
	if postgresStore == nil {
		return nil, errors.New("postgres bot workdir store not configured")
	}
	return postgresStore, nil
}

func provideUserRuntimeHub(lc fx.Lifecycle, log *slog.Logger) *userruntime.Hub {
	hub := userruntime.NewHub(log)
	lc.Append(fx.Hook{OnStop: hub.Shutdown})
	return hub
}

func provideUserRuntimePipe() userruntime.Pipe {
	return userruntime.NewDirectPipe()
}

func provideAccountService(log *slog.Logger, accountStore dbstore.AccountStore) *accounts.Service {
	return accounts.NewService(log, accountStore)
}

func provideSettingsService(
	log *slog.Logger,
	queries dbstore.Queries,
	aclService *acl.Service,
	networkService *netctl.Service,
	modelsService *models.Service,
	botAgentsService *botagents.Service,
) *settings.Service {
	service := settings.NewService(log, queries, aclService, networkService)
	service.SetReasoningOptionsResolver(modelsService)
	service.SetBotAgents(botAgentsService)
	return service
}

func provideBotAgentsService(log *slog.Logger, queries dbstore.Queries) *botagents.Service {
	return botagents.NewService(log, queries)
}

// provideWikiStore wires the PostgreSQL memory wiki store. Returns a pointer
// so FX can inject nil-safe into providers that may run without a wiki store.
func provideWikiStore(postgresStore *postgresstore.Store) (*wikistore.Store, error) {
	if postgresStore == nil {
		return nil, errors.New("postgres wiki store not configured")
	}
	ws := wikistore.Store(wikistore.NewPostgres(postgresStore.SQLC()))
	return &ws, nil
}

func provideBridgeProvider(manage *workspace.Manager) bridge.Provider {
	return manage
}

type nativeWorkspaceBridgeProvider struct {
	manager *workspace.Manager
}

type workspaceTargetPolicyResolver struct {
	manager *workspace.Manager
}

type toolApprovalPolicyProvider struct {
	settings *settings.Service
}

func (p toolApprovalPolicyProvider) ToolApprovalPolicy(ctx context.Context, botID string) (toolapproval.PolicyConfig, error) {
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return toolapproval.PolicyConfig{}, err
	}
	return toolApprovalPolicyConfig(botSettings.ToolApprovalConfig), nil
}

func provideToolApprovalService(log *slog.Logger, queries dbstore.Queries, settingsService *settings.Service) *toolapproval.Service {
	return toolapproval.NewService(log, queries, toolApprovalPolicyProvider{settings: settingsService})
}

func (r workspaceTargetPolicyResolver) ResolveWorkspaceTargetPolicy(ctx context.Context, botID, targetID string) (toolapproval.WorkspaceTargetPolicy, error) {
	resolved, err := r.manager.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil {
		return toolapproval.WorkspaceTargetPolicy{}, err
	}
	return toolapproval.WorkspaceTargetPolicy{
		TargetID: resolved.TargetID,
		Kind:     resolved.Kind,
		Name:     resolved.Name,
		Config:   toolApprovalPolicyConfig(resolved.Approval),
	}, nil
}

func toolApprovalPolicyConfig(config settings.ToolApprovalConfig) toolapproval.PolicyConfig {
	return toolapproval.PolicyConfig{
		Enabled: config.Enabled,
		Read: toolapproval.FilePolicy{
			Mode:             toolapproval.PolicyMode(config.Read.Mode),
			RequireApproval:  config.Read.RequireApproval,
			BypassGlobs:      cloneOptionalStrings(config.Read.BypassGlobs),
			ForceReviewGlobs: cloneOptionalStrings(config.Read.ForceReviewGlobs),
		},
		Write: toolapproval.FilePolicy{
			Mode:             toolapproval.PolicyMode(config.Write.Mode),
			RequireApproval:  config.Write.RequireApproval,
			BypassGlobs:      cloneOptionalStrings(config.Write.BypassGlobs),
			ForceReviewGlobs: cloneOptionalStrings(config.Write.ForceReviewGlobs),
		},
		Exec: toolapproval.ExecPolicy{
			Mode:                toolapproval.PolicyMode(config.Exec.Mode),
			RequireApproval:     config.Exec.RequireApproval,
			BypassCommands:      cloneOptionalStrings(config.Exec.BypassCommands),
			ForceReviewCommands: cloneOptionalStrings(config.Exec.ForceReviewCommands),
		},
	}
}

func cloneOptionalStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func (p nativeWorkspaceBridgeProvider) MCPClient(ctx context.Context, botID string) (*bridge.Client, error) {
	return p.manager.NativeMCPClient(ctx, botID)
}

func provideHooksService(log *slog.Logger, provider bridge.Provider) *hookspkg.Service {
	return hookspkg.NewService(log, provider)
}

func provideWorkspaceManager(log *slog.Logger, service ctr.Service, networkController netctl.Controller, cfg config.Config, conn *pgxpool.Pool, queries dbstore.Queries, remote *workspace.RemoteWorkspaceService) (*workspace.Manager, error) {
	mgr := workspace.NewManager(log, service, networkController, cfg.Workspace, cfg.Containerd.Namespace, conn, queries)
	mgr.SetRemoteWorkspaceService(remote)
	tlsOpts, err := workspace.BridgeTLSRuntimeOptionsFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	if tlsOpts != nil {
		mgr.SetBridgeTLS(tlsOpts)
	}
	return mgr, nil
}

func provideMemoryLLM(modelsService *models.Service, settingsService *settings.Service, queries dbstore.Queries, log *slog.Logger) memprovider.LLM {
	return &lazyLLMClient{
		modelsService:   modelsService,
		settingsService: settingsService,
		queries:         queries,
		timeout:         models.DefaultProviderRequestTimeout,
		logger:          log,
	}
}

func provideMemoryProviderRegistry(log *slog.Logger, llm memprovider.LLM, provider bridge.Provider, queries dbstore.Queries, vectorStore *pgvectordb.Store, wikiStore *wikistore.Store) *memprovider.Registry {
	registry := memprovider.NewRegistry(log)
	fileStore := storefs.New(log, provider)
	registry.RegisterFactory(string(memprovider.ProviderBuiltin), func(ctx context.Context, teamID, _ string, providerConfig map[string]any) (memprovider.Provider, error) {
		var ws wikistore.Store
		if wikiStore != nil {
			ws = *wikiStore
		}
		runtime, err := membuiltin.NewBuiltinRuntimeFromConfigContext(ctx, log, providerConfig, fileStore, queries, vectorStore, ws, memprovider.FixedTeamIDResolver(teamID))
		if err != nil {
			return nil, err
		}
		p := membuiltin.NewBuiltinProvider(log, runtime)
		p.SetLLM(llm)
		p.ApplyProviderConfig(providerConfig)
		return p, nil
	})
	registry.RegisterFactory(string(memprovider.ProviderMem0), func(_ context.Context, _, _ string, providerConfig map[string]any) (memprovider.Provider, error) {
		return memmem0.NewMem0Provider(log, providerConfig, fileStore)
	})
	registry.RegisterFactory(string(memprovider.ProviderOpenViking), func(_ context.Context, _, _ string, providerConfig map[string]any) (memprovider.Provider, error) {
		return memopenviking.NewOpenVikingProvider(log, providerConfig)
	})
	// Default provider for bots without an explicit memory_provider_id. Uses the
	// graph runtime (PG nodes/edges as source of truth) when a wiki store is
	// wired; falls back to the file runtime otherwise (e.g. bootstrap before the
	// DB is ready).
	var defaultRuntime membuiltin.Runtime
	if wikiStore != nil {
		defaultRuntime = membuiltin.NewGraphRuntime(log, *wikiStore, fileStore)
	} else {
		defaultRuntime = membuiltin.NewFileRuntime(fileStore)
	}
	defaultProvider := membuiltin.NewBuiltinProvider(log, defaultRuntime)
	defaultProvider.SetLLM(llm)
	registry.Register("__builtin_default__", defaultProvider)
	return registry
}

func provideSessionService(log *slog.Logger, queries dbstore.Queries, hub *event.Hub) *sessionpkg.Service {
	service := sessionpkg.NewService(log, queries, hub)
	service.SetACPSetupValidator(acpprofileadapter.NewCatalog())
	return service
}

func provideMessageService(log *slog.Logger, queries dbstore.Queries, hub *event.Hub) *message.DBService {
	return message.NewService(log, queries, hub)
}

func provideScheduleTriggerer(service *application.Service) schedule.Triggerer {
	return application.NewScheduleGateway(service)
}

type sessionCreatorAdapter struct {
	svc      *sessionpkg.Service
	workdirs *workdir.Service
}

func (a *sessionCreatorAdapter) CreateSession(ctx context.Context, botID, sessionType string) (string, error) {
	sess, err := a.svc.Create(ctx, sessionpkg.CreateInput{
		BotID: botID,
		Type:  sessionType,
	})
	if err != nil {
		return "", err
	}
	return sess.ID, nil
}

// CreateScheduleSession creates the user-visible session one schedule fire
// runs in. The schedule domain states intent (runtime, agent, workdir); this
// adapter resolves the workdir path and shapes the thread-create input the
// same way the interactive session-create handler does.
func (a *sessionCreatorAdapter) CreateScheduleSession(ctx context.Context, spec schedule.SessionSpec) (string, error) {
	input := sessionpkg.CreateInput{
		BotID:           spec.BotID,
		BotAgentID:      spec.BotAgentID,
		Type:            sessionpkg.TypeSchedule,
		Title:           spec.Title,
		CreatedByUserID: spec.OwnerUserID,
		// Schedule sessions surface in the sidebar so the user can open the
		// produced conversation and continue it; the schedule session mode
		// is preserved for prompt and tool gating.
		Visibility: sessionpkg.VisibilityUser,
	}
	if strings.TrimSpace(spec.ACPAgentID) != "" {
		input.RuntimeType = sessionpkg.RuntimeACPAgent
		// The thread service derives the ACP runtime owner from
		// CreatedByUserID and applies project-path defaults; the workdir
		// override below wins when a workdir is bound.
		input.Metadata = map[string]any{"acp_agent_id": spec.ACPAgentID}
	}
	if strings.TrimSpace(spec.WorkdirID) != "" {
		if a.workdirs == nil {
			return "", errors.New("workdir service not configured")
		}
		wd, err := a.workdirs.RequireActive(ctx, spec.BotID, spec.WorkdirID)
		if err != nil {
			return "", fmt.Errorf("resolve schedule workdir: %w", err)
		}
		input.WorkdirID = wd.ID
		input.WorkdirPath = wd.Path
	}
	sess, err := a.svc.Create(ctx, input)
	if err != nil {
		return "", err
	}
	return sess.ID, nil
}

func provideScheduleSessionCreator(sessionService *sessionpkg.Service, workdirService *workdir.Service) schedule.SessionCreator {
	return &sessionCreatorAdapter{svc: sessionService, workdirs: workdirService}
}

func provideAgent(log *slog.Logger, provider bridge.Provider, hookService *hookspkg.Service, cfg config.Config) *native.Agent {
	return native.New(native.Deps{
		BridgeProvider:     provider,
		HookService:        hookService,
		Logger:             log,
		Limits:             agentLimitsFromConfig(cfg.Agent),
		ContextViewApplier: contextview.ProviderRunConfigApplier(log),
		LoopReselectMode:   agentLoopReselectModeFromConfig(log, cfg.Agent),
	})
}

func agentLimitsFromConfig(cfg config.AgentConfig) native.Limits {
	return native.LimitsFromValues(
		cfg.ToolOutputMaxBytes,
		cfg.ToolOutputMaxLines,
		cfg.SystemFilesMaxBytes,
	)
}

func agentLoopReselectModeFromConfig(log *slog.Logger, cfg config.AgentConfig) native.LoopReselectMode {
	mode, recognized := cfg.EffectiveContextLoopReselectMode()
	if !recognized {
		log.Warn("unrecognized agent.context_loop_reselect value; defaulting to active", slog.String("value", cfg.ContextLoopReselect))
	}
	return native.LoopReselectMode(mode)
}

func injectToolProviders(a *native.Agent, msgService *message.DBService, hookService *hookspkg.Service, agentService *application.Service, providers []agenttools.ToolProvider) {
	a.SetToolProviders(providers)
	for _, p := range providers {
		if cp, ok := p.(*agenttools.ContainerProvider); ok {
			cp.SetHookService(hookService)
		}
		if sp, ok := p.(*agenttools.SpawnProvider); ok {
			adapter := native.NewSpawnAdapter(a)
			// Incremental step persistence and live runtime publishing both key
			// off the admitted run handle AdmitSubagentRun leaves on the run
			// context; when either is unavailable the adapter degrades to the
			// terminal-snapshot behavior on its own.
			adapter.SetStepCommitFactory(agentService.SubagentStepCommit)
			adapter.SetRunObserverFactory(agentService.SubagentRunObserver)
			sp.SetAgent(adapter)
			sp.SetMessageService(msgService)
			sp.SetSystemPromptFunc(native.SpawnSystemPrompt)
			sp.SetHookService(hookService)
			// A spawned turn takes its thread's single run slot like any other,
			// so without admission this provider starts nothing.
			sp.SetSubagentAdmitter(agentService)
		}
	}
}

func injectBotConnectorLifecycle(botService *bots.Service, connectorService *connectors.Service) {
	botService.SetConnectorLifecycle(connectorService)
}

func injectBotContainerLifecycle(botService *bots.Service, manager *workspace.Manager) {
	botService.SetContainerLifecycle(manager)
}

func provideACPRunner(log *slog.Logger, manager *workspace.Manager) *acpclient.Runner {
	return acpclient.NewRunner(log, manager)
}

func provideACPSessionPool(lc fx.Lifecycle, log *slog.Logger, runner *acpclient.Runner, botService *bots.Service, sessionService *sessionpkg.Service, workdirService *workdir.Service, queries dbstore.Queries, toolGateway *mcp.ToolGatewayService, toolContexts *mcp.ToolSessionContextStore, toolApproval *toolapproval.Service, userInput *userinput.Service, containerdHandler *handlers.ContainerdHandler, sessionRuntime *sessionruntime.Manager) *acpagent.SessionPool {
	pool := acpagent.NewSessionPool(log, runner, botService, acpsessionadapter.NewSource(sessionService, workdirService))
	pool.SetSessionRuntime(sessionRuntime)
	pool.SetSessionStateStore(acpsessionadapter.NewStateStore(queries))
	pool.SetToolGateway(toolGateway)
	pool.SetToolSessionContextStore(toolContexts)
	pool.SetToolApprovalService(toolApproval)
	pool.SetUserInputService(userInput)
	containerdHandler.SetACPRuntimeResolver(pool)
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			pool.StartReaper(ctx)
			return nil
		},
		OnStop: func(context.Context) error {
			pool.CloseAll() //nolint:contextcheck // ACP shutdown must close subprocesses even after lifecycle ctx cancellation.
			return nil
		},
	})
	return pool
}

func provideAgentService(log *slog.Logger, a *native.Agent, modelsService *models.Service, queries dbstore.Queries, msgService *message.DBService, settingsService *settings.Service, accountService *accounts.Service, botService *bots.Service, mediaService *media.Service, containerdHandler *handlers.ContainerdHandler, workspaceManager *workspace.Manager, memoryRegistry *memprovider.Registry, channelStore *channel.Store, _ *route.DBService, sessionService *sessionpkg.Service, eventHub *event.Hub, compactionService *compaction.Service, pipeline *timeline.Pipeline, rc *boot.RuntimeConfig, bgManager *background.Manager, toolApproval *toolapproval.Service, userInput *userinput.Service, acpPool *acpagent.SessionPool, hookService *hookspkg.Service, sessionRuntime *sessionruntime.Manager, workdirService *workdir.Service, cfg config.Config) *application.Service {
	service := application.NewService(log, modelsService, queries, msgService, settingsService, accountService, a, rc.TimezoneLocation, 120*time.Second)
	service.SetContextAbsoluteMaxTokens(cfg.Agent.EffectiveContextAbsoluteMaxTokens())
	service.SetBotPermissionChecker(&applicationBotPermissionChecker{bots: botService, accounts: accountService})
	// Every turn entry point goes through admission, so a service without it can
	// start nothing: this is the thread's single-run guarantee, not an add-on.
	service.SetSessionRuntime(sessionRuntime)
	service.SetWorkspaceTargetResolver(workspaceManager)
	service.SetWorkdirResolver(workdirService)
	service.SetHookService(hookService)
	if sessionService != nil {
		sessionService.SetHookService(hookService)
	}
	if compactionService != nil {
		compactionService.SetHookService(hookService)
	}
	if workspaceManager != nil {
		workspaceManager.SetHookService(hookService)
	}
	service.SetMemoryRegistry(memoryRegistry)
	service.SetSkillLoader(&skillLoaderAdapter{handler: containerdHandler})
	service.SetGatewayAssetLoader(&gatewayAssetLoaderAdapter{media: mediaService})
	service.SetPlatformIdentitySource(channelidentityadapter.NewSource(channelStore))
	service.SetSessionService(sessionService)
	service.SetEventPublisher(eventHub)
	service.SetCompactionService(compactionService)
	service.SetPipeline(pipeline)
	service.SetBackgroundManager(bgManager)
	if toolApproval != nil {
		toolApproval.SetHookService(hookService)
		toolApproval.SetWorkspaceTargetPolicyResolver(workspaceTargetPolicyResolver{manager: workspaceManager})
	}
	service.SetToolApprovalService(toolApproval)
	service.SetUserInputService(userInput)
	service.SetACPSessionPool(acpPool)
	if bgManager != nil {
		bgManager.SetEventFunc(func(evt background.TaskEvent) {
			if eventHub == nil {
				return
			}
			// The wire shape lives in internal/agent/event/payload — see its
			// BackgroundTask helper and the tests there that pin the
			// top-level `session_id` placement consumers route on.
			data, err := json.Marshal(agentpayload.BackgroundTask(evt))
			if err != nil {
				return
			}
			eventHub.Publish(event.Event{
				Type:  event.EventTypeBackgroundTask,
				BotID: evt.BotID,
				Data:  data,
			})
		})
	}
	return service
}

func provideContainerdHandler(log *slog.Logger, manager *workspace.Manager, cfg config.Config, rc *boot.RuntimeConfig, botService *bots.Service, accountService *accounts.Service, policyService *policy.Service) *handlers.ContainerdHandler {
	manager.SetSetupDiagnostics(botService)
	h := handlers.NewContainerdHandler(log, manager, cfg.Workspace, rc.ContainerBackend, botService, accountService, policyService)
	return h
}

func provideBotBackupService(log *slog.Logger, conn *pgxpool.Pool, queries dbstore.Queries, botService *bots.Service, settingsService *settings.Service, aclService *acl.Service, channelStore *channel.Store, mcpService *mcp.ConnectionService, scheduleService *schedule.Service, emailService *emailpkg.Service, providerService *providers.Service, modelsService *models.Service, searchProviderService *searchproviders.Service, fetchProviderService *fetchproviders.Service, memoryProviderService *memprovider.Service, manager *workspace.Manager, acpPool *acpagent.SessionPool, workdirStore dbstore.BotWorkdirStore) *botbackup.Service {
	return botbackup.New(botbackup.Params{
		Logger:          log,
		DB:              conn,
		Queries:         queries,
		Bots:            botService,
		Settings:        settingsService,
		ACL:             aclService,
		Channels:        channelStore,
		MCP:             mcpService,
		Schedules:       scheduleService,
		Email:           emailService,
		Providers:       providerService,
		Models:          modelsService,
		SearchProviders: searchProviderService,
		FetchProviders:  fetchProviderService,
		MemoryProviders: memoryProviderService,
		Workspace:       manager,
		ACPRuntimes:     acpPool,
		Workdirs:        workdirStore,
	})
}

func provideFederationGateway(log *slog.Logger, containerdHandler *handlers.ContainerdHandler) *handlers.MCPFederationGateway {
	return handlers.NewMCPFederationGateway(log, containerdHandler)
}

func provideOAuthService(log *slog.Logger, queries dbstore.Queries, cfg config.Config) *mcp.OAuthService {
	addr := strings.TrimSpace(cfg.Server.Addr)
	if addr == "" {
		addr = ":8080"
	}
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	callbackURL := "http://" + host + "/oauth/mcp/callback"
	return mcp.NewOAuthService(log, queries, callbackURL)
}

func provideACPToolSource(log *slog.Logger, toolApproval *toolapproval.Service, userInput *userinput.Service, toolContexts *mcp.ToolSessionContextStore, cfg config.Config) *agenttools.NativeToolSource {
	limits := agentLimitsFromConfig(cfg.Agent)
	return agenttools.NewNativeToolSource(log, nil, agenttools.NativeToolSourceOptions{
		AllowAll:        true,
		Approval:        toolApproval,
		UserInput:       userInput,
		ToolEvents:      toolContexts,
		ToolOutputLimit: limits.ToolOutputLimit(),
	})
}

func injectACPToolProviders(source *agenttools.NativeToolSource, toolProviders []agenttools.ToolProvider) {
	if source != nil {
		source.SetProviders(acpToolProviders(toolProviders))
	}
}

func provideToolGatewayService(log *slog.Logger, fedGateway *handlers.MCPFederationGateway, oauthService *mcp.OAuthService, mcpConnService *mcp.ConnectionService, connectorSource *connectors.Source, containerdHandler *handlers.ContainerdHandler, nativeSource *agenttools.NativeToolSource, toolContexts *mcp.ToolSessionContextStore, cfg config.Config) *mcp.ToolGatewayService {
	fedGateway.SetOAuthService(oauthService)
	fedSource := mcpfederation.NewSource(log, fedGateway, mcpConnService, mcpfederation.WithReservedToolName(agenttools.IsBuiltInToolName))
	limits := agentLimitsFromConfig(cfg.Agent)
	svc := mcp.NewToolGatewayService(log, []mcp.ToolSource{nativeSource, connectorSource, fedSource}, mcp.WithToolOutputLimit(limits.ToolOutputLimit()))
	containerdHandler.SetToolGatewayService(svc)
	containerdHandler.SetToolSessionContextStore(toolContexts)
	return svc
}

func acpToolProviders(providers []agenttools.ToolProvider) []agenttools.ToolProvider {
	filtered := make([]agenttools.ToolProvider, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		if _, ok := provider.(*agenttools.FederationProvider); ok {
			continue
		}
		filtered = append(filtered, provider)
	}
	return filtered
}

func provideBackgroundManager(log *slog.Logger) *background.Manager {
	return background.New(log)
}

func provideToolProviders(log *slog.Logger, channelRuntime channel.Runtime, registry *channel.Registry, routeService *route.DBService, scheduleService *schedule.Service, settingsService *settings.Service, searchProviderService *searchproviders.Service, fetchProviderService *fetchproviders.Service, manager *workspace.Manager, mediaService *media.Service, memoryRegistry *memprovider.Registry, emailService *emailpkg.Service, emailRuntime emailpkg.Runtime, fedGateway *handlers.MCPFederationGateway, mcpConnService *mcp.ConnectionService, connectorSource *connectors.Source, modelsService *models.Service, queries dbstore.Queries, audioService *audiopkg.Service, videoService *videopkg.Service, sessionService *sessionpkg.Service, messageService *message.DBService, bgManager *background.Manager, hookService *hookspkg.Service, workdirService *workdir.Service, acpPool *acpagent.SessionPool) []agenttools.ToolProvider {
	var assetResolver messaging.AssetResolver
	if mediaService != nil {
		assetResolver = &mediaAssetResolverAdapter{media: mediaService}
	}
	channelMessaging := channelmessagingadapter.New(channelRuntime, registry, assetResolver)
	historySessions := channelthreadadapter.NewLister(sessionService, routeService)
	fedSource := mcpfederation.NewSource(log, fedGateway, mcpConnService, mcpfederation.WithReservedToolName(agenttools.IsBuiltInToolName))
	return []agenttools.ToolProvider{
		agenttools.NewAskUserProvider(log),
		agenttools.NewMessageProvider(log, channelMessaging, channelMessaging, channelMessaging, assetResolver),
		agenttools.NewContactsProvider(log, channelcontactadapter.NewSource(routeService)),
		agenttools.NewScheduleProvider(log, scheduleService),
		agenttools.NewWorkdirProvider(log, workdirService),
		agenttools.NewACPAgentsProvider(log, &acpRuntimePoolAdapter{pool: acpPool}, queries),
		agenttools.NewMemoryProvider(log, memoryRegistry, settingsService, historySessions),
		agenttools.NewWebProvider(log, settingsService, searchProviderService),
		agenttools.NewContainerProvider(log, manager, bgManager, config.DefaultDataMount, hookService),
		agenttools.NewBackgroundProvider(log, bgManager),
		agenttools.NewBrowserProvider(log, settingsService, nativeWorkspaceBridgeProvider{manager: manager}, manager, config.DefaultDataMount),
		agenttools.NewEmailProvider(log, emailService, emailRuntime),
		agenttools.NewWebFetchProvider(log, settingsService, fetchProviderService),
		agenttools.NewSpawnProvider(log, settingsService, modelsService, queries, sessionService, bgManager),
		agenttools.NewSkillProvider(log),
		agenttools.NewTTSProvider(log, settingsService, audioService, channelMessaging, channelMessaging),
		agenttools.NewTranscriptionProvider(log, settingsService, audioService, mediaService),
		agenttools.NewImageGenProvider(log, settingsService, modelsService, queries, manager, config.DefaultDataMount),
		agenttools.NewVideoGenProvider(log, settingsService, videoService, bgManager, manager, config.DefaultDataMount),
		agenttools.NewFederationProvider(log, connectorSource),
		agenttools.NewFederationProvider(log, fedSource),
		agenttools.NewHistoryProvider(log, historySessions, messageService, queries),
	}
}

// acpRuntimePoolAdapter maps the ACP session pool onto the tool package's
// local ACPRuntimePool interface. The tool package deliberately does not
// import the acp packages (the acp client test binary imports the tool
// package), so the projection to tool-local DTOs happens here.
type acpRuntimePoolAdapter struct {
	pool *acpagent.SessionPool
}

func (a *acpRuntimePoolAdapter) CreateAgentRuntime(ctx context.Context, botID, agentID, runtimeOwnerAccountID string) (agenttools.ACPRuntimeSummary, error) {
	status, err := a.pool.CreateRuntime(ctx, acpagent.CreateRuntimeInput{
		BotID:                 botID,
		AgentID:               agentID,
		RuntimeOwnerAccountID: runtimeOwnerAccountID,
	})
	if err != nil {
		return agenttools.ACPRuntimeSummary{}, err
	}
	summary := agenttools.ACPRuntimeSummary{
		RuntimeID:      status.RuntimeID,
		DefaultModelID: status.DefaultModelID,
	}
	if status.Models != nil {
		summary.CurrentModelID = status.Models.CurrentModelID
		for _, m := range status.Models.Available {
			summary.Models = append(summary.Models, agenttools.ACPOptionInfo{ID: m.ID, Name: m.Name})
		}
	}
	if status.Reasoning != nil {
		summary.CurrentEffort = status.Reasoning.CurrentEffort
		for _, e := range status.Reasoning.Available {
			summary.Efforts = append(summary.Efforts, agenttools.ACPOptionInfo{ID: e.ID, Name: e.Name})
		}
	}
	return summary, nil
}

func (a *acpRuntimePoolAdapter) CloseAgentRuntime(botID, runtimeID string) error {
	return a.pool.CloseRuntime(botID, runtimeID)
}

func provideMediaService(log *slog.Logger, provider bridge.Provider, cfg config.Config) *media.Service {
	primary := containerfs.New(provider)
	dataRoot := cfg.Workspace.DataRoot
	if dataRoot == "" {
		dataRoot = config.DefaultDataRoot
	}
	secondary := localfs.New(filepath.Join(dataRoot, "media"))
	storageProvider := fallback.New(primary, secondary)
	return media.NewService(log, storageProvider)
}

func provideACPCodexOAuthHandler(providersService *providers.Service, botService *bots.Service, accountService *accounts.Service, workspaceManager *workspace.Manager, acpPool *acpagent.SessionPool) *handlers.ACPCodexOAuthHandler {
	handler := handlers.NewACPCodexOAuthHandler(providersService, botService, accountService, workspaceManager, defaultACPCodexOAuthCallbackURL())
	handler.SetRuntimeResetService(acpPool)
	return handler
}

func provideACPClaudeCodeOAuthHandler(botService *bots.Service, accountService *accounts.Service, workspaceManager *workspace.Manager, acpPool *acpagent.SessionPool) *handlers.ACPClaudeCodeOAuthHandler {
	handler := handlers.NewACPClaudeCodeOAuthHandler(botService, accountService, workspaceManager)
	handler.SetRuntimeResetService(acpPool)
	return handler
}

func provideAudioRegistry() *audiopkg.Registry {
	return audiopkg.NewRegistry()
}

func provideVideoRegistry() *videopkg.Registry {
	return videopkg.NewRegistry()
}

func provideAudioTempStore() (*audiopkg.TempStore, error) {
	return audiopkg.NewTempStore(os.TempDir())
}

func startAudioTempStoreCleanup(lc fx.Lifecycle, store *audiopkg.TempStore) {
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go store.StartCleanup(done)
			return nil
		},
		OnStop: func(_ context.Context) error {
			close(done)
			return nil
		},
	})
}

func startBackgroundTaskCleanup(lc fx.Lifecycle, mgr *background.Manager) {
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go mgr.StartCleanupLoop(done, background.DefaultCleanupInterval, background.DefaultTaskRetention)
			return nil
		},
		OnStop: func(_ context.Context) error {
			close(done)
			return nil
		},
	})
}

// inboundTranscriptionResult moved to the shared Channel module.

func provideProvidersService(log *slog.Logger, queries dbstore.Queries, cfg config.Config) *providers.Service {
	return providers.NewService(log, queries, defaultProviderOAuthCallbackURL(), cfg.Registry.ProvidersPath())
}

func defaultProviderOAuthCallbackURL() string {
	return "http://localhost:1455/auth/callback"
}

func defaultACPCodexOAuthCallbackURL() string {
	return defaultProviderOAuthCallbackURL()
}

func startProviderTemplateSync(
	lc fx.Lifecycle,
	log *slog.Logger,
	cfg config.Config,
	queries dbstore.Queries,
) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			defs, err := registry.Load(log, cfg.Registry.ProvidersPath())
			if err != nil {
				log.Warn("registry: failed to load provider definitions", slog.Any("error", err))
				defs = nil
			}
			templates := registry.ProviderTemplateDefinitions(defs)
			if len(templates) == 0 {
				return nil
			}
			return providertemplates.Sync(ctx, log, queries, templates)
		},
	})
}

func configureMemoryProviderRegistry(mpService *memprovider.Service, registry *memprovider.Registry) {
	mpService.SetRegistry(registry)
	registry.SetConfigLoader(func(ctx context.Context, id string) (string, map[string]any, error) {
		provider, err := mpService.Get(ctx, id)
		if err != nil {
			return "", nil, err
		}
		return provider.Provider, provider.Config, nil
	})
}

func startScheduleService(lc fx.Lifecycle, scheduleService *schedule.Service) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return scheduleService.Bootstrap(ctx)
		},
	})
}

func injectScheduleBotAgents(scheduleService *schedule.Service, botAgentsService *botagents.Service) {
	scheduleService.SetBotAgents(botAgentsService)
}

func startContainerReconciliation(lc fx.Lifecycle, manager *workspace.Manager, _ *handlers.ContainerdHandler, _ *mcp.ToolGatewayService) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go manager.ReconcileContainers(ctx)
			return nil
		},
	})
}

// EnsureAdminUser bootstraps the admin account on first start. Exported
// for the composing commands that host the HTTP server.
func EnsureAdminUser(ctx context.Context, log *slog.Logger, accountStore dbstore.AccountStore, emailService *emailpkg.Service, cfg config.Config) error {
	if accountStore == nil {
		return errors.New("account store not configured")
	}
	count, err := accountStore.CountAccounts(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	username := strings.TrimSpace(cfg.Admin.Username)
	password := strings.TrimSpace(cfg.Admin.Password)
	email := strings.TrimSpace(cfg.Admin.Email)
	if username == "" || password == "" {
		return errors.New("admin username/password required in config.toml")
	}
	if password == "change-your-password-here" {
		log.Warn("admin password uses default placeholder; please update config.toml")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	user, err := accountStore.CreateUser(ctx, dbstore.CreateUserInput{
		IsActive: true,
		Metadata: []byte("{}"),
	})
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	_, err = accountStore.CreateAccount(ctx, dbstore.CreateAccountInput{
		UserID:       user.ID,
		Username:     username,
		Email:        email,
		PasswordHash: string(hashed),
		Role:         "admin",
		DisplayName:  username,
		IsActive:     true,
		DataRoot:     cfg.Workspace.DataRoot,
	})
	if err != nil {
		return err
	}
	if emailService != nil {
		if err := emailService.EnsureDefaultGmailProvider(ctx, user.ID); err != nil {
			return fmt.Errorf("ensure admin gmail provider: %w", err)
		}
	}
	log.Info("Admin user created", slog.String("username", username))
	return nil
}

type lazyLLMClient struct {
	modelsService   *models.Service
	settingsService *settings.Service
	queries         dbstore.Queries
	timeout         time.Duration
	logger          *slog.Logger
}

func (c *lazyLLMClient) Extract(ctx context.Context, req memprovider.ExtractRequest) (memprovider.ExtractResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memprovider.ExtractResponse{}, err
	}
	return client.Extract(ctx, req)
}

func (c *lazyLLMClient) Decide(ctx context.Context, req memprovider.DecideRequest) (memprovider.DecideResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memprovider.DecideResponse{}, err
	}
	return client.Decide(ctx, req)
}

func (c *lazyLLMClient) Compact(ctx context.Context, req memprovider.CompactRequest) (memprovider.CompactResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memprovider.CompactResponse{}, err
	}
	return client.Compact(ctx, req)
}

func (c *lazyLLMClient) resolve(ctx context.Context, botID string) (memprovider.LLM, error) {
	if c.modelsService == nil || c.queries == nil {
		return nil, errors.New("models service not configured")
	}

	chatModelID := ""
	if c.settingsService != nil && strings.TrimSpace(botID) != "" {
		if botSettings, err := c.settingsService.GetBot(ctx, botID); err == nil {
			if id := strings.TrimSpace(botSettings.CompactionModelID); id != "" {
				chatModelID = id
			} else if id := strings.TrimSpace(botSettings.ChatModelID); id != "" {
				chatModelID = id
			}
		}
	}

	memoryModel, memoryProvider, err := models.SelectMemoryModelForBot(ctx, c.modelsService, c.queries, chatModelID)
	if err != nil {
		return nil, err
	}
	return memllm.New(memllm.Config{
		ModelID:               memoryModel.ModelID,
		BaseURL:               strings.TrimRight(providers.ProviderConfigString(memoryProvider, "base_url"), "/"),
		APIKey:                providers.ProviderConfigString(memoryProvider, "api_key"),
		ClientType:            memoryProvider.ClientType,
		ChatCompletionsCompat: providers.ProviderConfigString(memoryProvider, models.ChatCompletionsCompatConfigKey),
		Timeout:               c.timeout,
		PromptCacheTTL:        providers.ProviderConfigString(memoryProvider, "prompt_cache_ttl"),
	}), nil
}

type skillLoaderAdapter struct {
	handler *handlers.ContainerdHandler
}

func (a *skillLoaderAdapter) LoadSkills(ctx context.Context, botID string) ([]application.SkillEntry, error) {
	items, err := a.handler.LoadSkills(ctx, botID)
	if err != nil {
		return nil, err
	}
	entries := make([]application.SkillEntry, len(items))
	for i, item := range items {
		skillPath := ""
		if item.SourcePath != "" {
			skillPath = stdpath.Dir(item.SourcePath)
		}
		entries[i] = application.SkillEntry{
			Name:        item.Name,
			Description: item.Description,
			Content:     item.Content,
			Path:        skillPath,
			Metadata:    item.Metadata,
		}
	}
	return entries, nil
}

type mediaAssetResolverAdapter struct {
	media *media.Service
}

func (a *mediaAssetResolverAdapter) Stat(ctx context.Context, botID, contentHash string) (media.Asset, error) {
	if a == nil || a.media == nil {
		return media.Asset{}, errors.New("media service not configured")
	}
	return a.media.Stat(ctx, botID, contentHash)
}

func (a *mediaAssetResolverAdapter) Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error) {
	if a == nil || a.media == nil {
		return nil, media.Asset{}, errors.New("media service not configured")
	}
	return a.media.Open(ctx, botID, contentHash)
}

func (a *mediaAssetResolverAdapter) Ingest(ctx context.Context, input media.IngestInput) (media.Asset, error) {
	if a == nil || a.media == nil {
		return media.Asset{}, errors.New("media service not configured")
	}
	return a.media.Ingest(ctx, input)
}

func (a *mediaAssetResolverAdapter) GetByStorageKey(ctx context.Context, botID, storageKey string) (messaging.AssetMeta, error) {
	if a == nil || a.media == nil {
		return messaging.AssetMeta{}, errors.New("media service not configured")
	}
	return a.media.GetByStorageKey(ctx, botID, storageKey)
}

func (a *mediaAssetResolverAdapter) AccessPath(ctx context.Context, asset media.Asset) string {
	if a == nil || a.media == nil {
		return ""
	}
	return a.media.AccessPath(ctx, asset)
}

func (a *mediaAssetResolverAdapter) IngestContainerFile(ctx context.Context, botID, containerPath string) (messaging.AssetMeta, error) {
	if a == nil || a.media == nil {
		return messaging.AssetMeta{}, errors.New("media service not configured")
	}
	return a.media.IngestContainerFile(ctx, botID, containerPath)
}

type gatewayAssetLoaderAdapter struct {
	media *media.Service
}

func (a *gatewayAssetLoaderAdapter) OpenForGateway(ctx context.Context, botID, contentHash string) (io.ReadCloser, string, error) {
	if a == nil || a.media == nil {
		return nil, "", errors.New("media service not configured")
	}
	reader, asset, err := a.media.Open(ctx, botID, contentHash)
	if err != nil {
		return nil, "", err
	}
	return reader, strings.TrimSpace(asset.Mime), nil
}

func (a *gatewayAssetLoaderAdapter) AccessPathForGateway(ctx context.Context, botID, contentHash string) (string, error) {
	if a == nil || a.media == nil {
		return "", errors.New("media service not configured")
	}
	asset, err := a.media.Resolve(ctx, botID, contentHash)
	if err != nil {
		return "", err
	}
	accessPath, err := a.media.EnsureAccessPath(ctx, asset)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(accessPath), nil
}

// provideTurnService exposes the application service as Channel's only Agent
// surface. Both chat and discuss turns run directly on the same service.
func provideTurnService(service *application.Service) turn.Service {
	// The self-hosted runtime binds its DB pool to the singleton team, so
	// the service fails closed on any other TeamID (turn.ErrTeamNotServed).
	service.SetAllowedTeam(team.DefaultTeamID)
	return service
}

// applicationBotPermissionChecker duplicates the Channel module's inbound
// permission glue; both adapt bots/accounts onto the same
// HasBotPermission shape.
type applicationBotPermissionChecker struct {
	bots     *bots.Service
	accounts *accounts.Service
}

func (a *applicationBotPermissionChecker) HasBotPermission(ctx context.Context, botID, accountID, permission string) (bool, error) {
	if a == nil || a.bots == nil || a.accounts == nil {
		return false, errors.New("bot permission services not configured")
	}
	isAdmin, err := a.accounts.IsAdmin(ctx, accountID)
	if err != nil {
		return false, err
	}
	perms, err := a.bots.ResolveUserPermissions(ctx, botID, accountID, isAdmin)
	if err != nil {
		return false, err
	}
	return bots.HasPermission(perms, permission), nil
}
