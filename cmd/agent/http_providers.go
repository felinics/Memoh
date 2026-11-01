package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"go.uber.org/fx"

	coremodule "github.com/felinics/memoh/cmd/internal/core"
	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agentcredential"
	audiopkg "github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/boot"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/local"
	"github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/config"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/handlers"
	"github.com/felinics/memoh/internal/healthcheck"
	channelchecker "github.com/felinics/memoh/internal/healthcheck/checkers/channel"
	mcpchecker "github.com/felinics/memoh/internal/healthcheck/checkers/mcp"
	modelchecker "github.com/felinics/memoh/internal/healthcheck/checkers/model"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/media"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/version"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

func provideServerHandler(fn any) any {
	return fx.Annotate(
		fn,
		fx.As(new(server.Handler)),
		fx.ResultTags(`group:"server_handlers"`),
	)
}

func provideMemoryHandler(log *slog.Logger, botService *bots.Service, accountService *accounts.Service, _ config.Config, memoryRegistry *memprovider.Registry, settingsService *settings.Service, _ *handlers.ContainerdHandler) *handlers.MemoryHandler {
	h := handlers.NewMemoryHandler(log, botService, accountService)
	h.SetMemoryRegistry(memoryRegistry)
	h.SetSettingsService(settingsService)
	return h
}

func provideAuthHandler(log *slog.Logger, accountService *accounts.Service, rc *boot.RuntimeConfig) *handlers.AuthHandler {
	return handlers.NewAuthHandler(log, accountService, rc.JwtSecret, rc.JwtExpiresIn)
}

func provideSessionQueueHandler(queries dbstore.Queries, agentService *application.Service, botService *bots.Service, accountService *accounts.Service) *handlers.SessionQueueHandler {
	return handlers.NewSessionQueueHandler(queries, agentService, botService, accountService)
}

func provideMessageHandler(log *slog.Logger, msgService *message.DBService, sessionService *sessionpkg.Service, mediaService *media.Service, botService *bots.Service, accountService *accounts.Service, hub *event.Hub, toolApproval *toolapproval.Service, userInput *userinput.Service, bgManager *background.Manager, sessionRuntime *sessionruntime.Manager, runtimes *external.Registry, pipeline *timeline.Pipeline, compactionService *compaction.Service, cfg config.Config) *handlers.MessageHandler {
	h := handlers.NewMessageHandler(log, msgService, sessionService, botService, accountService, hub)
	h.SetMediaService(mediaService)
	h.SetToolApprovalService(toolApproval)
	h.SetUserInputService(userInput)
	h.SetBackgroundManager(bgManager)
	h.SetRuntimeResetService(sessionRuntime)
	h.SetRuntimeRegistry(runtimes)
	h.SetProjectionCache(pipeline)
	h.SetCompactionActivity(compactionService)
	// This hub is process-local. A cluster cannot promise it observes runtime
	// admissions and persisted writes performed by another instance.
	h.SetSessionActivityInvalidationSupported(!cfg.SessionRuntime.Cluster)
	return h
}

func provideSessionHandler(log *slog.Logger, sessionService *sessionpkg.Service, sessionRuntime *sessionruntime.Manager, botService *bots.Service, accountService *accounts.Service, routeService *route.DBService, workdirService *workdir.Service, botAgentsService *botagents.Service, agentService *application.Service, pipeline *timeline.Pipeline, runs ledger.Store, runtimes *external.Registry) *handlers.SessionHandler {
	handler := handlers.NewSessionHandler(log, sessionService, sessionRuntime, botService, accountService)
	handler.SetThreadEnricher(routeService)
	handler.SetWorkdirService(workdirService)
	handler.SetBotAgents(botAgentsService)
	handler.SetAgentRuntimeService(agentService)
	handler.SetModelPreferenceService(agentService)
	handler.SetRuntimeRegistry(runtimes)
	handler.SetProjectionCache(pipeline)
	handler.SetInvocationLookup(runs)
	return handler
}

func provideACPRuntimeHandler(pool *acpagent.SessionPool, sessionService *sessionpkg.Service, botService *bots.Service, accountService *accounts.Service, botAgentsService *botagents.Service, sessionRuntime *sessionruntime.Manager) *handlers.ACPRuntimeHandler {
	handler := handlers.NewACPRuntimeHandler(pool, sessionService, botService, accountService)
	handler.SetBotAgents(botAgentsService)
	handler.SetRuntimeResetService(sessionRuntime)
	return handler
}

func provideBotAgentsHandler(log *slog.Logger, service *botagents.Service, botService *bots.Service, accountService *accounts.Service, runtimes *external.Registry, workdirService *workdir.Service) *handlers.BotAgentsHandler {
	handler := handlers.NewBotAgentsHandler(log, service, botService, accountService, runtimes)
	handler.SetWorkdirService(workdirService)
	return handler
}

