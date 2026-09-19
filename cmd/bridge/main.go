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

	"github.com/felinics/memoh/internal/logger"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

const (
	defaultSocketPath = "/run/memoh/bridge.sock"
)

func main() {
	// The bridge runs inside the workspace container and writes to its own
	// stdout. It builds its logger here and passes it down rather than
	// reaching for a shared one; see docs/logging.md.
	os.Exit(runBridge(logger.New(os.Stdout, "info", "json")))
}

func runBridge(log *slog.Logger) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Append toolkit to PATH so child processes (via /bin/sh -c) can find npx/uvx.
	// Container-native tools take priority since toolkit is appended at the end.
	_ = os.Setenv("PATH", os.Getenv("PATH")+":/opt/memoh/toolkit/bin")

	reverseHTTP := bridgesvc.NewReverseHTTPBroker()
	startDisplaySupervisor(ctx, log)
	startToolsProxy(ctx, log, reverseHTTP)

	network := "unix"
	address := os.Getenv("BRIDGE_SOCKET_PATH")
	if tcpAddr := os.Getenv("BRIDGE_TCP_ADDR"); tcpAddr != "" {
		if !isBridgeTCPListenAddrAllowed(tcpAddr) {
			log.ErrorContext(ctx, "BRIDGE_TCP_ADDR must be loopback or use :port bind shorthand; explicit non-loopback TCP exposes bridge gRPC without TLS/auth", slog.String("addr", tcpAddr))
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
		log.ErrorContext(ctx, "failed to listen", slog.String("network", network), slog.String("address", address), slog.Any("error", err))
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
			log.ErrorContext(ctx, "bridge TLS configuration invalid", slog.Any("error", err))
			return 1
		}
		if creds != nil {
			serverOpts = append(serverOpts, grpc.Creds(creds))
			log.InfoContext(ctx, "bridge TCP gRPC requires mTLS", slog.String("mode", bridgeTLSModeStrict))
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
		stopBridgeGRPCServer(ctx, log, srv)
		close(shutdownDone)
	}()

	log.InfoContext(ctx, "bridge gRPC server listening", slog.String("network", network), slog.String("address", address))
	serveErr := srv.Serve(lis)
	unexpectedServeErr := serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped)
	if unexpectedServeErr {
		stop()
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
	if unexpectedServeErr {
		log.ErrorContext(ctx, "gRPC server failed", slog.Any("error", serveErr))
		return 1
	}
	return 0
}

func stopBridgeGRPCServer(ctx context.Context, log *slog.Logger, srv *grpc.Server) {
	<-ctx.Done()
	log.InfoContext(ctx, "shutting down gRPC server")
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
