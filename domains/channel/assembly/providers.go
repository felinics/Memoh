// Package assembly assembles the shared command-side Channel module:
// registry/manager/processor, discuss pipeline, email, and webhook tunnel.
package assembly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	stdpath "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.uber.org/fx"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/adapter/acp/profile"
	agentassembly "github.com/memohai/memoh/domains/agent/assembly"
	"github.com/memohai/memoh/domains/agent/automation/heartbeat"
	"github.com/memohai/memoh/domains/agent/automation/schedule"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/message"
	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/agent/chat/timeline"
	"github.com/memohai/memoh/domains/agent/chat/usage"
	"github.com/memohai/memoh/domains/agent/command"
	"github.com/memohai/memoh/domains/agent/mcp"
	"github.com/memohai/memoh/domains/api/access"
	"github.com/memohai/memoh/domains/api/access/acl"
	"github.com/memohai/memoh/domains/api/access/policy"
	apiassembly "github.com/memohai/memoh/domains/api/assembly"
	"github.com/memohai/memoh/domains/api/auth"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	httpx "github.com/memohai/memoh/domains/api/http/httpx"
	runtimehttp "github.com/memohai/memoh/domains/api/http/runtime"
	"github.com/memohai/memoh/domains/api/setting"
	"github.com/memohai/memoh/domains/channel"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	channelhttphost "github.com/memohai/memoh/domains/channel/http"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/inbound"
	"github.com/memohai/memoh/domains/channel/internal/discuss"
	channelpostgres "github.com/memohai/memoh/domains/channel/internal/postgres"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/domains/iam/account"
	team "github.com/memohai/memoh/domains/iam/team"
	"github.com/memohai/memoh/domains/media/asset"
	memcatalog "github.com/memohai/memoh/domains/memory/catalog"
	modelassembly "github.com/memohai/memoh/domains/model/assembly"
	audiopkg "github.com/memohai/memoh/domains/model/audio"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	providers "github.com/memohai/memoh/domains/model/provider"
	"github.com/memohai/memoh/domains/model/search"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/oauth"
)

func providePipeline() *timeline.Pipeline {
	return timeline.NewPipeline(timeline.RenderParams{})
}

func provideLocalMediaService(log *slog.Logger, cfg config.Config) *asset.Service {
	dataRoot := cfg.Workspace.DataRoot
	if strings.TrimSpace(dataRoot) == "" {
		dataRoot = config.DefaultDataRoot
	}
	return asset.NewLocalService(log, filepath.Join(dataRoot, "media"))
}

func provideEventStore(log *slog.Logger, store timeline.Store) *timeline.EventStore {
	return timeline.NewEventStore(log, store)
}

func provideDiscussDriver(log *slog.Logger, eventStore *timeline.EventStore, msgService *message.DBService) *discuss.DiscussDriver {
	return discuss.NewDiscussDriver(discuss.DiscussDriverDeps{
		MessageService: msgService,
		CursorStore:    eventStore,
		Logger:         log,
	})
}

func provideChannelIdentityBindingStore(pool *pgxpool.Pool) gateway.IdentityBindingStore {
	return apiassembly.NewChannelIdentityBindingStore(pool)
}

type channelBotPresence struct {
	bots apiassembly.BotPersistence
}

func (p channelBotPresence) EnsureBot(ctx context.Context, botID string) error {
	_, err := p.bots.GetBotByID(ctx, botID)
	return err
}

func (p channelBotPresence) TouchBot(ctx context.Context, botID string) error {
	return p.bots.TouchBot(ctx, botID)
}

func provideChannelPostgresStore(
	pool *pgxpool.Pool,
	identityBindings gateway.IdentityBindingStore,
	identityLinks access.Store,
	routeSessions *agentassembly.RouteSessionCoordinator,
	botsStore apiassembly.BotPersistence,
) *channelpostgres.Store {
	return channelpostgres.NewStore(
		channelsqlc.New(pool),
		identityBindings,
		identityLinks,
		routeSessions,
		channelBotPresence{bots: botsStore},
	)
}

func provideChannelPersistence(store *channelpostgres.Store) gateway.Persistence {
	return store
}

func provideChannelIdentityStore(store *channelpostgres.Store) identity.Store {
	return store
}

