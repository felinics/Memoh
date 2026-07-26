package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

type lifecycleHandler struct{}

func (lifecycleHandler) Register(e *echo.Echo) {
	e.GET("/health", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
}

func TestServerListenReportsBindConflict(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy address: %v", err)
	}
	defer func() { _ = occupied.Close() }()

	srv := NewServer(slog.New(slog.DiscardHandler), occupied.Addr().String(), "test-secret")
	listener, err := srv.Listen(t.Context())
	if err == nil {
		_ = listener.Close()
		t.Fatal("Listen() succeeded on an occupied address")
	}
}

func TestServerServeAndStopUseSameHTTPServer(t *testing.T) {
	srv := NewServer(
		slog.New(slog.DiscardHandler),
		"127.0.0.1:0",
		"test-secret",
		lifecycleHandler{},
	)
	listener, err := srv.Listen(t.Context())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(listener)
	}()
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Second)
		defer cancelCleanup()
		_ = srv.Stop(cleanupCtx)
	})

	requestCtx, cancelRequest := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelRequest()
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		fmt.Sprintf("http://%s/health", listener.Addr()),
		nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   2 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	stopCtx, cancelStop := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelStop()
	if err := srv.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve() error = %v, want %v", err, http.ErrServerClosed)
		}
	case <-stopCtx.Done():
		t.Fatalf("Serve() did not return after Stop(): %v", stopCtx.Err())
	}
}
