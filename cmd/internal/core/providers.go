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
	applicationpersistence "github.com/memohai/memoh/domains/agent/application/persistence"
	agentassembly "github.com/memohai/memoh/domains/agent/assembly"
	"github.com/memohai/memoh/domains/agent/automation/heartbeat"
	heartbeatpersistence "github.com/memohai/memoh/domains/agent/automation/heartbeat/persistence"
	"github.com/memohai/memoh/domains/agent/automation/schedule"
	schedulepersistence "github.com/memohai/memoh/domains/agent/automation/schedule/persistence"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/event"
	"github.com/memohai/memoh/domains/agent/chat/message"
	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/agent/chat/timeline"
	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	decisioncluster "github.com/memohai/memoh/domains/agent/decision/cluster"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/agent/engine"
	"github.com/memohai/memoh/domains/agent/engine/background"
	agentpayload "github.com/memohai/memoh/domains/agent/event/payload"
	hookspkg "github.com/memohai/memoh/domains/agent/extension/hooks"
	pluginspkg "github.com/memohai/memoh/domains/agent/extension/plugins"
	pluginspersistence "github.com/memohai/memoh/domains/agent/extension/plugins/persistence"
	"github.com/memohai/memoh/domains/agent/mcp"
	mcppersistence "github.com/memohai/memoh/domains/agent/mcp/persistence"
	mcpfederation "github.com/memohai/memoh/domains/agent/mcp/sources/federation"
	"github.com/memohai/memoh/domains/agent/tool"
	toolpersistence "github.com/memohai/memoh/domains/agent/tool/persistence"
	"github.com/memohai/memoh/domains/api/bot/access/acl"
	linkpersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"

	"github.com/memohai/memoh/domains/api/bot"
	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
	aclprojection "github.com/memohai/memoh/domains/api/bot/access/acl/projection"
	"github.com/memohai/memoh/domains/api/bot/access/policy"
	botbackup "github.com/memohai/memoh/domains/api/bot/backup"
	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"
	botprojection "github.com/memohai/memoh/domains/api/bot/projection"
	"github.com/memohai/memoh/domains/api/bot/setting"
	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
	apihttp "github.com/memohai/memoh/domains/api/http"
	agenthttp "github.com/memohai/memoh/domains/api/http/agent"
	runtimehttp "github.com/memohai/memoh/domains/api/http/runtime"
	"github.com/memohai/memoh/domains/api/identity/auth"
	identitylink "github.com/memohai/memoh/domains/api/identity/link"
	channeldomain "github.com/memohai/memoh/domains/channel"
	channelassembly "github.com/memohai/memoh/domains/channel/assembly"
	"github.com/memohai/memoh/domains/channel/delivery"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/iam/account"
	accountpersistence "github.com/memohai/memoh/domains/iam/account/persistence"
	team "github.com/memohai/memoh/domains/iam/team"
	"github.com/memohai/memoh/domains/media"
	"github.com/memohai/memoh/domains/media/asset"
	memcatalog "github.com/memohai/memoh/domains/memory/catalog"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
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
	managed, err := runtimeassembly.NewService(context.Background(), runtimeassembly.Deps{
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
		OnStart: managed.Start,
		OnStop:  managed.Stop,
	})
	return managed.Service, nil
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
		OnStop: func(ctx context.Context) error {
			return cleanup(ctx)
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

func provideRuntimeSettingsStore(pool *pgxpool.Pool) setting.Persistence {
	return setting.NewPostgresPersistence(pool)
}

func provideSettingsModelReader(models *modelcatalog.Service) settingpersistence.ModelReader {
	return models
}

func provideSettingsService(log *slog.Logger, store setting.Persistence, models settingpersistence.ModelReader, aclService *acl.Service, networkService *netctl.Service) *setting.Service {
	return setting.NewService(log, store, models, aclService, networkService)
}

func provideWorkspaceRuntimeSettings(store setting.Persistence) workspace.BotRuntimeSettingsReader {
	return store
}

func provideBotPersistenceStore(pool *pgxpool.Pool) bot.Persistence {
	return bot.NewPostgresPersistence(pool)
}

func providePolicyBotReader(service *bot.Service) policy.BotReader {
	return service
}

func provideRuntimeContainerStore(pool *pgxpool.Pool) workspace.ContainerStore {
	return runtimeassembly.NewContainerStore(pool)
}

func provideBotUserReader(accounts *account.Service) botpersistence.UserReader {
	return botprojection.NewBotUserReader(accounts)
}

func provideBotContainerReader(store workspace.ContainerStore) botpersistence.ContainerReader {
	return botprojection.NewBotContainerReader(store)
}

func provideWorkspaceBotProfiles(store bot.Persistence) workspace.BotProfileStore {
	return store
}

func provideWorkspaceBotOwners(store bot.Persistence) workspace.BotOwnerReader {
	return store
}

// settingsNetworkConfigReader adapts API-owned settings overlay reads to the
// Runtime network ConfigReader port without importing settings concrete into
// the public network package.
type settingsNetworkConfigReader struct {
	store settingpersistence.Store
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

func provideNetwork(log *slog.Logger, store setting.Persistence, conn *pgxpool.Pool, service ctr.Service, backend ctr.Backend, cfg config.Config) (*netctl.Service, netctl.Controller, error) {
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

func provideAccountCounter(pool *pgxpool.Pool) accountpersistence.AccountCounter {
	return account.NewPostgresCounter(pool)
}

func provideAccountTitleModelValidator(pool *pgxpool.Pool) accountpersistence.TitleModelValidator {
	return modelcatalog.NewPostgresTitleModelValidator(pool)
}

func provideACLStore(pool *pgxpool.Pool) (aclpersistence.Store, error) {
	if pool == nil {
		return nil, aclpersistence.ErrTransactionsRequired
	}
	return acl.NewPostgresStore(pool), nil
}

func provideObservedRouteReader(pool *pgxpool.Pool) message.ObservedRouteReader {
	return agentassembly.NewPostgresObservedRouteReader(pool)
}

func provideACLObservedConversationReader(
	observations message.ObservedRouteReader,
	conversations channeldomain.ConversationProjectionReader,
) aclpersistence.ObservedConversationReader {
	return aclprojection.NewObservedConversationReader(observations, conversations)
}

type aclChannelIdentityReader struct {
	reader channeldomain.IdentityReader
}

func provideACLChannelIdentityReader(reader channeldomain.IdentityReader) aclpersistence.ChannelIdentityReader {
	return &aclChannelIdentityReader{reader: reader}
}

func (r *aclChannelIdentityReader) ListChannelIdentities(ctx context.Context, ids []string) ([]aclpersistence.ChannelIdentity, error) {
	items, err := r.reader.ListIdentityProjections(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]aclpersistence.ChannelIdentity, 0, len(items))
	for _, item := range items {
		result = append(result, aclpersistence.ChannelIdentity{
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
	Store      aclpersistence.Store
	Identities aclpersistence.ChannelIdentityReader
	Observed   aclpersistence.ObservedConversationReader `optional:"true"`
}

func provideACLService(params aclServiceParams) *acl.Service {
	return acl.NewService(params.Log, params.Store, params.Identities, params.Observed)
}

func provideBotService(log *slog.Logger, store bot.Persistence, users botpersistence.UserReader, containers botpersistence.ContainerReader, aclService *acl.Service) *bot.Service {
	return bot.NewService(log, store, store, users, containers, aclService)
}

type applicationBotOwnerResolver struct {
	bots bot.Persistence
}

func (r applicationBotOwnerResolver) ResolveBotOwner(ctx context.Context, botID string) (string, error) {
	row, err := r.bots.GetBotByID(ctx, botID)
	if err != nil {
		return "", err
	}
	return row.OwnerUserID, nil
}

func provideApplicationBotOwnerResolver(bots bot.Persistence) application.BotOwnerResolver {
	return applicationBotOwnerResolver{bots: bots}
}

func provideIdentityLinkStore(pool *pgxpool.Pool) linkpersistence.Store {
	return identitylink.NewPostgresStore(pool)
}

type channelAccessIdentityReader struct {
	reader channeldomain.IdentityReader
}

func provideIdentityLinkIdentityReader(reader channeldomain.IdentityReader) linkpersistence.ChannelIdentityReader {
	return &channelAccessIdentityReader{reader: reader}
}

func (r *channelAccessIdentityReader) ListChannelIdentities(ctx context.Context, ids []string) ([]linkpersistence.ChannelIdentity, error) {
	items, err := r.reader.ListIdentityProjections(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]linkpersistence.ChannelIdentity, 0, len(items))
	for _, item := range items {
		result = append(result, linkpersistence.ChannelIdentity{
			ID:               item.ID,
			Channel:          item.Channel,
			ChannelSubjectID: item.ChannelSubjectID,
			DisplayName:      item.DisplayName,
			AvatarURL:        item.AvatarURL,
		})
	}
	return result, nil
}

func provideMCPConnectionStore(pool *pgxpool.Pool) mcppersistence.ConnectionStore {
	return mcp.NewPostgresConnectionStore(pool)
}

func provideMCPOAuthStore(pool *pgxpool.Pool) mcppersistence.OAuthStore {
	return mcp.NewPostgresOAuthStore(pool)
}

func provideFetchProviderService(log *slog.Logger, pool *pgxpool.Pool) *fetch.Service {
	return fetch.NewPostgresService(log, pool)
}

func provideSearchProviderService(log *slog.Logger, pool *pgxpool.Pool) *search.Service {
	return search.NewPostgresService(log, pool)
}

type automationBotReader struct {
	bots bot.Persistence
}

func (r automationBotReader) GetBot(ctx context.Context, botID string) (schedulepersistence.BotRecord, error) {
	row, err := r.bots.GetBotByID(ctx, botID)
	if err != nil {
		return schedulepersistence.BotRecord{}, err
	}
	return schedulepersistence.BotRecord{OwnerUserID: row.OwnerUserID, Timezone: row.Timezone}, nil
}

func (r automationBotReader) ListEnabledBots(ctx context.Context) ([]heartbeatpersistence.BotRecord, error) {
	rows, err := r.bots.ListHeartbeatEnabledBots(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]heartbeatpersistence.BotRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, heartbeatpersistence.BotRecord{
			ID:                row.ID,
			OwnerUserID:       row.OwnerUserID,
			Status:            row.Status,
			HeartbeatEnabled:  row.HeartbeatEnabled,
			HeartbeatInterval: row.HeartbeatInterval,
		})
	}
	return items, nil
}

func (r automationBotReader) GetHeartbeatBot(ctx context.Context, botID string) (heartbeatpersistence.BotRecord, error) {
	row, err := r.bots.GetBotByID(ctx, botID)
	if err != nil {
		return heartbeatpersistence.BotRecord{}, err
	}
	return heartbeatpersistence.BotRecord{
		ID:          row.ID,
		OwnerUserID: row.OwnerUserID,
		Status:      row.Status,
	}, nil
}

type heartbeatBotReader struct {
	automationBotReader
}

func (r heartbeatBotReader) GetBot(ctx context.Context, botID string) (heartbeatpersistence.BotRecord, error) {
	return r.GetHeartbeatBot(ctx, botID)
}

func provideScheduleStore(pool *pgxpool.Pool, botsStore bot.Persistence) schedulepersistence.Store {
	return schedule.NewPostgresStore(pool, automationBotReader{bots: botsStore})
}

func provideHeartbeatStore(pool *pgxpool.Pool, botsStore bot.Persistence) heartbeatpersistence.Store {
	return heartbeat.NewPostgresStore(pool, heartbeatBotReader{
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

func provideAccountService(log *slog.Logger, pool *pgxpool.Pool, titleModels accountpersistence.TitleModelValidator) *account.Service {
	return account.NewPostgresService(log, pool, titleModels)
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

func (r decisionIdentityReader) GetByID(ctx context.Context, id string) (decisioncluster.ChannelIdentity, error) {
	identity, err := r.reader.GetByID(ctx, id)
	if err != nil {
		return decisioncluster.ChannelIdentity{}, err
	}
	return decisioncluster.ChannelIdentity{ID: identity.ID, DisplayName: identity.DisplayName}, nil
}

func provideDecisionCluster(pool *pgxpool.Pool, identities application.ChannelIdentityReader) (decisioncluster.Cluster, error) {
	return decisioncluster.NewPostgres(
		pool,
		func(tx pgx.Tx) decisioncluster.BotSessionWriteLocker {
			return bot.NewSessionLockerFromTx(tx)
		},
		decisionIdentityReader{reader: identities},
	)
}

func provideToolApprovalPersistence(cluster decisioncluster.Cluster) toolapproval.Persistence {
	return cluster.Approval()
}

func provideUserInputPersistence(cluster decisioncluster.Cluster) userinput.Persistence {
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

func toolApprovalPolicyConfig(config settingpersistence.ToolApprovalConfig) toolapproval.PolicyConfig {
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

func providePluginStore(pool *pgxpool.Pool) pluginspersistence.Store {
	return pluginspkg.NewPostgresStore(pool)
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

type workspaceParams struct {
	fx.In

	Lifecycle       fx.Lifecycle
	Log             *slog.Logger
	Container       ctr.Service
	Network         netctl.Controller
	Config          config.Config
	Profiles        workspace.BotProfileStore
	BotOwners       workspace.BotOwnerReader
	RuntimeSettings workspace.BotRuntimeSettingsReader
	Pool            *pgxpool.Pool
	UserRuntime     *userruntime.Service
}

func provideWorkspace(params workspaceParams) (workspaceOut, error) {
	if params.Pool == nil {
		return workspaceOut{}, errors.New("postgres workspace store not configured")
	}
	assembled, cleanup, err := runtimeassembly.NewWorkspace(runtimeassembly.WorkspaceDeps{
		Log:             params.Log,
		Container:       params.Container,
		Network:         params.Network,
		Config:          params.Config.Workspace,
		Namespace:       params.Config.Containerd.Namespace,
		Profiles:        params.Profiles,
		BotOwners:       params.BotOwners,
		RuntimeSettings: params.RuntimeSettings,
		Pool:            params.Pool,
		UserRuntime:     params.UserRuntime,
		AppConfig:       &params.Config,
	})
	if err != nil {
		return workspaceOut{}, err
	}
	params.Lifecycle.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			cleanup()
			return nil
		},
	})
	return workspaceOut{Service: assembled.Service, Remote: assembled.Remote}, nil
}

func provideMemoryLLM(modelsService *modelcatalog.Service, settingsService *setting.Service, providerResolver modelcatalog.ProviderResolver, log *slog.Logger) memprovider.LLM {
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

func (r memoryEmbeddingModelResolver) ResolveEmbeddingModel(ctx context.Context, ref string) (memregistry.EmbeddingModelSpec, error) {
	model, err := r.model(ctx, ref)
	if err != nil {
		return memregistry.EmbeddingModelSpec{}, err
	}
	provider, err := r.providers.ResolveModelProvider(ctx, model.ProviderID)
	if err != nil {
		return memregistry.EmbeddingModelSpec{}, err
	}
	dimensions := 0
	if model.Config.Dimensions != nil {
		dimensions = *model.Config.Dimensions
	}
	return memregistry.EmbeddingModelSpec{
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
	llm memprovider.LLM,
	provider bridge.Provider,
	modelsService *modelcatalog.Service,
	modelProviders modelcatalog.ProviderResolver,
	pool *pgxpool.Pool,
	cfg config.Config,
) (*memregistry.Registry, error) {
	reg, cleanup, err := memregistry.NewPostgresRegistry(memregistry.Deps{
		Log:    log,
		Pool:   pool,
		Bridge: provider,
		LLM:    llm,
		EmbeddingModels: memoryEmbeddingModelResolver{
			models:    modelsService,
			providers: modelProviders,
		},
		PGVector: memregistry.PGVectorConfig{
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

func provideChatMessageStore(pool *pgxpool.Pool) message.Persistence {
	return agentassembly.NewPostgresMessagePersistence(
		pool,
		bot.NewSessionLocker(pool),
		func(tx pgx.Tx) agentassembly.BotSessionWriteLocker {
			return bot.NewSessionLockerFromTx(tx)
		},
	)
}

func provideChatThreadStore(pool *pgxpool.Pool) sessionpkg.Store {
	return agentassembly.NewPostgresThreadStore(pool)
}

type threadACPPolicyReader struct {
	bots bot.Persistence
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

func provideSessionService(log *slog.Logger, store sessionpkg.Store, botsStore bot.Persistence, hub *event.Hub) *sessionpkg.Service {
	service := sessionpkg.NewService(log, store, threadACPPolicyReader{bots: botsStore}, hub)
	service.SetACPSetupValidator(profile.NewCatalog())
	return service
}

func provideMessageService(log *slog.Logger, store message.Persistence, hub *event.Hub) *message.DBService {
	return message.NewService(log, store, hub)
}

func provideCompactionStore(pool *pgxpool.Pool) agentassembly.CompactionStore {
	return agentassembly.NewPostgresCompactionStore(pool)
}

func provideCompactionPersistence(store agentassembly.CompactionStore) compaction.CompactionStore {
	return store
}

func provideCompactionArtifacts(store agentassembly.CompactionStore) compaction.ArtifactStore {
	return store
}

func provideApplicationReads(pool *pgxpool.Pool) applicationpersistence.Reads {
	return application.NewPostgresReads(pool)
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

type acpSessionPoolParams struct {
	fx.In

	Lifecycle      fx.Lifecycle
	Log            *slog.Logger
	Runner         *acpclient.Runner
	Bots           *bot.Service
	Sessions       *sessionpkg.Service
	ToolGateway    *mcp.ToolGatewayService
	ToolContexts   *mcp.ToolSessionContextStore
	ToolApproval   *toolapproval.Service
	UserInput      *userinput.Service
	RuntimeHandler *runtimehttp.ContainerdHandler
}

func provideACPSessionPool(params acpSessionPoolParams) *acpagent.SessionPool {
	pool := acpagent.NewSessionPool(params.Log, params.Runner, params.Bots, session.NewSource(params.Sessions))
	pool.SetToolGateway(params.ToolGateway)
	pool.SetToolSessionContextStore(params.ToolContexts)
	pool.SetToolApprovalService(params.ToolApproval)
	pool.SetUserInputService(params.UserInput)
	params.RuntimeHandler.SetACPRuntimeResolver(pool)
	var (
		cancelReaper context.CancelFunc
		reaperDone   <-chan struct{}
	)
	params.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancelReaper = cancel
			reaperDone = pool.StartReaper(ctx)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if cancelReaper != nil {
				cancelReaper()
			}
			shutdownErr := pool.CloseAllContext(ctx)
			reaperErr := waitForBackground(ctx, reaperDone)
			return errors.Join(reaperErr, shutdownErr)
		},
	})
	return pool
}

type agentServiceParams struct {
	fx.In

	Log                 *slog.Logger
	Agent               *engine.Agent
	Models              *modelcatalog.Service
	ModelResolver       modelcatalog.ProviderResolver
	ApplicationReads    applicationpersistence.Reads
	ChannelIdentities   application.ChannelIdentityReader
	CompactionArtifacts compaction.ArtifactStore
	Messages            *message.DBService
	Settings            *setting.Service
	Accounts            *account.Service
	Bots                *bot.Service
	Media               *asset.Service
	RuntimeHandler      *runtimehttp.ContainerdHandler
	Workspace           workspace.Service
	Memory              *memregistry.Registry
	Channels            *gateway.Store
	Routes              *route.DBService
	Sessions            *sessionpkg.Service
	Events              *event.Hub
	Compaction          *compaction.Service
	Pipeline            *timeline.Pipeline
	Clock               runtimedomain.Clock
	Background          *background.Manager
	ToolApproval        *toolapproval.Service
	UserInput           *userinput.Service
	ACP                 *acpagent.SessionPool
	Hooks               *hookspkg.Service
}

func provideAgentService(params agentServiceParams) *application.Service {
	service := application.NewService(
		params.Log,
		params.Models,
		params.ModelResolver,
		params.Bots,
		params.Accounts,
		params.ChannelIdentities,
		params.ApplicationReads,
		params.CompactionArtifacts,
		params.ApplicationReads,
		params.Messages,
		params.Settings,
		params.Agent,
		params.Clock.Location,
		120*time.Second,
	)
	service.SetBotPermissionChecker(&applicationBotPermissionChecker{bots: params.Bots, accounts: params.Accounts})
	service.SetWorkspaceTargetResolver(params.Workspace)
	service.SetHookService(params.Hooks)
	if params.Sessions != nil {
		params.Sessions.SetHookService(params.Hooks)
	}
	if params.Compaction != nil {
		params.Compaction.SetHookService(params.Hooks)
	}
	if params.Workspace != nil {
		params.Workspace.SetHookService(params.Hooks)
	}
	service.SetMemoryRegistry(params.Memory)
	service.SetSkillLoader(&skillLoaderAdapter{handler: params.RuntimeHandler})
	service.SetGatewayAssetLoader(&gatewayAssetLoaderAdapter{media: params.Media})
	service.SetPlatformIdentitySource(identityadapter.NewSource(params.Channels))
	service.SetSessionService(params.Sessions)
	service.SetEventPublisher(params.Events)
	service.SetCompactionService(params.Compaction)
	service.SetPipeline(params.Pipeline)
	service.SetBackgroundManager(params.Background)
	if params.ToolApproval != nil {
		params.ToolApproval.SetHookService(params.Hooks)
		params.ToolApproval.SetWorkspaceTargetPolicyResolver(workspaceTargetPolicyResolver{manager: params.Workspace})
	}
	service.SetToolApprovalService(params.ToolApproval)
	service.SetUserInputService(params.UserInput)
	service.SetACPSessionPool(params.ACP)
	if params.Background != nil {
		params.Background.SetEventFunc(func(evt background.TaskEvent) {
			if params.Events == nil {
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
			params.Events.Publish(event.Event{
				Type:  event.EventTypeBackgroundTask,
				BotID: evt.BotID,
				Data:  data,
			})
		})
	}
	return service
}

type containerdHandlerParams struct {
	fx.In

	Log       *slog.Logger
	Workspace workspace.Service
	Config    config.Config
	Backend   ctr.Backend
	Bots      *bot.Service
	Accounts  *account.Service
	Policy    *policy.Service
	Plugins   *pluginspkg.Service
	Display   runtimedisplay.Service
}

func provideContainerdHandler(params containerdHandlerParams) *runtimehttp.ContainerdHandler {
	params.Workspace.SetSetupDiagnostics(params.Bots)
	h := runtimehttp.NewContainerdHandler(params.Log, params.Workspace, params.Config.Workspace, params.Backend.String(), params.Bots, params.Accounts, params.Policy, params.Display)
	h.SetPluginService(params.Plugins)
	return h
}

func provideScheduleService(log *slog.Logger, store schedulepersistence.Store, triggerer schedule.Triggerer, sessionCreator schedule.SessionCreator, tokens auth.TokenConfig, clock runtimedomain.Clock) *schedule.Service {
	return schedule.NewService(log, store, triggerer, sessionCreator, tokens.Secret, clock.Location)
}

func provideHeartbeatService(log *slog.Logger, store heartbeatpersistence.Store, triggerer heartbeat.Triggerer, sessionCreator heartbeat.SessionCreator, tokens auth.TokenConfig) *heartbeat.Service {
	return heartbeat.NewService(log, store, triggerer, sessionCreator, tokens.Secret)
}

func provideChatBackupStore(pool *pgxpool.Pool) (agentassembly.ChatBackupStore, error) {
	return agentassembly.NewPostgresChatBackupStore(pool, bot.ExclusiveLocker{})
}

func provideChannelBackupStore(pool *pgxpool.Pool) (channelassembly.BackupStore, error) {
	return channelassembly.NewPostgresBackupStore(pool, bot.ExclusiveLocker{})
}

type botBackupServiceParams struct {
	fx.In

	Log             *slog.Logger
	ChatBackup      agentassembly.ChatBackupStore
	ChannelBackup   channelassembly.BackupStore
	Bots            *bot.Service
	Settings        *setting.Service
	ACL             *acl.Service
	Channels        *gateway.Store
	MCP             *mcp.ConnectionService
	Schedules       *schedule.Service
	Email           *emailpkg.Service
	Providers       *providers.Service
	Models          *modelcatalog.Service
	SearchProviders *search.Service
	FetchProviders  *fetch.Service
	MemoryProviders *memcatalog.Service
	Workspace       workspace.Service
	ACP             *acpagent.SessionPool
}

func provideBotBackupService(params botBackupServiceParams) *botbackup.Service {
	return botbackup.New(botbackup.Params{
		Logger:          params.Log,
		Bots:            params.Bots,
		Settings:        params.Settings,
		ACL:             params.ACL,
		Channels:        params.Channels,
		MCP:             params.MCP,
		Schedules:       params.Schedules,
		Email:           params.Email,
		Providers:       params.Providers,
		Models:          params.Models,
		SearchProviders: params.SearchProviders,
		FetchProviders:  params.FetchProviders,
		MemoryProviders: params.MemoryProviders,
		Workspace:       params.Workspace,
		ChatBackup:      params.ChatBackup,
		ChannelBackup:   params.ChannelBackup,
		ACPRuntimes:     params.ACP,
	})
}

func provideFederationGateway(log *slog.Logger, containerdHandler *runtimehttp.ContainerdHandler) *runtimehttp.MCPFederationGateway {
	return runtimehttp.NewMCPFederationGateway(log, containerdHandler)
}

func provideOAuthService(log *slog.Logger, store mcppersistence.OAuthStore, cfg config.Config) *mcp.OAuthService {
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

type toolGatewayServiceParams struct {
	fx.In

	Log            *slog.Logger
	Federation     *runtimehttp.MCPFederationGateway
	OAuth          *mcp.OAuthService
	MCP            *mcp.ConnectionService
	RuntimeHandler *runtimehttp.ContainerdHandler
	Native         *tool.NativeToolSource
	ToolContexts   *mcp.ToolSessionContextStore
	Config         config.Config
}

func provideToolGatewayService(params toolGatewayServiceParams) *mcp.ToolGatewayService {
	params.Federation.SetOAuthService(params.OAuth)
	fedSource := mcpfederation.NewSource(params.Log, params.Federation, params.MCP, mcpfederation.WithReservedToolName(tool.IsBuiltInToolName))
	limits := agentLimitsFromConfig(params.Config.Agent)
	svc := mcp.NewToolGatewayService(params.Log, []mcp.ToolSource{params.Native, fedSource}, mcp.WithToolOutputLimit(limits.ToolOutputLimit()))
	params.RuntimeHandler.SetToolGatewayService(svc)
	params.RuntimeHandler.SetToolSessionContextStore(params.ToolContexts)
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

func provideHistorySearcher(pool *pgxpool.Pool) toolpersistence.HistorySearcher {
	return tool.NewPostgresHistorySearcher(pool)
}

type toolProviderParams struct {
	fx.In

	Log             *slog.Logger
	ChannelRuntime  gateway.Runtime
	ChannelRegistry *gateway.Registry
	Routes          *route.DBService
	Schedules       *schedule.Service
	Settings        *setting.Service
	Search          *search.Service
	Fetch           *fetch.Service
	Workspace       workspace.Service
	Media           *asset.Service
	Memory          *memregistry.Registry
	Email           *emailpkg.Service
	EmailRuntime    emailpkg.Runtime
	Federation      *runtimehttp.MCPFederationGateway
	MCP             *mcp.ConnectionService
	Models          *modelcatalog.Service
	ModelResolver   modelcatalog.ProviderResolver
	History         toolpersistence.HistorySearcher
	Audio           *audiopkg.Service
	Video           *videopkg.Service
	Sessions        *sessionpkg.Service
	Messages        *message.DBService
	Background      *background.Manager
	Hooks           *hookspkg.Service
	Display         runtimedisplay.Service
}

func provideToolProviders(params toolProviderParams) []tool.ToolProvider {
	var assetResolver delivery.AssetResolver
	if params.Media != nil {
		assetResolver = &mediaAssetResolverAdapter{media: params.Media}
	}
	channelMessaging := messaging.New(params.ChannelRuntime, params.ChannelRegistry, assetResolver)
	fedSource := mcpfederation.NewSource(params.Log, params.Federation, params.MCP, mcpfederation.WithReservedToolName(tool.IsBuiltInToolName))
	return []tool.ToolProvider{
		tool.NewAskUserProvider(params.Log),
		tool.NewMessageProvider(params.Log, channelMessaging, channelMessaging, channelMessaging, assetResolver),
		tool.NewContactsProvider(params.Log, contact.NewSource(params.Routes)),
		tool.NewScheduleProvider(params.Log, params.Schedules),
		tool.NewMemoryProvider(params.Log, params.Memory, params.Settings),
		tool.NewWebProvider(params.Log, params.Settings, params.Search),
		tool.NewContainerProvider(params.Log, params.Workspace, params.Background, runtimedomain.DefaultDataMount, params.Hooks),
		tool.NewBackgroundProvider(params.Log, params.Background),
		tool.NewBrowserProvider(params.Log, params.Settings, nativeWorkspaceBridgeProvider{manager: params.Workspace}, params.Display, runtimedomain.DefaultDataMount),
		tool.NewEmailProvider(params.Log, params.Email, params.EmailRuntime),
		tool.NewWebFetchProvider(params.Log, params.Settings, params.Fetch),
		tool.NewSpawnProvider(params.Log, params.Settings, params.Models, params.ModelResolver, params.Sessions, params.Background),
		tool.NewSkillProvider(params.Log),
		tool.NewTTSProvider(params.Log, params.Settings, params.Audio, channelMessaging, channelMessaging),
		tool.NewTranscriptionProvider(params.Log, params.Settings, params.Audio, params.Media),
		tool.NewImageGenProvider(params.Log, params.Settings, params.Models, params.ModelResolver, params.Workspace, runtimedomain.DefaultDataMount),
		tool.NewVideoGenProvider(params.Log, params.Settings, params.Video, params.Background, params.Workspace, runtimedomain.DefaultDataMount),
		tool.NewFederationProvider(params.Log, fedSource),
		tool.NewHistoryProvider(params.Log, thread.NewLister(params.Sessions, params.Routes), params.Messages, params.History),
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
	finished := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				defer close(finished)
				store.StartCleanup(done)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			close(done)
			return waitForBackground(ctx, finished)
		},
	})
}

func startBackgroundTaskCleanup(lc fx.Lifecycle, mgr *background.Manager) {
	done := make(chan struct{})
	finished := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				defer close(finished)
				mgr.StartCleanupLoop(done, background.DefaultCleanupInterval, background.DefaultTaskRetention)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			close(done)
			shutdownErr := mgr.Shutdown(ctx)
			cleanupErr := waitForBackground(ctx, finished)
			return errors.Join(shutdownErr, cleanupErr)
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
			if err := scheduleService.Bootstrap(ctx); err != nil {
				return err
			}
			return scheduleService.Start(ctx)
		},
		OnStop: scheduleService.Shutdown,
	})
}

func startHeartbeatService(lc fx.Lifecycle, heartbeatService *heartbeat.Service) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := heartbeatService.Bootstrap(ctx); err != nil {
				return err
			}
			return heartbeatService.Start(ctx)
		},
		OnStop: heartbeatService.Shutdown,
	})
}

func startContainerReconciliation(lc fx.Lifecycle, manager workspace.Service, _ *runtimehttp.ContainerdHandler, _ *mcp.ToolGatewayService) {
	var (
		cancel context.CancelFunc
		done   chan struct{}
	)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, cancelTask := context.WithCancel(context.Background())
			cancel = cancelTask
			done = make(chan struct{})
			go func() {
				defer close(done)
				manager.ReconcileContainers(ctx)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if cancel != nil {
				cancel()
			}
			return waitForBackground(ctx, done)
		},
	})
}

func waitForBackground(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type lazyLLMClient struct {
	modelsService    *modelcatalog.Service
	settingsService  *setting.Service
	providerResolver modelcatalog.ProviderResolver
	timeout          time.Duration
	logger           *slog.Logger
}

func (c *lazyLLMClient) Extract(ctx context.Context, req memprovider.ExtractRequest) (memprovider.ExtractResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memprovider.ExtractResponse{}, err
	}
	return client.Extract(ctx, req)
}

func (c *lazyLLMClient) Decide(ctx context.Context, req memprovider.DecideRequest) (memprovider.DecideResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memprovider.DecideResponse{}, err
	}
	return client.Decide(ctx, req)
}

func (c *lazyLLMClient) Compact(ctx context.Context, req memprovider.CompactRequest) (memprovider.CompactResponse, error) {
	client, err := c.resolve(ctx, req.BotID)
	if err != nil {
		return memprovider.CompactResponse{}, err
	}
	return client.Compact(ctx, req)
}

func (c *lazyLLMClient) resolve(ctx context.Context, botID string) (memprovider.LLM, error) {
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
	return memregistry.NewFormationClient(memregistry.FormationClientConfig{
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