// ProvideConversationProjectionReader exposes Channel-owned route metadata
// without leaking its PostgreSQL adapter into command composition.
func ProvideConversationProjectionReader(store *channelpostgres.Store) channel.ConversationProjectionReader {
	return store
}

func provideRouteService(log *slog.Logger, store *channelpostgres.Store) *route.DBService {
	return route.NewService(log, store)
}

func provideUsageService(pool *pgxpool.Pool, models *modelcatalog.Service, providersService *providers.Service) *usage.Service {
	modelReader := modelassembly.NewUsageModelReader(models, providersService)
	return agentassembly.NewUsageReader(pool, modelReader)
}

func provideUsageReader(service *usage.Service) usage.Reader {
	return service
}

func provideCommandQueries(service *usage.Service) usage.CommandReader {
	return service
}

func provideEmailOAuthTokenStore(pool *pgxpool.Pool) emailpkg.OAuthTokenStore {
	return emailpkg.NewPostgresOAuthTokenStore(pool)
}

func provideEmailService(log *slog.Logger, pool *pgxpool.Pool, registry *emailpkg.Registry) *emailpkg.Service {
	return emailpkg.NewPostgresService(log, pool, registry)
}

func provideEmailOutboxService(log *slog.Logger, pool *pgxpool.Pool) *emailpkg.OutboxService {
	return emailpkg.NewPostgresOutboxService(log, pool)
}

func provideLocalChannelRegistry(hub *local.RouteHub) *gateway.Registry {
	registry := gateway.NewRegistry()
	registry.MustRegister(local.NewWebAdapter(hub))
	return registry
}

// Optional adapter hooks are applied by duck typing so assembly does not import
// concrete platform packages (required for split Server dependency closure).
type qqIdentityResolver interface {
	GetByID(context.Context, string) (identity.ChannelIdentity, error)
	ListCanonicalChannelIdentities(context.Context, string) ([]identity.ChannelIdentity, error)
}

type qqRouteResolver interface {
	GetByID(context.Context, string) (route.Route, error)
}

type qqAdapterHooks interface {
	SetChannelIdentityResolver(qqIdentityResolver)
	SetRouteResolver(qqRouteResolver)
}

type matrixSyncHooks interface {
	SetSyncStateSaver(func(context.Context, string, string) error)
}

func wireOptionalPlatformAdapters(registry *gateway.Registry, identityService *identity.Service, routeService *route.DBService, channelStore *gateway.Store) {
	for _, adapter := range registry.List() {
		if hooks, ok := adapter.(qqAdapterHooks); ok {
			hooks.SetChannelIdentityResolver(identityService)
			hooks.SetRouteResolver(routeService)
		}
		if hooks, ok := adapter.(matrixSyncHooks); ok {
			hooks.SetSyncStateSaver(channelStore.SaveMatrixSyncSinceToken)
		}
	}
}

func provideChannelRouter(
	log *slog.Logger,
	registry *gateway.Registry,
	hub *local.RouteHub,
	routeService *route.DBService,
	sessionService *sessionpkg.Service,
	msgService *message.DBService,
	turnService agentdomain.Service,
	identityService *identity.Service,
	botService *bot.Service,
	accountService *account.Service,
	aclService *acl.Service,
	policyService *policy.Service,
	mediaService *asset.Service,
	audioService channelAudio,
	settingsService channelSettings,
	pipeline *timeline.Pipeline,
	eventStore *timeline.EventStore,
	discussDriver *discuss.DiscussDriver,
	cfg config.Config,
	cmdHandler inbound.CommandHandler,
	skillResolver inbound.RequestedSkillResolver,
	channelStore *gateway.Store,
) *inbound.ChannelInboundProcessor {
	wireOptionalPlatformAdapters(registry, identityService, routeService, channelStore)
	threadCoordinator := route.NewThreadCoordinator(log, routeService, sessionService)
	processor := inbound.NewChannelInboundProcessor(log, registry, routeService, msgService, turnService, identityService, policyService, cfg.Auth.JWTSecret, 5*time.Minute)
	processor.SetSessionEnsurer(&sessionEnsurerAdapter{coordinator: threadCoordinator})
	processor.SetPipeline(pipeline, eventStore, discussDriver)
	discussDriver.SetTurnService(turnService)
	discussDriver.SetBroadcaster(hub)
	processor.SetACLService(aclService)
	processor.SetMediaService(mediaService)
	processor.SetStreamObserver(local.NewRouteHubBroadcaster(hub))
	processor.SetDispatcher(inbound.NewRouteDispatcher(log))
	processor.SetSpeechService(audioService, &settingsSpeechModelResolver{settings: settingsService})
	processor.SetTranscriptionService(audioService, &settingsTranscriptionModelResolver{settings: settingsService})
	processor.SetIMDisplayOptions(&settingsIMDisplayOptions{settings: settingsService})
	processor.SetDefaultChatRuntime(&settingsDefaultChatRuntime{settings: settingsService})
	processor.SetACPAgentSetupReader(&botACPAgentSetupReader{bots: botService})
	processor.SetACPProfileResolver(profile.NewCatalog())
	processor.SetBotPermissionChecker(&botPermissionCheckerAdapter{bots: botService, accounts: accountService})
	processor.SetCommandHandler(cmdHandler)
	processor.SetRequestedSkillResolver(skillResolver)
	return processor
}

