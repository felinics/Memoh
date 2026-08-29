package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestLoadRejectsLegacyMCPSection(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[mcp]\nfoo = \"legacy\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected load to fail for legacy [mcp] section")
	}
	if !strings.Contains(err.Error(), "[mcp]") || !strings.Contains(err.Error(), "[container]") {
		t.Fatalf("expected migration error mentioning [mcp] and [container], got %v", err)
	}
}

func TestLoadRejectsMixedMCPAndWorkspaceSections(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[mcp]\nfoo = \"legacy\"\n[workspace]\ndefault_image = \"current\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected load to fail when both [mcp] and [workspace] are present")
	}
	if !strings.Contains(err.Error(), "both [mcp] and [workspace]") || !strings.Contains(err.Error(), "[container]") {
		t.Fatalf("expected mixed-section error, got %v", err)
	}
}

func TestLoadReadsWorkspaceDefaultImage(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[workspace]\ndefault_image = \"alpine:3.22\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Workspace.DefaultImage != "alpine:3.22" {
		t.Fatalf("expected default_image to load, got %q", cfg.Workspace.DefaultImage)
	}
}

func TestLoadReadsWorkspaceFieldsFromContainerSection(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[container]
backend = "docker"
default_image = "alpine:3.22"
image_pull_policy = "always"
bridge_path = "/opt/memoh/runtime/bridge"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Container.Backend != "docker" {
		t.Fatalf("container backend = %q", cfg.Container.Backend)
	}
	if cfg.Workspace.DefaultImage != "alpine:3.22" {
		t.Fatalf("workspace default_image = %q", cfg.Workspace.DefaultImage)
	}
	if cfg.Container.DefaultImage != "alpine:3.22" {
		t.Fatalf("container default_image = %q", cfg.Container.DefaultImage)
	}
	if cfg.Workspace.ImagePullPolicy != "always" {
		t.Fatalf("workspace image_pull_policy = %q", cfg.Workspace.ImagePullPolicy)
	}
	if cfg.Workspace.BridgePath != "/opt/memoh/runtime/bridge" {
		t.Fatalf("workspace bridge_path = %q", cfg.Workspace.BridgePath)
	}
}

func TestLoadRejectsMixedWorkspaceFields(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[container]
backend = "docker"
default_image = "alpine:3.22"

[workspace]
default_image = "debian:bookworm-slim"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected mixed [container]/[workspace] fields to fail")
	}
	if !strings.Contains(err.Error(), "both [container] and [workspace]") {
		t.Fatalf("expected mixed section error, got %v", err)
	}
}

func TestLoadReadsBackendSpecificConfigs(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[containerd]
socket_path = "/tmp/containerd.sock"
namespace = "memoh-test"

[docker]
host = "unix:///var/run/docker.sock"

[apple]
socket_path = "/tmp/socktainer.sock"
binary_path = "/opt/homebrew/bin/socktainer"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Containerd.SocketPath != "/tmp/containerd.sock" {
		t.Fatalf("containerd socket path = %q", cfg.Containerd.SocketPath)
	}
	if cfg.Containerd.Namespace != "memoh-test" {
		t.Fatalf("containerd namespace = %q", cfg.Containerd.Namespace)
	}
	if cfg.Docker.Host != "unix:///var/run/docker.sock" {
		t.Fatalf("docker host = %q", cfg.Docker.Host)
	}
	if cfg.Apple.SocketPath != "/tmp/socktainer.sock" {
		t.Fatalf("apple socket path = %q", cfg.Apple.SocketPath)
	}
	if cfg.Apple.BinaryPath != "/opt/homebrew/bin/socktainer" {
		t.Fatalf("apple binary path = %q", cfg.Apple.BinaryPath)
	}
}

