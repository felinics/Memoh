package channel_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/channel"
	intrpc "github.com/memohai/memoh/internal/rpc"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	channelclient "github.com/memohai/memoh/internal/rpc/channel/client"
	channelserver "github.com/memohai/memoh/internal/rpc/channel/server"
)

func TestStatusTransportRequiresInternalAuthentication(t *testing.T) {
	for _, test := range []struct {
		name     string
		secret   string
		wantCode codes.Code
		wantErr  error
	}{
		{name: "matching", secret: "server-secret", wantCode: codes.OK},
		{name: "missing", wantCode: codes.Unauthenticated, wantErr: channel.ErrUnauthenticated},
		{name: "wrong", secret: "wrong-secret", wantCode: codes.Unauthenticated, wantErr: channel.ErrUnauthenticated},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _, _ := openStatusTransport(t, test.secret, newFake())
			_, err := client.ConnectionStatuses(t.Context(), "team", "bot")
			if got := status.Code(err); got != test.wantCode {
				t.Fatalf("status code = %v, want %v; error = %v", got, test.wantCode, err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want identity %v", err, test.wantErr)
			}
		})
	}
}

func TestStatusUnaryGracefulShutdownDrainsInFlightCall(t *testing.T) {
	provider := newBlockingStatusProvider()
	client, server, _ := openStatusTransport(t, "server-secret", provider)
	callDone := make(chan error, 1)
	go func() {
		_, err := client.ConnectionStatuses(t.Context(), "team", "bot")
		callDone <- err
	}()
	<-provider.called

	stopDone := make(chan struct{})
	neverCanceled := make(chan struct{})
	go func() {
		intrpc.StopGracefully(server, neverCanceled, time.Second)
		close(stopDone)
	}()
	close(provider.release)

	if err := <-callDone; err != nil {
		t.Fatalf("in-flight status call during graceful shutdown = %v", err)
	}
	<-stopDone
}

func TestStatusUnaryHardShutdownCancelsInFlightCall(t *testing.T) {
	provider := newBlockingStatusProvider()
	client, server, _ := openStatusTransport(t, "server-secret", provider)
	callDone := make(chan error, 1)
	go func() {
		_, err := client.ConnectionStatuses(t.Context(), "team", "bot")
		callDone <- err
	}()
	<-provider.called

	hardStop := make(chan struct{})
	stopDone := make(chan struct{})
	go func() {
		intrpc.StopGracefully(server, hardStop, time.Hour)
		close(stopDone)
	}()
	close(hardStop)

	if err := <-callDone; err == nil {
		t.Fatal("in-flight status call unexpectedly succeeded after hard shutdown")
	}
	<-stopDone
}

type blockingStatusProvider struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingStatusProvider() *blockingStatusProvider {
	return &blockingStatusProvider{called: make(chan struct{}), release: make(chan struct{})}
}

func (p *blockingStatusProvider) ConnectionStatuses(ctx context.Context, _, _ string) ([]channel.ConnectionStatus, error) {
	p.once.Do(func() { close(p.called) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return []channel.ConnectionStatus{}, nil
	}
}

func (*blockingStatusProvider) TunnelStatus(context.Context) (channel.TunnelStatus, error) {
	return channel.TunnelStatus{Mode: channel.TunnelModeDisabled, Status: channel.TunnelStateDisabled}, nil
}

func openStatusTransport(t *testing.T, clientSecret string, provider channelserver.StatusProvider) (*channelclient.Client, *grpc.Server, *bufconn.Listener) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := intrpc.NewServer("server-secret")
	channelpb.RegisterChannelStatusServiceServer(server, channelserver.NewStatus(provider))
	go func() { _ = server.Serve(listener) }()

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	}
	if clientSecret != "" {
		opts = append(opts, grpc.WithUnaryInterceptor(intrpc.UnaryClientAuth(clientSecret)))
	}
	conn, err := grpc.NewClient("passthrough:///channel-status", opts...)
	if err != nil {
		server.Stop()
		_ = listener.Close()
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	})
	return channelclient.New(conn), server, listener
}
