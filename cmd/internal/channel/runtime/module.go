// Package runtime owns external Channel process composition. Importing the
// parent channel package does not compile this runtime profile.
package runtime

import (
	"go.uber.org/fx"

	channelprocess "github.com/memohai/memoh/cmd/internal/channel"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	webhooktunnel "github.com/memohai/memoh/domains/channel/webhook/tunnel"
)

const (
	runtimeModuleName  = "channel-runtime"
	embeddedModuleName = "channel-embedded"
)

// Module supplies the standalone Channel process. Agent-facing work arrives
// through the Server RPC client.
func Module() fx.Option {
	return fx.Module(
		runtimeModuleName,
		fx.Provide(
			provideRemoteCommandHandler,
			provideRemoteSkillResolver,
			provideRemoteChannelAudio,
			provideStandaloneChannelSettings,
			provideLocalChannelRuntime,
			fx.Private,
		),
		fx.Provide(
			provideLocalMediaService,
			provideEmailTrigger,
			emailpkg.NewManager,
			provideChannelLifecycleService,
			provideChannelRuntimeInterface,
			provideEmailRuntimeInterface,
			webhooktunnel.NewManager,
		),
		channelprocess.GatewayModule(),
		fx.Invoke(registerEmailAdapters),
	)
}

// EmbeddedModule adds long-running external Channel runtime ownership to an
// embedded Server. The parent profile supplies ServerLocalModule and the
// platform registry.
func EmbeddedModule() fx.Option {
	return fx.Module(
		embeddedModuleName,
		fx.Provide(
			provideLocalChannelRuntime,
			fx.Private,
		),
		fx.Provide(
			provideEmailTrigger,
			emailpkg.NewManager,
			provideChannelLifecycleService,
			provideChannelRuntimeInterface,
			provideEmailRuntimeInterface,
			webhooktunnel.NewManager,
		),
		fx.Invoke(registerEmailAdapters),
	)
}
