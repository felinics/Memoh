//go:build !split

package main

import (
	"log/slog"

	"go.uber.org/fx"

	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/platformreg"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/domains/media/asset"
	"github.com/memohai/memoh/internal/config"
)

type platformRegistryParams struct {
	fx.In

	Log           *slog.Logger
	Config        config.Config
	Hub           *local.RouteHub
	MediaService  *asset.Service
	TunnelManager webhook.Manager `optional:"true"`
	UserInput     *userinput.Service
}

func providePlatformChannelRegistry(params platformRegistryParams) *gateway.Registry {
	return platformreg.NewRegistry(platformreg.Deps{
		Log:           params.Log,
		Config:        params.Config,
		Hub:           params.Hub,
		MediaService:  params.MediaService,
		TunnelManager: params.TunnelManager,
		UserInput:     params.UserInput,
	})
}