func TestLoadAppliesBridgeTLSEnvOverrides(t *testing.T) {
	t.Setenv("MEMOH_INSTANCE_ID", "instance-1")
	t.Setenv("MEMOH_BRIDGE_TLS_MODE", BridgeTLSModeStrict)
	t.Setenv("MEMOH_BRIDGE_TLS_SERVER_DIR", "/server")
	t.Setenv("MEMOH_BRIDGE_TLS_BRIDGE_DIR", "/bridge")
	t.Setenv("MEMOH_BRIDGE_TLS_SERVER_NAME", "bridge.internal")

	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.InstanceID != "instance-1" {
		t.Fatalf("instance id = %q", cfg.InstanceID)
	}
	if cfg.BridgeTLS.Mode != BridgeTLSModeStrict ||
		cfg.BridgeTLS.ServerDir != "/server" ||
		cfg.BridgeTLS.BridgeDir != "/bridge" ||
		cfg.BridgeTLS.ServerName != "bridge.internal" {
		t.Fatalf("bridge tls config = %#v", cfg.BridgeTLS)
	}
}

func TestLoadAppliesWebhookTunnelEnvOverrides(t *testing.T) {
	t.Setenv("MEMOH_WEBHOOK_TUNNEL_MODE", WebhookTunnelModeExternal)
	t.Setenv("MEMOH_WEBHOOK_PUBLIC_BASE_URL", "https://memoh.example.com")
	t.Setenv("MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR", ":18732")
	t.Setenv("MEMOH_CLOUDFLARED_BIN", "/usr/local/bin/cloudflared")
	t.Setenv("MEMOH_WEBHOOK_TUNNEL_TARGET_URL", "http://127.0.0.1:18732")
	t.Setenv("MEMOH_WEBHOOK_TUNNEL_METRICS_ADDR", "127.0.0.1:18733")
	t.Setenv("MEMOH_WEBHOOK_TUNNEL_METRICS_URL", "http://webhook-tunnel:18733")

	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.WebhookTunnel.Mode != WebhookTunnelModeExternal ||
		cfg.WebhookTunnel.PublicBaseURL != "https://memoh.example.com" ||
		cfg.WebhookTunnel.ListenAddr != ":18732" ||
		cfg.WebhookTunnel.CloudflaredPath != "/usr/local/bin/cloudflared" ||
		cfg.WebhookTunnel.TargetURL != "http://127.0.0.1:18732" ||
		cfg.WebhookTunnel.MetricsAddr != "127.0.0.1:18733" ||
		cfg.WebhookTunnel.MetricsURL != "http://webhook-tunnel:18733" {
		t.Fatalf("webhook tunnel config = %#v", cfg.WebhookTunnel)
	}
}

func TestLoadAppliesConnectItEnvOverrides(t *testing.T) {
	t.Setenv("MEMOH_CONNECT_IT_BASE_URL", "http://connect-it:8421")
	t.Setenv("MEMOH_CONNECT_IT_API_TOKEN", "test-token")

	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ConnectIt.BaseURL != "http://connect-it:8421" || cfg.ConnectIt.APIToken != "test-token" {
		t.Fatalf("connect-it config = %#v", cfg.ConnectIt)
	}
}

