package main

import (
	"log/slog"

	"go.uber.org/fx"

	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	adaptercatalog "github.com/memohai/memoh/domains/channel/adapter/catalog"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/route"
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
	Identities    *identity.Service
	Routes        *route.DBService
}

func providePlatformChannelRegistry(params platformRegistryParams) *gateway.Registry {
	return adaptercatalog.NewRegistry(adaptercatalog.Deps{
		Log:           params.Log,
		Config:        params.Config,
		Hub:           params.Hub,
		MediaService:  params.MediaService,
		TunnelManager: params.TunnelManager,
		UserInput:     params.UserInput,
		Identities:    params.Identities,
		Routes:        params.Routes,
	})
}
