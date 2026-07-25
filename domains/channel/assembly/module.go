package assembly

import (
	"go.uber.org/fx"

	agentassembly "github.com/memohai/memoh/domains/agent/assembly"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/inbound"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/internal/rpc/runtime/server"
)

// Module assembles the shared Channel boundary providers: registry,
// manager, lifecycle, inbound processing, discuss pipeline, email, and
// webhook tunnel. Turn execution is consumed through the injected
// turn.Service; this module never touches the resolver or agent directly.
func FoundationModule() fx.Option {
	return fx.Options(
		fx.Provide(
			provideChannelIdentityBindingStore,
			agentassembly.NewRouteSessionCoordinator,
			provideChannelPostgresStore,
			provideChannelPersistence,
			provideChannelIdentityStore,
			identity.NewService,
			provideEmailOAuthTokenStore,
			provideEmailRegistry,
			provideEmailService,
			provideEmailOutboxService,
			provideRouteService,
			providePipeline,
			provideTimelineStore,
			provideEventStore,
			provideDiscussDriver,
			local.NewRouteHub,
			gateway.NewStore,
		),
	)
}

// ServerLocalModule supplies the local Web channel path. It does not start any
// external channel or email connections.
func ServerLocalModule() fx.Option {
	return fx.Options(
		fx.Provide(
			provideLocalChannelRegistry,
			provideUsageService,
			provideUsageReader,
			provideCommandQueries,
			provideCommandHandler,
			provideLocalCommandHandler,
			provideLocalSkillResolver,
			provideLocalChannelAudio,
			provideLocalChannelSettings,
			provideChannelRouter,
			provideChannelManager,
		),
	)
}

// RuntimeModule supplies the standalone Channel process. Agent-facing command,
// skill, audio, and turn work arrive through the Server RPC client.
func RuntimeModule() fx.Option {
	return fx.Options(
		fx.Provide(
			provideLocalMediaService,
			provideRemoteCommandHandler,
			provideRemoteSkillResolver,
			provideRemoteChannelAudio,
			provideStandaloneChannelSettings,
			provideEmailChatGateway,
			provideEmailTrigger,
			emailpkg.NewManager,
			provideChannelRouter,
			provideChannelManager,
			provideChannelLifecycleService,
			provideLocalChannelRuntime,
			provideChannelRuntimeInterface,
			provideEmailRuntimeInterface,
			webhook.NewManager,
		),
		fx.Invoke(
			startChannelManager,
			startEmailManager,
			startWebhookTunnelListener,
			startWebhookTunnel,
		),
	)
}

// EmbeddedModule runs the full channel runtime inside the Server process:
// external channel adapters, email manager, and webhook tunnel, wired to
// the local command/skill/audio surfaces with no RPC involved. This is the
// pre-split all-in-one deployment shape — bare-metal installs without an
// internal_rpc secret keep their channels working without operating a
// second binary.
func EmbeddedModule() fx.Option {
	return fx.Options(
		fx.Provide(
			provideUsageService,
			provideUsageReader,
			provideCommandQueries,
			provideCommandHandler,
			provideLocalCommandHandler,
			provideLocalSkillResolver,
			provideLocalChannelAudio,
			provideLocalChannelSettings,
			provideEmailChatGateway,
			provideEmailTrigger,
			emailpkg.NewManager,
			provideChannelRouter,
			provideChannelManager,
			provideChannelLifecycleService,
			provideLocalChannelRuntime,
			provideChannelRuntimeInterface,
			provideEmailRuntimeInterface,
			webhook.NewManager,
		),
		fx.Invoke(
			startChannelManager,
			startEmailManager,
			startWebhookTunnelListener,
			startWebhookTunnel,
		),
	)
}

// Module preserves the previous all-in-one assembly for focused tests.
func Module() fx.Option {
	return fx.Options(FoundationModule(), ServerLocalModule())
}

func provideLocalChannelRuntime(lifecycle *gateway.Lifecycle, store *gateway.Store, manager *gateway.Manager) *gateway.LocalRuntime {
	return &gateway.LocalRuntime{Lifecycle: lifecycle, Store: store, Manager: manager}
}

func provideChannelRuntimeInterface(runtime *gateway.LocalRuntime) gateway.Runtime { return runtime }

func provideEmailRuntimeInterface(manager *emailpkg.Manager) emailpkg.Runtime { return manager }

func provideRemoteCommandHandler(client *server.Client) inbound.CommandHandler { return client }

func provideRemoteSkillResolver(client *server.Client) inbound.RequestedSkillResolver {
	return client
}

func provideRemoteChannelAudio(client *server.Client) channelAudio { return client }
