package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/command"
	"github.com/memohai/memoh/domains/api/http/chat/local"
	runtimehttp "github.com/memohai/memoh/domains/api/http/runtime"
	"github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/domains/model/audio"
	"github.com/memohai/memoh/internal/config"
	intrpc "github.com/memohai/memoh/internal/rpc"
	channelclient "github.com/memohai/memoh/internal/rpc/channel/client"
	turntransport "github.com/memohai/memoh/internal/rpc/channel/turn/grpctransport"
	"github.com/memohai/memoh/internal/rpc/channel/turn/turnpb"
	runtimeRpc "github.com/memohai/memoh/internal/rpc/runtime"
	channelruntime "github.com/memohai/memoh/internal/rpc/runtime/channel"
	"github.com/memohai/memoh/internal/rpc/runtime/runtimepb"
	serverruntime "github.com/memohai/memoh/internal/rpc/runtime/server"
)

type serverRPC struct {
	server *grpc.Server
	addr   string
}

func provideChannelRPCConn(lc fx.Lifecycle, cfg config.Config) (*grpc.ClientConn, error) {
	conn, err := intrpc.Dial(cfg.InternalRPC.ChannelTarget, cfg.InternalRPC.SharedSecret)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error { return conn.Close() }})
	return conn, nil
}

func provideRuntimeRPCClient(conn *grpc.ClientConn) *runtimeRpc.Client {
	return runtimeRpc.NewClient(conn)
}

func provideChannelContractClient(conn *grpc.ClientConn) *channelclient.Client {
	return channelclient.New(conn)
}

func provideChannelRuntimeClient(client *runtimeRpc.Client) *channelruntime.Client {
	return channelruntime.NewClient(client)
}

func provideChannelRuntime(client *channelruntime.Client, manager *gateway.Manager) gateway.Runtime {
	return &localFirstChannelRuntime{local: manager, remote: client}
}
func provideEmailRuntime(client *channelruntime.Client) email.Runtime { return client }

// channelSendRuntime is the slice of *gateway.Manager the local route needs.
type channelSendRuntime interface {
	Send(context.Context, string, gateway.ChannelType, gateway.SendRequest) error
	React(context.Context, string, gateway.ChannelType, gateway.ReactRequest) error
}

// localFirstChannelRuntime routes local channel types (web/cli) to this
// process's manager and everything else over the internal RPC. The Web SSE
// stream subscribes to THIS process's RouteHub; the channel process has its
// own hub with no subscribers, so delivering web sends remotely would drop
// them silently (send_message/speak/schedule notifications to the Web
// surface would vanish).
type localFirstChannelRuntime struct {
	local  channelSendRuntime
	remote gateway.Runtime
}

func isLocalChannelType(typ gateway.ChannelType) bool {
	switch typ {
	case local.WebType, local.CLIType:
		return true
	default:
		return false
	}
}

func (r *localFirstChannelRuntime) Send(ctx context.Context, botID string, typ gateway.ChannelType, req gateway.SendRequest) error {
	if isLocalChannelType(typ) {
		return r.local.Send(ctx, botID, typ, req)
	}
	return r.remote.Send(ctx, botID, typ, req)
}

func (r *localFirstChannelRuntime) React(ctx context.Context, botID string, typ gateway.ChannelType, req gateway.ReactRequest) error {
	if isLocalChannelType(typ) {
		return r.local.React(ctx, botID, typ, req)
	}
	return r.remote.React(ctx, botID, typ, req)
}

func (r *localFirstChannelRuntime) UpsertBotChannelConfig(ctx context.Context, botID string, typ gateway.ChannelType, req gateway.UpsertConfigRequest) (gateway.ChannelConfig, error) {
	return r.remote.UpsertBotChannelConfig(ctx, botID, typ, req)
}

func (r *localFirstChannelRuntime) SetBotChannelStatus(ctx context.Context, botID string, typ gateway.ChannelType, disabled bool) (gateway.ChannelConfig, error) {
	return r.remote.SetBotChannelStatus(ctx, botID, typ, disabled)
}

func (r *localFirstChannelRuntime) DeleteBotChannelConfig(ctx context.Context, botID string, typ gateway.ChannelType) error {
	return r.remote.DeleteBotChannelConfig(ctx, botID, typ)
}

func (r *localFirstChannelRuntime) SetWebhookEndpoint(ctx context.Context, botID string, typ gateway.ChannelType, req gateway.SetWebhookEndpointRequest) (gateway.SetWebhookEndpointResponse, error) {
	return r.remote.SetWebhookEndpoint(ctx, botID, typ, req)
}

func (r *localFirstChannelRuntime) ConnectionStatusesByBot(botID string) []gateway.ConnectionStatus {
	return r.remote.ConnectionStatusesByBot(botID)
}

func provideWebhookTunnelStatus(client *channelruntime.Client) webhook.Service {
	return client
}

// provideLocalWebhookTunnelStatus is the embedded-mode counterpart: the
// tunnel manager runs in this process.
func provideLocalWebhookTunnelStatus(manager webhook.Manager) webhook.Service {
	return manager
}

func provideServerRPC(log *slog.Logger, cfg config.Config, turnService agentdomain.Service, commandHandler *command.Handler, skillHandler *runtimehttp.ContainerdHandler, audioService *audio.Service) (*serverRPC, error) {
	server := intrpc.NewServer(cfg.InternalRPC.SharedSecret)
	turnpb.RegisterTurnServiceServer(server, turntransport.NewServer(log, turnService))
	runtimepb.RegisterRuntimeServiceServer(server, runtimeRpc.NewServer(log, serverruntime.Handlers(commandHandler, skillHandler, audioService)))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	return &serverRPC{server: server, addr: cfg.Server.RPCListenAddr}, nil
}

func startServerRPC(lc fx.Lifecycle, log *slog.Logger, rpcServer *serverRPC, shutdowner fx.Shutdowner) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", rpcServer.addr)
			if err != nil {
				return fmt.Errorf("listen server rpc: %w", err)
			}
			go func() {
				log.InfoContext(ctx, "server rpc listening", slog.String("addr", rpcServer.addr))
				if err := rpcServer.server.Serve(lis); err != nil {
					log.ErrorContext(ctx, "server rpc failed", slog.Any("error", err))
					_ = shutdowner.Shutdown()
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			intrpc.StopGracefully(rpcServer.server, ctx.Done(), 10*time.Second)
			return nil
		},
	})
}
