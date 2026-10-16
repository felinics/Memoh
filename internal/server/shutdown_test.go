package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestStopCancelsLongLivedHTTPRequestBeforeDraining(t *testing.T) {
	srv := NewServer(slog.Default(), "127.0.0.1:0", "test")
	entered := make(chan struct{})
	released := make(chan struct{})
	srv.echo.GET("/ping", func(c echo.Context) error {
		c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
		c.Response().WriteHeader(http.StatusOK)
		c.Response().Flush()
		close(entered)
		<-c.Request().Context().Done()
		close(released)
		return nil
	})
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.echo.Listener = listener
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Start() }()
	defer func() { _ = srv.echo.Close() }()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request) //nolint:gosec // G704: only this test's loopback listener is reachable.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	<-entered
	stopCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := srv.Stop(stopCtx); err != nil {
		t.Fatalf("SSE blocked graceful shutdown: %v", err)
	}
	select {
	case <-released:
	default:
		t.Fatal("request context was not canceled")
	}
	if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve result: %v", err)
	}
}