func TestConnectItConfigValidationRequiresCompletePair(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		config  ConnectItConfig
		wantErr bool
	}{
		{name: "disabled"},
		{
			name: "configured",
			config: ConnectItConfig{
				BaseURL:  "http://connect-it:8421",
				APIToken: "cit_test",
			},
		},
		{
			name:    "missing token",
			config:  ConnectItConfig{BaseURL: "http://connect-it:8421"},
			wantErr: true,
		},
		{
			name:    "missing base URL",
			config:  ConnectItConfig{APIToken: "cit_test"},
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestRuntimeValidationRequiresInternalRPCSecret(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateServerRuntime(); err == nil || !strings.Contains(err.Error(), "shared_secret") {
		t.Fatalf("server validation error = %v", err)
	}
	if err := cfg.ValidateChannelRuntime(); err == nil || !strings.Contains(err.Error(), "shared_secret") {
		t.Fatalf("channel validation error = %v", err)
	}

	t.Setenv("MEMOH_INTERNAL_RPC_SHARED_SECRET", "test-only-secret")
	cfg, err = Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("load config with secret: %v", err)
	}
	if err := cfg.ValidateServerRuntime(); err != nil {
		t.Fatalf("server validation: %v", err)
	}
	if err := cfg.ValidateChannelRuntime(); err != nil {
		t.Fatalf("channel validation: %v", err)
	}
}

func TestLoadRejectsInvalidWebhookTunnelMode(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[webhook_tunnel]\nmode = \"managd\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected invalid webhook tunnel mode to fail")
	}
	if !strings.Contains(err.Error(), "unsupported webhook_tunnel mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsInvalidWebhookTunnelModeFromEnv(t *testing.T) {
	t.Setenv("MEMOH_WEBHOOK_TUNNEL_MODE", "managd")

	_, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil {
		t.Fatal("expected invalid webhook tunnel mode env to fail")
	}
	if !strings.Contains(err.Error(), "unsupported webhook_tunnel mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadDefaultsSessionRuntimeToMemory(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.SessionRuntime.BackendOrDefault() != SessionRuntimeBackendMemory {
		t.Fatalf("session runtime backend = %q, want memory", cfg.SessionRuntime.BackendOrDefault())
	}
	if cfg.SessionRuntime.StateTTLOrDefault() != DefaultSessionRuntimeStateTTL {
		t.Fatalf("session runtime state ttl = %q, want %q", cfg.SessionRuntime.StateTTLOrDefault(), DefaultSessionRuntimeStateTTL)
	}
	if cfg.SessionRuntime.OwnerLeaseTTLOrDefault() != DefaultSessionRuntimeOwnerLeaseTTL {
		t.Fatalf("session runtime owner lease ttl = %q, want %q", cfg.SessionRuntime.OwnerLeaseTTLOrDefault(), DefaultSessionRuntimeOwnerLeaseTTL)
	}
}

func TestLoadReadsSessionRuntimeRedisConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[session_runtime]
backend = "redis"
state_ttl = "12h"
owner_lease_ttl = "45s"

[session_runtime.redis]
url = "redis://redis.example:6379/2"
key_prefix = "test:runtime:"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.SessionRuntime.BackendOrDefault() != SessionRuntimeBackendRedis {
		t.Fatalf("session runtime backend = %q, want redis", cfg.SessionRuntime.BackendOrDefault())
	}
	if cfg.SessionRuntime.StateTTLOrDefault() != "12h" || cfg.SessionRuntime.OwnerLeaseTTLOrDefault() != "45s" {
		t.Fatalf("session runtime ttl config = %#v", cfg.SessionRuntime)
	}
	if cfg.SessionRuntime.Redis.URLOrDefault() != "redis://redis.example:6379/2" {
		t.Fatalf("redis url = %q", cfg.SessionRuntime.Redis.URLOrDefault())
	}
	if cfg.SessionRuntime.Redis.KeyPrefixOrDefault() != "test:runtime:" {
		t.Fatalf("redis key prefix = %q", cfg.SessionRuntime.Redis.KeyPrefixOrDefault())
	}
}

func TestLoadRejectsInvalidSessionRuntimeBackend(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[session_runtime]\nbackend = \"nats\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected invalid session runtime backend to fail")
	}
	if !strings.Contains(err.Error(), "unsupported session_runtime backend") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsInvalidSessionRuntimeDuration(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[session_runtime]\nstate_ttl = \"forever\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected invalid session runtime duration to fail")
	}
	if !strings.Contains(err.Error(), "invalid session_runtime state_ttl") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsNonPositiveSessionRuntimeDuration(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[session_runtime]\nbackend = \"redis\"\nowner_lease_ttl = \"-1s\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected non-positive session runtime duration to fail")
	}
	if !strings.Contains(err.Error(), "invalid session_runtime owner_lease_ttl") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsSessionRuntimeOwnerLeaseBelowMinimum(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[session_runtime]\nbackend = \"redis\"\nowner_lease_ttl = \"500ms\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected too-small session runtime owner lease to fail")
	}
	if !strings.Contains(err.Error(), "must be at least 1s") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsSessionRuntimeStateTTLShorterThanOwnerLease(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[session_runtime]
backend = "redis"
state_ttl = "10s"
owner_lease_ttl = "30s"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected session runtime state ttl shorter than owner lease to fail")
	}
	if !strings.Contains(err.Error(), "must be greater than or equal to owner_lease_ttl") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// owner_lease_ttl paces the reaper tick and the orphan grace, both of which run