func provideCommandHandler(
	log *slog.Logger,
	botService *bot.Service,
	channelAccessService *access.Service,
	scheduleService *schedule.Service,
	settingsService *setting.Service,
	mcpConnService *mcp.ConnectionService,
	modelsService *modelcatalog.Service,
	modelProviderResolver modelcatalog.ProviderResolver,
	providersService *providers.Service,
	memProvService *memcatalog.Service,
	searchProvService *search.Service,
	emailService *emailpkg.Service,
	emailOutboxService *emailpkg.OutboxService,
	heartbeatService *heartbeat.Service,
	queries command.CommandQueries,
	aclService *acl.Service,
	containerdHandler *runtimehttp.ContainerdHandler,
	provider bridge.Provider,
	compactionService *compaction.Service,
) *command.Handler {
	cmdHandler := command.NewHandler(
		log,
		&command.BotMemberRoleAdapter{BotService: botService, ManageResolver: channelAccessService},
		scheduleService,
		settingsService,
		mcpConnService,
		modelsService,
		providersService,
		memProvService,
		searchProvService,
		emailService,
		emailOutboxService,
		heartbeatService,
		queries,
		aclService,
		&commandSkillLoaderAdapter{handler: containerdHandler},
		&commandContainerFSAdapter{provider: provider},
		modelProviderResolver,
	)
	cmdHandler.SetCompactionService(compactionService)
	cmdHandler.SetLinkConsumer(channelAccessService)
	return cmdHandler
}

func provideChannelManager(log *slog.Logger, registry *gateway.Registry, channelStore *gateway.Store, channelRouter *inbound.ChannelInboundProcessor, mediaService *asset.Service) *gateway.Manager {
	mgr := gateway.NewManager(log, registry, channelStore, channelRouter)
	mgr.SetAttachmentStore(mediaService)
	if mw := channelRouter.IdentityMiddleware(); mw != nil {
		mgr.Use(mw)
	}
	channelRouter.SetReactor(mgr)
	return mgr
}

func provideChannelLifecycleService(channelStore *gateway.Store, channelManager *gateway.Manager) *gateway.Lifecycle {
	return gateway.NewLifecycle(channelStore, channelManager)
}

func startWebhookTunnel(lc fx.Lifecycle, manager webhook.Manager) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			return manager.Start(ctx)
		},
		OnStop: func(stopCtx context.Context) error {
			err := manager.Stop(stopCtx)
			cancel()
			return err
		},
	})
}

