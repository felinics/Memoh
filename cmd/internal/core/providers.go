// Package core assembles the shared command-side domain and agent runtime
// providers used by the Memoh binaries (all-in-one, channel).
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	stdpath "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	agentdomain "github.com/memohai/memoh/domains/agent"
	acpagent "github.com/memohai/memoh/domains/agent/acp"
	acpclient "github.com/memohai/memoh/domains/agent/acp/client"
	"github.com/memohai/memoh/domains/agent/adapter/acp/profile"
	"github.com/memohai/memoh/domains/agent/adapter/acp/session"
	"github.com/memohai/memoh/domains/agent/adapter/channel/contact"
	identityadapter "github.com/memohai/memoh/domains/agent/adapter/channel/identity"
	"github.com/memohai/memoh/domains/agent/adapter/channel/messaging"
	"github.com/memohai/memoh/domains/agent/adapter/channel/thread"
	"github.com/memohai/memoh/domains/agent/application"
	applicationpostgres "github.com/memohai/memoh/domains/agent/application/postgres"
	agentassembly "github.com/memohai/memoh/domains/agent/assembly"
	"github.com/memohai/memoh/domains/agent/automation/heartbeat"
	heartbeatpostgres "github.com/memohai/memoh/domains/agent/automation/heartbeat/postgres"
	"github.com/memohai/memoh/domains/agent/automation/schedule"
	schedulepostgres "github.com/memohai/memoh/domains/agent/automation/schedule/postgres"
	chatbackuppostgres "github.com/memohai/memoh/domains/agent/chat/backup/postgres"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	compactionpostgres "github.com/memohai/memoh/domains/agent/chat/compaction/postgres"
	"github.com/memohai/memoh/domains/agent/chat/event"
	"github.com/memohai/memoh/domains/agent/chat/message"
	chatpostgres "github.com/memohai/memoh/domains/agent/chat/postgres"
	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/agent/chat/timeline"
	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	decisionpostgres "github.com/memohai/memoh/domains/agent/decision/postgres"
	"github.com/memohai/memoh/domains/agent/engine"
	"github.com/memohai/memoh/domains/agent/engine/background"
	agentpayload "github.com/memohai/memoh/domains/agent/event/payload"
	hookspkg "github.com/memohai/memoh/domains/agent/extension/hooks"
	pluginspkg "github.com/memohai/memoh/domains/agent/extension/plugins"
	"github.com/memohai/memoh/domains/agent/mcp"
	mcpfederation "github.com/memohai/memoh/domains/agent/mcp/sources/federation"
	"github.com/memohai/memoh/domains/agent/tool"
	"github.com/memohai/memoh/domains/api/access"
	"github.com/memohai/memoh/domains/api/access/acl"
	aclassembly "github.com/memohai/memoh/domains/api/access/acl/assembly"
	"github.com/memohai/memoh/domains/api/access/policy"
	apiassembly "github.com/memohai/memoh/domains/api/assembly"
	"github.com/memohai/memoh/domains/api/auth"
	"github.com/memohai/memoh/domains/api/bot"
	botbackup "github.com/memohai/memoh/domains/api/botbackup"
	apihttp "github.com/memohai/memoh/domains/api/http"
	agenthttp "github.com/memohai/memoh/domains/api/http/agent"
	runtimehttp "github.com/memohai/memoh/domains/api/http/runtime"
	"github.com/memohai/memoh/domains/api/setting"
	channeldomain "github.com/memohai/memoh/domains/channel"
	channelbackuppostgres "github.com/memohai/memoh/domains/channel/backup/postgres"
	"github.com/memohai/memoh/domains/channel/delivery"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/iam/account"
	iamassembly "github.com/memohai/memoh/domains/iam/assembly"
	team "github.com/memohai/memoh/domains/iam/team"
	"github.com/memohai/memoh/domains/media"
	"github.com/memohai/memoh/domains/media/asset"
	memoryassembly "github.com/memohai/memoh/domains/memory/assembly"
	memcatalog "github.com/memohai/memoh/domains/memory/catalog"
	memregistry "github.com/memohai/memoh/domains/memory/registry"
	modeldomain "github.com/memohai/memoh/domains/model"
	audiopkg "github.com/memohai/memoh/domains/model/audio"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	modelexecution "github.com/memohai/memoh/domains/model/execution"
	"github.com/memohai/memoh/domains/model/fetch"
	providers "github.com/memohai/memoh/domains/model/provider"
	"github.com/memohai/memoh/domains/model/search"
	"github.com/memohai/memoh/domains/model/template"
	videopkg "github.com/memohai/memoh/domains/model/video"
	runtimedomain "github.com/memohai/memoh/domains/runtime"
	runtimeassembly "github.com/memohai/memoh/domains/runtime/assembly"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	userruntime "github.com/memohai/memoh/domains/runtime/client"
	ctr "github.com/memohai/memoh/domains/runtime/container"
	runtimedisplay "github.com/memohai/memoh/domains/runtime/display"
	netctl "github.com/memohai/memoh/domains/runtime/network"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/logger"
	"github.com/memohai/memoh/internal/oauth"
	"github.com/memohai/memoh/internal/version"
)

func provideLogger(cfg config.Config) *slog.Logger {
	logger.Init(cfg.Log.Level, cfg.Log.Format)
	return logger.L
}

func provideTokenConfig(cfg config.Config) (auth.TokenConfig, error) {
	return auth.NewTokenConfig(cfg.Auth.JWTSecret, cfg.Auth.JWTExpiresIn)
}

func provideListenAddr(cfg config.Config) apihttp.ListenAddr {
	return apihttp.ResolveListenAddr(cfg.Server.Addr)
}

func provideContainerBackend(cfg config.Config) (ctr.Backend, error) {
	return ctr.ParseBackend(cfg.Container.Backend)
}

func provideRuntimeClock(cfg config.Config) (runtimedomain.Clock, error) {
	return runtimedomain.ResolveClock(cfg.Timezone)
}

func provideContainerService(lc fx.Lifecycle, log *slog.Logger, cfg config.Config, backend ctr.Backend) (ctr.Service, error) {
	svc, cleanup, err := runtimeassembly.NewService(context.Background(), runtimeassembly.Deps{
		Log:     log,
		Backend: backend.String(),
		Apple: runtimeassembly.AppleOptions{
			SocketPath: cfg.Apple.SocketPath,
			BinaryPath: cfg.Apple.BinaryPath,
		},
		Docker: runtimeassembly.DockerOptions{
			Host: cfg.Docker.Host,
		},
		Containerd: runtimeassembly.ContainerdOptions{
			SocketPath:   cfg.Containerd.SocketPath,
			Namespace:    cfg.Containerd.Namespace,
			RuntimeType:  cfg.Containerd.RuntimeType,
			CNIBinaryDir: cfg.Workspace.CNIBinaryDir,
			CNIConfigDir: cfg.Workspace.CNIConfigDir,
		},
	})
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			cleanup()
			return nil
		},
	})
	return svc, nil
}

