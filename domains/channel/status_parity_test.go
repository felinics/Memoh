package channel_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	channelclient "github.com/memohai/memoh/internal/rpc/channel/client"
	channelserver "github.com/memohai/memoh/internal/rpc/channel/server"
)

func TestConnectionStatusParityDetails(t *testing.T) {
	for _, factory := range transports {
		t.Run(factory.name, func(t *testing.T) {
			fake := newFake()
			fake.statuses = []channel.ConnectionStatus{
				{ConfigID: "z", BotID: "bot", ChannelType: channel.ChannelTypeTelegram, LastError: strings.Repeat("x", (4<<10)+8), UpdatedAt: time.Date(2026, 7, 23, 12, 0, 0, 0, time.FixedZone("test", 9*60*60))},
				{ConfigID: "a", BotID: "bot", ChannelType: channel.ChannelTypeMatrix, UpdatedAt: time.Date(2026, 7, 23, 11, 0, 0, 0, time.FixedZone("test", 9*60*60))},
			}
			got, err := factory.open(t, fake).ConnectionStatuses(t.Context(), "team", "bot")
			if err != nil {
				t.Fatalf("ConnectionStatuses() error = %v", err)
			}
			if len(got) != 2 || got[0].ConfigID != "a" || got[1].ConfigID != "z" {
				t.Fatalf("statuses not sorted: %#v", got)
			}
			if got[1].UpdatedAt.Location() != time.UTC || len(got[1].LastError) != 4<<10 {
				t.Fatalf("status normalization = %#v", got[1])
			}
		})
	}
}

func TestTunnelStateParity(t *testing.T) {
	values := []channel.TunnelStatus{
		{Mode: channel.TunnelModeDisabled, Status: channel.TunnelStateDisabled},
		{Enabled: true, Mode: channel.TunnelModeConfigured, Status: channel.TunnelStateStarting},
		{Enabled: true, Mode: channel.TunnelModeManaged, Status: channel.TunnelStateReady, PublicBaseURL: "https://example.test"},
		{Enabled: true, Mode: channel.TunnelModeManaged, Status: channel.TunnelStateError, Error: "tunnel failed"},
	}
	for _, factory := range transports {
		for _, value := range values {
			t.Run(fmt.Sprintf("%s/%d", factory.name, value.Status), func(t *testing.T) {
				fake := newFake()
				fake.tunnel = value
				got, err := factory.open(t, fake).TunnelStatus(t.Context())
				if err != nil || got != value {
					t.Fatalf("TunnelStatus() = %#v, %v; want %#v", got, err, value)
				}
			})
		}
	}
}

func TestStatusCapabilitiesPropagateCancelAndDeadline(t *testing.T) {
	for _, factory := range transports {
		for _, capability := range []string{"statuses", "tunnel"} {
			t.Run(factory.name+"/"+capability+"/pre-cancel", func(t *testing.T) {
				fake := newFake()
				runtime := factory.open(t, fake)
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				var err error
				if capability == "statuses" {
					_, err = runtime.ConnectionStatuses(ctx, "team", "bot")
				} else {
					_, err = runtime.TunnelStatus(ctx)
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("pre-canceled error = %v", err)
				}
				if fake.count(capability) != 0 {
					t.Fatalf("provider calls after pre-cancel = %d", fake.count(capability))
				}
			})

			t.Run(factory.name+"/"+capability+"/cancel", func(t *testing.T) {
				fake := newFake()
				fake.block = true
				runtime := factory.open(t, fake)
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() {
					if capability == "statuses" {
						_, err := runtime.ConnectionStatuses(ctx, "team", "bot")
						done <- err
						return
					}
					_, err := runtime.TunnelStatus(ctx)
					done <- err
				}()
				if called := <-fake.called; called != capability {
					t.Fatalf("called = %q", called)
				}
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled error = %v", err)
				}
			})

			t.Run(factory.name+"/"+capability+"/deadline", func(t *testing.T) {
				fake := newFake()
				fake.block = true
				runtime := factory.open(t, fake)
				ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
				defer cancel()
				var err error
				if capability == "statuses" {
					_, err = runtime.ConnectionStatuses(ctx, "team", "bot")
				} else {
					_, err = runtime.TunnelStatus(ctx)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline error = %v", err)
				}
			})
		}
	}
}

func TestUnavailableStatusCapabilitiesDoNotSynthesizeValues(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	channelpb.RegisterChannelStatusServiceServer(server, channelserver.NewStatus(newFake()))
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///bufconn", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	client := channelclient.New(conn)
	server.Stop()
	_ = listener.Close()
	t.Cleanup(func() { _ = conn.Close() })

	statuses, err := client.ConnectionStatuses(t.Context(), "team", "bot")
	if !errors.Is(err, channel.ErrUnavailable) || statuses != nil {
		t.Fatalf("statuses = %#v, error = %v", statuses, err)
	}
	tunnel, err := client.TunnelStatus(t.Context())
	if !errors.Is(err, channel.ErrUnavailable) || tunnel != (channel.TunnelStatus{}) {
		t.Fatalf("tunnel = %#v, error = %v", tunnel, err)
	}
}
