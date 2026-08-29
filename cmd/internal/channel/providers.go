// Package channel assembles the shared command-side Channel module:
// registry/manager/processor, discuss pipeline, email, and webhook tunnel.
package channel

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

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.uber.org/fx"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/acl"
	acpprofileadapter "github.com/felinics/memoh/internal/agent/adapter/acpprofile"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/turn"
	audiopkg "github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/dingtalk"
	"github.com/felinics/memoh/internal/channel/adapters/discord"
	"github.com/felinics/memoh/internal/channel/adapters/feishu"
	"github.com/felinics/memoh/internal/channel/adapters/line"
	"github.com/felinics/memoh/internal/channel/adapters/local"
	"github.com/felinics/memoh/internal/channel/adapters/matrix"
	"github.com/felinics/memoh/internal/channel/adapters/misskey"
	"github.com/felinics/memoh/internal/channel/adapters/qq"
	slackadapter "github.com/felinics/memoh/internal/channel/adapters/slack"
	"github.com/felinics/memoh/internal/channel/adapters/telegram"
	"github.com/felinics/memoh/internal/channel/adapters/wechatoa"
	"github.com/felinics/memoh/internal/channel/adapters/wecom"
	"github.com/felinics/memoh/internal/channel/adapters/weixin"
	"github.com/felinics/memoh/internal/channel/discuss"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/inbound"
	"github.com/felinics/memoh/internal/channel/publicmedia"
	"github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/channelaccess"
	"github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	emailpkg "github.com/felinics/memoh/internal/email"
	emailgeneric "github.com/felinics/memoh/internal/email/adapters/generic"
	emailgmail "github.com/felinics/memoh/internal/email/adapters/gmail"
	emailmailgun "github.com/felinics/memoh/internal/email/adapters/mailgun"
	"github.com/felinics/memoh/internal/handlers"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/media"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthclients"
	"github.com/felinics/memoh/internal/policy"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/schedule"
	"github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/storage/providers/localfs"
	"github.com/felinics/memoh/internal/team"
	"github.com/felinics/memoh/internal/webhooktunnel"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func providePipeline() *timeline.Pipeline {
	return timeline.NewPipeline(timeline.RenderParams{})
}

func provideLocalMediaService(log *slog.Logger, cfg config.Config) *media.Service {
	dataRoot := cfg.Workspace.DataRoot
	if strings.TrimSpace(dataRoot) == "" {
		dataRoot = config.DefaultDataRoot
	}
	return media.NewService(log, localfs.New(filepath.Join(dataRoot, "media")))
}

func provideEventStore(log *slog.Logger, queries dbstore.Queries) *timeline.EventStore {
	return timeline.NewEventStore(log, queries)
}

func provideDiscussDriver(log *slog.Logger, eventStore *timeline.EventStore, msgService *message.DBService, queries dbstore.Queries, cfg config.Config) *discuss.DiscussDriver {
	return discuss.NewDiscussDriver(discuss.DiscussDriverDeps{
		MessageService:     msgService,
		CursorStore:        eventStore,
		Artifacts:          compaction.NewTimelineArtifactSource(queries),
		Logger:             log,
		AdmissionMaxTokens: cfg.Agent.EffectiveContextAbsoluteMaxTokens(),
	})
}

func provideRouteService(log *slog.Logger, queries dbstore.Queries) *route.DBService {
	return route.NewService(log, queries)
}

type channelRegistryParams struct {
	fx.In

	Log           *slog.Logger
	Config        config.Config
	Hub           *local.RouteHub
	MediaService  *media.Service
	TunnelManager *webhooktunnel.Manager `optional:"true"`
	UserInput     *userinput.Service
}

func provideChannelRegistry(params channelRegistryParams) *channel.Registry {
	log := params.Log
	cfg := params.Config
	hub := params.Hub
	mediaService := params.MediaService
	tunnelManager := params.TunnelManager
	userInput := params.UserInput
	registry := channel.NewRegistry()

	tgAdapter := telegram.NewTelegramAdapter(log)
	tgAdapter.SetAssetOpener(mediaService)
	// Telegram's ask_user buttons drive the durable interaction state.
	tgAdapter.SetUserInputService(userInput)
	registry.MustRegister(tgAdapter)

	discordAdapter := discord.NewDiscordAdapter(log)
	discordAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(discordAdapter)

	qqAdapter := qq.NewQQAdapter(log)
	qqAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(qqAdapter)

	matrixAdapter := matrix.NewMatrixAdapter(log)
	matrixAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(matrixAdapter)

	feishuAdapter := feishu.NewFeishuAdapter(log)
	feishuAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(feishuAdapter)

	slackAdapter := slackadapter.NewSlackAdapter(log)
	slackAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(slackAdapter)

	registry.MustRegister(wecom.NewWeComAdapter(log))

	dingTalkAdapter := dingtalk.NewDingTalkAdapter(log)
	registry.MustRegister(dingTalkAdapter)
	registry.MustRegister(wechatoa.NewWeChatOAAdapter(log))
	lineAdapter := line.NewAdapter(log)
	lineAdapter.SetPublicBaseURLProvider(newPublicMediaBaseProvider(cfg, tunnelManager))
	registry.MustRegister(lineAdapter)

	weixinAdapter := weixin.NewWeixinAdapter(log)
	weixinAdapter.SetAssetOpener(mediaService)
	registry.MustRegister(weixinAdapter)
	registry.MustRegister(local.NewWebAdapter(hub))
	registry.MustRegister(misskey.NewMisskeyAdapter(log))

	return registry
}