func provideDisplayService(lc fx.Lifecycle, log *slog.Logger, manager workspace.Service) (runtimedisplay.Service, error) {
	svc, cleanup, err := runtimeassembly.NewDisplay(runtimeassembly.DisplayDeps{
		Log:       log,
		Workspace: manager,
	})
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			cleanup()
			return nil
		},
	})
	return svc, nil
}

func provideDBConn(lc fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	conn, err := db.Open(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	if conn == nil {
		return nil, nil
	}
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			conn.Close()
			return nil
		},
	})
	return conn, nil
}

func provideRuntimeSettingsStore(pool *pgxpool.Pool) apiassembly.SettingPersistence {
	return apiassembly.NewSettingPersistence(pool)
}

func provideSettingsModelReader(models *modelcatalog.Service) setting.ModelReader {
	return models
}

func provideSettingsService(log *slog.Logger, store apiassembly.SettingPersistence, models setting.ModelReader, aclService *acl.Service, networkService *netctl.Service) *setting.Service {
	return setting.NewService(log, store, models, aclService, networkService)
}

func provideWorkspaceRuntimeSettings(store apiassembly.SettingPersistence) workspace.BotRuntimeSettingsReader {
	return store
}

func provideBotPersistenceStore(pool *pgxpool.Pool) apiassembly.BotPersistence {
	return apiassembly.NewBotPersistence(pool)
}

func provideRuntimeContainerStore(pool *pgxpool.Pool) workspace.ContainerStore {
	return runtimeassembly.NewContainerStore(pool)
}

func provideBotUserReader(store account.Store) bot.UserReader {
	return apiassembly.NewBotUserReader(store)
}

func provideBotContainerReader(store workspace.ContainerStore) bot.ContainerReader {
	return apiassembly.NewBotContainerReader(store)
}

func provideWorkspaceBotProfiles(store apiassembly.BotPersistence) workspace.BotProfileStore {
	return store
}

// settingsNetworkConfigReader adapts API-owned settings overlay reads to the
// Runtime network ConfigReader port without importing settings concrete into
// the public network package.
type settingsNetworkConfigReader struct {
	store setting.Store
}

func (r settingsNetworkConfigReader) GetBotOverlayConfig(ctx context.Context, botID string) (netctl.BotOverlayConfig, error) {
	row, err := r.store.GetOverlay(ctx, botID)
	if err != nil {
		return netctl.BotOverlayConfig{}, err
	}
	return netctl.BotOverlayConfig{
		Enabled:  row.Enabled,
		Provider: row.Provider,
		Config:   decodeSettingsOverlayConfig(row.Config),
	}, nil
}

func decodeSettingsOverlayConfig(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil || config == nil {
		return map[string]any{}
	}
	return config
}

func provideNetwork(log *slog.Logger, store apiassembly.SettingPersistence, conn *pgxpool.Pool, service ctr.Service, backend ctr.Backend, cfg config.Config) (*netctl.Service, netctl.Controller, error) {
	assembled, err := runtimeassembly.NewNetwork(runtimeassembly.NetworkDeps{
		Log:          log,
		Container:    service,
		Backend:      backend.String(),
		ConfigReader: settingsNetworkConfigReader{store: store},
		Pool:         conn,
		CNIBinaryDir: cfg.Workspace.CNIBinaryDir,
		CNIConfigDir: cfg.Workspace.CNIConfigDir,
		DataRoot:     cfg.Workspace.DataRoot,
	})
	if err != nil {
		return nil, nil, err
	}
	return assembled.Service, assembled.Controller, nil
}

func provideAccountPersistenceStore(pool *pgxpool.Pool) account.Store {
	return iamassembly.NewAccountStore(pool)
}

func provideAccountCounter(pool *pgxpool.Pool) account.AccountCounter {
	return iamassembly.NewAccountCounter(pool)
}

func provideAccountTitleModelValidator(pool *pgxpool.Pool) account.TitleModelValidator {
	return modelcatalog.NewPostgresTitleModelValidator(pool)
}

func provideACLStore(pool *pgxpool.Pool) (acl.Store, error) {
	if pool == nil {
		return nil, acl.ErrTransactionsRequired
	}
	return aclassembly.NewStore(pool), nil
}

func provideObservedRouteReader(pool *pgxpool.Pool) message.ObservedRouteReader {
	return chatpostgres.NewObservedRouteReader(pool)
}

func provideACLObservedConversationReader(
	observations message.ObservedRouteReader,
	conversations channeldomain.ConversationProjectionReader,
) acl.ObservedConversationReader {
	return aclassembly.NewObservedConversationReader(observations, conversations)
}

type aclChannelIdentityReader struct {
	reader channeldomain.IdentityReader
}

func provideACLChannelIdentityReader(reader channeldomain.IdentityReader) acl.ChannelIdentityReader {
	return &aclChannelIdentityReader{reader: reader}
}

func (r *aclChannelIdentityReader) ListChannelIdentities(ctx context.Context, ids []string) ([]acl.ChannelIdentity, error) {
	items, err := r.reader.ListIdentityProjections(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]acl.ChannelIdentity, 0, len(items))
	for _, item := range items {
		result = append(result, acl.ChannelIdentity{
			ID:               item.ID,
			Channel:          item.Channel,
			ChannelSubjectID: item.ChannelSubjectID,
			DisplayName:      item.DisplayName,
			AvatarURL:        item.AvatarURL,
		})
	}
	return result, nil
}

type aclServiceParams struct {
	fx.In

	Log        *slog.Logger
	Store      acl.Store
	Identities acl.ChannelIdentityReader
	Observed   acl.ObservedConversationReader `optional:"true"`
}

func provideACLService(params aclServiceParams) *acl.Service {
	return acl.NewService(params.Log, params.Store, params.Identities, params.Observed)
}

func provideBotService(log *slog.Logger, store apiassembly.BotPersistence, users bot.UserReader, containers bot.ContainerReader, aclService *acl.Service) *bot.Service {
	return bot.NewService(log, store, store, users, containers, aclService)
}

func provideChannelAccessStore(pool *pgxpool.Pool) access.Store {
	return apiassembly.NewChannelAccessStore(pool)
}

type channelAccessIdentityReader struct {
	reader channeldomain.IdentityReader
}

func provideChannelAccessIdentityReader(reader channeldomain.IdentityReader) access.ChannelIdentityReader {
	return &channelAccessIdentityReader{reader: reader}
}

func (r *channelAccessIdentityReader) ListChannelIdentities(ctx context.Context, ids []string) ([]access.ChannelIdentity, error) {
	items, err := r.reader.ListIdentityProjections(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]access.ChannelIdentity, 0, len(items))
	for _, item := range items {
		result = append(result, access.ChannelIdentity{
			ID:               item.ID,
			Channel:          item.Channel,
			ChannelSubjectID: item.ChannelSubjectID,
			DisplayName:      item.DisplayName,
			AvatarURL:        item.AvatarURL,
		})
	}
	return result, nil
}

func provideMCPConnectionStore(pool *pgxpool.Pool) mcp.ConnectionStore {
	return agentassembly.NewMCPConnectionStore(pool)
}

func provideMCPOAuthStore(pool *pgxpool.Pool) mcp.OAuthStore {
	return agentassembly.NewMCPOAuthStore(pool)
}