// on a memory backend too, so it is no longer ignorable there.
func TestLoadMemorySessionRuntimeValidatesOwnerLeaseTTL(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[session_runtime]\nbackend = \"memory\"\nowner_lease_ttl = \"not-a-duration\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected memory runtime to reject an unparseable owner lease ttl")
	}
	if !strings.Contains(err.Error(), "invalid session_runtime owner_lease_ttl") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsClusterWithMemorySessionRuntime(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[session_runtime]\nbackend = \"memory\"\ncluster = true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected cluster mode to require the redis backend")
	}
	if !strings.Contains(err.Error(), "multi-instance mode requires") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionRuntimeBackendLossGraceDerivesFromOwnerLease(t *testing.T) {
	t.Parallel()

	cfg := SessionRuntimeConfig{Backend: SessionRuntimeBackendRedis, OwnerLeaseTTL: "20s"}
	grace, err := cfg.BackendLossGraceDuration()
	if err != nil {
		t.Fatalf("derive backend loss grace: %v", err)
	}
	if want := SessionRuntimeBackendLossGraceFactor * 20 * time.Second; grace != want {
		t.Fatalf("backend loss grace = %s, want %s", grace, want)
	}

	cfg.BackendLossGrace = "5m"
	grace, err = cfg.BackendLossGraceDuration()
	if err != nil {
		t.Fatalf("read backend loss grace: %v", err)
	}
	if grace != 5*time.Minute {
		t.Fatalf("backend loss grace = %s, want 5m", grace)
	}

	cfg.BackendLossGrace = "1s"
	if _, err := cfg.BackendLossGraceDuration(); err == nil {
		t.Fatal("expected a grace shorter than the owner lease to be rejected")
	}
}

func TestLoadAppExampleTemplateKeepsRootKeysOutsideAgentSection(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "conf", "app.example.toml"))
	if err != nil {
		t.Fatalf("read app.example.toml: %v", err)
	}
	rendered := strings.Replace(string(raw), `timezone = "UTC"`, `timezone = "Asia/Tokyo"`, 1)
	rendered = strings.Replace(rendered, `instance_id = ""`, `instance_id = "instance-example"`, 1)
	configPath := filepath.Join(t.TempDir(), "app.example.toml")
	if err := os.WriteFile(configPath, []byte(rendered), 0o600); err != nil {
		t.Fatalf("write rendered app.example.toml: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load app.example.toml: %v", err)
	}
	if cfg.Timezone != "Asia/Tokyo" {
		t.Fatalf("timezone = %q, want Asia/Tokyo", cfg.Timezone)
	}
	if cfg.InstanceID != "instance-example" {
		t.Fatalf("instance_id = %q, want instance-example", cfg.InstanceID)
	}
	if cfg.Agent.ToolOutputMaxBytes != DefaultAgentToolOutputBytes ||
		cfg.Agent.ToolOutputMaxLines != DefaultAgentToolOutputLines ||
		cfg.Agent.SystemFilesMaxBytes != DefaultAgentSystemFilesBytes {
		t.Fatalf("agent limits = %#v", cfg.Agent)
	}
}

func TestLoadResolvesRelativePaths(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[database]
driver = "postgres"

[container]
data_root = "data/local"
runtime_dir = "data/runtime"
bridge_path = "data/runtime/custom-bridge"

[registry]
providers_dir = "conf/providers"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for name, path := range map[string]string{
		"data_root":     cfg.Workspace.DataRoot,
		"runtime_dir":   cfg.Workspace.RuntimeDir,
		"bridge_path":   cfg.Workspace.BridgePath,
		"providers_dir": cfg.Registry.ProvidersPath(),
	} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s = %q, want absolute path", name, path)
		}
	}
}

