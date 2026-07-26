package channel

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	agentusage "github.com/memohai/memoh/domains/agent/chat/usage"
	usagepersistence "github.com/memohai/memoh/domains/agent/chat/usage/persistence"
	"github.com/memohai/memoh/domains/agent/command"
	"github.com/memohai/memoh/domains/agent/mcp"
	"github.com/memohai/memoh/domains/api/bot"
	botaccess "github.com/memohai/memoh/domains/api/bot/access"
	"github.com/memohai/memoh/domains/api/bot/access/acl"
	"github.com/memohai/memoh/domains/api/bot/access/policy"
	"github.com/memohai/memoh/domains/api/bot/setting"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	runtimehttp "github.com/memohai/memoh/domains/api/http/runtime"
	apibinding "github.com/memohai/memoh/domains/api/identity/binding"
	identitylink "github.com/memohai/memoh/domains/api/identity/link"
	linkpersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"
	channeldomain "github.com/memohai/memoh/domains/channel"
	channeladapter "github.com/memohai/memoh/domains/channel/adapter"
	channelassembly "github.com/memohai/memoh/domains/channel/assembly"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/inbound"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/domains/media/asset"
	memcatalog "github.com/memohai/memoh/domains/memory/catalog"
	audiopkg "github.com/memohai/memoh/domains/model/audio"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	modelprojection "github.com/memohai/memoh/domains/model/projection"
	providers "github.com/memohai/memoh/domains/model/provider"
	"github.com/memohai/memoh/domains/model/search"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	"github.com/memohai/memoh/internal/config"
)

func providePipeline() *timeline.Pipeline {
	return timeline.NewPipeline(timeline.RenderParams{})
}

func provideEventStore(log *slog.Logger, store timeline.Store) *timeline.EventStore {
	return timeline.NewEventStore(log, store)
}

func provideDiscussDriver(
	log *slog.Logger,
	turns agentdomain.Service,
	eventStore *timeline.EventStore,
	msgService *message.DBService,
	artifacts compaction.ArtifactStore,
	hub *local.RouteHub,
) inbound.DiscussDriver {
	return channelassembly.NewDiscussDriver(
		log,
		turns,
		msgService,
		eventStore,
		compaction.NewTimelineArtifactSource(artifacts),
		hub,
	)
}

func provideTimelineStore(pool *pgxpool.Pool) timeline.Store {
	return channelassembly.NewPostgresTimelineStore(pool)
}

func provideChannelIdentityBindingStore(pool *pgxpool.Pool) gateway.IdentityBindingStore {
	return apibinding.NewPostgresStore(pool)
}

func provideChannelPostgresStore(
	pool *pgxpool.Pool,
	identityBindings gateway.IdentityBindingStore,
	identityLinks linkpersistence.Store,
	routeSessions *agentassembly.RouteSessionCoordinator,
	botsStore bot.Persistence,
) channelassembly.Store {
	return channelassembly.NewPostgresStore(
		pool,
		identityBindings,
		identityLinks,
		routeSessions,
		channeladapter.NewBotPresence(botsStore),
	)
}

func provideChannelPersistence(store channelassembly.Store) gateway.Persistence {
	return store
}

func provideChannelIdentityStore(store channelassembly.Store) identity.Store {
	return store
}

func provideConversationProjectionReader(store channelassembly.Store) channeldomain.ConversationProjectionReader {
	return store
}

func provideRouteService(log *slog.Logger, store channelassembly.Store) *route.DBService {
	return route.NewService(log, store)
}

func provideUsageService(pool *pgxpool.Pool, models *modelcatalog.Service, providersService *providers.Service) *agentusage.Service {
	modelReader := modelprojection.NewUsageModelReader(models, providersService)
	return agentusage.NewPostgresService(pool, modelReader)
}

func provideUsageReader(service *agentusage.Service) usagepersistence.Reader {
	return service
}

func provideCommandQueries(service *agentusage.Service) usagepersistence.CommandReader {
	return service
}

// NewLocalRegistry constructs the split Server registry. It intentionally
// contains only the in-process Web adapter.
func NewLocalRegistry(hub *local.RouteHub) *gateway.Registry {
	registry := gateway.NewRegistry()
	registry.MustRegister(local.NewWebAdapter(hub))
	return registry
}

