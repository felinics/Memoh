package main

import (
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	channelmodule "github.com/felinics/memoh/cmd/internal/channel"
	coremodule "github.com/felinics/memoh/cmd/internal/core"
	channelpkg "github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/weixin"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/handlers"
)

func runServe() {
	cfg, err := provideConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "memoh: %v\n", err)
		os.Exit(1)
	}
	fx.New(optionsFor(cfg)).Run()
}

// optionsFor assembles the server for one of two deployment shapes.
// With internal_rpc.shared_secret set (docker compose), the channel
// runtime is a separate process reached over authenticated gRPC. Without
// it (pre-split bare-metal installs), the full channel runtime runs
// embedded in this process so external channels, email, and webhook
// endpoints keep working with a single binary and an unchanged config.
func optionsFor(cfg config.Config) fx.Option {
	if cfg.SplitChannelRuntime() {
		return fx.Options(commonOptions(), splitOptions())
	}
	return fx.Options(commonOptions(), embeddedOptions())
}

func splitOptions() fx.Option {
	return fx.Options(
		channelmodule.ServerLocalModule(),
		fx.Provide(
			provideChannelRPCConn,
			provideRuntimeRPCClient,
			provideChannelRuntimeClient,
			provideChannelRuntime,
			provideEmailRuntime,
			provideWebhookTunnelStatus,
			provideServerRPC,
		),
		fx.Invoke(startServerRPC),
	)
}

func embeddedOptions() fx.Option {
	return fx.Options(
		channelmodule.EmbeddedModule(),
		fx.Provide(
			provideLocalWebhookTunnelStatus,
			// The webhook surfaces the channel process owns in split mode
			// are served from this process again, as before the split.
			provideServerHandler(channelpkg.NewWebhookServerHandler),
			provideServerHandler(weixin.NewQRServerHandler),
			provideServerHandler(handlers.NewEmailWebhookHandler),
			provideServerHandler(handlers.NewConfiguredPublicMediaHandler),
		),
	)
}

func commonOptions() fx.Option {
	return fx.Options(
		fx.Provide(provideConfig),
		coremodule.FoundationModule(),
		channelmodule.FoundationModule(),
		coremodule.ServerModule(),
		fx.Provide(
			provideServerHandler(handlers.NewPingHandler),
			provideServerHandler(handlers.NewWebhookTunnelHandler),
			provideServerHandler(provideAuthHandler),
			provideServerHandler(provideMemoryHandler),
			provideServerHandler(provideMessageHandler),
			provideServerHandler(provideSessionHandler),
			provideServerHandler(handlers.NewUserRuntimeHandler),
			provideServerHandler(handlers.NewUserComputerAccessHandler),
			provideServerHandler(handlers.NewRuntimeConnectHandler),
			provideServerHandler(handlers.NewBotRemoteRuntimeHandler),
			provideServerHandler(handlers.NewWorkdirHandler),
			provideServerHandler(handlers.NewACPHandler),
			provideServerHandler(handlers.NewACPRuntimeHandlerWithWorkspaceAccess),
			provideServerHandler(handlers.NewSwaggerHandler),
			provideServerHandler(handlers.NewProvidersHandler),
			provideServerHandler(handlers.NewProviderTemplatesHandler),
			provideServerHandler(provideProviderOAuthHandler),
			provideServerHandler(provideACPCodexOAuthServerHandler),
			provideServerHandler(provideACPClaudeCodeOAuthServerHandler),
			provideServerHandler(handlers.NewFetchProvidersHandler),
			provideServerHandler(handlers.NewSearchProvidersHandler),
			provideServerHandler(handlers.NewModelsHandler),
			provideServerHandler(handlers.NewBotAgentsHandler),
			provideServerHandler(handlers.NewSettingsHandler),
			provideServerHandler(handlers.NewToolApprovalHandler),
			provideServerHandler(handlers.NewHooksHandler),
			provideServerHandler(handlers.NewACLHandler),
			provideServerHandler(handlers.NewBotUserAccessHandler),
			provideServerHandler(handlers.NewChannelAccessHandler),
			provideServerHandler(handlers.NewScheduleHandler),
			provideServerHandler(handlers.NewCompactionHandler),
			provideServerHandler(handlers.NewChannelHandler),
			provideServerHandler(provideUsersHandler),
			provideServerHandler(handlers.NewMemoryProvidersHandler),
			provideServerHandler(handlers.NewNetworkHandler),
			provideServerHandler(handlers.NewAudioHandler),
			provideServerHandler(handlers.NewVideoHandler),
			provideServerHandler(handlers.NewBotAudioHandler),
			provideServerHandler(handlers.NewEmailProvidersHandler),
			provideServerHandler(handlers.NewEmailBindingsHandler),
			provideServerHandler(handlers.NewEmailOutboxHandler),
			provideServerHandler(provideEmailOAuthHandler),
			provideServerHandler(handlers.NewMCPHandler),
			provideServerHandler(handlers.NewMCPOAuthHandler),
			provideServerHandler(handlers.NewConnectorsHandler),
			provideServerHandler(handlers.NewBotBackupHandler),
			provideServerHandler(handlers.NewTokenUsageHandler),
			provideServerHandler(handlers.NewSessionInfoHandler),
			provideServerHandler(handlers.NewSupermarketHandler),
			provideServerHandler(provideWebHandler),
			provideServer,
		),
		fx.Invoke(
			startServer,
		),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger.With(slog.String("component", "fx"))}
		}),
	)
}
