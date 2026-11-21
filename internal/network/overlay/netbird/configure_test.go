package netbird

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/netbird/daemonpb"
)

// fakeDaemon implements the daemon calls Memoh makes and records them.
type fakeDaemon struct {
	daemonpb.UnimplementedDaemonServiceServer

	mu        sync.Mutex
	status    *daemonpb.StatusResponse
	config    *daemonpb.GetConfigResponse
	loginResp *daemonpb.LoginResponse
	loginErr  error
	calls     []string
	logins    []*daemonpb.LoginRequest
	setConfig []*daemonpb.SetConfigRequest
	ssoDone   chan struct{}
}

func (f *fakeDaemon) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeDaemon) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c != "Status" && c != "GetConfig" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeDaemon) Status(context.Context, *daemonpb.StatusRequest) (*daemonpb.StatusResponse, error) {
	f.record("Status")
	return f.status, nil
}

func (f *fakeDaemon) GetConfig(context.Context, *daemonpb.GetConfigRequest) (*daemonpb.GetConfigResponse, error) {
	f.record("GetConfig")
	return f.config, nil
}

func (f *fakeDaemon) Login(_ context.Context, req *daemonpb.LoginRequest) (*daemonpb.LoginResponse, error) {
	f.record("Login")
	f.mu.Lock()
	f.logins = append(f.logins, req)
	f.mu.Unlock()
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	if f.loginResp != nil {
		return f.loginResp, nil
	}
	return &daemonpb.LoginResponse{}, nil
}