func startWebhookTunnelListener(lc fx.Lifecycle, log *slog.Logger, cfg config.Config, store *gateway.Store, channelManager *gateway.Manager, mediaService *asset.Service, emailService *emailpkg.Service, emailManager *emailpkg.Manager, emailTrigger *emailpkg.Trigger) {
	if cfg.WebhookTunnel.EffectiveMode() == config.WebhookTunnelModeDisabled {
		return
	}
	addr := strings.TrimSpace(cfg.WebhookTunnel.ListenAddr)
	if addr == "" {
		addr = webhook.DefaultListenAddr
	}
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	httpx.HardenServer(e.Server)
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimit("1M"))
	e.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok\n")
	})
	gateway.NewWebhookServerHandler(log, store, channelManager).Register(e)
	channelhttphost.NewEmailWebhookHandler(log, emailService, emailManager, emailTrigger).Register(e)
	// This listener is only started for tunnel modes. Its public base URL is
	// resolved from either configured public_base_url or the running tunnel, so
	// the configured-public-base gate used by the main server is intentionally
	// not applied here.
	channelhttphost.NewPublicMediaHandler(log, mediaService, cfg.Auth.JWTSecret).Register(e)
	logger := log.With(slog.String("component", "webhook_tunnel_listener"), slog.String("addr", addr))
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
			if err != nil {
				return fmt.Errorf("webhook tunnel listener: %w", err)
			}
			go func() {
				logger.InfoContext(ctx, "webhook tunnel listener started")
				if err := e.Server.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.ErrorContext(ctx, "webhook tunnel listener failed", slog.Any("error", err))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return e.Shutdown(ctx)
		},
	})
}

type sessionEnsurerAdapter struct {
	coordinator *route.ThreadCoordinator
}

func (a *sessionEnsurerAdapter) EnsureActiveSession(ctx context.Context, botID, routeID, channelType string) (inbound.SessionResult, error) {
	sess, err := a.coordinator.EnsureActive(ctx, botID, routeID, channelType)
	if err != nil {
		return inbound.SessionResult{}, err
	}
	return inboundSessionResult(sess), nil
}

func (a *sessionEnsurerAdapter) GetActiveSession(ctx context.Context, routeID string) (inbound.SessionResult, error) {
	sess, err := a.coordinator.GetActive(ctx, routeID)
	if err != nil {
		return inbound.SessionResult{}, err
	}
	return inboundSessionResult(sess), nil
}

func (a *sessionEnsurerAdapter) CreateNewSession(ctx context.Context, botID, routeID, channelType string, spec inbound.NewSessionSpec) (inbound.SessionResult, error) {
	createdByUserID := newSessionCreatedByUserID(spec)
	sess, err := a.coordinator.CreateNew(ctx, sessionpkg.CreateInput{
		BotID:           botID,
		RouteID:         routeID,
		ChannelType:     channelType,
		Type:            spec.Type,
		SessionMode:     spec.Mode,
		RuntimeType:     spec.Runtime,
		Metadata:        spec.Metadata,
		RuntimeMetadata: spec.Metadata,
		Title:           spec.Title,
		CreatedByUserID: createdByUserID,
	})
	if err != nil {
		return inbound.SessionResult{}, err
	}
	return inboundSessionResult(sess), nil
}

func newSessionCreatedByUserID(spec inbound.NewSessionSpec) string {
	if userID := strings.TrimSpace(spec.CreatedByUserID); userID != "" {
		return userID
	}
	return strings.TrimSpace(spec.RuntimeOwnerAccountID)
}

func inboundSessionResult(sess sessionpkg.Thread) inbound.SessionResult {
	return inbound.SessionResult{
		ID:                    sess.ID,
		Type:                  sess.Type,
		Mode:                  sess.SessionMode,
		Runtime:               sess.RuntimeType,
		RuntimeOwnerAccountID: sessionRuntimeOwnerAccountID(sess),
	}
}

func sessionRuntimeOwnerAccountID(sess sessionpkg.Thread) string {
	if value := runtimeMetadataString(sess.RuntimeMetadata, "runtime_owner_account_id"); value != "" {
		return value
	}
	return runtimeMetadataString(sess.Metadata, "runtime_owner_account_id")
}

func runtimeMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

type settingsSpeechModelResolver struct {
	settings channelSettings
}

func (r *settingsSpeechModelResolver) ResolveSpeechModelID(ctx context.Context, botID string) (string, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return "", err
	}
	return s.TtsModelID, nil
}

type settingsIMDisplayOptions struct {
	settings channelSettings
}

func (r *settingsIMDisplayOptions) ShowToolCallsInIM(ctx context.Context, botID string) (bool, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return false, err
	}
	return s.ShowToolCallsInIM, nil
}

type settingsDefaultChatRuntime struct {
	settings channelSettings
}

