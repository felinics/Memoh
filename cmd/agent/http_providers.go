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
	"github.com/memohai/memoh/domains/agent/chat/event"
	"github.com/memohai/memoh/domains/agent/chat/message"
	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/agent/command"
	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/agent/engine/background"
	agenthealth "github.com/memohai/memoh/domains/agent/health"
	"github.com/memohai/memoh/domains/agent/mcp"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/bot/setting"
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
	"github.com/memohai/memoh/domains/api/identity/auth"
	channelmodule "github.com/memohai/memoh/domains/channel/assembly"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	emailoauth "github.com/memohai/memoh/domains/channel/email/oauth"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/iam/account"
	accountpersistence "github.com/memohai/memoh/domains/iam/account/persistence"
	"github.com/memohai/memoh/domains/media/asset"
	memregistry "github.com/memohai/memoh/domains/memory/registry"
	audiopkg "github.com/memohai/memoh/domains/model/audio"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	modelhealth "github.com/memohai/memoh/domains/model/health"
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

func provideMemoryHandler(log *slog.Logger, botService *bot.Service, accountService *account.Service, _ config.Config, memoryRegistry *memregistry.Registry, settingsService *setting.Service, _ *runtimehttp.ContainerdHandler) *memoryhttp.MemoryHandler {
	h := memoryhttp.NewMemoryHandler(log, botService, accountService)
	h.SetMemoryRegistry(memoryRegistry)
	h.SetSettingsService(settingsService)
	return h
}

func provideAuthHandler(log *slog.Logger, accountService *account.Service, tokens auth.TokenConfig) *authhttp.AuthHandler {
	return authhttp.NewAuthHandler(log, accountService, tokens.Secret, tokens.ExpiresIn)
}

type messageHandlerParams struct {
	fx.In

	Log          *slog.Logger
	Messages     *message.DBService
	Sessions     *sessionpkg.Service
	Media        *asset.Service
	Bots         *bot.Service
	Accounts     *account.Service
	Events       *event.Hub
	ToolApproval *toolapproval.Service
	UserInput    *userinput.Service
	Background   *background.Manager
}

func provideMessageHandler(params messageHandlerParams) *chathttp.MessageHandler {
	h := chathttp.NewMessageHandler(params.Log, params.Messages, params.Sessions, params.Bots, params.Accounts, params.Events)
	h.SetMediaService(params.Media)
	h.SetToolApprovalService(params.ToolApproval)
	h.SetUserInputService(params.UserInput)
	h.SetBackgroundManager(params.Background)
	return h
}

func provideSessionHandler(log *slog.Logger, sessionService *sessionpkg.Service, acpPool *acpagent.SessionPool, botService *bot.Service, accountService *account.Service, routeService *route.DBService) *chathttp.SessionHandler {
	handler := chathttp.NewSessionHandler(log, sessionService, acpPool, botService, accountService)
	handler.SetThreadEnricher(routeService)
	return handler
}

type usersHandlerParams struct {
	fx.In

	Log            *slog.Logger
	Accounts       *account.Service
	Bots           *bot.Service
	Routes         *route.DBService
	Channels       *gateway.Store
	ChannelRuntime gateway.Runtime
	Registry       *gateway.Registry
	Workspace      workspace.Service
	ACP            *acpagent.SessionPool
}