func (f *fakeDaemon) WaitSSOLogin(ctx context.Context, _ *daemonpb.WaitSSOLoginRequest) (*daemonpb.WaitSSOLoginResponse, error) {
	f.record("WaitSSOLogin")
	select {
	case <-f.ssoDone:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &daemonpb.WaitSSOLoginResponse{}, nil
}

func (f *fakeDaemon) Up(context.Context, *daemonpb.UpRequest) (*daemonpb.UpResponse, error) {
	f.record("Up")
	return &daemonpb.UpResponse{}, nil
}

func (f *fakeDaemon) Down(context.Context, *daemonpb.DownRequest) (*daemonpb.DownResponse, error) {
	f.record("Down")
	return &daemonpb.DownResponse{}, nil
}

func (f *fakeDaemon) SetConfig(_ context.Context, req *daemonpb.SetConfigRequest) (*daemonpb.SetConfigResponse, error) {
	f.record("SetConfig")
	f.mu.Lock()
	f.setConfig = append(f.setConfig, req)
	f.mu.Unlock()
	return &daemonpb.SetConfigResponse{}, nil
}

func (f *fakeDaemon) Logout(context.Context, *daemonpb.LogoutRequest) (*daemonpb.LogoutResponse, error) {
	f.record("Logout")
	return &daemonpb.LogoutResponse{}, nil
}

// newFakeDriver serves the fake daemon on the socket the driver dials for
// bot-1. Unix socket paths are length-limited, so it lives under /tmp.
func newFakeDriver(t *testing.T, config map[string]any, daemon *fakeDaemon) *nativeDriver {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "nbc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	driver := newNativeDriver(netctl.BotOverlayConfig{Enabled: true, Provider: "netbird", Config: config}, nil, root, nil)
	socket := driver.socketPath(netctl.AttachmentRequest{BotID: "bot-1"})
	if err := os.MkdirAll(filepath.Dir(socket), 0o750); err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	daemonpb.RegisterDaemonServiceServer(server, daemon)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return driver
}

func connected() *daemonpb.StatusResponse {
	return &daemonpb.StatusResponse{
		Status: daemonConnected,
		FullStatus: &daemonpb.FullStatus{
			ManagementState: &daemonpb.ManagementState{Connected: true},
			SignalState:     &daemonpb.SignalState{Connected: true},
			LocalPeerState:  &daemonpb.LocalPeerState{IP: "100.78.0.2/16"},
		},
	}
}

func configureBot(t *testing.T, driver *nativeDriver) {
	t.Helper()
	if err := driver.configure(context.Background(), netctl.AttachmentRequest{BotID: "bot-1"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
}

func TestConfigureLogsInWithSetupKey(t *testing.T) {
	daemon := &fakeDaemon{status: &daemonpb.StatusResponse{Status: daemonNeedsLogin}}
	driver := newFakeDriver(t, map[string]any{"setup_key": "key-1", "management_url": "http://mgmt.example", "disable_dns": true}, daemon)

	configureBot(t, driver)

	if got := daemon.writes(); !slices.Equal(got, []string{"Login", "Up"}) {
		t.Fatalf("calls = %v, want Login, Up", got)
	}
	login := daemon.logins[0]
	if login.GetSetupKey() != "key-1" || login.GetManagementUrl() != "http://mgmt.example:80" || login.GetHostname() != "bot-1" || !login.GetDisableDns() {
		t.Fatalf("unexpected login request: %v", login)
	}
}

func TestConfigureKeepsPendingSSOLogin(t *testing.T) {
	daemon := &fakeDaemon{
		status:    &daemonpb.StatusResponse{Status: daemonNeedsLogin},
		loginResp: &daemonpb.LoginResponse{NeedsSSOLogin: true, UserCode: "ABCD", VerificationURIComplete: "https://idp/device?user_code=ABCD"},
		ssoDone:   make(chan struct{}),
	}
	driver := newFakeDriver(t, map[string]any{"management_url": "https://mgmt.example"}, daemon)
	req := netctl.AttachmentRequest{BotID: "bot-1"}

	configureBot(t, driver)
	configureBot(t, driver)

	status := netctl.OverlayStatus{State: netctl.OverlayStateReady, Attached: true}
	driver.enrichStatus(context.Background(), req, &status)
	if status.State != netctl.OverlayStateNeedsLogin || status.Details["auth_url"] != "https://idp/device?user_code=ABCD" {
		t.Fatalf("status = %s %v", status.State, status.Details)
	}
	logins := 0
	for _, c := range daemon.writes() {
		if c == "Login" {
			logins++
		}
	}
	if logins != 1 {
		t.Fatalf("a pending SSO login must not be restarted, got calls %v", daemon.writes())
	}

	close(daemon.ssoDone)
	deadline := time.Now().Add(3 * time.Second)
	for !slices.Contains(daemon.writes(), "Up") {
		if time.Now().After(deadline) {
			t.Fatalf("expected Up after the SSO login, got %v", daemon.writes())
		}
		time.Sleep(20 * time.Millisecond)
	}
	waits := 0
	for _, c := range daemon.writes() {
		if c == "WaitSSOLogin" {
			waits++
		}
	}
	if waits != 1 {
		t.Fatalf("expected one waiter, got calls %v", daemon.writes())
	}
	for {
		if _, ok := driver.pendingSSO(req); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending SSO state was not cleared")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestConfigureAppliesDisableDNS(t *testing.T) {
	daemon := &fakeDaemon{status: connected(), config: &daemonpb.GetConfigResponse{ManagementUrl: "https://mgmt.example:443"}}
	driver := newFakeDriver(t, map[string]any{"management_url": "https://mgmt.example", "disable_dns": true}, daemon)

	configureBot(t, driver)

	if got := daemon.writes(); !slices.Equal(got, []string{"SetConfig", "Down", "Up"}) {
		t.Fatalf("calls = %v, want SetConfig, Down, Up", got)
	}
	if !daemon.setConfig[0].GetDisableDns() {
		t.Fatalf("SetConfig did not disable DNS: %v", daemon.setConfig[0])
	}
}

func TestConfigureLeavesSyncedPeerAlone(t *testing.T) {
	// The same server written two ways must not count as a change.
	daemon := &fakeDaemon{status: connected(), config: &daemonpb.GetConfigResponse{ManagementUrl: "https://mgmt.example"}}
	driver := newFakeDriver(t, map[string]any{"management_url": "https://MGMT.example:443/"}, daemon)

	configureBot(t, driver)

	if got := daemon.writes(); len(got) != 0 {
		t.Fatalf("expected no writes, got %v", got)
	}
}

func TestConfigureLogsInAgainWhenManagementServerChanges(t *testing.T) {
	daemon := &fakeDaemon{status: connected(), config: &daemonpb.GetConfigResponse{ManagementUrl: "https://old.example:443"}}
	driver := newFakeDriver(t, map[string]any{"management_url": "https://mgmt.example", "setup_key": "key-1"}, daemon)

	configureBot(t, driver)

	if got := daemon.writes(); !slices.Equal(got, []string{"Logout", "Login", "Up"}) {
		t.Fatalf("calls = %v, want Logout, Login, Up", got)
	}
}

func TestConfigureBringsIdlePeerUp(t *testing.T) {
	idle := connected()
	idle.Status = daemonIdle
	daemon := &fakeDaemon{status: idle, config: &daemonpb.GetConfigResponse{ManagementUrl: "https://mgmt.example:443"}}
	driver := newFakeDriver(t, map[string]any{"management_url": "https://mgmt.example"}, daemon)

	configureBot(t, driver)

	if got := daemon.writes(); !slices.Equal(got, []string{"Up"}) {
		t.Fatalf("calls = %v, want Up", got)
	}
}

func TestOverlayState(t *testing.T) {
	signalDown := connected()
	signalDown.FullStatus.SignalState = &daemonpb.SignalState{Connected: false, Error: "signal unreachable"}
	noAddress := connected()
	noAddress.FullStatus.LocalPeerState = nil
	cases := []struct {
		name   string
		status *daemonpb.StatusResponse
		want   netctl.OverlayState
	}{
		{"connected", connected(), netctl.OverlayStateReady},
		{"signal down", signalDown, netctl.OverlayStateDegraded},
		{"no address", noAddress, netctl.OverlayStateStarting},
		{"login failed", &daemonpb.StatusResponse{Status: daemonLoginFailed}, netctl.OverlayStateUnauthorized},
		{"connecting", &daemonpb.StatusResponse{Status: daemonConnecting}, netctl.OverlayStateStarting},
		{"idle", &daemonpb.StatusResponse{Status: daemonIdle}, netctl.OverlayStateStopped},
		{"needs login", &daemonpb.StatusResponse{Status: daemonNeedsLogin}, netctl.OverlayStateNeedsLogin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := overlayState(tc.status, false, ""); got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLoginRejectionShowsAsUnauthorized(t *testing.T) {
	daemon := &fakeDaemon{status: &daemonpb.StatusResponse{Status: daemonNeedsLogin}, loginErr: grpcstatus.Error(codes.PermissionDenied, "setup key is invalid")}
	driver := newFakeDriver(t, map[string]any{"setup_key": "bad", "management_url": "https://mgmt.example"}, daemon)
	req := netctl.AttachmentRequest{BotID: "bot-1"}

	if err := driver.configure(context.Background(), req); err == nil {
		t.Fatal("expected the rejected login to fail configure")
	}
	status := netctl.OverlayStatus{State: netctl.OverlayStateReady, Attached: true}
	driver.enrichStatus(context.Background(), req, &status)
	if status.State != netctl.OverlayStateUnauthorized || !strings.Contains(status.Message, "setup key is invalid") {
		t.Fatalf("status = %s %q", status.State, status.Message)
	}

	daemon.loginErr = nil
	configureBot(t, driver)
	if driver.loginError(req) != "" {
		t.Fatal("a successful login must clear the recorded rejection")
	}
}