type publicMediaBaseProvider struct {
	tunnel *webhooktunnel.Manager
	signer *publicmedia.Signer
}

func newPublicMediaBaseProvider(cfg config.Config, tunnel *webhooktunnel.Manager) *publicMediaBaseProvider {
	return &publicMediaBaseProvider{
		tunnel: tunnel,
		signer: publicmedia.NewSigner(cfg.Auth.JWTSecret, publicmedia.SignedURLTTL),
	}
}

func (p *publicMediaBaseProvider) PublicBaseURL() string {
	if p == nil {
		return ""
	}
	if p.tunnel != nil {
		return p.tunnel.PublicBaseURL()
	}
	return ""
}

func (p *publicMediaBaseProvider) SignPublicMediaPath(path string) (string, bool) {
	if p == nil || p.signer == nil {
		return "", false
	}
	return p.signer.SignPath(path, time.Now().UTC())
}

func provideChannelRouter(
	log *slog.Logger,
	registry *channel.Registry,
	hub *local.RouteHub,
	routeService *route.DBService,
	sessionService *sessionpkg.Service,
	msgService *message.DBService,
	turnService turn.Service,
	identityService *identities.Service,
	botService *bots.Service,
	accountService *accounts.Service,
	aclService *acl.Service,
	policyService *policy.Service,
	mediaService *media.Service,
	audioService channelAudio,
	settingsService channelSettings,
	pipeline *timeline.Pipeline,
	eventStore *timeline.EventStore,
	discussDriver *discuss.DiscussDriver,
	cfg config.Config,
	cmdHandler inbound.CommandHandler,
	skillResolver inbound.RequestedSkillResolver,
) *inbound.ChannelInboundProcessor {
	adapter, ok := registry.Get(qq.Type)
	if !ok {
		panic("qq adapter not registered")
	}
	qqAdapter, ok := adapter.(*qq.QQAdapter)
	if !ok {
		panic("qq adapter has unexpected type")
	}
	qqAdapter.SetChannelIdentityResolver(identityService)
	qqAdapter.SetRouteResolver(routeService)

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
	processor.SetACPProfileResolver(acpprofileadapter.NewCatalog())
	processor.SetBotPermissionChecker(&botPermissionCheckerAdapter{bots: botService, accounts: accountService})
	processor.SetCommandHandler(cmdHandler)
	processor.SetRequestedSkillResolver(skillResolver)
	return processor
}

func provideCommandHandler(
	log *slog.Logger,
	botService *bots.Service,
	channelAccessService *channelaccess.Service,
	scheduleService *schedule.Service,
	settingsService *settings.Service,
	mcpConnService *mcp.ConnectionService,
	modelsService *models.Service,
	providersService *providers.Service,
	memProvService *memprovider.Service,
	searchProvService *searchproviders.Service,
	emailService *emailpkg.Service,
	emailOutboxService *emailpkg.OutboxService,
	queries dbstore.Queries,
	aclService *acl.Service,
	containerdHandler *handlers.ContainerdHandler,
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
		queries,
		aclService,
		&commandSkillLoaderAdapter{handler: containerdHandler},
		&commandContainerFSAdapter{provider: provider},
	)
	cmdHandler.SetCompactionService(compactionService, queries)
	cmdHandler.SetLinkConsumer(channelAccessService)
	return cmdHandler
}

func provideChannelManager(log *slog.Logger, registry *channel.Registry, channelStore *channel.Store, channelRouter *inbound.ChannelInboundProcessor, mediaService *media.Service) *channel.Manager {
	if adapter, ok := registry.Get(matrix.Type); ok {
		if matrixAdapter, ok := adapter.(*matrix.MatrixAdapter); ok {
			matrixAdapter.SetSyncStateSaver(channelStore.SaveMatrixSyncSinceToken)
		}
	}
	mgr := channel.NewManager(log, registry, channelStore, channelRouter)
	mgr.SetAttachmentStore(mediaService)
	if mw := channelRouter.IdentityMiddleware(); mw != nil {
		mgr.Use(mw)
	}
	channelRouter.SetReactor(mgr)
	return mgr
}

func provideChannelLifecycleService(channelStore *channel.Store, channelManager *channel.Manager) *channel.Lifecycle {
	return channel.NewLifecycle(channelStore, channelManager)
}

