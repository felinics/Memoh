//go:build split

package main

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/config"
	channelclient "github.com/memohai/memoh/internal/rpc/channel/client"
)

func TestBuildProfileIsSplit(t *testing.T) {
	if buildProfile != "split" {
		t.Fatalf("build profile = %q, want split", buildProfile)
	}
}

func TestSplitIdentityReaderUsesChannelRPCClient(t *testing.T) {
	client := &channelclient.Client{}
	if got := provideSplitChannelIdentityReader(client); got != client {
		t.Fatalf("identity reader = %T, want channel RPC client", got)
	}
}

func TestSplitConversationProjectionReaderUsesChannelRPCClient(t *testing.T) {
	client := &channelclient.Client{}
	if got := provideSplitConversationProjectionReader(client); got != client {
		t.Fatalf("conversation projection reader = %T, want channel RPC client", got)
	}
}

func TestSplitProfileRequiresServerRPCConfiguration(t *testing.T) {
	cfg := config.Config{
		Server: config.ServerConfig{
			Addr:          "127.0.0.1:8080",
			RPCListenAddr: config.DefaultServerRPCListenAddr,
		},
		InternalRPC: config.InternalRPCConfig{
			SharedSecret:  "test-only-secret",
			ChannelTarget: config.DefaultChannelRPCTarget,
		},
	}
	if err := validateProfile(cfg); err != nil {
		t.Fatalf("valid split profile: %v", err)
	}

	cfg.InternalRPC.ChannelTarget = ""
	err := validateProfile(cfg)
	if err == nil || !strings.Contains(err.Error(), "channel_target") {
		t.Fatalf("validation error = %v", err)
	}
}
