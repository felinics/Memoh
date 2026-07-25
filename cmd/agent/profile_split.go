//go:build split

package main

import (
	"go.uber.org/fx"

	"github.com/memohai/memoh/domains/channel"
	channelmodule "github.com/memohai/memoh/domains/channel/assembly"
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
		channelmodule.ServerLocalModule(),
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
