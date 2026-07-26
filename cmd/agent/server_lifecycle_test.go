package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"go.uber.org/fx"
)

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