func provideFetchProviderService(log *slog.Logger, pool *pgxpool.Pool) *fetch.Service {
	return fetch.NewPostgresService(log, pool)
}

func provideSearchProviderService(log *slog.Logger, pool *pgxpool.Pool) *search.Service {
	return search.NewPostgresService(log, pool)
}

type automationBotReader struct {
	bots apiassembly.BotPersistence
}

func (r automationBotReader) GetBot(ctx context.Context, botID string) (schedule.BotRecord, error) {
	row, err := r.bots.GetBotByID(ctx, botID)
	if err != nil {
		return schedule.BotRecord{}, err
	}
	return schedule.BotRecord{OwnerUserID: row.OwnerUserID, Timezone: row.Timezone}, nil
}

func (r automationBotReader) ListEnabledBots(ctx context.Context) ([]heartbeat.BotRecord, error) {
	rows, err := r.bots.ListHeartbeatEnabledBots(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]heartbeat.BotRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, heartbeat.BotRecord{
			ID:                row.ID,
			OwnerUserID:       row.OwnerUserID,
			Status:            row.Status,
			HeartbeatEnabled:  row.HeartbeatEnabled,
			HeartbeatInterval: row.HeartbeatInterval,
		})
	}
	return items, nil
}

func (r automationBotReader) GetHeartbeatBot(ctx context.Context, botID string) (heartbeat.BotRecord, error) {
	row, err := r.bots.GetBotByID(ctx, botID)
	if err != nil {
		return heartbeat.BotRecord{}, err
	}
	return heartbeat.BotRecord{
		ID:          row.ID,
		OwnerUserID: row.OwnerUserID,
		Status:      row.Status,
	}, nil
}

type heartbeatBotReader struct {
	automationBotReader
}

func (r heartbeatBotReader) GetBot(ctx context.Context, botID string) (heartbeat.BotRecord, error) {
	return r.GetHeartbeatBot(ctx, botID)
}

func provideScheduleStore(pool *pgxpool.Pool, botsStore apiassembly.BotPersistence) schedule.Store {
	return schedulepostgres.NewStoreFromDB(pool, automationBotReader{bots: botsStore})
}

func provideHeartbeatStore(pool *pgxpool.Pool, botsStore apiassembly.BotPersistence) heartbeat.Store {
	return heartbeatpostgres.NewStoreFromDB(pool, heartbeatBotReader{
		automationBotReader: automationBotReader{bots: botsStore},
	})
}

type userRuntimeOut struct {
	fx.Out

	Service *userruntime.Service
	Pipe    userruntime.Pipe
}

func provideUserRuntime(lc fx.Lifecycle, log *slog.Logger, pool *pgxpool.Pool, membership *account.Service) (userRuntimeOut, error) {
	assembled, cleanup, err := runtimeassembly.NewClient(runtimeassembly.ClientDeps{
		Log:        log,
		Pool:       pool,
		Membership: membership,
	})
	if err != nil {
		return userRuntimeOut{}, err
	}
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			cleanup()
			return nil
		},
	})
	return userRuntimeOut{Service: assembled.Service, Pipe: assembled.Pipe}, nil
}

func provideAccountService(log *slog.Logger, store account.Store, titleModels account.TitleModelValidator) *account.Service {
	return account.NewService(log, store, titleModels)
}

func provideBridgeProvider(manage workspace.Service) bridge.Provider {
	return manage
}

type nativeWorkspaceBridgeProvider struct {
	manager workspace.Service
}

type workspaceTargetPolicyResolver struct {
	manager workspace.Service
}

type toolApprovalPolicyProvider struct {
	settings *setting.Service
}

func (p toolApprovalPolicyProvider) ToolApprovalPolicy(ctx context.Context, botID string) (toolapproval.PolicyConfig, error) {
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return toolapproval.PolicyConfig{}, err
	}
	return toolApprovalPolicyConfig(botSettings.ToolApprovalConfig), nil
}

type decisionIdentityReader struct {
	reader application.ChannelIdentityReader
}

func (r decisionIdentityReader) GetByID(ctx context.Context, id string) (decisionpostgres.ChannelIdentity, error) {
	identity, err := r.reader.GetByID(ctx, id)
	if err != nil {
		return decisionpostgres.ChannelIdentity{}, err
	}
	return decisionpostgres.ChannelIdentity{ID: identity.ID, DisplayName: identity.DisplayName}, nil
}

func provideDecisionCluster(pool *pgxpool.Pool, identities application.ChannelIdentityReader) (*decisionpostgres.Cluster, error) {
	return decisionpostgres.New(
		pool,
		func(tx pgx.Tx) decisionpostgres.BotSessionWriteLocker {
			return apiassembly.NewBotSessionLockerFromTx(tx)
		},
		decisionIdentityReader{reader: identities},
	)
}

func provideToolApprovalPersistence(cluster *decisionpostgres.Cluster) toolapproval.Persistence {
	return cluster.Approval()
}

func provideUserInputPersistence(cluster *decisionpostgres.Cluster) userinput.Persistence {
	return cluster.Input()
}

func provideToolApprovalService(log *slog.Logger, persistence toolapproval.Persistence, settingsService *setting.Service) *toolapproval.Service {
	return toolapproval.NewService(log, persistence, toolApprovalPolicyProvider{settings: settingsService})
}

func (r workspaceTargetPolicyResolver) ResolveWorkspaceTargetPolicy(ctx context.Context, botID, targetID string) (toolapproval.WorkspaceTargetPolicy, error) {
	resolved, err := r.manager.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil {
		return toolapproval.WorkspaceTargetPolicy{}, err
	}
	return toolapproval.WorkspaceTargetPolicy{
		TargetID: resolved.TargetID,
		Kind:     resolved.Kind,
		Name:     resolved.Name,
		Config:   toolApprovalPolicyConfig(resolved.Approval),
	}, nil
}

func toolApprovalPolicyConfig(config setting.ToolApprovalConfig) toolapproval.PolicyConfig {
	return toolapproval.PolicyConfig{
		Enabled: config.Enabled,
		Read: toolapproval.FilePolicy{
			Mode:             toolapproval.PolicyMode(config.Read.Mode),
			RequireApproval:  config.Read.RequireApproval,
			BypassGlobs:      cloneOptionalStrings(config.Read.BypassGlobs),
			ForceReviewGlobs: cloneOptionalStrings(config.Read.ForceReviewGlobs),
		},
		Write: toolapproval.FilePolicy{
			Mode:             toolapproval.PolicyMode(config.Write.Mode),
			RequireApproval:  config.Write.RequireApproval,
			BypassGlobs:      cloneOptionalStrings(config.Write.BypassGlobs),
			ForceReviewGlobs: cloneOptionalStrings(config.Write.ForceReviewGlobs),
		},
		Exec: toolapproval.ExecPolicy{
			Mode:                toolapproval.PolicyMode(config.Exec.Mode),
			RequireApproval:     config.Exec.RequireApproval,
			BypassCommands:      cloneOptionalStrings(config.Exec.BypassCommands),
			ForceReviewCommands: cloneOptionalStrings(config.Exec.ForceReviewCommands),
		},
	}
}

func cloneOptionalStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func (p nativeWorkspaceBridgeProvider) MCPClient(ctx context.Context, botID string) (*bridge.Client, error) {
	return p.manager.NativeMCPClient(ctx, botID)
}

func providePluginBridgeProvider(provider bridge.Provider) pluginspkg.BridgeProvider {
	return pluginspkg.BridgeProvider{Provider: provider}
}

func providePluginStore(pool *pgxpool.Pool) pluginspkg.Store {
	return agentassembly.NewPluginStore(pool)
}

func provideHooksService(log *slog.Logger, provider bridge.Provider, pluginService *pluginspkg.Service) *hookspkg.Service {
	service := hookspkg.NewService(log, provider)
	service.SetPluginService(pluginService)
	return service
}

type workspaceOut struct {
	fx.Out

	Service workspace.Service
	Remote  workspace.RemoteService
}

func provideWorkspace(lc fx.Lifecycle, log *slog.Logger, service ctr.Service, networkController netctl.Controller, cfg config.Config, profiles workspace.BotProfileStore, botOwners *policy.Service, runtimeSettings workspace.BotRuntimeSettingsReader, pool *pgxpool.Pool, userRuntime *userruntime.Service) (workspaceOut, error) {
	if pool == nil {
		return workspaceOut{}, errors.New("postgres workspace store not configured")
	}
	assembled, cleanup, err := runtimeassembly.NewWorkspace(runtimeassembly.WorkspaceDeps{
		Log:             log,
		Container:       service,
		Network:         networkController,
		Config:          cfg.Workspace,
		Namespace:       cfg.Containerd.Namespace,
		Profiles:        profiles,
		BotOwners:       botOwners,
		RuntimeSettings: runtimeSettings,
		Pool:            pool,
		UserRuntime:     userRuntime,
		AppConfig:       &cfg,
	})
	if err != nil {
		return workspaceOut{}, err
	}
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			cleanup()
			return nil
		},
	})
	return workspaceOut{Service: assembled.Service, Remote: assembled.Remote}, nil
}

func provideMemoryLLM(modelsService *modelcatalog.Service, settingsService *setting.Service, providerResolver modelcatalog.ProviderResolver, log *slog.Logger) memregistry.LLM {
	return &lazyLLMClient{
		modelsService:    modelsService,
		settingsService:  settingsService,
		providerResolver: providerResolver,
		timeout:          modelexecution.DefaultProviderRequestTimeout,
		logger:           log,
	}
}

type memoryEmbeddingModelResolver struct {
	models    *modelcatalog.Service
	providers modelcatalog.ProviderResolver
}

func (r memoryEmbeddingModelResolver) model(ctx context.Context, ref string) (modelcatalog.GetResponse, error) {
	ref = strings.TrimSpace(ref)
	if _, err := uuid.Parse(ref); err == nil {
		if model, getErr := r.models.GetByID(ctx, ref); getErr == nil {
			return model, nil
		}
	}
	return r.models.GetByModelID(ctx, ref)
}

func (r memoryEmbeddingModelResolver) ResolveEmbeddingModel(ctx context.Context, ref string) (memoryassembly.EmbeddingModelSpec, error) {
	model, err := r.model(ctx, ref)
	if err != nil {
		return memoryassembly.EmbeddingModelSpec{}, err
	}
	provider, err := r.providers.ResolveModelProvider(ctx, model.ProviderID)
	if err != nil {
		return memoryassembly.EmbeddingModelSpec{}, err
	}
	dimensions := 0
	if model.Config.Dimensions != nil {
		dimensions = *model.Config.Dimensions
	}
	return memoryassembly.EmbeddingModelSpec{
		ID:         model.ID,
		ModelID:    model.ModelID,
		Type:       string(model.Type),
		Enabled:    model.Enable,
		Dimensions: dimensions,
		ProviderID: model.ProviderID,
		ClientType: string(provider.ClientType),
		BaseURL:    provider.BaseURL,
		APIKey:     provider.APIKey,
	}, nil
}

func (r memoryEmbeddingModelResolver) EmbeddingModelEnabled(ctx context.Context, ref string) (bool, error) {
	model, err := r.model(ctx, ref)
	if err != nil {
		return false, err
	}
	return model.Enable, nil
}

func provideMemoryProviderRegistry(
	lc fx.Lifecycle,
	log *slog.Logger,
	llm memregistry.LLM,
	provider bridge.Provider,
	modelsService *modelcatalog.Service,
	modelProviders modelcatalog.ProviderResolver,
	pool *pgxpool.Pool,
	cfg config.Config,
) (*memregistry.Registry, error) {
	reg, cleanup, err := memoryassembly.NewRegistry(memoryassembly.Deps{
		Log:    log,
		Pool:   pool,
		Bridge: provider,
		LLM:    llm,
		EmbeddingModels: memoryEmbeddingModelResolver{
			models:    modelsService,
			providers: modelProviders,
		},
		PGVector: memoryassembly.PGVectorConfig{
			Enabled:  cfg.PGVector.Enabled,
			Host:     cfg.PGVector.Host,
			Port:     cfg.PGVector.Port,
			User:     cfg.PGVector.User,
			Password: cfg.PGVector.Password,
			Database: cfg.PGVector.Database,
			SSLMode:  cfg.PGVector.SSLMode,
		},
	})
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			_ = reg.Close()
			cleanup()
			return nil
		},
	})
	return reg, nil
}

func provideMemoryCatalogService(log *slog.Logger, pool *pgxpool.Pool) *memcatalog.Service {
	return memcatalog.NewPostgresService(log, pool)
}

func provideChatMessageStore(pool *pgxpool.Pool) *chatpostgres.MessageStore {
	return chatpostgres.NewMessageStoreWithPool(
		apiassembly.NewBotSessionLocker(pool),
		func(tx pgx.Tx) chatpostgres.BotSessionWriteLocker {
			return apiassembly.NewBotSessionLockerFromTx(tx)
		},
		pool,
	)
}

func provideChatThreadStore(pool *pgxpool.Pool) *chatpostgres.ThreadStore {
	return chatpostgres.NewThreadStoreWithPool(pool)
}

type threadACPPolicyReader struct {
	bots apiassembly.BotPersistence
}

