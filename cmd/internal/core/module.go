package core

import (
	"go.uber.org/fx"

	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	audiopkg "github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/boot"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channelaccess"
	"github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/connectors"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/mcp"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthclients"
	"github.com/felinics/memoh/internal/policy"
	"github.com/felinics/memoh/internal/providertemplates"
	"github.com/felinics/memoh/internal/schedule"
	"github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/skillpackages"
	"github.com/felinics/memoh/internal/userruntime"
	videopkg "github.com/felinics/memoh/internal/video"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
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