func provideUsersHandler(params usersHandlerParams) *bothttp.UsersHandler {
	handler := bothttp.NewUsersHandler(params.Log, params.Accounts, params.Bots, params.Routes, params.Channels, params.ChannelRuntime, params.Registry, params.Workspace)
	handler.SetACPRuntimeCloser(params.ACP)
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

type webHandlerParams struct {
	fx.In

	ChannelManager *gateway.Manager
	Channels       *gateway.Store
	RouteHub       *local.RouteHub
	Bots           *bot.Service
	Accounts       *account.Service
	Sessions       *sessionpkg.Service
	Agent          *application.Service
	Media          *asset.Service
	Audio          *audiopkg.Service
	Settings       *setting.Service
	Tokens         auth.TokenConfig
	Commands       *command.Handler
	RuntimeHandler *runtimehttp.ContainerdHandler
}

func provideWebHandler(params webHandlerParams) *chathttp.LocalChannelHandler {
	h := chathttp.NewLocalChannelHandler(local.WebType, params.ChannelManager, params.Channels, params.RouteHub, params.Bots, params.Accounts, params.Sessions)
	h.SetAgentService(params.Agent)
	h.SetCommandHandler(params.Commands)
	h.SetRuntimeSkillResolver(params.RuntimeHandler)
	h.SetAuthTokenConfig(params.Tokens.Secret, params.Tokens.ExpiresIn)
	h.SetMediaService(params.Media)
	h.SetSpeechService(params.Audio, &webSpeechModelResolver{settings: params.Settings})
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
	gmail := emailoauth.NewGmail(tokenStore, emailOAuthClientResolver{inner: oauthClients})
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

type startServerParams struct {
	fx.In

	Lifecycle      fx.Lifecycle
	Log            *slog.Logger
	Server         *server.Server
	Shutdowner     fx.Shutdowner
	Config         config.Config
	AccountCounter accountpersistence.AccountCounter
	Accounts       *account.Service
	Email          *emailpkg.Service
	Bots           *bot.Service
	Settings       *setting.Service
	RuntimeHandler *runtimehttp.ContainerdHandler
	Workspace      workspace.Service
	MCPConnections *mcp.ConnectionService
	ToolGateway    *mcp.ToolGatewayService
	ChannelRuntime gateway.Runtime
	Models         *modelcatalog.Service
}

func startServer(params startServerParams) {
	fmt.Printf("Starting Memoh Agent %s\n", version.GetInfo(buildProfile))

	serveDone := make(chan struct{})
	params.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := ensureAdminUser(ctx, params.Log, params.AccountCounter, params.Accounts, params.Email, params.Config); err != nil {
				return err
			}
			params.Bots.SetContainerLifecycle(params.Workspace)
			params.Bots.SetContainerReachability(func(ctx context.Context, botID string) error {
				_, err := params.Workspace.MCPClient(ctx, botID)
				return err
			})
			params.Bots.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				agenthealth.NewHealthChecker(params.Log, params.MCPConnections, params.ToolGateway),
			))
			params.Bots.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				channelmodule.NewHealthChecker(params.Log, params.ChannelRuntime),
			))
			params.Bots.AddRuntimeChecker(healthcheck.NewRuntimeCheckerAdapter(
				modelhealth.NewHealthChecker(params.Log, modelhealth.NewBotModelLookup(
					func(ctx context.Context, botID string) (string, string, error) {
						record, err := params.Bots.GetForAccess(ctx, botID)
						if err != nil {
							return "", "", err
						}
						botSettings, err := params.Settings.GetBot(ctx, botID)
						if err != nil {
							return "", "", err
						}
						return record.OwnerUserID, botSettings.ChatModelID, nil
					},
				), params.Models),
			))

			listener, err := params.Server.Listen(ctx)
			if err != nil {
				return fmt.Errorf("listen agent http: %w", err)
			}
			go func() {
				defer close(serveDone)
				handleServeError(params.Log, params.Shutdowner, "agent http server", params.Server.Serve(listener), http.ErrServerClosed)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			var stopErr error
			if err := params.Server.Stop(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				stopErr = fmt.Errorf("stop agent http: %w", err)
			}
			return errors.Join(stopErr, waitForServe(ctx, serveDone, "agent http server"))
		},
	})
}

func handleServeError(logger *slog.Logger, shutdowner fx.Shutdowner, name string, err, stoppedErr error) {
	if err == nil || errors.Is(err, stoppedErr) {
		return
	}
	logger.Error(name+" failed", slog.Any("error", err))
	if shutdownErr := shutdowner.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
		logger.Error("request process shutdown failed", slog.String("server", name), slog.Any("error", shutdownErr))
	}
}

func waitForServe(ctx context.Context, done <-chan struct{}, name string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for %s: %w", name, ctx.Err())
	}
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