type channelRouterParams struct {
	fx.In

	Log        *slog.Logger
	Registry   *gateway.Registry
	Hub        *local.RouteHub
	Routes     *route.DBService
	Sessions   *sessionpkg.Service
	Messages   *message.DBService
	Turns      agentdomain.Service
	Identities *identity.Service
	Bots       *bot.Service
	Accounts   *account.Service
	ACL        *acl.Service
	Policy     *policy.Service
	Media      *asset.Service
	Audio      channeladapter.Audio
	Settings   channeladapter.Settings
	Pipeline   *timeline.Pipeline
	Events     *timeline.EventStore
	Discuss    inbound.DiscussDriver
	Config     config.Config
	Commands   inbound.CommandHandler
	Skills     inbound.RequestedSkillResolver
}

func provideChannelRouter(params channelRouterParams) *inbound.ChannelInboundProcessor {
	threadCoordinator := route.NewThreadCoordinator(params.Log, params.Routes, params.Sessions)
	processor := inbound.NewChannelInboundProcessor(params.Log, params.Registry, params.Routes, params.Messages, params.Turns, params.Identities, params.Policy, params.Config.Auth.JWTSecret, 5*time.Minute)
	processor.SetSessionEnsurer(channeladapter.NewSessionEnsurer(threadCoordinator))
	processor.SetPipeline(params.Pipeline, params.Events, params.Discuss)
	processor.SetACLService(params.ACL)
	processor.SetMediaService(params.Media)
	processor.SetStreamObserver(local.NewRouteHubBroadcaster(params.Hub))
	processor.SetDispatcher(inbound.NewRouteDispatcher(params.Log))
	processor.SetSpeechService(params.Audio, channeladapter.NewSpeechModelResolver(params.Settings))
	processor.SetTranscriptionService(params.Audio, channeladapter.NewTranscriptionModelResolver(params.Settings))
	processor.SetIMDisplayOptions(channeladapter.NewIMDisplayOptions(params.Settings))
	processor.SetDefaultChatRuntime(channeladapter.NewDefaultChatRuntime(params.Settings))
	processor.SetACPAgentSetupReader(channeladapter.NewACPAgentSetupReader(params.Bots))
	processor.SetACPProfileResolver(profile.NewCatalog())
	processor.SetBotPermissionChecker(channeladapter.NewBotPermissionChecker(params.Bots, params.Accounts))
	processor.SetCommandHandler(params.Commands)
	processor.SetRequestedSkillResolver(params.Skills)
	return processor
}

type commandHandlerParams struct {
	fx.In

	Log             *slog.Logger
	Bots            *bot.Service
	BotAccess       *botaccess.Service
	IdentityLinks   *identitylink.Service
	Schedules       *schedule.Service
	Settings        *setting.Service
	MCP             *mcp.ConnectionService
	Models          *modelcatalog.Service
	ModelResolver   modelcatalog.ProviderResolver
	Providers       *providers.Service
	MemoryProviders *memcatalog.Service
	SearchProviders *search.Service
	Email           *emailpkg.Service
	EmailOutbox     *emailpkg.OutboxService
	Heartbeat       *heartbeat.Service
	Queries         command.CommandQueries
	ACL             *acl.Service
	RuntimeHandler  *runtimehttp.ContainerdHandler
	Bridge          bridge.Provider
	Compaction      *compaction.Service
}

func provideCommandHandler(params commandHandlerParams) *command.Handler {
	cmdHandler := command.NewHandler(
		params.Log,
		&command.BotMemberRoleAdapter{BotService: params.Bots, ManageResolver: params.BotAccess},
		params.Schedules,
		params.Settings,
		params.MCP,
		params.Models,
		params.Providers,
		params.MemoryProviders,
		params.SearchProviders,
		params.Email,
		params.EmailOutbox,
		params.Heartbeat,
		params.Queries,
		params.ACL,
		channeladapter.NewCommandSkillLoader(params.RuntimeHandler),
		channeladapter.NewCommandContainerFS(params.Bridge),
		params.ModelResolver,
	)
	cmdHandler.SetCompactionService(params.Compaction)
	cmdHandler.SetLinkConsumer(params.IdentityLinks)
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

func provideLocalChannelAudio(service *audiopkg.Service) channeladapter.Audio {
	return channeladapter.NewLocalAudio(service)
}

func provideLocalCommandHandler(handler *command.Handler) inbound.CommandHandler { return handler }

func provideLocalSkillResolver(handler *runtimehttp.ContainerdHandler) inbound.RequestedSkillResolver {
	return handler
}

func provideLocalChannelSettings(service *setting.Service) channeladapter.Settings { return service }
