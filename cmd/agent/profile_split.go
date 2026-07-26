//go:build split

package main

import (
	"go.uber.org/fx"

	channelprocess "github.com/memohai/memoh/cmd/internal/channel"
	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/config"
	channelclient "github.com/memohai/memoh/internal/rpc/channel/client"
)

const buildProfile = "split"

func optionsFor(cfg config.Config) fx.Option {
	return fx.Options(commonOptions(cfg), splitOptions())
}

func validateProfile(cfg config.Config) error {
	return cfg.ValidateServerRuntime()
}

func splitOptions() fx.Option {
	return fx.Options(
		channelprocess.FoundationModule(),
		fx.Provide(channelprocess.NewLocalRegistry),
		channelprocess.ServerLocalModule(),
		fx.Provide(
			provideChannelRPCConn,
			provideChannelContractClient,
			provideSplitChannelIdentityReader,
			provideSplitConversationProjectionReader,
			provideRuntimeRPCClient,
			provideChannelRuntimeClient,
			provideChannelRuntime,
			provideEmailRuntime,
			provideWebhookTunnelStatus,
			provideServerRPC,
		),
		fx.Invoke(startServerRPC),
	)
}

func provideSplitChannelIdentityReader(client *channelclient.Client) channel.IdentityReader {
	return client
}

func provideSplitConversationProjectionReader(client *channelclient.Client) channel.ConversationProjectionReader {
	return client
}
