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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	channelprocess "github.com/memohai/memoh/cmd/internal/channel"
	channelruntime "github.com/memohai/memoh/cmd/internal/channel/runtime"
	coremodule "github.com/memohai/memoh/cmd/internal/core"
	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/application"
	agentassembly "github.com/memohai/memoh/domains/agent/assembly"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/api/http/server"
	"github.com/memohai/memoh/domains/channel"
	adaptercatalog "github.com/memohai/memoh/domains/channel/adapter/catalog"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	channelhttphost "github.com/memohai/memoh/domains/channel/http"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/media/asset"
	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/version"
)

const buildProfile = "channel"

type healthHandler struct{}

func newHealthHandler() *healthHandler { return &healthHandler{} }

func (*healthHandler) Register(e *echo.Echo) {
	e.GET("/ping", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"status":      "ok",
			"service":     "channel",
			"version":     version.Version,
			"commit_hash": version.ShortCommitHash(),
			"profile":     buildProfile,
		})
	})
	e.HEAD("/health", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("memoh-channel %s\n", version.GetInfo(buildProfile))
		return
	}
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "Usage: memoh-channel [serve|version]")
		os.Exit(1)
	}
	cfg, err := provideConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "memoh-channel: %v\n", err)
		os.Exit(1)
	}
	fx.New(options(cfg)).Run()
}

func provideConfig() (config.Config, error) {
	cfg, err := config.Load(os.Getenv("CONFIG_PATH"))
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	if err := cfg.ValidateChannelRuntime(); err != nil {
		return config.Config{}, fmt.Errorf("validate runtime config: %w", err)
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

func providePublicMediaHandler(log *slog.Logger, cfg config.Config, mediaService *asset.Service) *channelhttphost.PublicMediaHandler {
	return channelhttphost.NewConfiguredPublicMediaHandler(log, cfg, mediaService)
}

func provideChannelIdentityReader(service *identity.Service) channel.IdentityReader {
	return service
}

// provideCompactionArtifacts binds the standalone Channel process to the
// Agent-owned persistence seam. Domain wiring consumes only the Agent port.
func provideCompactionArtifacts(pool *pgxpool.Pool) compaction.ArtifactStore {
	return agentassembly.NewPostgresCompactionStore(pool)
}

func provideEmailChatTriggerer(turns agentdomain.Service, owners application.BotOwnerResolver, cfg config.Config, log *slog.Logger) emailpkg.ChatTriggerer {
	return application.NewEmailChatGateway(turns, owners, cfg.Auth.JWTSecret, log)
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
	fmt.Printf("Starting Memoh Channel %s\n", version.GetInfo(buildProfile))
	serveDone := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			listener, err := srv.Listen(ctx)
			if err != nil {
				return fmt.Errorf("listen channel http: %w", err)
			}
			go func() {
				defer close(serveDone)
				handleServeError(logger, shutdowner, "channel http server", srv.Serve(listener), http.ErrServerClosed)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			var stopErr error
			if err := srv.Stop(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				stopErr = fmt.Errorf("stop channel http: %w", err)
			}
			return errors.Join(stopErr, waitForServe(ctx, serveDone, "channel http server"))
		},
	})
}

func handleServeError(logger *slog.Logger, shutdowner fx.Shutdowner, name string, err, stoppedErr error) {
	if err == nil || errors.Is(err, stoppedErr) {
		return
	}
	logger.Error(name+" failed", slog.Any("error", err))
	if shutdownErr := shutdowner.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
		logger.Error("request process shutdown failed", slog.String("server", name), slog.Any("error", shutdownErr))
	}
}

func waitForServe(ctx context.Context, done <-chan struct{}, name string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for %s: %w", name, ctx.Err())
	}
}

func options(cfg config.Config) fx.Option {
	return fx.Options(
		fx.Supply(cfg),
		coremodule.FoundationModule(),
		channelprocess.LocalFoundationModule(),
		fx.Provide(
			providePlatformChannelRegistry,
			provideCompactionArtifacts,
			provideEmailChatTriggerer,
		),
		channelruntime.Module(),
		channelruntime.LifecycleModule(),
		fx.Provide(
			provideChannelIdentityReader,
			provideServerRPCConn,
			provideTurnClient,
			provideRuntimeRPCClient,
			provideServerRuntimeClient,
			provideChannelRPC,
			provideServerHandler(newHealthHandler),
			provideServerHandler(gateway.NewWebhookServerHandler),
			provideServerHandler(adaptercatalog.NewWeixinQRServerHandler),
			provideServerHandler(providePublicMediaHandler),
			provideServerHandler(channelhttphost.NewEmailWebhookHandler),
			provideServer,
		),
		fx.Invoke(adaptercatalog.WirePersistence, startChannelRPC, startServer),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger.With(slog.String("component", "fx"))}
		}),
	)
}
