package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func startACPToolsProxy(ctx context.Context, handler http.Handler) {
	if handler == nil {
		return
	}
	addr := strings.TrimSpace(os.Getenv("MEMOH_ACP_TOOLS_PROXY_ADDR"))
	if addr == "" {
		addr = bridge.ACPToolsProxyAddr
	}
	if !isLoopbackTCPAddr(addr) {
		logger.FromContext(ctx).Warn("ACP tools proxy skipped; proxy addr must be loopback", slog.String("addr", addr))
		return
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		logger.FromContext(ctx).Warn("ACP tools proxy listen failed", slog.String("addr", addr), slog.Any("error", err))
		return
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		logger.FromContext(ctx).Info("ACP tools proxy listening", slog.String("addr", addr))
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.FromContext(ctx).Warn("ACP tools proxy stopped", slog.Any("error", err))
		}
	}()
}

func isLoopbackTCPAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