func (r threadACPPolicyReader) GetBotMetadata(ctx context.Context, botID string) (map[string]any, error) {
	row, err := r.bots.GetBotByID(ctx, botID)
	if err != nil {
		return nil, err
	}
	if len(row.Metadata) == 0 {
		return map[string]any{}, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("decode bot metadata: %w", err)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	return metadata, nil
}

func provideSessionService(log *slog.Logger, store *chatpostgres.ThreadStore, botsStore apiassembly.BotPersistence, hub *event.Hub) *sessionpkg.Service {
	service := sessionpkg.NewService(log, store, threadACPPolicyReader{bots: botsStore}, hub)
	service.SetACPSetupValidator(profile.NewCatalog())
	return service
}

func provideMessageService(log *slog.Logger, store *chatpostgres.MessageStore, hub *event.Hub) *message.DBService {
	return message.NewService(log, store, hub)
}

func provideCompactionStore(pool *pgxpool.Pool) *compactionpostgres.Store {
	return compactionpostgres.NewStoreFromDB(pool)
}

func provideCompactionPersistence(store *compactionpostgres.Store) compaction.CompactionStore {
	return store
}

func provideCompactionArtifacts(store *compactionpostgres.Store) compaction.ArtifactStore {
	return store
}

func provideApplicationReads(pool *pgxpool.Pool) *applicationpostgres.Reads {
	return agentassembly.NewApplicationReads(pool)
}

func provideApplicationChannelIdentityReader(service *identity.Service) application.ChannelIdentityReader {
	return identityadapter.NewReader(service)
}

func provideScheduleTriggerer(service *application.Service) schedule.Triggerer {
	return application.NewScheduleGateway(service)
}

func provideHeartbeatTriggerer(service *application.Service) heartbeat.Triggerer {
	return application.NewHeartbeatGateway(service)
}

type sessionCreatorAdapter struct {
	svc *sessionpkg.Service
}

func (a *sessionCreatorAdapter) CreateSession(ctx context.Context, botID, sessionType string) (string, error) {
	sess, err := a.svc.Create(ctx, sessionpkg.CreateInput{
		BotID: botID,
		Type:  sessionType,
	})
	if err != nil {
		return "", err
	}
	return sess.ID, nil
}

func provideHeartbeatSessionCreator(sessionService *sessionpkg.Service) heartbeat.SessionCreator {
	return &sessionCreatorAdapter{svc: sessionService}
}

func provideScheduleSessionCreator(sessionService *sessionpkg.Service) schedule.SessionCreator {
	return &sessionCreatorAdapter{svc: sessionService}
}

func provideAgent(log *slog.Logger, provider bridge.Provider, hookService *hookspkg.Service, cfg config.Config) *engine.Agent {
	return engine.New(engine.Deps{
		BridgeProvider: provider,
		HookService:    hookService,
		Logger:         log,
		Limits:         agentLimitsFromConfig(cfg.Agent),
	})
}

func agentLimitsFromConfig(cfg config.AgentConfig) engine.Limits {
	return engine.LimitsFromValues(
		cfg.ToolOutputMaxBytes,
		cfg.ToolOutputMaxLines,
		cfg.SystemFilesMaxBytes,
	)
}

func injectToolProviders(a *engine.Agent, msgService *message.DBService, hookService *hookspkg.Service, providers []tool.ToolProvider) {
	a.SetToolProviders(providers)
	for _, p := range providers {
		if cp, ok := p.(*tool.ContainerProvider); ok {
			cp.SetHookService(hookService)
		}
		if sp, ok := p.(*tool.SpawnProvider); ok {
			sp.SetAgent(engine.NewSpawnAdapter(a))
			sp.SetMessageService(msgService)
			sp.SetSystemPromptFunc(engine.SpawnSystemPrompt)
			sp.SetHookService(hookService)
		}
	}
}

func provideACPRunner(log *slog.Logger, manager workspace.Service) *acpclient.Runner {
	return acpclient.NewRunner(log, manager)
}

func provideACPSessionPool(lc fx.Lifecycle, log *slog.Logger, runner *acpclient.Runner, botService *bot.Service, sessionService *sessionpkg.Service, toolGateway *mcp.ToolGatewayService, toolContexts *mcp.ToolSessionContextStore, toolApproval *toolapproval.Service, userInput *userinput.Service, containerdHandler *runtimehttp.ContainerdHandler) *acpagent.SessionPool {
	pool := acpagent.NewSessionPool(log, runner, botService, session.NewSource(sessionService))
	pool.SetToolGateway(toolGateway)
	pool.SetToolSessionContextStore(toolContexts)
	pool.SetToolApprovalService(toolApproval)
	pool.SetUserInputService(userInput)
	containerdHandler.SetACPRuntimeResolver(pool)
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			pool.StartReaper(ctx)
			return nil
		},
		OnStop: func(context.Context) error {
			pool.CloseAll() //nolint:contextcheck // ACP shutdown must close subprocesses even after lifecycle ctx cancellation.
			return nil
		},
	})
	return pool
}

func provideAgentService(log *slog.Logger, a *engine.Agent, modelsService *modelcatalog.Service, modelProviderResolver modelcatalog.ProviderResolver, applicationReads *applicationpostgres.Reads, channelIdentityReader application.ChannelIdentityReader, compactionArtifacts compaction.ArtifactStore, msgService *message.DBService, settingsService *setting.Service, accountService *account.Service, botService *bot.Service, mediaService *asset.Service, containerdHandler *runtimehttp.ContainerdHandler, workspaceManager workspace.Service, memoryRegistry *memregistry.Registry, channelStore *gateway.Store, _ *route.DBService, sessionService *sessionpkg.Service, eventHub *event.Hub, compactionService *compaction.Service, pipeline *timeline.Pipeline, clock runtimedomain.Clock, bgManager *background.Manager, toolApproval *toolapproval.Service, userInput *userinput.Service, acpPool *acpagent.SessionPool, hookService *hookspkg.Service) *application.Service {
	service := application.NewService(log, modelsService, modelProviderResolver, botService, accountService, channelIdentityReader, applicationReads, compactionArtifacts, applicationReads, msgService, settingsService, a, clock.Location, 120*time.Second)
	service.SetBotPermissionChecker(&applicationBotPermissionChecker{bots: botService, accounts: accountService})
	service.SetWorkspaceTargetResolver(workspaceManager)
	service.SetHookService(hookService)
	if sessionService != nil {
		sessionService.SetHookService(hookService)
	}
	if compactionService != nil {
		compactionService.SetHookService(hookService)
	}
	if workspaceManager != nil {
		workspaceManager.SetHookService(hookService)
	}
	service.SetMemoryRegistry(memoryRegistry)
	service.SetSkillLoader(&skillLoaderAdapter{handler: containerdHandler})
	service.SetGatewayAssetLoader(&gatewayAssetLoaderAdapter{media: mediaService})
	service.SetPlatformIdentitySource(identityadapter.NewSource(channelStore))
	service.SetSessionService(sessionService)
	service.SetEventPublisher(eventHub)
	service.SetCompactionService(compactionService)
	service.SetPipeline(pipeline)
	service.SetBackgroundManager(bgManager)
	if toolApproval != nil {
		toolApproval.SetHookService(hookService)
		toolApproval.SetWorkspaceTargetPolicyResolver(workspaceTargetPolicyResolver{manager: workspaceManager})
	}
	service.SetToolApprovalService(toolApproval)
	service.SetUserInputService(userInput)
	service.SetACPSessionPool(acpPool)
	if bgManager != nil {
		bgManager.SetEventFunc(func(evt background.TaskEvent) {
			if eventHub == nil {
				return
			}
			// The wire shape lives in domains/agent/event/payload — see its
			// BackgroundTask helper and the tests there that pin the
			// top-level `session_id` placement the per-session SSE handler
			// routes on.
			data, err := json.Marshal(agentpayload.BackgroundTask(evt))
			if err != nil {
				return
			}
			eventHub.Publish(event.Event{
				Type:  event.EventTypeBackgroundTask,
				BotID: evt.BotID,
				Data:  data,
			})
		})
	}
	return service
}

