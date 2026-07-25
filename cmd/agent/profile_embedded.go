//go:build !split

package main

import (
	"errors"
	"strings"

	"go.uber.org/fx"

	"github.com/memohai/memoh/domains/channel"
	channelmodule "github.com/memohai/memoh/domains/channel/assembly"
	"github.com/memohai/memoh/domains/channel/gateway"
	channelhttphost "github.com/memohai/memoh/domains/channel/http"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/platformreg"
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
		fx.Provide(
			providePlatformChannelRegistry,
			provideEmbeddedChannelIdentityReader,
			channelmodule.ProvideConversationProjectionReader,
		),
		channelmodule.EmbeddedModule(),
		fx.Provide(
			provideLocalWebhookTunnelStatus,
			provideServerHandler(gateway.NewWebhookServerHandler),
			provideServerHandler(platformreg.NewWeixinQRServerHandler),
			provideServerHandler(channelhttphost.NewEmailWebhookHandler),
			provideServerHandler(providePublicMediaHandler),
		),
	)
}

func provideEmbeddedChannelIdentityReader(service *identity.Service) channel.IdentityReader {
	return service
}
