package main

import (
	"context"
	"errors"
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
	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
	"github.com/memohai/memoh/internal/workspace/bridgesvc"
)

const (
	defaultSocketPath = "/run/memoh/bridge.sock"
)

func main() {
	os.Exit(runBridge())
}

func runBridge() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Append toolkit to PATH so child processes (via /bin/sh -c) can find npx/uvx.
	// Container-native tools take priority since toolkit is appended at the end.
	_ = os.Setenv("PATH", os.Getenv("PATH")+":/opt/memoh/toolkit/bin")

	reverseHTTP := bridgesvc.NewReverseHTTPBroker()
	startDisplaySupervisor(ctx)
	startACPToolsProxy(ctx, reverseHTTP)

	network := "unix"
	address := os.Getenv("BRIDGE_SOCKET_PATH")
	if tcpAddr := os.Getenv("BRIDGE_TCP_ADDR"); tcpAddr != "" {
		if !isBridgeTCPListenAddrAllowed(tcpAddr) {
			logger.Error("BRIDGE_TCP_ADDR must be loopback or use :port bind shorthand; explicit non-loopback TCP exposes bridge gRPC without TLS/auth", slog.String("addr", tcpAddr))
			return 1
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
		return 1
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
			return 1
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

	shutdownDone := make(chan struct{})
	go func() {
		stopBridgeGRPCServer(ctx, srv)
		close(shutdownDone)
	}()

	logger.Info("bridge gRPC server listening", slog.String("network", network), slog.String("address", address))
	serveErr := srv.Serve(lis)
	unexpectedServeErr := serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped)
	if unexpectedServeErr {
		stop()
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
	if unexpectedServeErr {
		logger.Error("gRPC server failed", slog.Any("error", serveErr))
		return 1
	}
	return 0
}

func stopBridgeGRPCServer(ctx context.Context, srv *grpc.Server) {
	<-ctx.Done()
	logger.FromContext(ctx).Info("shutting down gRPC server")
	// Bridge Exec streams can legitimately live for the lifetime of an ACP
	// process, so graceful draining has no useful upper bound here. Calling
	// GracefulStop and Stop concurrently can also deadlock inside grpc-go when
	// an active handler ignores cancellation. Stop closes transports and
	// returns without waiting for those handlers, allowing the container's init
	// process to complete shutdown.
	srv.Stop()
}

func isBridgeTCPListenAddrAllowed(addr string) bool {
	if isLoopbackTCPAddr(addr) {
		return true
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	return err == nil && host == ""
}