func provideUsersHandler(log *slog.Logger, accountService *accounts.Service, botService *bots.Service, routeService *route.DBService, channelStore *channel.Store, channelRuntime channel.Runtime, registry *channel.Registry, workspaces *botworkspace.Service, manager *workspace.Manager, sessionRuntime *sessionruntime.Manager, credentialService *agentcredential.Service) *handlers.UsersHandler {
	handler := handlers.NewUsersHandler(log, accountService, botService, routeService, channelStore, channelRuntime, registry, workspaces)
	handler.SetWorkspaceStatus(manager)
	handler.SetRuntimeResetService(sessionRuntime)
	handler.SetCredentialService(credentialService)
	return handler
}

func provideExternalAgentLoginServerHandler(handler *handlers.ExternalAgentLoginHandler) *handlers.ExternalAgentLoginHandler {
	return handler
}

func provideProviderOAuthHandler(providersService *providers.Service) *handlers.ProviderOAuthHandler {
	return handlers.NewProviderOAuthHandler(providersService)
}

func provideWebHandler(channelManager *channel.Manager, channelStore *channel.Store, hub *local.RouteHub, botService *bots.Service, accountService *accounts.Service, sessionService *sessionpkg.Service, resolver *application.Service, sessionRuntime *sessionruntime.Manager, mediaService *media.Service, audioService *audiopkg.Service, settingsService *settings.Service, rc *boot.RuntimeConfig, commandHandler *command.Handler, containerdHandler *handlers.ContainerdHandler, workdirService *workdir.Service) *handlers.LocalChannelHandler {
	h := handlers.NewLocalChannelHandler(local.WebType, channelManager, channelStore, hub, botService, accountService, sessionService)
	h.SetAgentService(resolver)
	h.SetSessionRuntime(sessionRuntime)
	h.SetWorkdirService(workdirService)
	h.SetCommandHandler(commandHandler)
	h.SetRuntimeSkillResolver(containerdHandler)
	h.SetAuthTokenConfig(rc.JwtSecret, rc.JwtExpiresIn)
	h.SetMediaService(mediaService)
	h.SetSpeechService(audioService, &webSpeechModelResolver{settings: settingsService})
	return h
}

type serverParams struct {
	fx.In

	Logger            *slog.Logger
	RuntimeConfig     *boot.RuntimeConfig
	Config            config.Config
	AccountService    *accounts.Service
	ServerHandlers    []server.Handler `group:"server_handlers"`
	ContainerdHandler *handlers.ContainerdHandler
}

func provideServer(params serverParams) *server.Server {
	allHandlers := make([]server.Handler, 0, len(params.ServerHandlers)+1)
	allHandlers = append(allHandlers, params.ServerHandlers...)
	allHandlers = append(allHandlers, params.ContainerdHandler)
	return server.NewServerWithSessionValidator(
		params.Logger,
		params.RuntimeConfig.ServerAddr,
		params.Config.Auth.JWTSecret,
		params.AccountService.ValidateSession,
		allHandlers...,
	)
}

func startServer(lc fx.Lifecycle, logger *slog.Logger, srv *server.Server, agentService *application.Service, shutdowner fx.Shutdowner, cfg config.Config, queries dbstore.Queries, accountStore dbstore.AccountStore, botService *bots.Service, _ *handlers.ContainerdHandler, manager *workspace.Manager, mcpConnService *mcp.ConnectionService, toolGateway *mcp.ToolGatewayService, channelRuntime channel.Runtime, modelsService *models.Service) {
	fmt.Printf("Starting Memoh Agent %s\n", version.GetInfo())

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := coremodule.EnsureAdminUser(ctx, logger, accountStore, cfg); err != nil {
				return err
			}
			botService.SetContainerReachability(func(ctx context.Context, botID string) error {
				_, err := manager.MCPClient(ctx, botID)
				return err
			})
			botService.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				mcpchecker.NewChecker(logger, mcpConnService, toolGateway),
			))
			botService.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				channelchecker.NewChecker(logger, channelRuntime),
			))
			botService.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				modelchecker.NewChecker(logger, modelchecker.NewQueriesLookup(queries), modelsService),
			))

			go func() {
				if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("server failed", slog.Any("error", err))
					_ = shutdowner.Shutdown()
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopErr := srv.Stop(ctx)
			if errors.Is(stopErr, http.ErrServerClosed) {
				stopErr = nil
			} else if stopErr != nil {
				stopErr = fmt.Errorf("server stop: %w", stopErr)
			}
			return errors.Join(stopErr, agentService.DrainActiveTurns(ctx))
		},
	})
}

// webSpeechModelResolver adapts bot settings to the web chat speech model
// lookup (same shape as the shared Channel module's inbound resolver glue).
type webSpeechModelResolver struct {
	settings *settings.Service
}

func (r *webSpeechModelResolver) ResolveSpeechModelID(ctx context.Context, botID string) (string, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return "", err
	}
	return s.TtsModelID, nil
}
