package core

import (
	"go.uber.org/fx"

	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/event"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	pluginspkg "github.com/memohai/memoh/domains/agent/extension/plugins"
	"github.com/memohai/memoh/domains/agent/mcp"
	botaccess "github.com/memohai/memoh/domains/api/bot/access"
	"github.com/memohai/memoh/domains/api/bot/access/policy"
	identitylink "github.com/memohai/memoh/domains/api/identity/link"
	"github.com/memohai/memoh/internal/oauth"
)

const (
	foundationModuleName = "core-foundation"
	serverModuleName     = "core-server"
)

// FoundationModule assembles process-neutral domain infrastructure shared by
// Server and Channel. It intentionally excludes Agent, workspace runtimes,
// schedulers, and provider bootstrap loops.
func FoundationModule() fx.Option {
	return fx.Module(
		foundationModuleName,
		fx.Provide(
			provideLogger,
			provideDBConn,
			provideAccountCounter,
			provideAccountTitleModelValidator,
			provideBotPersistenceStore,
			provideRuntimeContainerStore,
			provideBotUserReader,
			provideBotContainerReader,
			provideBotService,
			providePolicyBotReader,
			provideApplicationBotOwnerResolver,
			provideAccountService,
			provideACLStore,
			provideACLChannelIdentityReader,
			provideACLService,
			provideIdentityLinkStore,
			provideIdentityLinkIdentityReader,
			botaccess.NewService,
			identitylink.NewService,
			provideApplicationChannelIdentityReader,
			provideDecisionCluster,
			provideUserInputPersistence,
			userinput.NewService,
			policy.NewService,
			oauth.NewRegistry,
			event.NewHub,
			provideChatMessageStore,
			provideChatThreadStore,
			provideSessionService,
			provideMessageService,
		),
	)
}

// ServerModule assembles the Server-owned Agent and workspace runtime. It
// expects FoundationModule and the Channel catalog/runtime interfaces to be
// provided by the composing command.
func ServerModule() fx.Option {
	return fx.Module(
		serverModuleName,
		fx.Provide(
			provideTokenConfig,
			provideListenAddr,
			provideContainerBackend,
			provideRuntimeClock,
			provideContainerService,
			provideDisplayService,
			provideRuntimeSettingsStore,
			provideNetwork,
			provideSettingsModelReader,
			provideSettingsService,
			provideToolApprovalPersistence,
			provideToolApprovalService,
			provideUserRuntime,
			provideWorkspaceBotProfiles,
			provideWorkspaceBotOwners,
			provideWorkspaceRuntimeSettings,
			provideWorkspace,
			provideBridgeProvider,
			providePluginBridgeProvider,
			provideTemplateService,
			provideProvidersService,
			provideModelProviderResolver,
			provideModelsService,
			provideModelExecutionResolver,
			provideMemoryLLM,
			provideMemoryCatalogService,
			provideMemoryProviderRegistry,
			provideACPRunner,
			provideACPSessionPool,
			provideACPCodexOAuthHandler,
			provideACPClaudeCodeOAuthHandler,
			provideHooksService,
			provideFetchProviderService,
			provideSearchProviderService,
			provideMCPConnectionStore,
			provideMCPOAuthStore,
			mcp.NewConnectionService,
			providePluginStore,
			pluginspkg.NewService,
			mcp.NewToolSessionContextStore,
			provideAudioService,
			provideVideoService,
			provideAudioTempStore,
			provideMediaService,
			provideObservedRouteReader,
			provideACLObservedConversationReader,
			provideAgent,
			provideApplicationReads,
			provideCompactionStore,
			provideCompactionPersistence,
			provideCompactionArtifacts,
			provideAgentService,
			provideTurnService,
			provideScheduleStore,
			provideScheduleTriggerer,
			provideHeartbeatStore,
			provideHeartbeatSessionCreator,
			provideScheduleSessionCreator,
			provideScheduleService,
			provideHeartbeatTriggerer,
			provideHeartbeatService,
			compaction.NewService,
			provideContainerdHandler,
			provideChatBackupStore,
			provideChannelBackupStore,
			provideBotBackupService,
			provideFederationGateway,
			provideACPToolSource,
			provideToolGatewayService,
			provideBackgroundManager,
			provideHistorySearcher,
			provideToolProviders,
			provideOAuthService,
		),
		fx.Invoke(
			injectToolProviders,
			injectACPToolProviders,
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
