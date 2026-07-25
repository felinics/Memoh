package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.uber.org/fx"

	acpagent "github.com/memohai/memoh/domains/agent/acp"
	"github.com/memohai/memoh/domains/agent/application"
	agentassembly "github.com/memohai/memoh/domains/agent/assembly"
	"github.com/memohai/memoh/domains/agent/chat/event"
	"github.com/memohai/memoh/domains/agent/chat/message"
	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/agent/command"
	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/agent/engine/background"
	"github.com/memohai/memoh/domains/agent/mcp"
	"github.com/memohai/memoh/domains/api/auth"
	"github.com/memohai/memoh/domains/api/bot"
	apihttp "github.com/memohai/memoh/domains/api/http"
	agenthttp "github.com/memohai/memoh/domains/api/http/agent"
	authhttp "github.com/memohai/memoh/domains/api/http/auth"
	bothttp "github.com/memohai/memoh/domains/api/http/bot"
	chathttp "github.com/memohai/memoh/domains/api/http/chat"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	emailhttp "github.com/memohai/memoh/domains/api/http/email"
	memoryhttp "github.com/memohai/memoh/domains/api/http/memory"
	modelhttp "github.com/memohai/memoh/domains/api/http/model"
	runtimehttp "github.com/memohai/memoh/domains/api/http/runtime"
	"github.com/memohai/memoh/domains/api/http/server"
	"github.com/memohai/memoh/domains/api/setting"
	channelmodule "github.com/memohai/memoh/domains/channel/assembly"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	channelhttphost "github.com/memohai/memoh/domains/channel/http"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/domains/media/asset"
	memprovider "github.com/memohai/memoh/domains/memory/registry"
	modelassembly "github.com/memohai/memoh/domains/model/assembly"
	audiopkg "github.com/memohai/memoh/domains/model/audio"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	providers "github.com/memohai/memoh/domains/model/provider"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/healthcheck"
	"github.com/memohai/memoh/internal/oauth"
	"github.com/memohai/memoh/internal/version"
)

func provideServerHandler(fn any) any {
	return fx.Annotate(
		fn,
		fx.As(new(server.Handler)),
		fx.ResultTags(`group:"server_handlers"`),
	)
}

func providePublicMediaHandler(log *slog.Logger, cfg config.Config, mediaService *asset.Service) *channelhttphost.PublicMediaHandler {
	return channelhttphost.NewConfiguredPublicMediaHandler(log, cfg, mediaService)
}

func provideMemoryHandler(log *slog.Logger, botService *bot.Service, accountService *account.Service, _ config.Config, memoryRegistry *memprovider.Registry, settingsService *setting.Service, _ *runtimehttp.ContainerdHandler) *memoryhttp.MemoryHandler {
	h := memoryhttp.NewMemoryHandler(log, botService, accountService)
	h.SetMemoryRegistry(memoryRegistry)
	h.SetSettingsService(settingsService)
	return h
}

func provideAuthHandler(log *slog.Logger, accountService *account.Service, tokens auth.TokenConfig) *authhttp.AuthHandler {
	return authhttp.NewAuthHandler(log, accountService, tokens.Secret, tokens.ExpiresIn)
}

func provideMessageHandler(log *slog.Logger, msgService *message.DBService, sessionService *sessionpkg.Service, mediaService *asset.Service, botService *bot.Service, accountService *account.Service, hub *event.Hub, toolApproval *toolapproval.Service, userInput *userinput.Service, bgManager *background.Manager) *chathttp.MessageHandler {
	h := chathttp.NewMessageHandler(log, msgService, sessionService, botService, accountService, hub)
	h.SetMediaService(mediaService)
	h.SetToolApprovalService(toolApproval)
	h.SetUserInputService(userInput)
	h.SetBackgroundManager(bgManager)
	return h
}

func provideSessionHandler(log *slog.Logger, sessionService *sessionpkg.Service, acpPool *acpagent.SessionPool, botService *bot.Service, accountService *account.Service, routeService *route.DBService) *chathttp.SessionHandler {
	handler := chathttp.NewSessionHandler(log, sessionService, acpPool, botService, accountService)
	handler.SetThreadEnricher(routeService)
	return handler
}

func provideUsersHandler(log *slog.Logger, accountService *account.Service, botService *bot.Service, routeService *route.DBService, channelStore *gateway.Store, channelRuntime gateway.Runtime, registry *gateway.Registry, workspaceManager workspace.Service, acpPool *acpagent.SessionPool) *bothttp.UsersHandler {
	handler := bothttp.NewUsersHandler(log, accountService, botService, routeService, channelStore, channelRuntime, registry, workspaceManager)
	handler.SetACPRuntimeCloser(acpPool)
	return handler
}

func provideACPCodexOAuthServerHandler(handler *agenthttp.ACPCodexOAuthHandler) *agenthttp.ACPCodexOAuthHandler {
	return handler
}

func provideACPClaudeCodeOAuthServerHandler(handler *agenthttp.ACPClaudeCodeOAuthHandler) *agenthttp.ACPClaudeCodeOAuthHandler {
	return handler
}

