package core

import (
	"go.uber.org/fx"

	"github.com/memohai/memoh/internal/acl"
	"github.com/memohai/memoh/internal/agent/context/compaction"
	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	audiopkg "github.com/memohai/memoh/internal/audio"
	"github.com/memohai/memoh/internal/boot"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/channelaccess"
	"github.com/memohai/memoh/internal/chat/event"
	"github.com/memohai/memoh/internal/connectors"
	"github.com/memohai/memoh/internal/fetchproviders"
	"github.com/memohai/memoh/internal/heartbeat"
	"github.com/memohai/memoh/internal/mcp"
	memprovider "github.com/memohai/memoh/internal/memory/adapters"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/oauthclients"
	pluginspkg "github.com/memohai/memoh/internal/plugins"
	"github.com/memohai/memoh/internal/policy"
	"github.com/memohai/memoh/internal/providertemplates"
	"github.com/memohai/memoh/internal/schedule"
	"github.com/memohai/memoh/internal/searchproviders"
	"github.com/memohai/memoh/internal/settings"
	"github.com/memohai/memoh/internal/userruntime"
	videopkg "github.com/memohai/memoh/internal/video"
	"github.com/memohai/memoh/internal/workspace"
)

// FoundationModule assembles process-neutral domain infrastructure shared by
// Server and Channel. It intentionally excludes Agent, workspace runtimes,
// schedulers, and provider bootstrap loops.
func FoundationModule() fx.Option {
	return fx.Options(
		fx.Provide(
			provideLogger,
			provideDBConn,
			providePostgresStore,
			provideDBQueries,
			provideAccountStore,
			bots.NewService,
			provideAccountService,
			acl.NewService,
			channelaccess.NewService,
			userinput.NewService,
			policy.NewService,
			oauthclients.NewRegistry,
			event.NewHub,
			provideSessionService,
			provideMessageService,
		),
	)
}

// ServerModule assembles the Server-owned Agent and workspace runtime. It
// expects FoundationModule and the Channel catalog/runtime interfaces to be
// provided by the composing command.
func ServerModule() fx.Option {
	return fx.Options(
		fx.Provide(
			boot.ProvideRuntimeConfig,
			provideContainerService,
			provideOverlayProviderRegistry,
			provideNetworkService,
			provideNetworkController,
			settings.NewService,
			provideToolApprovalService,
			providePGVectorStore,
			provideUserRuntimeStore,
			provideBotRemoteRuntimeBindingStore,
			provideUserRuntimeHub,
			userruntime.NewService,
			workspace.NewRemoteWorkspaceService,
			provideUserRuntimePipe,
			provideWikiStore,
			provideWorkspaceManager,
			provideBridgeProvider,
			providePluginBridgeProvider,
			provideMemoryLLM,
			memprovider.NewService,
			provideMemoryProviderRegistry,
			models.NewService,
			provideACPRunner,
			provideACPSessionPool,
			provideACPCodexOAuthHandler,
			provideACPClaudeCodeOAuthHandler,
			provideHooksService,
			provideProvidersService,
			providertemplates.NewService,
			fetchproviders.NewService,
			searchproviders.NewService,
			mcp.NewConnectionService,
			connectors.NewService,
			connectors.NewSource,
			pluginspkg.NewService,
			mcp.NewToolSessionContextStore,
			provideAudioRegistry,
			audiopkg.NewService,
			provideVideoRegistry,
			videopkg.NewService,
			provideAudioTempStore,
			provideMediaService,
			provideSessionRunLedger,
			provideRuntimeFenceActivator,
			provideSessionRuntimeManager,
			provideAgent,
			provideAgentService,
			provideTurnService,
			provideScheduleTriggerer,
			provideHeartbeatSessionCreator,
			provideScheduleSessionCreator,
			schedule.NewService,
			provideHeartbeatTriggerer,
			heartbeat.NewService,
			compaction.NewService,
			provideContainerdHandler,
			provideBotBackupService,
			provideFederationGateway,
			provideACPToolSource,
			provideToolGatewayService,
			provideBackgroundManager,
			provideToolProviders,
			provideOAuthService,
		),
		fx.Invoke(
			injectToolProviders,
			injectACPToolProviders,
			injectBotConnectorLifecycle,
			configureMemoryProviderRegistry,
			startProviderTemplateSync,
			startScheduleService,
			startHeartbeatService,
			startContainerReconciliation,
			startBackgroundTaskCleanup,
			startAudioTempStoreCleanup,
		),
	)
}

// Module preserves the all-in-one composition API for tests and transitional
// callers. Production commands compose the two modules explicitly.
func Module() fx.Option {
	return fx.Options(FoundationModule(), ServerModule())
}