func (r *settingsDefaultChatRuntime) DefaultChatRuntime(ctx context.Context, botID string) (inbound.DefaultChatRuntimeSettings, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return inbound.DefaultChatRuntimeSettings{}, err
	}
	return inbound.DefaultChatRuntimeSettings{
		Runtime:     s.ChatRuntime,
		ACPAgentID:  s.ChatACPAgentID,
		ProjectPath: s.ChatACPProjectPath,
		ProjectMode: s.ChatACPProjectMode,
	}, nil
}

type botACPAgentSetupReader struct {
	bots *bot.Service
}

func (r *botACPAgentSetupReader) ACPAgentSetupMetadata(ctx context.Context, botID string) (map[string]any, error) {
	if r == nil || r.bots == nil {
		return nil, errors.New("bot setup reader not configured")
	}
	bot, err := r.bots.Get(ctx, botID)
	if err != nil {
		return nil, err
	}
	return bot.Metadata, nil
}

type botPermissionCheckerAdapter struct {
	bots     *bot.Service
	accounts *account.Service
}

func (a *botPermissionCheckerAdapter) HasBotPermission(ctx context.Context, botID, accountID, permission string) (bool, error) {
	if a == nil || a.bots == nil || a.accounts == nil {
		return false, errors.New("bot permission services not configured")
	}
	isAdmin, err := a.accounts.IsAdmin(ctx, accountID)
	if err != nil {
		return false, err
	}
	perms, err := a.bots.ResolveUserPermissions(ctx, botID, accountID, isAdmin)
	if err != nil {
		return false, err
	}
	return bot.HasPermission(perms, permission), nil
}

type settingsTranscriptionModelResolver struct {
	settings channelSettings
}

func (r *settingsTranscriptionModelResolver) ResolveTranscriptionModelID(ctx context.Context, botID string) (string, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return "", err
	}
	return s.TranscriptionModelID, nil
}

type channelAudio interface {
	Synthesize(ctx context.Context, modelID string, text string, overrideCfg map[string]any) ([]byte, string, error)
	Transcribe(ctx context.Context, modelID string, audio []byte, filename string, contentType string, overrideCfg map[string]any) (inbound.TranscriptionResult, error)
}

type localChannelAudio struct{ service *audiopkg.Service }

func (a *localChannelAudio) Synthesize(ctx context.Context, modelID, text string, overrideCfg map[string]any) ([]byte, string, error) {
	return a.service.Synthesize(ctx, modelID, text, overrideCfg)
}

func (a *localChannelAudio) Transcribe(ctx context.Context, modelID string, data []byte, filename, contentType string, overrideCfg map[string]any) (inbound.TranscriptionResult, error) {
	result, err := a.service.Transcribe(ctx, modelID, data, filename, contentType, overrideCfg)
	if err != nil {
		return nil, err
	}
	return inboundTranscriptionResult{text: result.Text}, nil
}

func provideLocalChannelAudio(service *audiopkg.Service) channelAudio {
	return &localChannelAudio{service: service}
}

func provideLocalCommandHandler(handler *command.Handler) inbound.CommandHandler { return handler }

func provideLocalSkillResolver(handler *runtimehttp.ContainerdHandler) inbound.RequestedSkillResolver {
	return handler
}

type channelSettings interface {
	GetBot(context.Context, string) (setting.Settings, error)
}

func provideLocalChannelSettings(service *setting.Service) channelSettings { return service }

func provideStandaloneChannelSettings(log *slog.Logger, pool *pgxpool.Pool, aclService *acl.Service) channelSettings {
	store := apiassembly.NewSettingPersistence(pool)
	return setting.NewService(log, store, nil, aclService, nil)
}

