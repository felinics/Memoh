package main

import (
	"fmt"
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	coremodule "github.com/memohai/memoh/cmd/internal/core"
	accesshttp "github.com/memohai/memoh/domains/api/http/access"
	agenthttp "github.com/memohai/memoh/domains/api/http/agent"
	bothttp "github.com/memohai/memoh/domains/api/http/bot"
	channelhttp "github.com/memohai/memoh/domains/api/http/channel"
	chathttp "github.com/memohai/memoh/domains/api/http/chat"
	emailhttp "github.com/memohai/memoh/domains/api/http/email"
	memoryhttp "github.com/memohai/memoh/domains/api/http/memory"
	modelhttp "github.com/memohai/memoh/domains/api/http/model"
	runtimehttp "github.com/memohai/memoh/domains/api/http/runtime"
	systemhttp "github.com/memohai/memoh/domains/api/http/system"
	channelmodule "github.com/memohai/memoh/domains/channel/assembly"
	"github.com/memohai/memoh/internal/config"
)

func runServe() {
	cfg, err := provideConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "memoh: %v\n", err)
		os.Exit(1)
	}
	if err := validateProfile(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "memoh: %v\n", err)
		os.Exit(1)
	}
	fx.New(optionsFor(cfg)).Run()
}

func commonOptions(cfg config.Config) fx.Option {
	return fx.Options(
		fx.Supply(cfg),
		coremodule.FoundationModule(),
		channelmodule.FoundationModule(),
		coremodule.ServerModule(),
		fx.Provide(
			provideServerHandler(systemhttp.NewPingHandler),
			provideServerHandler(channelhttp.NewWebhookTunnelHandler),
			provideServerHandler(provideAuthHandler),
			provideServerHandler(provideMemoryHandler),
			provideServerHandler(provideMessageHandler),
			provideServerHandler(provideSessionHandler),
			provideServerHandler(runtimehttp.NewUserRuntimeHandler),
			provideServerHandler(runtimehttp.NewRuntimeConnectHandler),
			provideServerHandler(runtimehttp.NewBotRemoteRuntimeHandler),
			provideServerHandler(agenthttp.NewACPHandler),
			provideServerHandler(agenthttp.NewACPRuntimeHandler),
			provideServerHandler(systemhttp.NewSwaggerHandler),
			provideServerHandler(modelhttp.NewProvidersHandler),
			provideServerHandler(modelhttp.NewProviderTemplatesHandler),
			provideServerHandler(provideProviderOAuthHandler),
			provideServerHandler(provideACPCodexOAuthServerHandler),
			provideServerHandler(provideACPClaudeCodeOAuthServerHandler),
			provideServerHandler(modelhttp.NewFetchProvidersHandler),
			provideServerHandler(modelhttp.NewSearchProvidersHandler),
			provideServerHandler(modelhttp.NewModelsHandler),
			provideServerHandler(bothttp.NewSettingsHandler),
			provideServerHandler(agenthttp.NewToolApprovalHandler),
			provideServerHandler(agenthttp.NewHooksHandler),
			provideServerHandler(accesshttp.NewACLHandler),
			provideServerHandler(accesshttp.NewBotUserAccessHandler),
			provideServerHandler(accesshttp.NewChannelAccessHandler),
			provideServerHandler(agenthttp.NewScheduleHandler),
			provideServerHandler(agenthttp.NewHeartbeatHandler),
			provideServerHandler(chathttp.NewCompactionHandler),
			provideServerHandler(channelhttp.NewChannelHandler),
			provideServerHandler(provideUsersHandler),
			provideServerHandler(memoryhttp.NewMemoryProvidersHandler),
			provideServerHandler(runtimehttp.NewNetworkHandler),
			provideServerHandler(modelhttp.NewAudioHandler),
			provideServerHandler(modelhttp.NewVideoHandler),
			provideServerHandler(bothttp.NewBotAudioHandler),
			provideServerHandler(emailhttp.NewEmailProvidersHandler),
			provideServerHandler(emailhttp.NewEmailBindingsHandler),
			provideServerHandler(emailhttp.NewEmailOutboxHandler),
			provideServerHandler(provideEmailOAuthHandler),
			provideServerHandler(agenthttp.NewMCPHandler),
			provideServerHandler(agenthttp.NewMCPOAuthHandler),
			provideServerHandler(agenthttp.NewPluginsHandler),
			provideServerHandler(bothttp.NewBotBackupHandler),
			provideServerHandler(chathttp.NewTokenUsageHandler),
			provideServerHandler(chathttp.NewSessionInfoHandler),
			provideServerHandler(bothttp.NewSupermarketHandler),
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