func provideContainerdHandler(log *slog.Logger, manager workspace.Service, cfg config.Config, backend ctr.Backend, botService *bot.Service, accountService *account.Service, policyService *policy.Service, pluginService *pluginspkg.Service, displayService runtimedisplay.Service) *runtimehttp.ContainerdHandler {
	manager.SetSetupDiagnostics(botService)
	h := runtimehttp.NewContainerdHandler(log, manager, cfg.Workspace, backend.String(), botService, accountService, policyService, displayService)
	h.SetPluginService(pluginService)
	return h
}

func provideScheduleService(log *slog.Logger, store schedule.Store, triggerer schedule.Triggerer, sessionCreator schedule.SessionCreator, tokens auth.TokenConfig, clock runtimedomain.Clock) *schedule.Service {
	return schedule.NewService(log, store, triggerer, sessionCreator, tokens.Secret, clock.Location)
}

func provideHeartbeatService(log *slog.Logger, store heartbeat.Store, triggerer heartbeat.Triggerer, sessionCreator heartbeat.SessionCreator, tokens auth.TokenConfig) *heartbeat.Service {
	return heartbeat.NewService(log, store, triggerer, sessionCreator, tokens.Secret)
}

func provideChatBackupStore(pool *pgxpool.Pool) (*chatbackuppostgres.Store, error) {
	return chatbackuppostgres.New(pool, apiassembly.BotExclusiveLocker{})
}

func provideChannelBackupStore(pool *pgxpool.Pool) (*channelbackuppostgres.Store, error) {
	return channelbackuppostgres.New(pool, apiassembly.BotExclusiveLocker{})
}

func provideBotBackupService(log *slog.Logger, chatBackup *chatbackuppostgres.Store, channelBackup *channelbackuppostgres.Store, botService *bot.Service, settingsService *setting.Service, aclService *acl.Service, channelStore *gateway.Store, mcpService *mcp.ConnectionService, scheduleService *schedule.Service, emailService *emailpkg.Service, providerService *providers.Service, modelsService *modelcatalog.Service, searchProviderService *search.Service, fetchProviderService *fetch.Service, memoryProviderService *memcatalog.Service, manager workspace.Service, acpPool *acpagent.SessionPool) *botbackup.Service {
	return botbackup.New(botbackup.Params{
		Logger:          log,
		Bots:            botService,
		Settings:        settingsService,
		ACL:             aclService,
		Channels:        channelStore,
		MCP:             mcpService,
		Schedules:       scheduleService,
		Email:           emailService,
		Providers:       providerService,
		Models:          modelsService,
		SearchProviders: searchProviderService,
		FetchProviders:  fetchProviderService,
		MemoryProviders: memoryProviderService,
		Workspace:       manager,
		ChatBackup:      chatBackup,
		ChannelBackup:   channelBackup,
		ACPRuntimes:     acpPool,
	})
}

func provideFederationGateway(log *slog.Logger, containerdHandler *runtimehttp.ContainerdHandler) *runtimehttp.MCPFederationGateway {
	return runtimehttp.NewMCPFederationGateway(log, containerdHandler)
}

func provideOAuthService(log *slog.Logger, store mcp.OAuthStore, cfg config.Config) *mcp.OAuthService {
	addr := strings.TrimSpace(cfg.Server.Addr)
	if addr == "" {
		addr = ":8080"
	}
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	callbackURL := "http://" + host + "/oauth/mcp/callback"
	return mcp.NewOAuthService(log, store, callbackURL)
}

func provideACPToolSource(log *slog.Logger, toolApproval *toolapproval.Service, userInput *userinput.Service, toolContexts *mcp.ToolSessionContextStore, cfg config.Config) *tool.NativeToolSource {
	limits := agentLimitsFromConfig(cfg.Agent)
	return tool.NewNativeToolSource(log, nil, tool.NativeToolSourceOptions{
		AllowAll:        true,
		Approval:        toolApproval,
		UserInput:       userInput,
		ToolEvents:      toolContexts,
		ToolOutputLimit: limits.ToolOutputLimit(),
	})
}

func injectACPToolProviders(source *tool.NativeToolSource, toolProviders []tool.ToolProvider) {
	if source != nil {
		source.SetProviders(acpToolProviders(toolProviders))
	}
}

func provideToolGatewayService(log *slog.Logger, fedGateway *runtimehttp.MCPFederationGateway, oauthService *mcp.OAuthService, mcpConnService *mcp.ConnectionService, containerdHandler *runtimehttp.ContainerdHandler, nativeSource *tool.NativeToolSource, toolContexts *mcp.ToolSessionContextStore, cfg config.Config) *mcp.ToolGatewayService {
	fedGateway.SetOAuthService(oauthService)
	fedSource := mcpfederation.NewSource(log, fedGateway, mcpConnService, mcpfederation.WithReservedToolName(tool.IsBuiltInToolName))
	limits := agentLimitsFromConfig(cfg.Agent)
	svc := mcp.NewToolGatewayService(log, []mcp.ToolSource{nativeSource, fedSource}, mcp.WithToolOutputLimit(limits.ToolOutputLimit()))
	containerdHandler.SetToolGatewayService(svc)
	containerdHandler.SetToolSessionContextStore(toolContexts)
	return svc
}

func acpToolProviders(providers []tool.ToolProvider) []tool.ToolProvider {
	filtered := make([]tool.ToolProvider, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		if _, ok := provider.(*tool.FederationProvider); ok {
			continue
		}
		filtered = append(filtered, provider)
	}
	return filtered
}

func provideBackgroundManager(log *slog.Logger) *background.Manager {
	return background.New(log)
}

func provideHistorySearcher(pool *pgxpool.Pool) tool.HistorySearcher {
	return agentassembly.NewHistorySearcher(pool)
}

