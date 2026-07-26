package runtime

import (
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/api/bot/access/acl"
	"github.com/memohai/memoh/domains/api/bot/setting"
	channeladapter "github.com/memohai/memoh/domains/channel/adapter"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	emailcatalog "github.com/memohai/memoh/domains/channel/email/catalog"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/inbound"
	"github.com/memohai/memoh/domains/media/asset"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/oauth"
	"github.com/memohai/memoh/internal/rpc/runtime/server"
)

func provideLocalMediaService(log *slog.Logger, cfg config.Config) *asset.Service {
	dataRoot := cfg.Workspace.DataRoot
	if strings.TrimSpace(dataRoot) == "" {
		dataRoot = config.DefaultDataRoot
	}
	return asset.NewLocalService(log, filepath.Join(dataRoot, "media"))
}

func provideStandaloneChannelSettings(log *slog.Logger, pool *pgxpool.Pool, aclService *acl.Service) channeladapter.Settings {
	store := setting.NewPostgresPersistence(pool)
	return setting.NewService(log, store, nil, aclService, nil)
}

func provideEmailTrigger(log *slog.Logger, service *emailpkg.Service, chatTriggerer emailpkg.ChatTriggerer) *emailpkg.Trigger {
	return emailpkg.NewTrigger(log, service, chatTriggerer)
}

func registerEmailAdapters(log *slog.Logger, service *emailpkg.Service, tokens emailpkg.OAuthTokenStore, clients *oauth.Registry) {
	emailcatalog.RegisterDefaults(
		service.Registry(),
		log,
		tokens,
		channeladapter.NewEmailOAuthClientResolver(clients),
	)
}

func provideLocalChannelRuntime(lifecycle *gateway.Lifecycle, store *gateway.Store, manager *gateway.Manager) *gateway.LocalRuntime {
	return &gateway.LocalRuntime{Lifecycle: lifecycle, Store: store, Manager: manager}
}

func provideChannelLifecycleService(store *gateway.Store, manager *gateway.Manager) *gateway.Lifecycle {
	return gateway.NewLifecycle(store, manager)
}

func provideChannelRuntimeInterface(runtime *gateway.LocalRuntime) gateway.Runtime { return runtime }

func provideEmailRuntimeInterface(manager *emailpkg.Manager) emailpkg.Runtime { return manager }

func provideRemoteCommandHandler(client *server.Client) inbound.CommandHandler { return client }

func provideRemoteSkillResolver(client *server.Client) inbound.RequestedSkillResolver {
	return client
}

func provideRemoteChannelAudio(client *server.Client) channeladapter.Audio { return client }