func provideProviderOAuthHandler(providersService *providers.Service, acpCodexOAuthHandler *agenthttp.ACPCodexOAuthHandler) *modelhttp.ProviderOAuthHandler {
	handler := modelhttp.NewProviderOAuthHandler(providersService)
	handler.SetACPCodexOAuthHandler(acpCodexOAuthHandler)
	return handler
}

func provideWebHandler(channelManager *gateway.Manager, channelStore *gateway.Store, hub *local.RouteHub, botService *bot.Service, accountService *account.Service, sessionService *sessionpkg.Service, resolver *application.Service, mediaService *asset.Service, audioService *audiopkg.Service, settingsService *setting.Service, tokens auth.TokenConfig, commandHandler *command.Handler, containerdHandler *runtimehttp.ContainerdHandler) *chathttp.LocalChannelHandler {
	h := chathttp.NewLocalChannelHandler(local.WebType, channelManager, channelStore, hub, botService, accountService, sessionService)
	h.SetAgentService(resolver)
	h.SetCommandHandler(commandHandler)
	h.SetRuntimeSkillResolver(containerdHandler)
	h.SetAuthTokenConfig(tokens.Secret, tokens.ExpiresIn)
	h.SetMediaService(mediaService)
	h.SetSpeechService(audioService, &webSpeechModelResolver{settings: settingsService})
	return h
}

func provideEmailOAuthHandler(log *slog.Logger, service *emailpkg.Service, tokenStore emailpkg.OAuthTokenStore, oauthClients *oauth.Registry, cfg config.Config) *emailhttp.EmailOAuthHandler {
	addr := strings.TrimSpace(cfg.Server.Addr)
	if addr == "" {
		addr = ":8080"
	}
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	callbackURL := "http://" + host + "/api/email/oauth/callback"
	gmail := emailpkg.NewGmailOAuth(log, tokenStore, emailOAuthClientResolver{inner: oauthClients})
	return emailhttp.NewEmailOAuthHandler(log, service, tokenStore, gmail, callbackURL)
}

type emailOAuthClientResolver struct {
	inner oauth.Resolver
}

func (r emailOAuthClientResolver) Get(ref string) (emailpkg.OAuthClient, bool) {
	if r.inner == nil {
		return emailpkg.OAuthClient{}, false
	}
	client, ok := r.inner.Get(ref)
	if !ok {
		return emailpkg.OAuthClient{}, false
	}
	return emailpkg.OAuthClient{
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		RedirectURI:  client.RedirectURI,
	}, true
}

func (r emailOAuthClientResolver) HasUsableClient(ref string) bool {
	return r.inner != nil && r.inner.HasUsableClient(ref)
}

type serverParams struct {
	fx.In

	Logger            *slog.Logger
	ListenAddr        apihttp.ListenAddr
	Tokens            auth.TokenConfig
	AccountService    *account.Service
	ServerHandlers    []server.Handler `group:"server_handlers"`
	ContainerdHandler *runtimehttp.ContainerdHandler
}

func provideServer(params serverParams) *server.Server {
	allHandlers := make([]server.Handler, 0, len(params.ServerHandlers)+1)
	allHandlers = append(allHandlers, params.ServerHandlers...)
	allHandlers = append(allHandlers, params.ContainerdHandler)
	return server.NewServerWithSessionValidator(
		params.Logger,
		params.ListenAddr.String(),
		params.Tokens.Secret,
		params.AccountService.ValidateSession,
		allHandlers...,
	)
}

func startServer(lc fx.Lifecycle, logger *slog.Logger, srv *server.Server, shutdowner fx.Shutdowner, cfg config.Config, accountCounter account.AccountCounter, accountStore account.Store, emailService *emailpkg.Service, botService *bot.Service, settingsService *setting.Service, _ *runtimehttp.ContainerdHandler, manager workspace.Service, mcpConnService *mcp.ConnectionService, toolGateway *mcp.ToolGatewayService, channelRuntime gateway.Runtime, modelsService *modelcatalog.Service) {
	fmt.Printf("Starting Memoh Agent %s\n", version.GetInfo(buildProfile))

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := ensureAdminUser(ctx, logger, accountCounter, accountStore, emailService, cfg); err != nil {
				return err
			}
			botService.SetContainerLifecycle(manager)
			botService.SetContainerReachability(func(ctx context.Context, botID string) error {
				_, err := manager.MCPClient(ctx, botID)
				return err
			})
			botService.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				agentassembly.NewHealthChecker(logger, mcpConnService, toolGateway),
			))
			botService.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				channelmodule.NewHealthChecker(logger, channelRuntime),
			))
			botService.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				modelassembly.NewHealthChecker(logger, modelassembly.NewBotModelLookup(
					func(ctx context.Context, botID string) (string, string, error) {
						record, err := botService.GetForAccess(ctx, botID)
						if err != nil {
							return "", "", err
						}
						botSettings, err := settingsService.GetBot(ctx, botID)
						if err != nil {
							return "", "", err
						}
						return record.OwnerUserID, botSettings.ChatModelID, nil
					},
				), modelsService),
			))

			go func() {
				if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.ErrorContext(ctx, "server failed", slog.Any("error", err))
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
	settings *setting.Service
}

func (r *webSpeechModelResolver) ResolveSpeechModelID(ctx context.Context, botID string) (string, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return "", err
	}
	return s.TtsModelID, nil
}