func provideEmailRegistry(log *slog.Logger, tokenStore emailpkg.OAuthTokenStore, oauthClients *oauth.Registry) *emailpkg.Registry {
	return emailpkg.NewDefaultRegistry(log, tokenStore, emailOAuthClientResolver{inner: oauthClients})
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

type emailBotReader interface {
	Get(context.Context, string) (bot.Bot, error)
}

func provideEmailChatGateway(turnService agentdomain.Service, botService *bot.Service, cfg config.Config, log *slog.Logger) emailpkg.ChatTriggerer {
	return &emailTurnGateway{turnService: turnService, bots: botService, jwtSecret: cfg.Auth.JWTSecret, logger: log}
}

type emailTurnGateway struct {
	turnService agentdomain.Service
	bots        emailBotReader
	jwtSecret   string
	logger      *slog.Logger
}

func (g *emailTurnGateway) TriggerBotChat(ctx context.Context, botID, content string) error {
	bot, err := g.bots.Get(ctx, botID)
	if err != nil {
		return fmt.Errorf("get bot: %w", err)
	}
	ownerID := bot.OwnerUserID
	token, _, err := auth.GenerateToken(ownerID, g.jwtSecret, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("generate email turn token: %w", err)
	}
	handle, err := g.turnService.StartTurn(ctx, agentdomain.StartTurnCommand{
		SchemaVersion:  1,
		TeamID:         team.DefaultTeamID,
		Mode:           agentdomain.ModeChat,
		BotID:          botID,
		ChatID:         botID,
		UserID:         ownerID,
		Token:          "Bearer " + token,
		Query:          content,
		CurrentChannel: "email",
	})
	if err != nil {
		return fmt.Errorf("start email turn: %w", err)
	}
	defer handle.Cancel()
	events, errs := handle.Events(), handle.Errs()
	for events != nil || errs != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case runErr, ok := <-errs:
			if ok && runErr != nil {
				return runErr
			}
			if !ok {
				errs = nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func provideEmailTrigger(log *slog.Logger, service *emailpkg.Service, chatTriggerer emailpkg.ChatTriggerer) *emailpkg.Trigger {
	return emailpkg.NewTrigger(log, service, chatTriggerer)
}

func startEmailManager(lc fx.Lifecycle, emailManager *emailpkg.Manager) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				if err := emailManager.Start(ctx); err != nil {
					slog.Default().ErrorContext(ctx, "email manager start failed", slog.Any("error", err))
				}
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			emailManager.Stop(stopCtx)
			return nil
		},
	})
}

func startChannelManager(lc fx.Lifecycle, channelManager *gateway.Manager) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			channelManager.Start(ctx)
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			return channelManager.Shutdown(stopCtx)
		},
	})
}

type commandSkillLoaderAdapter struct {
	handler *runtimehttp.ContainerdHandler
}

func (a *commandSkillLoaderAdapter) LoadSkills(ctx context.Context, botID string) ([]command.Skill, error) {
	items, err := a.handler.LoadSkills(ctx, botID)
	if err != nil {
		return nil, err
	}
	skills := make([]command.Skill, len(items))
	for i, item := range items {
		skills[i] = command.Skill{Name: item.Name, Description: item.Description}
	}
	return skills, nil
}

// ListRuntimeSkills exposes the runtime-usable safe catalog (the same list the
// Web slash picker shows) as the command layer's optional RuntimeSkillLister
// capability, upgrading /skill list to tap-to-activate rows.
func (a *commandSkillLoaderAdapter) ListRuntimeSkills(ctx context.Context, botID string) ([]command.Skill, error) {
	items, err := a.handler.ListSafeSkillCatalog(ctx, botID)
	if err != nil {
		return nil, err
	}
	skills := make([]command.Skill, len(items))
	for i, item := range items {
		skills[i] = command.Skill{Name: item.Name, Description: item.Description}
	}
	return skills, nil
}

type commandContainerFSAdapter struct {
	provider bridge.Provider
}

func (a *commandContainerFSAdapter) ListDir(ctx context.Context, botID, dirPath string) ([]command.FSEntry, error) {
	client, err := a.provider.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	entries, err := client.ListDirAll(ctx, dirPath, false)
	if err != nil {
		return nil, err
	}
	result := make([]command.FSEntry, len(entries))
	for i, e := range entries {
		name := stdpath.Base(e.GetPath())
		result[i] = command.FSEntry{Name: name, IsDir: e.GetIsDir(), Size: e.GetSize()}
	}
	return result, nil
}

func (a *commandContainerFSAdapter) ReadFile(ctx context.Context, botID, filePath string) (string, error) {
	client, err := a.provider.MCPClient(ctx, botID)
	if err != nil {
		return "", err
	}
	resp, err := client.ReadFile(ctx, filePath, 0, 0)
	if err != nil {
		return "", err
	}
	return resp.GetContent(), nil
}

type inboundTranscriptionResult struct {
	text string
}

func (r inboundTranscriptionResult) GetText() string { return r.text }
