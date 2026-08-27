package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	"github.com/memohai/memoh/internal/logger"
	"github.com/memohai/memoh/internal/runtimeauth"
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
	"github.com/memohai/memoh/internal/workspace/bridgesvc"
)

const (
	defaultSocketPath = "/run/memoh/bridge.sock"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Append toolkit to PATH so child processes (via /bin/sh -c) can find npx/uvx.
	// Container-native tools take priority since toolkit is appended at the end.
	_ = os.Setenv("PATH", os.Getenv("PATH")+":/opt/memoh/toolkit/bin")
	if err := runtimeauth.CleanupStale(time.Now()); err != nil {
		logger.Warn("failed to clean stale runtime auth directories", slog.Any("error", err))
	}

	reverseHTTP := bridgesvc.NewReverseHTTPBroker()
	startDisplaySupervisor(ctx)
	startACPToolsProxy(ctx, reverseHTTP)

	// PID 1 zombie reaping: when bridge runs as PID 1 inside a container,
	// orphaned child processes become zombies unless reaped.
	// On Linux 5.3+, Go's os/exec uses pidfd_open which avoids races between
	// this reaper and cmd.Wait(). Kernels below 5.3 may see rare ECHILD errors.
	go func() {
		var status syscall.WaitStatus
		for {
			if _, err := syscall.Wait4(-1, &status, 0, nil); err != nil {
				time.Sleep(time.Second)
			}
		}
	}()

	network := "unix"
	address := os.Getenv("BRIDGE_SOCKET_PATH")
	if tcpAddr := os.Getenv("BRIDGE_TCP_ADDR"); tcpAddr != "" {
		if !isBridgeTCPListenAddrAllowed(tcpAddr) {
			logger.Error("BRIDGE_TCP_ADDR must be loopback or use :port bind shorthand; explicit non-loopback TCP exposes bridge gRPC without TLS/auth", slog.String("addr", tcpAddr))
			return
		}
		network = "tcp"
		address = tcpAddr
	}
	if address == "" {
		address = defaultSocketPath
	}
	if network == "unix" {
		// Clean up residual socket from a previous run.
		_ = os.Remove(filepath.Clean(address)) //nolint:gosec // G703: address is from BRIDGE_SOCKET_PATH env or a compiled-in default, not end-user input
	}

	lis, err := (&net.ListenConfig{}).Listen(ctx, network, address)
	if err != nil {
		logger.Error("failed to listen", slog.String("network", network), slog.String("address", address), slog.Any("error", err))
		return
	}

	serverOpts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(16 * 1024 * 1024),
		grpc.MaxSendMsgSize(16 * 1024 * 1024),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 10 * time.Second,
			Time:                  60 * time.Second,
			Timeout:               15 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	// strict mTLS 只约束 TCP 通道；UDS 走文件系统 socket 权限的本地信任模型。
	// strict 下材料缺失/损坏直接拒绝启动，不回退明文（设计 §10）。
	if network == "tcp" {
		creds, err := bridgeServerCredentials()
		if err != nil {
			logger.Error("bridge TLS configuration invalid", slog.Any("error", err))
			return
		}
		if creds != nil {
			serverOpts = append(serverOpts, grpc.Creds(creds))
			logger.Info("bridge TCP gRPC requires mTLS", slog.String("mode", bridgeTLSModeStrict))
		}
	}
	srv := grpc.NewServer(serverOpts...)
	pb.RegisterContainerServiceServer(srv, bridgesvc.New(bridgesvc.Options{
		DefaultWorkDir:    bridgesvc.DefaultWorkDir,
		DataMount:         bridgesvc.DefaultWorkDir,
		AllowHostAbsolute: true,
		ReverseHTTP:       reverseHTTP,
	}))
	reflection.Register(srv)

	go func() {
		<-ctx.Done()
		logger.FromContext(ctx).Info("shutting down gRPC server")
		srv.GracefulStop()
	}()

	logger.Info("bridge gRPC server listening", slog.String("network", network), slog.String("address", address))
	if err := srv.Serve(lis); err != nil {
		logger.Error("gRPC server failed", slog.Any("error", err))
		return
	}
}

func isBridgeTCPListenAddrAllowed(addr string) bool {
	if isLoopbackTCPAddr(addr) {
		return true
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	return err == nil && host == ""
}
