package channel

import (
	"go.uber.org/fx"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/local"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/inbound"
	"github.com/felinics/memoh/internal/rpc/serverruntime"
	"github.com/felinics/memoh/internal/webhooktunnel"
)

// Module assembles the shared Channel boundary providers: registry,
// manager, lifecycle, inbound processing, discuss pipeline, and
// webhook tunnel. Turn execution is consumed through the injected
// turn.Service; this module never touches the resolver or agent directly.
func FoundationModule() fx.Option {
	return fx.Options(
		fx.Provide(
			identities.NewService,
			provideRouteService,
			providePipeline,
			provideEventStore,
			provideDiscussDriver,
			local.NewRouteHub,
			provideChannelRegistry,
			channel.NewStore,
		),
	)
}

// ServerLocalModule supplies the local Web channel path. It does not start any
// external channel connections.
func ServerLocalModule() fx.Option {
	return fx.Options(
		fx.Provide(
			provideCommandHandler,
			provideLocalCommandHandler,
			provideLocalQueueCommandHandler,
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
			provideRemoteQueueCommandHandler,
			provideRemoteSkillResolver,
			provideRemoteChannelAudio,
			provideStandaloneChannelSettings,
			provideChannelRouter,
			provideChannelManager,
			provideChannelLifecycleService,
			provideLocalChannelRuntime,
			provideChannelRuntimeInterface,
			webhooktunnel.NewManager,
		),
		fx.Invoke(
			startChannelManager,
			startWebhookTunnelListener,
			startWebhookTunnel,
		),
	)
}

// EmbeddedModule runs the full channel runtime inside the Server process:
// external channel adapters, and webhook tunnel, wired to
// the local command/skill/audio surfaces with no RPC involved. This is the
// pre-split all-in-one deployment shape — bare-metal installs without an
// internal_rpc secret keep their channels working without operating a
// second binary.
func EmbeddedModule() fx.Option {
	return fx.Options(
		fx.Provide(
			provideCommandHandler,
			provideLocalCommandHandler,
			provideLocalQueueCommandHandler,
			provideLocalSkillResolver,
			provideLocalChannelAudio,
			provideLocalChannelSettings,
			provideChannelRouter,
			provideChannelManager,
			provideChannelLifecycleService,
			provideLocalChannelRuntime,
			provideChannelRuntimeInterface,
			webhooktunnel.NewManager,
		),
		fx.Invoke(
			startChannelManager,
			startWebhookTunnelListener,
			startWebhookTunnel,
		),
	)
}

// Module preserves the previous all-in-one assembly for focused tests.
func Module() fx.Option {
	return fx.Options(FoundationModule(), ServerLocalModule())
}

func provideLocalChannelRuntime(lifecycle *channel.Lifecycle, store *channel.Store, manager *channel.Manager) *channel.LocalRuntime {
	return &channel.LocalRuntime{Lifecycle: lifecycle, Store: store, Manager: manager}
}

func provideChannelRuntimeInterface(runtime *channel.LocalRuntime) channel.Runtime { return runtime }

func provideRemoteCommandHandler(client *serverruntime.Client) inbound.CommandHandler { return client }

func provideRemoteQueueCommandHandler(client *serverruntime.Client) inbound.QueueCommandHandler {
	return client
}

func provideRemoteSkillResolver(client *serverruntime.Client) inbound.RequestedSkillResolver {
	return client
}

func provideRemoteChannelAudio(client *serverruntime.Client) channelAudio { return client }
