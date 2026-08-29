package tailscale

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	netctl "github.com/felinics/memoh/internal/network"
)

func TestProviderStatusRejectsExitNodeWithUserspace(t *testing.T) {
	provider := NewProvider(Deps{StateRoot: t.TempDir()})
	status, err := provider.Status(context.Background(), netctl.BotOverlayConfig{
		Enabled:  true,
		Provider: "tailscale",
		Config: map[string]any{
			"auth_key":  "tskey-test",
			"userspace": true,
			"exit_node": "100.64.0.10",
		},
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.State != netctl.StatusStateNeedsConfig {
		t.Fatalf("expected needs config, got %s", status.State)
	}
	if !strings.Contains(status.Description, "userspace=false") {
		t.Fatalf("expected userspace hint, got %q", status.Description)
	}
}

func TestProviderStatusAllowsExitNodeWithKernelTUN(t *testing.T) {
	provider := NewProvider(Deps{StateRoot: t.TempDir()})
	status, err := provider.Status(context.Background(), netctl.BotOverlayConfig{
		Enabled:  true,
		Provider: "tailscale",
		Config: map[string]any{
			"auth_key":  "tskey-test",
			"userspace": false,
			"exit_node": "100.64.0.10",
		},
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.State != netctl.StatusStateReady {
		t.Fatalf("expected ready, got %s: %s", status.State, status.Description)
	}
}

func TestNativeDriverBuildSpecExitNodeArgs(t *testing.T) {
	driver := newNativeDriver(netctl.BotOverlayConfig{
		Enabled:  true,
		Provider: "tailscale",
		Config: map[string]any{
			"auth_key":  "tskey-test",
			"userspace": false,
		},
	}, nil, t.TempDir())
	spec, err := driver.buildSpec(netctl.AttachmentRequest{
		BotID: "bot-1",
		Overlay: netctl.BotOverlayConfig{
			Enabled:  true,
			Provider: "tailscale",
			Config: map[string]any{
				"exit_node": "100.64.0.10",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildSpec returned error: %v", err)
	}
	if spec.ProxyAddress != "" {
		t.Fatalf("expected no explicit proxy address in transparent mode, got %q", spec.ProxyAddress)
	}
	if env := strings.Join(spec.Env, " "); !strings.Contains(env, "--exit-node=100.64.0.10") {
		t.Fatalf("expected exit node arg in env, got %q", env)
	}
}

func TestNativeDriverBuildSpecSocks5Enabled(t *testing.T) {
	driver := newNativeDriver(netctl.BotOverlayConfig{
		Enabled:  true,
		Provider: "tailscale",
		Config: map[string]any{
			"auth_key":       "tskey-test",
			"userspace":      true,
			"socks5_enabled": true,
			"socks5_port":    float64(1055),
		},
	}, nil, t.TempDir())
	spec, err := driver.buildSpec(netctl.AttachmentRequest{BotID: "bot-1", Overlay: netctl.BotOverlayConfig{Enabled: true, Provider: "tailscale", Config: map[string]any{}}})
	if err != nil {
		t.Fatalf("buildSpec returned error: %v", err)
	}
	if !strings.Contains(spec.ProxyAddress, "socks5://") {
		t.Fatalf("expected socks5 proxy address, got %q", spec.ProxyAddress)
	}
	if env := strings.Join(spec.Env, " "); !strings.Contains(env, "TS_SOCKS5_SERVER=:1055") {
		t.Fatalf("expected socks5 server env, got %q", env)
	}
}

func TestNativeDriverListNodesFiltersExitNodes(t *testing.T) {
	tempDir, err := os.MkdirTemp("/tmp", "tsn-")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("remove temp dir: %v", removeErr)
		}
	})
	driver := newNativeDriver(netctl.BotOverlayConfig{
		Provider: "tailscale",
		Config: map[string]any{
			"exit_node": "100.64.0.10",
		},
	}, nil, tempDir)
	socketPath := filepath.Join(tempDir, "bot-1", "tailscale", "run", "tailscaled.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	closeServer := startUnixJSONServer(t, socketPath, map[string]any{
		"BackendState": "Running",
		"Peer": map[string]any{
			"nodekey:exit": map[string]any{
				"ID":             "node-stable-id-1",
				"HostName":       "exit-a",
				"DNSName":        "exit-a.tailnet.ts.net.",
				"Online":         true,
				"TailscaleIPs":   []string{"100.64.0.10"},
				"OS":             "linux",
				"ExitNodeOption": true,
			},
			"nodekey:regular": map[string]any{
				"ID":             "node-stable-id-2",
				"HostName":       "worker-b",
				"DNSName":        "worker-b.tailnet.ts.net.",
				"Online":         true,
				"TailscaleIPs":   []string{"100.64.0.20"},
				"OS":             "linux",
				"ExitNodeOption": false,
			},
		},
	})
	defer closeServer()
	nodes, err := driver.listNodes(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("listNodes returned error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 exit node, got %d", len(nodes))
	}
	if nodes[0].Value != "100.64.0.10" || !nodes[0].Selected {
		t.Fatalf("unexpected node: %+v", nodes[0])
	}
}

func startUnixJSONServer(t *testing.T, socketPath string, payload map[string]any) func() {
	t.Helper()
	if err := os.RemoveAll(socketPath); err != nil {
		t.Fatalf("remove stale socket: %v", err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		t.Fatalf("listen on unix socket: %v", err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
		}),
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() {
		_ = server.Serve(listener)
	}()
	return func() {
		_ = server.Close()
		_ = listener.Close()
	}
}
