package netbird

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
	"github.com/felinics/memoh/internal/network/overlay/netbird/daemonpb"
)

// The sidecar runs a single daemon as root with the default profile.
const (
	daemonProfile = "default"
	daemonUser    = "root"
)

// Daemon status values, from netbird client/internal/state.go.
const (
	daemonIdle           = "Idle"
	daemonConnecting     = "Connecting"
	daemonConnected      = "Connected"
	daemonNeedsLogin     = "NeedsLogin"
	daemonLoginFailed    = "LoginFailed"
	daemonSessionExpired = "SessionExpired"
)

// daemonClient talks to the NetBird client daemon over its unix socket.
type daemonClient struct {
	socketPath string
}

func (c daemonClient) call(ctx context.Context, fn func(context.Context, daemonpb.DaemonServiceClient) error) error {
	conn, err := grpc.NewClient("passthrough:///netbird-daemon",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return sidecar.DialUnix(ctx, c.socketPath)
		}))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return fn(ctx, daemonpb.NewDaemonServiceClient(conn))
}

func (c daemonClient) status(ctx context.Context) (*daemonpb.StatusResponse, error) {
	var resp *daemonpb.StatusResponse
	err := c.call(ctx, func(ctx context.Context, d daemonpb.DaemonServiceClient) error {
		var err error
		resp, err = d.Status(ctx, &daemonpb.StatusRequest{GetFullPeerStatus: true})
		return err
	})
	return resp, err
}

func (c daemonClient) config(ctx context.Context) (*daemonpb.GetConfigResponse, error) {
	var resp *daemonpb.GetConfigResponse
	err := c.call(ctx, func(ctx context.Context, d daemonpb.DaemonServiceClient) error {
		var err error
		resp, err = d.GetConfig(ctx, &daemonpb.GetConfigRequest{ProfileName: daemonProfile, Username: daemonUser})
		return err
	})
	return resp, err
}

func (c daemonClient) login(ctx context.Context, req *daemonpb.LoginRequest) (*daemonpb.LoginResponse, error) {
	req.ProfileName, req.Username = ptr(daemonProfile), ptr(daemonUser)
	var resp *daemonpb.LoginResponse
	err := c.call(ctx, func(ctx context.Context, d daemonpb.DaemonServiceClient) error {
		var err error
		resp, err = d.Login(ctx, req)
		return err
	})
	return resp, err
}

func (c daemonClient) waitSSOLogin(ctx context.Context, userCode, hostname string) error {
	return c.call(ctx, func(ctx context.Context, d daemonpb.DaemonServiceClient) error {
		_, err := d.WaitSSOLogin(ctx, &daemonpb.WaitSSOLoginRequest{UserCode: userCode, Hostname: hostname})
		return err
	})
}

func (c daemonClient) up(ctx context.Context) error {
	return c.call(ctx, func(ctx context.Context, d daemonpb.DaemonServiceClient) error {
		_, err := d.Up(ctx, &daemonpb.UpRequest{ProfileName: ptr(daemonProfile), Username: ptr(daemonUser)})
		return err
	})
}

func (c daemonClient) down(ctx context.Context) error {
	return c.call(ctx, func(ctx context.Context, d daemonpb.DaemonServiceClient) error {
		_, err := d.Down(ctx, &daemonpb.DownRequest{})
		return err
	})
}

func (c daemonClient) setDisableDNS(ctx context.Context, managementURL string, disable bool) error {
	return c.call(ctx, func(ctx context.Context, d daemonpb.DaemonServiceClient) error {
		_, err := d.SetConfig(ctx, &daemonpb.SetConfigRequest{
			ProfileName:   daemonProfile,
			Username:      daemonUser,
			ManagementUrl: managementURL,
			DisableDns:    &disable,
		})
		return err
	})
}

func (c daemonClient) logout(ctx context.Context) error {
	return c.call(ctx, func(ctx context.Context, d daemonpb.DaemonServiceClient) error {
		_, err := d.Logout(ctx, &daemonpb.LogoutRequest{ProfileName: ptr(daemonProfile), Username: ptr(daemonUser)})
		return err
	})
}

// waitReady polls until the daemon answers on its socket.
func (c daemonClient) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := c.status(callCtx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("netbird daemon did not answer within %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func ptr[T any](v T) *T { return &v }
