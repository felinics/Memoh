package tailscale

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	netctl "github.com/felinics/memoh/internal/network"
)

type recordedCall struct {
	method string
	path   string
	body   map[string]any
}

// fakeLocalAPI serves the LocalAPI endpoints configure and enrichStatus use
// and records every request.
type fakeLocalAPI struct {
	mu       sync.Mutex
	status   map[string]any
	prefs    map[string]any
	warnings map[string]any
	calls    []recordedCall
}

func (f *fakeLocalAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := recordedCall{method: r.Method, path: strings.TrimPrefix(r.URL.Path, "/localapi/v0/")}
	_ = json.NewDecoder(r.Body).Decode(&call.body)
	f.calls = append(f.calls, call)
	w.Header().Set("Content-Type", "application/json")
	switch call.path {
	case "status":
		_ = json.NewEncoder(w).Encode(f.status)
	case "prefs":
		_ = json.NewEncoder(w).Encode(f.prefs)
	case "watch-ipn-bus":
		_ = json.NewEncoder(w).Encode(map[string]any{"Health": map[string]any{"Warnings": f.warnings}})
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (f *fakeLocalAPI) writes() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedCall
	for _, c := range f.calls {
		if c.method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

// newFakeDriver starts a fake LocalAPI on the socket the driver dials for
// bot-1. Unix socket paths are length-limited, so it lives under /tmp.
func newFakeDriver(t *testing.T, config map[string]any, api *fakeLocalAPI) *nativeDriver {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "tsc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	driver := newNativeDriver(netctl.BotOverlayConfig{Enabled: true, Provider: "tailscale", Config: config}, nil, root, nil)
	socket := driver.socketPath(netctl.AttachmentRequest{BotID: "bot-1"})
	if err := os.MkdirAll(filepath.Dir(socket), 0o750); err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: api, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return driver
}

func runningStatus() map[string]any {
	return map[string]any{
		"BackendState": "Running",
		"Self":         map[string]any{"TailscaleIPs": []string{"100.64.0.2"}},
		"Peer": map[string]any{
			"nodekey:exit": map[string]any{"ID": "stable-exit", "HostName": "exit-a", "DNSName": "exit-a.ts.net.", "TailscaleIPs": []string{"100.64.0.10"}, "ExitNodeOption": true},
		},
	}
}

func syncedPrefs() map[string]any {
	return map[string]any{
		"ControlURL":             "https://hs.example.com",
		"Hostname":               "bot-1",
		"RouteAll":               false,
		"AdvertiseRoutes":        nil,
		"ExitNodeID":             "",
		"ExitNodeIP":             "",
		"CorpDNS":                true,
		"ExitNodeAllowLANAccess": true,
		"WantRunning":            true,
		"NetfilterMode":          2,
		"Config":                 map[string]any{"NodeID": "n1", "PrivateNodeKey": "privkey:0000"},
	}
}

func configureBot(t *testing.T, driver *nativeDriver) {
	t.Helper()
	if err := driver.configure(context.Background(), netctl.AttachmentRequest{BotID: "bot-1"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
}

func TestConfigureLogsInWhenNodeHasNoIdentity(t *testing.T) {
	prefs := syncedPrefs()
	prefs["ControlURL"], prefs["CorpDNS"], prefs["WantRunning"] = "", false, false
	prefs["Config"] = map[string]any{"PrivateNodeKey": "privkey:0000"}
	api := &fakeLocalAPI{status: map[string]any{"BackendState": "NeedsLogin"}, prefs: prefs}
	driver := newFakeDriver(t, map[string]any{"auth_key": "tskey-x", "control_url": "https://hs.example.com/", "accept_routes": true}, api)

	configureBot(t, driver)

	writes := api.writes()
	if len(writes) != 2 || writes[0].path != "start" || writes[1].path != "login-interactive" {
		t.Fatalf("expected start then login-interactive, got %+v", writes)
	}
	start := writes[0].body
	if start["AuthKey"] != "tskey-x" {
		t.Fatalf("auth key not passed to start: %v", start)
	}
	up, _ := start["UpdatePrefs"].(map[string]any)
	for key, want := range map[string]any{
		"ControlURL": "https://hs.example.com", "Hostname": "bot-1", "RouteAll": true,
		"CorpDNS": true, "ExitNodeAllowLANAccess": true, "WantRunning": true,
		// Preferences Memoh does not manage keep tailscaled's value.
		"NetfilterMode": float64(2),
	} {
		if up[key] != want {
			t.Fatalf("UpdatePrefs[%s] = %v, want %v", key, up[key], want)
		}
	}
	// The LocalAPI returns the persisted login with zeroed keys; sending it
	// back would overwrite the real ones.
	if _, ok := up["Config"]; ok {
		t.Fatalf("UpdatePrefs must not carry the persisted login: %v", up)
	}
}

func TestConfigureKeepsIdentityWhileControlServerIsUnreachable(t *testing.T) {
	api := &fakeLocalAPI{status: map[string]any{"BackendState": "NoState"}, prefs: syncedPrefs()}
	driver := newFakeDriver(t, map[string]any{"control_url": "https://hs.example.com"}, api)

	configureBot(t, driver)

	if writes := api.writes(); len(writes) != 0 {
		t.Fatalf("a node with an identity must not log in again, got %+v", writes)
	}
	status := netctl.OverlayStatus{State: netctl.OverlayStateReady, Attached: true}
	driver.enrichStatus(context.Background(), netctl.AttachmentRequest{BotID: "bot-1"}, &status)
	if status.State != netctl.OverlayStateStarting {
		t.Fatalf("state = %s, want starting", status.State)
	}
}

func TestConfigureLogsInAgainWhenKeyExpired(t *testing.T) {
	api := &fakeLocalAPI{status: map[string]any{"BackendState": "NeedsLogin"}, prefs: syncedPrefs()}
	driver := newFakeDriver(t, map[string]any{"control_url": "https://hs.example.com", "auth_key": "tskey-x"}, api)

	configureBot(t, driver)

	writes := api.writes()
	if len(writes) != 2 || writes[0].path != "start" || writes[1].path != "login-interactive" {
		t.Fatalf("expected start then login-interactive, got %+v", writes)
	}
}

func TestConfigureKeepsPendingInteractiveLogin(t *testing.T) {
	api := &fakeLocalAPI{status: map[string]any{"BackendState": "NeedsLogin", "AuthURL": "https://login/abc"}, prefs: syncedPrefs()}
	driver := newFakeDriver(t, map[string]any{"control_url": "https://hs.example.com"}, api)

	configureBot(t, driver)

	if writes := api.writes(); len(writes) != 0 {
		t.Fatalf("a pending login must not be restarted, got %+v", writes)
	}
}

func TestConfigurePatchesOnlyDriftedPrefs(t *testing.T) {
	prefs := syncedPrefs()
	prefs["ExitNodeAllowLANAccess"] = false
	api := &fakeLocalAPI{status: runningStatus(), prefs: prefs}
	driver := newFakeDriver(t, map[string]any{
		"control_url":      "https://hs.example.com",
		"accept_routes":    true,
		"exit_node":        "100.64.0.10",
		"advertise_routes": "10.0.1.7/24, 10.0.0.0/24",
	}, api)

	configureBot(t, driver)

	writes := api.writes()
	if len(writes) != 1 || writes[0].method != http.MethodPatch || writes[0].path != "prefs" {
		t.Fatalf("expected one PATCH prefs, got %+v", writes)
	}
	patch := writes[0].body
	for key, want := range map[string]any{
		"RouteAll": true, "RouteAllSet": true,
		"ExitNodeAllowLANAccess": true, "ExitNodeAllowLANAccessSet": true,
		"ExitNodeID": "stable-exit", "ExitNodeIDSet": true,
		"AdvertiseRoutesSet": true,
	} {
		if patch[key] != want {
			t.Fatalf("patch[%s] = %v, want %v (patch %v)", key, patch[key], want, patch)
		}
	}
	if routes, _ := patch["AdvertiseRoutes"].([]any); len(routes) != 2 || routes[0] != "10.0.0.0/24" || routes[1] != "10.0.1.0/24" {
		t.Fatalf("routes not canonical: %v", patch["AdvertiseRoutes"])
	}
	for _, key := range []string{"HostnameSet", "CorpDNSSet", "WantRunningSet", "ControlURLSet"} {
		if _, ok := patch[key]; ok {
			t.Fatalf("unchanged field %s must not be patched: %v", key, patch)
		}
	}
}

func TestConfigureLeavesSyncedNodeAlone(t *testing.T) {
	api := &fakeLocalAPI{status: runningStatus(), prefs: syncedPrefs()}
	driver := newFakeDriver(t, map[string]any{"control_url": "https://hs.example.com"}, api)

	configureBot(t, driver)

	if writes := api.writes(); len(writes) != 0 {
		t.Fatalf("expected no writes, got %+v", writes)
	}
}

func TestConfigureClearsExitNode(t *testing.T) {
	prefs := syncedPrefs()
	prefs["ExitNodeID"] = "stable-exit"
	api := &fakeLocalAPI{status: runningStatus(), prefs: prefs}
	driver := newFakeDriver(t, map[string]any{"control_url": "https://hs.example.com"}, api)

	configureBot(t, driver)

	writes := api.writes()
	if len(writes) != 1 || writes[0].body["ExitNodeIDSet"] != true || writes[0].body["ExitNodeID"] != "" {
		t.Fatalf("expected exit node to be cleared, got %+v", writes)
	}
}

func TestConfigureLogsInAgainWhenControlServerChanges(t *testing.T) {
	prefs := syncedPrefs()
	prefs["ControlURL"] = "https://old.example.com"
	api := &fakeLocalAPI{status: runningStatus(), prefs: prefs}
	driver := newFakeDriver(t, map[string]any{"control_url": "https://hs.example.com"}, api)

	configureBot(t, driver)

	writes := api.writes()
	if len(writes) != 3 || writes[0].path != "logout" || writes[1].path != "start" || writes[2].path != "login-interactive" {
		t.Fatalf("expected logout, start, login-interactive, got %+v", writes)
	}
}

func TestEnrichStatusDegradesOnlyOnConnectivityWarnings(t *testing.T) {
	cases := []struct {
		name     string
		status   map[string]any
		warnings map[string]any
		want     netctl.OverlayState
	}{
		{"running", runningStatus(), nil, netctl.OverlayStateReady},
		{"informational warning", runningStatus(), map[string]any{"update-available": map[string]any{"Severity": "low", "Text": "update"}}, netctl.OverlayStateReady},
		{"relay down", runningStatus(), map[string]any{"no-derp-connection": map[string]any{"Severity": "medium", "Text": "relay down", "ImpactsConnectivity": true}}, netctl.OverlayStateDegraded},
		{"high severity", runningStatus(), map[string]any{"x": map[string]any{"Severity": "high", "Text": "broken"}}, netctl.OverlayStateDegraded},
		{"no address", map[string]any{"BackendState": "Running"}, nil, netctl.OverlayStateStarting},
		{"awaiting approval", map[string]any{"BackendState": "NeedsMachineAuth"}, nil, netctl.OverlayStateNeedsLogin},
		{"stopped", map[string]any{"BackendState": "Stopped"}, nil, netctl.OverlayStateStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeLocalAPI{status: tc.status, prefs: syncedPrefs(), warnings: tc.warnings}
			driver := newFakeDriver(t, map[string]any{}, api)
			status := netctl.OverlayStatus{State: netctl.OverlayStateReady, Attached: true}
			driver.enrichStatus(context.Background(), netctl.AttachmentRequest{BotID: "bot-1"}, &status)
			if status.State != tc.want {
				t.Fatalf("state = %s, want %s (message %q)", status.State, tc.want, status.Message)
			}
		})
	}
}

func TestParseRoutesRejectsInvalidCIDR(t *testing.T) {
	if _, err := parseRoutes("10.0.0.0/24, not-a-cidr"); err == nil {
		t.Fatal("expected an error for an invalid CIDR")
	}
	routes, err := parseRoutes(" 10.0.2.1/24,10.0.0.0/24,10.0.2.0/24 ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(routes, ",") != "10.0.0.0/24,10.0.2.0/24" {
		t.Fatalf("routes = %v", routes)
	}
}
