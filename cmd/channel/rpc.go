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

	"github.com/felinics/memoh/internal/agent/turn"
	turntransport "github.com/felinics/memoh/internal/agent/turn/grpctransport"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/email"
	intrpc "github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/rpc/channelruntime"
	runtimeRpc "github.com/felinics/memoh/internal/rpc/runtime"
	"github.com/felinics/memoh/internal/rpc/runtimepb"
	"github.com/felinics/memoh/internal/rpc/serverruntime"
	"github.com/felinics/memoh/internal/webhooktunnel"
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

func provideTurnClient(conn *grpc.ClientConn, log *slog.Logger) turn.Service {
	return turntransport.NewClient(conn, turntransport.WithClientLogger(log))
}

func provideRuntimeRPCClient(conn *grpc.ClientConn) *runtimeRpc.Client {
	return runtimeRpc.NewClient(conn)
}

func provideServerRuntimeClient(client *runtimeRpc.Client) *serverruntime.Client {
	return serverruntime.NewClient(client)
}

func provideChannelRPC(log *slog.Logger, cfg config.Config, channelRuntime channel.Runtime, emailRuntime email.Runtime, tunnel *webhooktunnel.Manager) (*channelRPC, error) {
	if err := cfg.ValidateChannelRuntime(); err != nil {
		return nil, err
	}
	server := intrpc.NewServer(cfg.InternalRPC.SharedSecret)
	runtimepb.RegisterRuntimeServiceServer(server, runtimeRpc.NewServer(log, channelruntime.Handlers(channelRuntime, emailRuntime, tunnel)))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	return &channelRPC{server: server, addr: cfg.Channel.RPCListenAddr}, nil
}

func startChannelRPC(lc fx.Lifecycle, log *slog.Logger, rpcServer *channelRPC, shutdowner fx.Shutdowner) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", rpcServer.addr)
			if err != nil {
				return fmt.Errorf("listen channel rpc: %w", err)
			}
			go func() {
				log.Info("channel rpc listening", slog.String("addr", rpcServer.addr))
				if err := rpcServer.server.Serve(lis); err != nil {
					log.Error("channel rpc failed", slog.Any("error", err))
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
