// cmd/channel hosts external channel adapters, and webhook
// endpoints as a standalone service. Agent turns run in the Server process and
// are reached through the authenticated internal RPC transport.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"google.golang.org/grpc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	channelmodule "github.com/felinics/memoh/cmd/internal/channel"
	coremodule "github.com/felinics/memoh/cmd/internal/core"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/weixin"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/handlers"
	"github.com/felinics/memoh/internal/rpc/storagepb"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/telemetry"
	"github.com/felinics/memoh/internal/version"
)

type healthHandler struct {
	serverHealth grpc_health_v1.HealthClient
}

func newHealthHandler(conn *grpc.ClientConn) *healthHandler {
	return &healthHandler{serverHealth: grpc_health_v1.NewHealthClient(conn)}
}

func (h *healthHandler) Register(e *echo.Echo) {
	e.GET("/ping", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"status":      "ok",
			"service":     "channel",
			"version":     version.Version,
			"commit_hash": version.ShortCommitHash(),
		})
	})
	// Kubernetes httpGet probes always issue GET. Keep readiness separate from
	// /ping so a transient Server outage removes Channel from Service endpoints
	// without turning it into a liveness restart loop.
	e.GET("/ready", h.readiness)
	e.HEAD("/ready", h.readiness)
	e.HEAD("/health", h.readiness)
}

func (h *healthHandler) readiness(c echo.Context) error {
	if h == nil || h.serverHealth == nil {
		return c.NoContent(http.StatusServiceUnavailable)
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
	defer cancel()
	response, err := h.serverHealth.Check(ctx, &grpc_health_v1.HealthCheckRequest{
		Service: storagepb.StorageService_ServiceDesc.ServiceName,
	})
	if err != nil || response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return c.NoContent(http.StatusServiceUnavailable)
	}
	return c.NoContent(http.StatusOK)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("memoh-channel %s\n", version.GetInfo())
		return
	}
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "Usage: memoh-channel [serve|version]")
		os.Exit(1)
	}
	fx.New(options()).Run()
}

func provideConfig() (config.Config, error) {
	cfg, err := config.Load(os.Getenv("CONFIG_PATH"))
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func provideServerHandler(fn any) any {
	return fx.Annotate(
		fn,
		fx.As(new(server.Handler)),
		fx.ResultTags(`group:"server_handlers"`),
	)
}

type serverParams struct {
	fx.In

	Logger         *slog.Logger
	Config         config.Config
	ServerHandlers []server.Handler `group:"server_handlers"`
}

// provideServer hosts only the channel-owned HTTP surface: platform
// webhooks and the weixin QR callback, plus ping for liveness.
func provideServer(params serverParams) *server.Server {
	return server.NewServer(params.Logger, params.Config.Channel.Addr, params.Config.Auth.JWTSecret, params.ServerHandlers...)
}

func startServer(lc fx.Lifecycle, logger *slog.Logger, srv *server.Server, shutdowner fx.Shutdowner) {
	fmt.Printf("Starting Memoh Channel %s\n", version.GetInfo())
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("server failed", slog.Any("error", err))
					_ = shutdowner.Shutdown()
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return srv.Stop(ctx)
		},
	})
}

func options() fx.Option {
	return fx.Options(
		fx.Provide(provideConfig),
		fx.Supply(telemetry.Service{Name: "memoh-channel"}),
		coremodule.FoundationModule(),
		channelmodule.FoundationModule(),
		channelmodule.RuntimeModule(),
		fx.Provide(
			provideServerRPCConn,
			provideRemoteMediaService,
			provideTurnClient,
			provideRuntimeRPCClient,
			provideServerRuntimeClient,
			provideChannelRPC,
			provideServerHandler(newHealthHandler),
			provideServerHandler(channel.NewWebhookServerHandler),
			provideServerHandler(weixin.NewQRServerHandler),
			provideServerHandler(handlers.NewConfiguredPublicMediaHandler),
			provideServer,
		),
		fx.Invoke(startChannelRPC, startServer),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger.With(slog.String("component", "fx"))}
		}),
	)
}