func provideToolProviders(log *slog.Logger, channelRuntime gateway.Runtime, registry *gateway.Registry, routeService *route.DBService, scheduleService *schedule.Service, settingsService *setting.Service, searchProviderService *search.Service, fetchProviderService *fetch.Service, manager workspace.Service, mediaService *asset.Service, memoryRegistry *memregistry.Registry, emailService *emailpkg.Service, emailRuntime emailpkg.Runtime, fedGateway *runtimehttp.MCPFederationGateway, mcpConnService *mcp.ConnectionService, modelsService *modelcatalog.Service, modelProviderResolver modelcatalog.ProviderResolver, historySearcher tool.HistorySearcher, audioService *audiopkg.Service, videoService *videopkg.Service, sessionService *sessionpkg.Service, messageService *message.DBService, bgManager *background.Manager, hookService *hookspkg.Service, displayService runtimedisplay.Service) []tool.ToolProvider {
	var assetResolver delivery.AssetResolver
	if mediaService != nil {
		assetResolver = &mediaAssetResolverAdapter{media: mediaService}
	}
	channelMessaging := messaging.New(channelRuntime, registry, assetResolver)
	fedSource := mcpfederation.NewSource(log, fedGateway, mcpConnService, mcpfederation.WithReservedToolName(tool.IsBuiltInToolName))
	return []tool.ToolProvider{
		tool.NewAskUserProvider(log),
		tool.NewMessageProvider(log, channelMessaging, channelMessaging, channelMessaging, assetResolver),
		tool.NewContactsProvider(log, contact.NewSource(routeService)),
		tool.NewScheduleProvider(log, scheduleService),
		tool.NewMemoryProvider(log, memoryRegistry, settingsService),
		tool.NewWebProvider(log, settingsService, searchProviderService),
		tool.NewContainerProvider(log, manager, bgManager, runtimedomain.DefaultDataMount, hookService),
		tool.NewBackgroundProvider(log, bgManager),
		tool.NewBrowserProvider(log, settingsService, nativeWorkspaceBridgeProvider{manager: manager}, displayService, runtimedomain.DefaultDataMount),
		tool.NewEmailProvider(log, emailService, emailRuntime),
		tool.NewWebFetchProvider(log, settingsService, fetchProviderService),
		tool.NewSpawnProvider(log, settingsService, modelsService, modelProviderResolver, sessionService, bgManager),
		tool.NewSkillProvider(log),
		tool.NewTTSProvider(log, settingsService, audioService, channelMessaging, channelMessaging),
		tool.NewTranscriptionProvider(log, settingsService, audioService, mediaService),
		tool.NewImageGenProvider(log, settingsService, modelsService, modelProviderResolver, manager, runtimedomain.DefaultDataMount),
		tool.NewVideoGenProvider(log, settingsService, videoService, bgManager, manager, runtimedomain.DefaultDataMount),
		tool.NewFederationProvider(log, fedSource),
		tool.NewHistoryProvider(log, thread.NewLister(sessionService, routeService), messageService, historySearcher),
	}
}

func provideMediaService(log *slog.Logger, provider bridge.Provider, cfg config.Config) *asset.Service {
	dataRoot := cfg.Workspace.DataRoot
	if dataRoot == "" {
		dataRoot = config.DefaultDataRoot
	}
	return asset.NewContainerFallbackService(log, newBridgeContainerFileClientProvider(provider), filepath.Join(dataRoot, "media"))
}

func provideACPCodexOAuthHandler(providersService *providers.Service, botService *bot.Service, accountService *account.Service, workspaceManager workspace.Service) *agenthttp.ACPCodexOAuthHandler {
	return agenthttp.NewACPCodexOAuthHandler(providersService, botService, accountService, workspaceManager, defaultACPCodexOAuthCallbackURL())
}

func provideACPClaudeCodeOAuthHandler(botService *bot.Service, accountService *account.Service, workspaceManager workspace.Service) *agenthttp.ACPClaudeCodeOAuthHandler {
	return agenthttp.NewACPClaudeCodeOAuthHandler(botService, accountService, workspaceManager)
}

func provideAudioService(log *slog.Logger, pool *pgxpool.Pool) *audiopkg.Service {
	return audiopkg.NewPostgresService(log, pool)
}

func provideVideoService(log *slog.Logger, pool *pgxpool.Pool) *videopkg.Service {
	return videopkg.NewPostgresService(log, pool)
}

func provideAudioTempStore() (*audiopkg.TempStore, error) {
	return audiopkg.NewTempStore(os.TempDir())
}

func startAudioTempStoreCleanup(lc fx.Lifecycle, store *audiopkg.TempStore) {
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go store.StartCleanup(done)
			return nil
		},
		OnStop: func(_ context.Context) error {
			close(done)
			return nil
		},
	})
}

func startBackgroundTaskCleanup(lc fx.Lifecycle, mgr *background.Manager) {
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go mgr.StartCleanupLoop(done, background.DefaultCleanupInterval, background.DefaultTaskRetention)
			return nil
		},
		OnStop: func(_ context.Context) error {
			close(done)
			return nil
		},
	})
}

// inboundTranscriptionResult moved to the shared Channel module.

func provideTemplateService(log *slog.Logger, pool *pgxpool.Pool) *template.Service {
	return template.NewPostgresService(log, pool)
}

func provideProvidersService(log *slog.Logger, pool *pgxpool.Pool, templates *template.Service, cfg config.Config) *providers.Service {
	modelexecution.ConfigureProviderUserAgent(version.Version, version.ShortCommitHash())
	return providers.NewPostgresService(
		log,
		pool,
		defaultProviderOAuthCallbackURL(),
		templates,
		cfg.Registry.ProvidersPath(),
		providers.WithProbeSDK(modelexecution.ProbeSDK{}),
	)
}

type modelProviderResolver struct {
	service *providers.Service
}

func (r modelProviderResolver) ResolveModelProvider(ctx context.Context, providerID string) (modelcatalog.ResolvedProvider, error) {
	record, err := r.service.LookupProvider(ctx, providerID)
	if err != nil {
		return modelcatalog.ResolvedProvider{}, err
	}
	credentials, err := r.service.ResolveModelCredentials(ctx, record)
	if err != nil {
		return modelcatalog.ResolvedProvider{}, err
	}
	return modelcatalog.ResolvedProvider{
		ID:                    record.ID,
		Name:                  record.Name,
		ClientType:            modeldomain.ClientType(record.ClientType),
		Enable:                record.Enable,
		BaseURL:               providers.ProviderConfigString(record.Config, "base_url"),
		APIKey:                credentials.APIKey,
		CodexAccountID:        credentials.CodexAccountID,
		PromptCacheTTL:        providers.ProviderConfigString(record.Config, "prompt_cache_ttl"),
		ChatCompletionsCompat: providers.ProviderConfigString(record.Config, "chat_completions_compat"),
	}, nil
}

func provideModelProviderResolver(service *providers.Service) modelcatalog.ProviderResolver {
	return modelProviderResolver{service: service}
}

func provideModelsService(log *slog.Logger, pool *pgxpool.Pool, providerResolver modelcatalog.ProviderResolver) *modelcatalog.Service {
	return modelcatalog.NewPostgresService(log, pool, providerResolver)
}

func provideModelExecutionResolver(modelsService *modelcatalog.Service, providerService *providers.Service, pool *pgxpool.Pool) *modelexecution.Resolver {
	return modelexecution.NewPostgresResolver(
		modelsService.ExecutionModelReader(),
		pool,
		providerService,
		modelexecution.WithUserIDContext(oauth.WithUserID),
	)
}

func defaultProviderOAuthCallbackURL() string {
	return "http://localhost:1455/auth/callback"
}

func defaultACPCodexOAuthCallbackURL() string {
	return defaultProviderOAuthCallbackURL()
}

func startProviderTemplateSync(
	lc fx.Lifecycle,
	log *slog.Logger,
	cfg config.Config,
	pool *pgxpool.Pool,
) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			defs, err := template.Load(log, cfg.Registry.ProvidersPath())
			if err != nil {
				log.WarnContext(ctx, "registry: failed to load provider definitions", slog.Any("error", err))
				defs = nil
			}
			if len(defs) == 0 {
				return nil
			}
			return template.SyncCatalog(ctx, log, pool, defs)
		},
	})
}

