//go:build !split

package main

import (
	"errors"
	"log/slog"
	"strings"

	"go.uber.org/fx"

	channelprocess "github.com/memohai/memoh/cmd/internal/channel"
	channelruntime "github.com/memohai/memoh/cmd/internal/channel/runtime"
	"github.com/memohai/memoh/domains/channel"
	adaptercatalog "github.com/memohai/memoh/domains/channel/adapter/catalog"
	"github.com/memohai/memoh/domains/channel/gateway"
	channelhttphost "github.com/memohai/memoh/domains/channel/http"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/media/asset"
	"github.com/memohai/memoh/internal/config"
)

const buildProfile = "embedded"

func optionsFor(cfg config.Config) fx.Option {
	return fx.Options(commonOptions(cfg), embeddedOptions())
}

func validateProfile(cfg config.Config) error {
	if strings.TrimSpace(cfg.InternalRPC.SharedSecret) != "" {
		return errors.New("embedded build profile does not use internal_rpc.shared_secret; clear it or use memoh-server built with -tags split")
	}
	return nil
}

func embeddedOptions() fx.Option {
	return fx.Options(
		channelprocess.LocalFoundationModule(),
		fx.Provide(
			providePlatformChannelRegistry,
			provideEmbeddedChannelIdentityReader,
		),
		channelprocess.ServerLocalModule(),
		channelruntime.EmbeddedModule(),
		channelruntime.LifecycleModule(),
		fx.Provide(
			provideLocalWebhookTunnelStatus,
			provideServerHandler(gateway.NewWebhookServerHandler),
			provideServerHandler(adaptercatalog.NewWeixinQRServerHandler),
			provideServerHandler(channelhttphost.NewEmailWebhookHandler),
			provideServerHandler(providePublicMediaHandler),
		),
		fx.Invoke(adaptercatalog.WirePersistence),
	)
}

func provideEmbeddedChannelIdentityReader(service *identity.Service) channel.IdentityReader {
	return service
}

func providePublicMediaHandler(log *slog.Logger, cfg config.Config, mediaService *asset.Service) *channelhttphost.PublicMediaHandler {
	return channelhttphost.NewConfiguredPublicMediaHandler(log, cfg, mediaService)
}
