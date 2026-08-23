package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.uber.org/fx"

	coremodule "github.com/memohai/memoh/cmd/internal/core"
	"github.com/memohai/memoh/internal/accounts"
	"github.com/memohai/memoh/internal/agent/application"
	"github.com/memohai/memoh/internal/agent/background"
	toolapproval "github.com/memohai/memoh/internal/agent/decision/approval"
	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	acpagent "github.com/memohai/memoh/internal/agent/runtime/acp"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	audiopkg "github.com/memohai/memoh/internal/audio"
	"github.com/memohai/memoh/internal/boot"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/channel/adapters/local"
	"github.com/memohai/memoh/internal/channel/route"
	"github.com/memohai/memoh/internal/chat/event"
	"github.com/memohai/memoh/internal/chat/message"
	sessionpkg "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/command"
	"github.com/memohai/memoh/internal/config"
	dbstore "github.com/memohai/memoh/internal/db/store"
	emailpkg "github.com/memohai/memoh/internal/email"
	"github.com/memohai/memoh/internal/handlers"
	"github.com/memohai/memoh/internal/healthcheck"
	channelchecker "github.com/memohai/memoh/internal/healthcheck/checkers/channel"
	mcpchecker "github.com/memohai/memoh/internal/healthcheck/checkers/mcp"
	modelchecker "github.com/memohai/memoh/internal/healthcheck/checkers/model"
	"github.com/memohai/memoh/internal/mcp"
	"github.com/memohai/memoh/internal/media"
	memprovider "github.com/memohai/memoh/internal/memory/adapters"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/oauthclients"
	"github.com/memohai/memoh/internal/providers"
	"github.com/memohai/memoh/internal/server"
	"github.com/memohai/memoh/internal/settings"
	"github.com/memohai/memoh/internal/version"
	"github.com/memohai/memoh/internal/workdir"
	"github.com/memohai/memoh/internal/workspace"
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

func provideMessageHandler(log *slog.Logger, msgService *message.DBService, sessionService *sessionpkg.Service, mediaService *media.Service, botService *bots.Service, accountService *accounts.Service, hub *event.Hub, toolApproval *toolapproval.Service, userInput *userinput.Service, bgManager *background.Manager, acpPool *acpagent.SessionPool) *handlers.MessageHandler {
	h := handlers.NewMessageHandler(log, msgService, sessionService, botService, accountService, hub)
	h.SetMediaService(mediaService)
	h.SetToolApprovalService(toolApproval)
	h.SetUserInputService(userInput)
	h.SetBackgroundManager(bgManager)
	h.SetRuntimeResetService(acpPool)
	return h
}

func provideSessionHandler(log *slog.Logger, sessionService *sessionpkg.Service, acpPool *acpagent.SessionPool, botService *bots.Service, accountService *accounts.Service, routeService *route.DBService, workdirService *workdir.Service) *handlers.SessionHandler {
	handler := handlers.NewSessionHandler(log, sessionService, acpPool, botService, accountService)
	handler.SetThreadEnricher(routeService)
	handler.SetWorkdirService(workdirService)
	return handler
}

func provideUsersHandler(log *slog.Logger, accountService *accounts.Service, botService *bots.Service, routeService *route.DBService, channelStore *channel.Store, channelRuntime channel.Runtime, registry *channel.Registry, workspaceManager *workspace.Manager, acpPool *acpagent.SessionPool) *handlers.UsersHandler {
	handler := handlers.NewUsersHandler(log, accountService, botService, routeService, channelStore, channelRuntime, registry, workspaceManager)
	handler.SetRuntimeResetService(acpPool)
	return handler
}

func provideACPCodexOAuthServerHandler(handler *handlers.ACPCodexOAuthHandler) *handlers.ACPCodexOAuthHandler {
	return handler
}

func provideACPClaudeCodeOAuthServerHandler(handler *handlers.ACPClaudeCodeOAuthHandler) *handlers.ACPClaudeCodeOAuthHandler {
	return handler
}

func provideProviderOAuthHandler(providersService *providers.Service, acpCodexOAuthHandler *handlers.ACPCodexOAuthHandler) *handlers.ProviderOAuthHandler {
	handler := handlers.NewProviderOAuthHandler(providersService)
	handler.SetACPCodexOAuthHandler(acpCodexOAuthHandler)
	return handler
}

func provideWebHandler(channelManager *channel.Manager, channelStore *channel.Store, hub *local.RouteHub, botService *bots.Service, accountService *accounts.Service, sessionService *sessionpkg.Service, resolver *application.Service, sessionRuntime *sessionruntime.Manager, acpPool *acpagent.SessionPool, mediaService *media.Service, audioService *audiopkg.Service, settingsService *settings.Service, rc *boot.RuntimeConfig, commandHandler *command.Handler, containerdHandler *handlers.ContainerdHandler) *handlers.LocalChannelHandler {
	h := handlers.NewLocalChannelHandler(local.WebType, channelManager, channelStore, hub, botService, accountService, sessionService)
	h.SetAgentService(resolver)
	h.SetSessionRuntime(sessionRuntime)
	h.SetACPRuntimeStatusReader(acpPool)
	h.SetCommandHandler(commandHandler)
	h.SetRuntimeSkillResolver(containerdHandler)
	h.SetAuthTokenConfig(rc.JwtSecret, rc.JwtExpiresIn)
	h.SetMediaService(mediaService)
	h.SetSpeechService(audioService, &webSpeechModelResolver{settings: settingsService})
	return h
}

func provideEmailOAuthHandler(log *slog.Logger, service *emailpkg.Service, tokenStore *emailpkg.DBOAuthTokenStore, oauthClients *oauthclients.Registry, cfg config.Config) *handlers.EmailOAuthHandler {
	addr := strings.TrimSpace(cfg.Server.Addr)
	if addr == "" {
		addr = ":8080"
	}
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	callbackURL := "http://" + host + "/api/email/oauth/callback"
	return handlers.NewEmailOAuthHandler(log, service, tokenStore, oauthClients, callbackURL)
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

func startServer(lc fx.Lifecycle, logger *slog.Logger, srv *server.Server, shutdowner fx.Shutdowner, cfg config.Config, queries dbstore.Queries, accountStore dbstore.AccountStore, emailService *emailpkg.Service, botService *bots.Service, _ *handlers.ContainerdHandler, manager *workspace.Manager, mcpConnService *mcp.ConnectionService, toolGateway *mcp.ToolGatewayService, channelRuntime channel.Runtime, modelsService *models.Service) {
	fmt.Printf("Starting Memoh Agent %s\n", version.GetInfo())

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := coremodule.EnsureAdminUser(ctx, logger, accountStore, emailService, cfg); err != nil {
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
			if err := srv.Stop(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("server stop: %w", err)
			}
			return nil
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
