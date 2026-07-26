package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.uber.org/fx"

	httpx "github.com/memohai/memoh/domains/api/http"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/gateway"
	channelhttphost "github.com/memohai/memoh/domains/channel/http"
	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/domains/media/asset"
	"github.com/memohai/memoh/internal/config"
)

const lifecycleModuleName = "channel-process-lifecycle"

// LifecycleModule owns long-running Channel process components. Domain
// assembly supplies their constructors but never starts a process or goroutine.
func LifecycleModule() fx.Option {
	return fx.Module(
		lifecycleModuleName,
		fx.Invoke(
			startChannelManager,
			startEmailManager,
			startWebhookTunnelListener,
			startWebhookTunnel,
		),
	)
}

func startChannelManager(lifecycle fx.Lifecycle, manager *gateway.Manager) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			manager.Start(ctx)
			return nil
		},
		OnStop: manager.Shutdown,
	})
}

func startEmailManager(lifecycle fx.Lifecycle, manager *emailpkg.Manager) {
	lifecycle.Append(fx.StartStopHook(manager.Start, manager.Stop))
}

func startWebhookTunnel(lifecycle fx.Lifecycle, manager webhook.Manager) {
	lifecycle.Append(fx.StartStopHook(manager.Start, manager.Stop))
}

func startWebhookTunnelListener(
	lifecycle fx.Lifecycle,
	log *slog.Logger,
	cfg config.Config,
	store *gateway.Store,
	manager *gateway.Manager,
	media *asset.Service,
	emailService *emailpkg.Service,
	emailManager *emailpkg.Manager,
	emailTrigger *emailpkg.Trigger,
	shutdowner fx.Shutdowner,
) {
	if cfg.WebhookTunnel.EffectiveMode() == config.WebhookTunnelModeDisabled {
		return
	}
	addr := strings.TrimSpace(cfg.WebhookTunnel.ListenAddr)
	if addr == "" {
		addr = webhook.DefaultListenAddr
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	httpx.HardenServer(e.Server)
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimit("1M"))
	e.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok\n")
	})
	gateway.NewWebhookServerHandler(log, store, manager).Register(e)
	channelhttphost.NewEmailWebhookHandler(log, emailService, emailManager, emailTrigger).Register(e)
	channelhttphost.NewPublicMediaHandler(log, media, cfg.Auth.JWTSecret).Register(e)

	logger := log.With(slog.String("component", "webhook_tunnel_listener"), slog.String("addr", addr))
	var serveDone chan struct{}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
			if err != nil {
				return fmt.Errorf("listen webhook tunnel: %w", err)
			}
			serveDone = make(chan struct{})
			go func() {
				defer close(serveDone)
				logger.Info("webhook tunnel listener started", slog.String("bound_addr", listener.Addr().String()))
				if serveErr := e.Server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
					logger.Error("webhook tunnel listener failed", slog.Any("error", serveErr))
					if shutdownErr := shutdowner.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
						logger.Error("request process shutdown failed", slog.Any("error", shutdownErr))
					}
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return errors.Join(e.Shutdown(ctx), waitForServe(ctx, serveDone))
		},
	})
}

func waitForServe(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