func startWebhookTunnel(lc fx.Lifecycle, manager *webhooktunnel.Manager) {
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

func startWebhookTunnelListener(lc fx.Lifecycle, log *slog.Logger, cfg config.Config, store *channel.Store, channelManager *channel.Manager, mediaService *media.Service, emailService *emailpkg.Service, emailManager *emailpkg.Manager, emailTrigger *emailpkg.Trigger) {
	if cfg.WebhookTunnel.EffectiveMode() == config.WebhookTunnelModeDisabled {
		return
	}
	addr := strings.TrimSpace(cfg.WebhookTunnel.ListenAddr)
	if addr == "" {
		addr = webhooktunnel.DefaultListenAddr
	}
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimit("1M"))
	e.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok\n")
	})
	channel.NewWebhookServerHandler(log, store, channelManager).Register(e)
	handlers.NewEmailWebhookHandler(log, emailService, emailManager, emailTrigger).Register(e)
	// This listener is only started for tunnel modes. Its public base URL is
	// resolved from either configured public_base_url or the running tunnel, so
	// the configured-public-base gate used by the main server is intentionally
	// not applied here.
	handlers.NewPublicMediaHandler(log, mediaService, cfg.Auth.JWTSecret).Register(e)
	logger := log.With(slog.String("component", "webhook_tunnel_listener"), slog.String("addr", addr))
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
			if err != nil {
				return fmt.Errorf("webhook tunnel listener: %w", err)
			}
			go func() {
				logger.Info("webhook tunnel listener started")
				if err := e.Server.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("webhook tunnel listener failed", slog.Any("error", err))
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
		BotAgentID:      spec.BotAgentID,
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
		BotAgentID:  s.DefaultBotAgentID,
		Runtime:     s.ChatRuntime,
		ACPAgentID:  s.ChatACPAgentID,
		ProjectPath: s.ChatACPProjectPath,
		ProjectMode: s.ChatACPProjectMode,
	}, nil
}

type botACPAgentSetupReader struct {
	bots *bots.Service
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
	bots     *bots.Service
	accounts *accounts.Service
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
	return bots.HasPermission(perms, permission), nil
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

func provideLocalSkillResolver(handler *handlers.ContainerdHandler) inbound.RequestedSkillResolver {
	return handler
}

type channelSettings interface {
	GetBot(context.Context, string) (settings.Settings, error)
}

func provideLocalChannelSettings(service *settings.Service) channelSettings { return service }

func provideStandaloneChannelSettings(log *slog.Logger, queries dbstore.Queries, aclService *acl.Service) channelSettings {
	return settings.NewService(log, queries, aclService, nil)
}

func provideEmailRegistry(log *slog.Logger, tokenStore *emailpkg.DBOAuthTokenStore, oauthClients *oauthclients.Registry) *emailpkg.Registry {
	reg := emailpkg.NewRegistry()
	reg.Register(emailgeneric.New(log))
	reg.Register(emailmailgun.New(log))
	reg.Register(emailgmail.New(log, tokenStore, oauthClients))
	return reg
}

func provideEmailChatGateway(turnService turn.Service, queries dbstore.Queries, sessionService *sessionpkg.Service, cfg config.Config, log *slog.Logger) emailpkg.ChatTriggerer {
	return &emailTurnGateway{turnService: turnService, queries: queries, sessions: sessionService, jwtSecret: cfg.Auth.JWTSecret, logger: log}
}

type emailTurnGateway struct {
	turnService turn.Service
	queries     dbstore.Queries
	sessions    *sessionpkg.Service
	jwtSecret   string
	logger      *slog.Logger
}

func (g *emailTurnGateway) TriggerBotChat(ctx context.Context, botID, content string) error {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	bot, err := g.queries.GetBotByID(ctx, pgBotID)
	if err != nil {
		return fmt.Errorf("get bot: %w", err)
	}
	ownerID := bot.OwnerUserID.String()
	token, _, err := auth.GenerateToken(ownerID, g.jwtSecret, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("generate email turn token: %w", err)
	}
	// Each inbound email runs in a thread of its own, which is what it already
	// meant by carrying no thread at all: no shared history with the previous
	// email. It needs a real one now because a turn is admitted against a thread,
	// and that is what gives the run an owner, a fence, and a terminal state.
	thread, err := g.sessions.Create(ctx, sessionpkg.CreateInput{
		BotID:       botID,
		ChannelType: "email",
		Type:        "chat",
	})
	if err != nil {
		return fmt.Errorf("create email turn thread: %w", err)
	}
	handle, err := g.turnService.StartTurn(ctx, turn.StartTurnCommand{
		SchemaVersion:  1,
		TeamID:         team.DefaultTeamID,
		Mode:           turn.ModeChat,
		BotID:          botID,
		ChatID:         botID,
		ThreadID:       thread.ID,
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
					slog.Default().Error("email manager start failed", slog.Any("error", err))
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

func startChannelManager(lc fx.Lifecycle, channelManager *channel.Manager) {
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
	handler *handlers.ContainerdHandler
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
