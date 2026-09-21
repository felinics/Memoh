package rpc

import (
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const MaxMessageBytes = 16 << 20

// Keepalive intervals for the long-lived Run streams: without HTTP/2 PINGs
// a silent partition (dropped NAT entry, dead peer) is only detected by
// TCP-level timeouts minutes later, wedging the inbound route on a Recv
// that never returns.
const (
	keepaliveInterval = 30 * time.Second
	keepaliveTimeout  = 10 * time.Second
)

func Dial(target, secret string) (*grpc.ClientConn, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("internal rpc target is required")
	}
	return grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// A stats handler rather than interceptors: otelgrpc deprecated the
		// interceptor constructors, and only the handler sees streaming
		// message events, which is most of what this connection carries.
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithUnaryInterceptor(UnaryClientAuth(secret)),
		grpc.WithStreamInterceptor(StreamClientAuth(secret)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                keepaliveInterval,
			Timeout:             keepaliveTimeout,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxMessageBytes),
			grpc.MaxCallSendMsgSize(MaxMessageBytes),
		),
	)
}

func NewServer(secret string, opts ...grpc.ServerOption) *grpc.Server {
	base := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(UnaryServerAuth(secret)),
		grpc.ChainStreamInterceptor(StreamServerAuth(secret)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    keepaliveInterval,
			Timeout: keepaliveTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             keepaliveInterval / 2,
			PermitWithoutStream: true,
		}),
		grpc.MaxRecvMsgSize(MaxMessageBytes),
		grpc.MaxSendMsgSize(MaxMessageBytes),
	}
	return grpc.NewServer(append(base, opts...)...)
}

// StopGracefully drains srv but falls back to a hard stop when the context
// expires or the drain deadline passes — a long-lived Run stream with an
// active turn would otherwise block shutdown until fx kills the process.
func StopGracefully(srv *grpc.Server, done <-chan struct{}, deadline time.Duration) {
	drained := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(drained)
	}()
	select {
	case <-drained:
	case <-done:
		srv.Stop()
		<-drained
	case <-time.After(deadline):
		srv.Stop()
		<-drained
	}
}
