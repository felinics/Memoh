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
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/fetchproviders"
	"github.com/memohai/memoh/internal/mcp"
	memprovider "github.com/memohai/memoh/internal/memory/adapters"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/oauthclients"
	"github.com/memohai/memoh/internal/policy"
	"github.com/memohai/memoh/internal/providertemplates"
	"github.com/memohai/memoh/internal/schedule"
	"github.com/memohai/memoh/internal/searchproviders"
	"github.com/memohai/memoh/internal/skillpackages"
	"github.com/memohai/memoh/internal/userruntime"
	videopkg "github.com/memohai/memoh/internal/video"
	"github.com/memohai/memoh/internal/workdir"
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
			provideSettingsService,
			provideBotAgentsService,
			provideToolApprovalService,
			providePGVectorStore,
			provideUserRuntimeStore,
			provideBotRemoteRuntimeBindingStore,
			provideBotWorkdirStore,
			provideUserRuntimeHub,
			userruntime.NewService,
			workspace.NewRemoteWorkspaceService,
			provideUserRuntimePipe,
			provideWikiStore,
			provideWorkspaceManager,
			workdir.NewService,
			provideBridgeProvider,
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
			provideSkillPackageService,
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
			provideScheduleSessionCreator,
			schedule.NewService,
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
			injectBotContainerLifecycle,
			configureMemoryProviderRegistry,
			injectScheduleBotAgents,
			startProviderTemplateSync,
			startScheduleService,
			startContainerReconciliation,
			startBackgroundTaskCleanup,
			startAudioTempStoreCleanup,
		),
	)
}

func provideSkillPackageService(queries dbstore.Queries) *skillpackages.Service {
	return skillpackages.NewService(queries)
}

// Module preserves the all-in-one composition API for tests and transitional
// callers. Production commands compose the two modules explicitly.
func Module() fx.Option {
	return fx.Options(FoundationModule(), ServerModule())
}
