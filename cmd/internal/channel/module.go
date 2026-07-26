// Package channel owns process-neutral Channel composition shared by Server
// and Channel process profiles. Long-running external runtime composition
// lives in the runtime subpackage so split Server builds do not compile it.
package channel

import (
	"go.uber.org/fx"

	agentassembly "github.com/memohai/memoh/domains/agent/assembly"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
)

const (
	foundationModuleName  = "channel-foundation"
	serverLocalModuleName = "channel-server-local"
	gatewayModuleName     = "channel-gateway"
)

// FoundationModule assembles process-neutral Channel dependencies. It keeps
// the concrete persistence bundle private; split mode supplies conversation
// projections through RPC.
func FoundationModule() fx.Option {
	return fx.Module(foundationModuleName, foundationProviders())
}

// LocalFoundationModule exports the projection backed by local Channel
// persistence for embedded and standalone profiles.
func LocalFoundationModule() fx.Option {
	return fx.Module(
		foundationModuleName,
		foundationProviders(),
		fx.Provide(provideConversationProjectionReader),
	)
}

func foundationProviders() fx.Option {
	return fx.Options(
		fx.Provide(
			provideChannelIdentityBindingStore,
			agentassembly.NewRouteSessionCoordinator,
			provideChannelPostgresStore,
			provideEmailRegistry,
			provideTimelineStore,
			fx.Private,
		),
		fx.Provide(
			provideChannelPersistence,
			provideChannelIdentityStore,
			identity.NewService,
			provideEmailOAuthTokenStore,
			provideEmailService,
			provideEmailOutboxService,
			provideRouteService,
			providePipeline,
			provideEventStore,
			provideDiscussDriver,
			local.NewRouteHub,
			gateway.NewStore,
		),
		fx.Invoke(registerDiscussLifecycle),
	)
}

// ServerLocalModule supplies local command, skill, audio, and settings
// bindings plus the shared inbound gateway. The profile must provide exactly
// one *gateway.Registry: NewLocalRegistry for split Server, or the external
// platform catalog for embedded Server.
func ServerLocalModule() fx.Option {
	return fx.Module(
		serverLocalModuleName,
		fx.Provide(
			provideUsageService,
			provideCommandQueries,
			provideLocalCommandHandler,
			provideLocalSkillResolver,
			provideLocalChannelAudio,
			provideLocalChannelSettings,
			fx.Private,
		),
		fx.Provide(
			provideUsageReader,
			provideCommandHandler,
		),
		gatewayProviders(),
	)
}

// GatewayModule assembles the shared inbound processor and manager from
// profile-supplied registry, command, skill, audio, and settings ports.
func GatewayModule() fx.Option {
	return fx.Module(gatewayModuleName, gatewayProviders())
}

func gatewayProviders() fx.Option {
	return fx.Options(
		fx.Provide(provideChannelRouter, fx.Private),
		fx.Provide(provideChannelManager),
	)
}

// Module preserves the previous local-only assembly for focused tests.
func Module() fx.Option {
	return fx.Options(
		LocalFoundationModule(),
		fx.Provide(NewLocalRegistry),
		ServerLocalModule(),
	)
}
