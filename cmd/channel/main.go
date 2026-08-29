// cmd/channel hosts external channel adapters, email receivers, and webhook
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

	"github.com/labstack/echo/v4"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	channelmodule "github.com/felinics/memoh/cmd/internal/channel"
	coremodule "github.com/felinics/memoh/cmd/internal/core"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/weixin"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/handlers"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/version"
)

type healthHandler struct{}

func newHealthHandler() *healthHandler { return &healthHandler{} }

func (*healthHandler) Register(e *echo.Echo) {
	e.GET("/ping", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"status":      "ok",
			"service":     "channel",
			"version":     version.Version,
			"commit_hash": version.ShortCommitHash(),
		})
	})
	e.HEAD("/health", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
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
		coremodule.FoundationModule(),
		channelmodule.FoundationModule(),
		channelmodule.RuntimeModule(),
		fx.Provide(
			provideServerRPCConn,
			provideTurnClient,
			provideRuntimeRPCClient,
			provideServerRuntimeClient,
			provideChannelRPC,
			provideServerHandler(newHealthHandler),
			provideServerHandler(channel.NewWebhookServerHandler),
			provideServerHandler(weixin.NewQRServerHandler),
			provideServerHandler(handlers.NewConfiguredPublicMediaHandler),
			provideServerHandler(handlers.NewEmailWebhookHandler),
			provideServer,
		),
		fx.Invoke(startChannelRPC, startServer),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger.With(slog.String("component", "fx"))}
		}),
	)
}