func configureMemoryProviderRegistry(mpService *memcatalog.Service, registry *memregistry.Registry) {
	mpService.SetRegistry(registry)
}

func startScheduleService(lc fx.Lifecycle, scheduleService *schedule.Service) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return scheduleService.Bootstrap(ctx)
		},
	})
}

func startHeartbeatService(lc fx.Lifecycle, heartbeatService *heartbeat.Service) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return heartbeatService.Bootstrap(ctx)
		},
	})
}

func startContainerReconciliation(lc fx.Lifecycle, manager workspace.Service, _ *runtimehttp.ContainerdHandler, _ *mcp.ToolGatewayService) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go manager.ReconcileContainers(ctx)
			return nil
		},
	})
}

type lazyLLMClient struct {
	modelsService    *modelcatalog.Service
	settingsService  *setting.Service
	providerResolver modelcatalog.ProviderResolver
	timeout          time.Duration
	logger           *slog.Logger
}

func (c *lazyLLMClient) Extract(ctx context.Context, req memregistry.ExtractRequest) (memregistry.ExtractResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memregistry.ExtractResponse{}, err
	}
	return client.Extract(ctx, req)
}

func (c *lazyLLMClient) Decide(ctx context.Context, req memregistry.DecideRequest) (memregistry.DecideResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memregistry.DecideResponse{}, err
	}
	return client.Decide(ctx, req)
}

func (c *lazyLLMClient) Compact(ctx context.Context, req memregistry.CompactRequest) (memregistry.CompactResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memregistry.CompactResponse{}, err
	}
	return client.Compact(ctx, req)
}

func (c *lazyLLMClient) resolve(ctx context.Context, botID string) (memregistry.LLM, error) {
	if c.modelsService == nil || c.providerResolver == nil {
		return nil, errors.New("models service not configured")
	}

	chatModelID := ""
	if c.settingsService != nil && strings.TrimSpace(botID) != "" {
		if botSettings, err := c.settingsService.GetBot(ctx, botID); err == nil {
			if id := strings.TrimSpace(botSettings.CompactionModelID); id != "" {
				chatModelID = id
			} else if id := strings.TrimSpace(botSettings.ChatModelID); id != "" {
				chatModelID = id
			}
		}
	}

	memoryModel, memoryProvider, err := modelcatalog.SelectMemoryModelForBot(ctx, c.modelsService, c.providerResolver, chatModelID)
	if err != nil {
		return nil, err
	}
	return memoryassembly.NewFormationClient(memoryassembly.FormationClientConfig{
		ModelID:        memoryModel.ModelID,
		BaseURL:        strings.TrimRight(memoryProvider.BaseURL, "/"),
		APIKey:         memoryProvider.APIKey,
		ClientType:     string(memoryProvider.ClientType),
		Timeout:        c.timeout,
		PromptCacheTTL: memoryProvider.PromptCacheTTL,
	}), nil
}

type skillLoaderAdapter struct {
	handler *runtimehttp.ContainerdHandler
}

func (a *skillLoaderAdapter) LoadSkills(ctx context.Context, botID string) ([]application.SkillEntry, error) {
	items, err := a.handler.LoadSkills(ctx, botID)
	if err != nil {
		return nil, err
	}
	entries := make([]application.SkillEntry, len(items))
	for i, item := range items {
		skillPath := ""
		if item.SourcePath != "" {
			skillPath = stdpath.Dir(item.SourcePath)
		}
		entries[i] = application.SkillEntry{
			Name:        item.Name,
			Description: item.Description,
			Content:     item.Content,
			Path:        skillPath,
			Metadata:    item.Metadata,
		}
	}
	return entries, nil
}

type mediaAssetResolverAdapter struct {
	media *asset.Service
}

func (a *mediaAssetResolverAdapter) Stat(ctx context.Context, botID, contentHash string) (media.Asset, error) {
	if a == nil || a.media == nil {
		return media.Asset{}, errors.New("media service not configured")
	}
	return a.media.Stat(ctx, botID, contentHash)
}

func (a *mediaAssetResolverAdapter) Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error) {
	if a == nil || a.media == nil {
		return nil, media.Asset{}, errors.New("media service not configured")
	}
	return a.media.Open(ctx, botID, contentHash)
}

func (a *mediaAssetResolverAdapter) Ingest(ctx context.Context, input media.IngestInput) (media.Asset, error) {
	if a == nil || a.media == nil {
		return media.Asset{}, errors.New("media service not configured")
	}
	return a.media.Ingest(ctx, input)
}

func (a *mediaAssetResolverAdapter) GetByStorageKey(ctx context.Context, botID, storageKey string) (delivery.AssetMeta, error) {
	if a == nil || a.media == nil {
		return delivery.AssetMeta{}, errors.New("media service not configured")
	}
	return a.media.GetByStorageKey(ctx, botID, storageKey)
}

func (a *mediaAssetResolverAdapter) AccessPath(ctx context.Context, asset media.Asset) string {
	if a == nil || a.media == nil {
		return ""
	}
	return a.media.AccessPath(ctx, asset)
}

func (a *mediaAssetResolverAdapter) IngestContainerFile(ctx context.Context, botID, containerPath string) (delivery.AssetMeta, error) {
	if a == nil || a.media == nil {
		return delivery.AssetMeta{}, errors.New("media service not configured")
	}
	return a.media.IngestContainerFile(ctx, botID, containerPath)
}

type gatewayAssetLoaderAdapter struct {
	media *asset.Service
}

func (a *gatewayAssetLoaderAdapter) OpenForGateway(ctx context.Context, botID, contentHash string) (io.ReadCloser, string, error) {
	if a == nil || a.media == nil {
		return nil, "", errors.New("media service not configured")
	}
	reader, asset, err := a.media.Open(ctx, botID, contentHash)
	if err != nil {
		return nil, "", err
	}
	return reader, strings.TrimSpace(asset.Mime), nil
}

func (a *gatewayAssetLoaderAdapter) AccessPathForGateway(ctx context.Context, botID, contentHash string) (string, error) {
	if a == nil || a.media == nil {
		return "", errors.New("media service not configured")
	}
	asset, err := a.media.Resolve(ctx, botID, contentHash)
	if err != nil {
		return "", err
	}
	accessPath, err := a.media.EnsureAccessPath(ctx, asset)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(accessPath), nil
}

// provideTurnService exposes the application service as Channel's only Agent
// surface. Both chat and discuss turns run directly on the same service.
func provideTurnService(service *application.Service) agentdomain.Service {
	// The self-hosted runtime binds its DB pool to the singleton team, so
	// the service fails closed on any other TeamID (agentdomain.ErrTeamNotServed).
	service.SetAllowedTeam(team.DefaultTeamID)
	return service
}

// applicationBotPermissionChecker duplicates the Channel module's inbound
// permission glue; both adapt bots/accounts onto the same
// HasBotPermission shape.
type applicationBotPermissionChecker struct {
	bots     *bot.Service
	accounts *account.Service
}

func (a *applicationBotPermissionChecker) HasBotPermission(ctx context.Context, botID, accountID, permission string) (bool, error) {
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
