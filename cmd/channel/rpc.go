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
	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/internal/config"
	intrpc "github.com/memohai/memoh/internal/rpc"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	channelserver "github.com/memohai/memoh/internal/rpc/channel/server"
	turntransport "github.com/memohai/memoh/internal/rpc/channel/turn/grpctransport"
	runtimeRpc "github.com/memohai/memoh/internal/rpc/runtime"
	channelruntime "github.com/memohai/memoh/internal/rpc/runtime/channel"
	"github.com/memohai/memoh/internal/rpc/runtime/runtimepb"
	serverruntime "github.com/memohai/memoh/internal/rpc/runtime/server"
)

type channelRPC struct {
	server *grpc.Server
	addr   string
}

func provideServerRPCConn(lc fx.Lifecycle, cfg config.Config) (*grpc.ClientConn, error) {
	conn, err := intrpc.Dial(cfg.InternalRPC.ServerTarget, cfg.InternalRPC.SharedSecret)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error { return conn.Close() }})
	return conn, nil
}

func provideTurnClient(conn *grpc.ClientConn, log *slog.Logger) agentdomain.Service {
	return turntransport.NewClient(conn, turntransport.WithClientLogger(log))
}

func provideRuntimeRPCClient(conn *grpc.ClientConn) *runtimeRpc.Client {
	return runtimeRpc.NewClient(conn)
}

func provideServerRuntimeClient(client *runtimeRpc.Client) *serverruntime.Client {
	return serverruntime.NewClient(client)
}

func provideChannelRPC(log *slog.Logger, cfg config.Config, channelRuntime gateway.Runtime, emailRuntime email.Runtime, tunnel webhook.Manager, identities *identity.Service, projections channel.ConversationProjectionReader) (*channelRPC, error) {
	server := intrpc.NewServer(cfg.InternalRPC.SharedSecret)
	channelpb.RegisterChannelAdminServiceServer(server, channelserver.NewAdmin(nil, identities, projections))
	runtimepb.RegisterRuntimeServiceServer(server, runtimeRpc.NewServer(log, channelruntime.Handlers(channelRuntime, emailRuntime, tunnel)))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	return &channelRPC{server: server, addr: cfg.Channel.RPCListenAddr}, nil
}

func startChannelRPC(lc fx.Lifecycle, log *slog.Logger, rpcServer *channelRPC, shutdowner fx.Shutdowner) {
	serveDone := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", rpcServer.addr)
			if err != nil {
				return fmt.Errorf("listen channel rpc: %w", err)
			}
			go func() {
				defer close(serveDone)
				log.Info("channel rpc listening", slog.String("addr", lis.Addr().String()))
				handleServeError(log, shutdowner, "channel rpc", rpcServer.server.Serve(lis), grpc.ErrServerStopped)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			intrpc.StopGracefully(rpcServer.server, ctx.Done(), 10*time.Second)
			return waitForServe(ctx, serveDone, "channel rpc")
		},
	})
}