func TestWorkspaceImagePullPolicyDefaultsAndNormalizes(t *testing.T) {
	if got := (WorkspaceConfig{}).EffectiveImagePullPolicy(); got != ImagePullPolicyIfNotPresent {
		t.Fatalf("default policy = %q", got)
	}
	if got := (WorkspaceConfig{ImagePullPolicy: "always"}).EffectiveImagePullPolicy(); got != ImagePullPolicyAlways {
		t.Fatalf("always policy = %q", got)
	}
	if got := (WorkspaceConfig{ImagePullPolicy: "invalid"}).EffectiveImagePullPolicy(); got != ImagePullPolicyIfNotPresent {
		t.Fatalf("invalid policy = %q", got)
	}
}

func TestWorkspaceImageRefDefaultsToPackagedWorkspace(t *testing.T) {
	got := (WorkspaceConfig{}).ImageRef()
	want := "docker.io/memohai/workspace:debian"
	if got != want {
		t.Fatalf("default image ref = %q, want %q", got, want)
	}
}

func TestWorkspaceImagePullCandidatesAddsWorkspaceMirror(t *testing.T) {
	got := WorkspaceImagePullCandidates("memohai/workspace:debian")
	want := []string{"docker.io/memohai/workspace:debian", "memoh.cn/memohai/workspace:debian"}
	if len(got) != len(want) {
		t.Fatalf("candidate count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWorkspaceImagePullCandidatesDoesNotMirrorCustomImages(t *testing.T) {
	got := WorkspaceImagePullCandidates("debian:bookworm-slim")
	if len(got) != 1 || got[0] != "docker.io/library/debian:bookworm-slim" {
		t.Fatalf("unexpected candidates: %v", got)
	}
}

func TestAgentConfigEffectiveContextLoopReselectMode(t *testing.T) {
	cases := []struct {
		name           string
		value          string
		wantMode       string
		wantRecognized bool
	}{
		{"empty defaults to active", "", ContextLoopReselectModeActive, true},
		{"active", "active", ContextLoopReselectModeActive, true},
		{"shadow", "shadow", ContextLoopReselectModeShadow, true},
		{"off", "off", ContextLoopReselectModeOff, true},
		{"case insensitive", "SHADOW", ContextLoopReselectModeShadow, true},
		{"whitespace", "  off  ", ContextLoopReselectModeOff, true},
		{"unknown normalizes to active", "garbage", ContextLoopReselectModeActive, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMode, gotRecognized := (AgentConfig{ContextLoopReselect: tc.value}).EffectiveContextLoopReselectMode()
			if gotMode != tc.wantMode || gotRecognized != tc.wantRecognized {
				t.Fatalf("EffectiveContextLoopReselectMode() = (%q, %v), want (%q, %v)", gotMode, gotRecognized, tc.wantMode, tc.wantRecognized)
			}
		})
	}
}

func TestAgentConfigEffectiveContextAbsoluteMaxTokens(t *testing.T) {
	if got := (AgentConfig{}).EffectiveContextAbsoluteMaxTokens(); got != contextfrag.DefaultAbsoluteCapTokens {
		t.Fatalf("unset cap = %d, want default %d", got, contextfrag.DefaultAbsoluteCapTokens)
	}
	if got := (AgentConfig{ContextAbsoluteMaxTokens: -5}).EffectiveContextAbsoluteMaxTokens(); got != contextfrag.DefaultAbsoluteCapTokens {
		t.Fatalf("negative cap = %d, want default %d", got, contextfrag.DefaultAbsoluteCapTokens)
	}
	if got := (AgentConfig{ContextAbsoluteMaxTokens: 500_000}).EffectiveContextAbsoluteMaxTokens(); got != 500_000 {
		t.Fatalf("explicit cap = %d, want 500000", got)
	}
}
