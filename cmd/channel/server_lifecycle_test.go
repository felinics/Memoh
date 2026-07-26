package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"google.golang.org/grpc"

	httpserver "github.com/memohai/memoh/domains/api/http/server"
)

type recordingShutdowner struct {
	calls int
}

func (s *recordingShutdowner) Shutdown(...fx.ShutdownOption) error {
	s.calls++
	return nil
}

func TestHandleServeErrorRequestsExitCodeOne(t *testing.T) {
	var shutdowner fx.Shutdowner
	app := fx.New(fx.Populate(&shutdowner), fx.NopLogger)
	startCtx, cancelStart := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelStart()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start Fx app: %v", err)
	}
	defer func() {
		stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Second)
		defer cancelStop()
		if err := app.Stop(stopCtx); err != nil {
			t.Errorf("stop Fx app: %v", err)
		}
	}()

	wait := app.Wait()
	handleServeError(
		slog.New(slog.DiscardHandler),
		shutdowner,
		"test server",
		fmt.Errorf("wrapped: %w", http.ErrServerClosed),
		http.ErrServerClosed,
	)
	select {
	case signal := <-wait:
		t.Fatalf("expected stop error requested shutdown: %+v", signal)
	default:
	}

	handleServeError(slog.New(slog.DiscardHandler), shutdowner, "test server", errors.New("serve failed"), http.ErrServerClosed)
	select {
	case signal := <-wait:
		if signal.ExitCode != 1 {
			t.Fatalf("exit code = %d, want 1", signal.ExitCode)
		}
	case <-startCtx.Done():
		t.Fatalf("wait for shutdown signal: %v", startCtx.Err())
	}
}

func TestServerLifecyclesReportBindConflict(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy address: %v", err)
	}
	defer func() { _ = occupied.Close() }()

	tests := []struct {
		name   string
		append func(fx.Lifecycle)
	}{
		{
			name: "http",
			append: func(lifecycle fx.Lifecycle) {
				startServer(
					lifecycle,
					slog.New(slog.DiscardHandler),
					httpserver.NewServer(slog.New(slog.DiscardHandler), occupied.Addr().String(), "test-secret"),
					&recordingShutdowner{},
				)
			},
		},
		{
			name: "rpc",
			append: func(lifecycle fx.Lifecycle) {
				startChannelRPC(
					lifecycle,
					slog.New(slog.DiscardHandler),
					&channelRPC{server: grpc.NewServer(), addr: occupied.Addr().String()},
					&recordingShutdowner{},
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := fxtest.NewLifecycle(t)
			test.append(lifecycle)
			if err := lifecycle.Start(t.Context()); err == nil {
				t.Fatal("lifecycle start succeeded on an occupied address")
			}
		})
	}
}

func TestServerLifecyclesStopWithoutProcessShutdown(t *testing.T) {
	tests := []struct {
		name   string
		append func(fx.Lifecycle, fx.Shutdowner)
	}{
		{
			name: "http",
			append: func(lifecycle fx.Lifecycle, shutdowner fx.Shutdowner) {
				startServer(
					lifecycle,
					slog.New(slog.DiscardHandler),
					httpserver.NewServer(slog.New(slog.DiscardHandler), "127.0.0.1:0", "test-secret"),
					shutdowner,
				)
			},
		},
		{
			name: "rpc",
			append: func(lifecycle fx.Lifecycle, shutdowner fx.Shutdowner) {
				startChannelRPC(
					lifecycle,
					slog.New(slog.DiscardHandler),
					&channelRPC{server: grpc.NewServer(), addr: "127.0.0.1:0"},
					shutdowner,
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := fxtest.NewLifecycle(t)
			shutdowner := &recordingShutdowner{}
			test.append(lifecycle, shutdowner)

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := lifecycle.Start(ctx); err != nil {
				t.Fatalf("start lifecycle: %v", err)
			}
			if err := lifecycle.Stop(ctx); err != nil {
				t.Fatalf("stop lifecycle: %v", err)
			}
			if shutdowner.calls != 0 {
				t.Fatalf("shutdown calls = %d, want 0", shutdowner.calls)
			}
		})
	}
}
