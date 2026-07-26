//go:build split

package main

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"google.golang.org/grpc"
)

type recordingShutdowner struct {
	calls int
}

func (s *recordingShutdowner) Shutdown(...fx.ShutdownOption) error {
	s.calls++
	return nil
}

func TestServerRPCLifecycleReportsBindConflict(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy address: %v", err)
	}
	defer func() { _ = occupied.Close() }()

	lifecycle := fxtest.NewLifecycle(t)
	startServerRPC(
		lifecycle,
		slog.New(slog.DiscardHandler),
		&serverRPC{server: grpc.NewServer(), addr: occupied.Addr().String()},
		&recordingShutdowner{},
	)
	err = lifecycle.Start(t.Context())
	if err == nil {
		t.Fatal("lifecycle start succeeded on an occupied address")
	}
}

func TestServerRPCLifecycleStopsWithoutProcessShutdown(t *testing.T) {
	lifecycle := fxtest.NewLifecycle(t)
	shutdowner := &recordingShutdowner{}
	startServerRPC(
		lifecycle,
		slog.New(slog.DiscardHandler),
		&serverRPC{server: grpc.NewServer(), addr: "127.0.0.1:0"},
		shutdowner,
	)

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
}
