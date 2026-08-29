package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const (
	DefaultConfigPath      = "config.toml"
	DefaultHTTPAddr        = ":8080"
	DefaultChannelHTTPAddr = ":8081"
	// The internal RPC is plaintext; default to loopback so bare-metal
	// split deployments never expose it on all interfaces by accident.
	// Container deployments override this in their config template.
	DefaultServerRPCListenAddr   = "127.0.0.1:9090"
	DefaultChannelRPCListenAddr  = "127.0.0.1:9091"
	DefaultServerRPCTarget       = "127.0.0.1:9090"
	DefaultChannelRPCTarget      = "127.0.0.1:9091"
	DefaultNamespace             = "default"
	DefaultSocketPath            = "/run/containerd/containerd.sock"
	DefaultDataRoot              = "data"
	DefaultDataMount             = "/data"
	DefaultCNIBinaryDir          = "/opt/cni/bin"
	DefaultCNIConfigDir          = "/etc/cni/net.d"
	DefaultJWTExpiresIn          = "24h"
	DefaultDatabaseDriver        = "postgres"
	DefaultPGHost                = "127.0.0.1"
	DefaultPGPort                = 5432
	DefaultPGUser                = "postgres"
	DefaultPGDatabase            = "memoh"
	DefaultPGSSLMode             = "disable"
	DefaultPGVectorHost          = "127.0.0.1"
	DefaultPGVectorPort          = 5432
	DefaultPGVectorUser          = "memoh"
	DefaultPGVectorDatabase      = "memoh_vector"
	DefaultPGVectorSSLMode       = "disable"
	DefaultRuntimeDir            = "/opt/memoh/runtime"
	DefaultBridgePath            = DefaultRuntimeDir + "/bridge"
	DefaultWorkspaceImage        = "memohai/workspace:debian"
	DefaultBaseImage             = DefaultWorkspaceImage
	DefaultWorkspaceMirrorImage  = "memoh.cn/memohai/workspace:debian"
	DefaultTimezone              = "UTC"
	DefaultAgentToolOutputBytes  = 64 * 1024
	DefaultAgentToolOutputLines  = 2000
	DefaultAgentSystemFilesBytes = 32 * 1024

	ImagePullPolicyIfNotPresent = "if_not_present"
	ImagePullPolicyAlways       = "always"
	ImagePullPolicyNever        = "never"
)

type Config struct {
	Log            LogConfig            `toml:"log"`
	Server         ServerConfig         `toml:"server"`
	Channel        ChannelConfig        `toml:"channel"`
	InternalRPC    InternalRPCConfig    `toml:"internal_rpc"`
	Admin          AdminConfig          `toml:"admin"`
	Auth           AuthConfig           `toml:"auth"`
	Agent          AgentConfig          `toml:"agent"`
	Timezone       string               `toml:"timezone"`
	Database       DatabaseConfig       `toml:"database"`
	Container      ContainerConfig      `toml:"container"`
	Containerd     ContainerdConfig     `toml:"containerd"`
	Docker         DockerConfig         `toml:"docker"`
	Apple          AppleConfig          `toml:"apple"`
	Workspace      WorkspaceConfig      `toml:"workspace"`
	Postgres       PostgresConfig       `toml:"postgres"`
	PGVector       PGVectorConfig       `toml:"pgvector"`
	Registry       RegistryConfig       `toml:"registry"`
	Supermarket    SupermarketConfig    `toml:"supermarket"`
	OAuthClients   OAuthClientsConfig   `toml:"oauth_clients"`
	SessionRuntime SessionRuntimeConfig `toml:"session_runtime"`
	InstanceID     string               `toml:"instance_id"`
	BridgeTLS      BridgeTLSConfig      `toml:"bridge_tls"`
	WebhookTunnel  WebhookTunnelConfig  `toml:"webhook_tunnel"`
	ConnectIt      ConnectItConfig      `toml:"connect_it"`
}

// ConnectItConfig is the deployment-level credential Memoh uses to call its
// trusted Connect-It instance. It is never persisted in bot configuration.
type ConnectItConfig struct {
	BaseURL  string `toml:"base_url"`
	APIToken string `toml:"api_token" json:"-"`
}

func (c ConnectItConfig) Configured() bool {
	return strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.APIToken) != ""
}

func (c ConnectItConfig) Validate() error {
	hasBaseURL := strings.TrimSpace(c.BaseURL) != ""
	hasAPIToken := strings.TrimSpace(c.APIToken) != ""
	if hasBaseURL == hasAPIToken {
		return nil
	}
	return errors.New("connect_it: base_url and api_token must be configured together")
}

const (
	BridgeTLSModeDisabled = "disabled"
	BridgeTLSModeStrict   = "strict"
)

// BridgeTLSConfig controls mTLS for Memoh server -> workspace bridge TCP gRPC.
// Strict mode never falls back to plaintext. UDS/local targets are unaffected.
type BridgeTLSConfig struct {
	Mode       string `toml:"mode"`
	ServerDir  string `toml:"server_dir"`
	BridgeDir  string `toml:"bridge_dir"`
	ServerName string `toml:"server_name"`
}

func (c BridgeTLSConfig) EffectiveMode() string {
	mode := strings.TrimSpace(strings.ToLower(c.Mode))
	if mode == "" {
		return BridgeTLSModeDisabled
	}
	return mode
}

func (c BridgeTLSConfig) Strict() bool {
	return c.EffectiveMode() == BridgeTLSModeStrict
}

func BridgeServerName(instanceID string) string {
	return fmt.Sprintf("memoh-bridge.%s.bridge.memoh.internal", instanceID)
}

func BridgeServerSPIFFE(instanceID string) string {
	return fmt.Sprintf("spiffe://memoh/instance/%s/bridge", instanceID)
}

func ServerClientSPIFFE(instanceID string) string {
	return fmt.Sprintf("spiffe://memoh/instance/%s/server", instanceID)
}

type LogConfig struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type ServerConfig struct {
	Addr          string `toml:"addr"`
	RPCListenAddr string `toml:"rpc_listen_addr"`
}

type ChannelConfig struct {
	Addr          string `toml:"addr"`
	RPCListenAddr string `toml:"rpc_listen_addr"`
}

type InternalRPCConfig struct {
	ServerTarget  string `toml:"server_target"`
	ChannelTarget string `toml:"channel_target"`
	SharedSecret  string `toml:"shared_secret" json:"-"`
}

func (c InternalRPCConfig) Validate() error {
	if strings.TrimSpace(c.SharedSecret) == "" {
		return errors.New("internal_rpc.shared_secret is required")
	}
	if strings.TrimSpace(c.ServerTarget) == "" {
		return errors.New("internal_rpc.server_target is required")
	}
	if strings.TrimSpace(c.ChannelTarget) == "" {
		return errors.New("internal_rpc.channel_target is required")
	}
	return nil
}

const (
	WebhookTunnelModeDisabled = "disabled"
	WebhookTunnelModeManaged  = "managed"
	WebhookTunnelModeExternal = "external"
)

type WebhookTunnelConfig struct {
	Mode            string `toml:"mode"`
	PublicBaseURL   string `toml:"public_base_url"`
	ListenAddr      string `toml:"listen_addr"`
	CloudflaredPath string `toml:"cloudflared_path"`
	TargetURL       string `toml:"target_url"`
	MetricsAddr     string `toml:"metrics_addr"`
	MetricsURL      string `toml:"metrics_url"`
}

func (c WebhookTunnelConfig) EffectiveMode() string {
	mode := strings.TrimSpace(strings.ToLower(c.Mode))
	if mode == "" {
		return WebhookTunnelModeDisabled
	}
	return mode
}

func (c WebhookTunnelConfig) Validate() error {
	switch c.EffectiveMode() {
	case WebhookTunnelModeDisabled, WebhookTunnelModeManaged, WebhookTunnelModeExternal:
		return nil
	default:
		return fmt.Errorf("unsupported webhook_tunnel mode %q", c.Mode)
	}
}

type AdminConfig struct {
	Username string `toml:"username"`
	Password string `toml:"password" json:"-"`
	Email    string `toml:"email"`
}

type AuthConfig struct {
	JWTSecret    string `toml:"jwt_secret"    json:"-"`
	JWTExpiresIn string `toml:"jwt_expires_in"`
}

type AgentConfig struct {
	ToolOutputMaxBytes  int    `toml:"tool_output_max_bytes"`
	ToolOutputMaxLines  int    `toml:"tool_output_max_lines"`
	SystemFilesMaxBytes int    `toml:"system_files_max_bytes"`
	ContextLoopReselect string `toml:"context_loop_reselect"`
	// ContextAbsoluteMaxTokens is the server-wide context admission cap
	// (CM-ADM-001): the effective per-turn budget is
	// min(model context window − reserve, this cap), and the cap alone when
	// the model has no configured window. Zero or negative selects the
	// built-in default; the cap can be raised but never disabled.
	ContextAbsoluteMaxTokens int `toml:"context_absolute_max_tokens"`
}

// EffectiveContextAbsoluteMaxTokens resolves the server-wide context
// admission cap, falling back to the shared default when unset.
func (c AgentConfig) EffectiveContextAbsoluteMaxTokens() int {
	if c.ContextAbsoluteMaxTokens > 0 {
		return c.ContextAbsoluteMaxTokens
	}
	return contextfrag.DefaultAbsoluteCapTokens
}

const (
	ContextLoopReselectModeActive = "active"
	ContextLoopReselectModeShadow = "shadow"
	ContextLoopReselectModeOff    = "off"
)

// EffectiveContextLoopReselectMode normalizes the configured in-loop context
// step reselector rollout mode. Empty defaults to active. recognized is false
// when a non-empty value does not match active/shadow/off.
func (c AgentConfig) EffectiveContextLoopReselectMode() (mode string, recognized bool) {
	value := strings.TrimSpace(strings.ToLower(c.ContextLoopReselect))
	switch value {
	case "":
		return ContextLoopReselectModeActive, true
	case ContextLoopReselectModeActive, ContextLoopReselectModeShadow, ContextLoopReselectModeOff:
		return value, true
	default:
		return ContextLoopReselectModeActive, false
	}
}

const (
	SessionRuntimeBackendMemory = "memory"
	SessionRuntimeBackendRedis  = "redis"

	DefaultSessionRuntimeStateTTL       = "24h"
	DefaultSessionRuntimeOwnerLeaseTTL  = "30s"
	DefaultSessionRuntimeRedisURL       = "redis://127.0.0.1:6379/0"
	DefaultSessionRuntimeRedisKeyPrefix = "memoh:session_runtime:"
	MinSessionRuntimeOwnerLeaseTTL      = time.Second

	// SessionRuntimeBackendLossGraceFactor derives the default fail-closed
	// grace from owner_lease_ttl. Three lease periods absorbs a short blip
	// without letting a genuinely lost backend hold runs open indefinitely.
	SessionRuntimeBackendLossGraceFactor = 3
)

// SessionRuntimeConfig exposes only the values that depend on something the
// process cannot observe. Every other timing in the session runtime is derived
// from OwnerLeaseTTL or is a package constant in the sessionruntime package;
// see docs and internal/agent/runtime/session/tuning.go.
type SessionRuntimeConfig struct {
	Backend string `toml:"backend"`
	// Cluster declares that more than one server instance shares this
	// deployment. It is a topology statement rather than a tuning value: no
	// process can infer how many peers it has, and SR-DEP-001 requires
	// refusing multi-instance mode without a shared live backend.
	Cluster       bool   `toml:"cluster"`
	StateTTL      string `toml:"state_ttl"`
	OwnerLeaseTTL string `toml:"owner_lease_ttl"`
	// BackendLossGrace is how long to wait after observing a new live backend
	// generation before the fail-closed sweep marks stale runs lost. This is
	// the budget for a Redis restart or failover, which varies by deployment
	// and cannot be derived from anything the server knows. Empty derives
	// SessionRuntimeBackendLossGraceFactor x OwnerLeaseTTL.
	BackendLossGrace string                    `toml:"backend_loss_grace"`
	Redis            SessionRuntimeRedisConfig `toml:"redis"`
}

type SessionRuntimeRedisConfig struct {
	URL       string `toml:"url"`
	KeyPrefix string `toml:"key_prefix"`
}

func (c SessionRuntimeConfig) BackendOrDefault() string {
	backend := strings.TrimSpace(strings.ToLower(c.Backend))
	if backend == "" {
		return SessionRuntimeBackendMemory
	}
	return backend
}

func (c SessionRuntimeConfig) StateTTLOrDefault() string {
	if strings.TrimSpace(c.StateTTL) != "" {
		return strings.TrimSpace(c.StateTTL)
	}
	return DefaultSessionRuntimeStateTTL
}

func (c SessionRuntimeConfig) OwnerLeaseTTLOrDefault() string {
	if strings.TrimSpace(c.OwnerLeaseTTL) != "" {
		return strings.TrimSpace(c.OwnerLeaseTTL)
	}
	return DefaultSessionRuntimeOwnerLeaseTTL
}

// OwnerLeaseTTLDuration is the single tuning dial for the session runtime. It
// paces owner lease renewal, the reaper tick, the reaper leader lease and the
// orphan grace, so it matters on both backends: with a memory backend there is
// no cross-process lease, but the reaper still runs.
func (c SessionRuntimeConfig) OwnerLeaseTTLDuration() (time.Duration, error) {
	value := c.OwnerLeaseTTLOrDefault()
	ttl, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid session_runtime owner_lease_ttl %q: %w", c.OwnerLeaseTTL, err)
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("invalid session_runtime owner_lease_ttl %q: must be positive", value)
	}
	if ttl < MinSessionRuntimeOwnerLeaseTTL {
		return 0, fmt.Errorf("invalid session_runtime owner_lease_ttl %q: must be at least %s", value, MinSessionRuntimeOwnerLeaseTTL)
	}
	return ttl, nil
}

// BackendLossGraceDuration returns the configured grace, or the derived default
// when unset. A grace shorter than one lease period would start the sweep while
// a healthy owner could still be renewing, so it is rejected.
func (c SessionRuntimeConfig) BackendLossGraceDuration() (time.Duration, error) {
	ownerLeaseTTL, err := c.OwnerLeaseTTLDuration()
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(c.BackendLossGrace)
	if value == "" {
		return SessionRuntimeBackendLossGraceFactor * ownerLeaseTTL, nil
	}
	grace, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid session_runtime backend_loss_grace %q: %w", c.BackendLossGrace, err)
	}
	if grace < ownerLeaseTTL {
		return 0, fmt.Errorf("invalid session_runtime backend_loss_grace %q: must be greater than or equal to owner_lease_ttl %q", value, c.OwnerLeaseTTLOrDefault())
	}
	return grace, nil
}

func (c SessionRuntimeRedisConfig) URLOrDefault() string {
	if strings.TrimSpace(c.URL) != "" {
		return strings.TrimSpace(c.URL)
	}
	return DefaultSessionRuntimeRedisURL
}

func (c SessionRuntimeRedisConfig) KeyPrefixOrDefault() string {
	if strings.TrimSpace(c.KeyPrefix) != "" {
		return strings.TrimSpace(c.KeyPrefix)
	}
	return DefaultSessionRuntimeRedisKeyPrefix
}

func (c SessionRuntimeConfig) Validate() error {
	backend := c.BackendOrDefault()
	switch backend {
	case SessionRuntimeBackendMemory, SessionRuntimeBackendRedis:
	default:
		return fmt.Errorf("unsupported session_runtime backend %q", c.Backend)
	}
	stateTTL, err := time.ParseDuration(c.StateTTLOrDefault())
	if err != nil {
		return fmt.Errorf("invalid session_runtime state_ttl %q: %w", c.StateTTL, err)
	}
	if stateTTL <= 0 {
		return fmt.Errorf("invalid session_runtime state_ttl %q: must be positive", c.StateTTLOrDefault())
	}
	ownerLeaseTTL, err := c.OwnerLeaseTTLDuration()
	if err != nil {
		return err
	}
	if _, err := c.BackendLossGraceDuration(); err != nil {
		return err
	}
	// Live snapshots must outlive the lease on either backend: state that
	// expires while a run is still owned would leave an owner with nothing to
	// attach a reconnecting subscriber to.
	if stateTTL < ownerLeaseTTL {
		return fmt.Errorf("invalid session_runtime state_ttl %q: must be greater than or equal to owner_lease_ttl %q", c.StateTTLOrDefault(), c.OwnerLeaseTTLOrDefault())
	}
	// SR-DEP-001: a memory backend keeps live state inside one process, so it
	// cannot arbitrate ownership between instances. Refusing here is the
	// difference between a clear startup failure and silent concurrent execution
	// of the same session on two servers.
	if backend == SessionRuntimeBackendMemory && c.Cluster {
		return errors.New(`invalid session_runtime cluster: multi-instance mode requires backend = "redis"`)
	}
	return nil
}

type DatabaseConfig struct {
	Driver string `toml:"driver"`
}

func (c DatabaseConfig) DriverOrDefault() string {
	driver := strings.TrimSpace(strings.ToLower(c.Driver))
	if driver == "" {
		return DefaultDatabaseDriver
	}
	return driver
}

type ContainerConfig struct {
	Backend string `toml:"backend"`
	WorkspaceConfig
}

type ContainerdConfig struct {
	SocketPath string `toml:"socket_path"`
	Namespace  string `toml:"namespace"`
}

type DockerConfig struct {
	Host string `toml:"host"`
}

type AppleConfig struct {
	SocketPath string `toml:"socket_path"`
	BinaryPath string `toml:"binary_path"`
}

type WorkspaceConfig struct {
	Registry        string `toml:"registry"`
	DefaultImage    string `toml:"default_image"`
	ImagePullPolicy string `toml:"image_pull_policy"`
	Snapshotter     string `toml:"snapshotter"`
	DataRoot        string `toml:"data_root"`
	CNIBinaryDir    string `toml:"cni_bin_dir"`
	CNIConfigDir    string `toml:"cni_conf_dir"`
	BridgePath      string `toml:"bridge_path"`
	// RuntimeDir is accepted for one compatibility release. New deployments
	// should configure bridge_path because the Server no longer owns a toolkit
	// or workspace templates directory.
	RuntimeDir string `toml:"runtime_dir"`
}

// ImageRef returns the fully qualified image reference for the base image,
// prepending the registry mirror when configured and normalizing for containerd
// compatibility.
func (c WorkspaceConfig) ImageRef() string {
	img := c.DefaultImage
	if img == "" {
		img = DefaultBaseImage
	}
	if c.Registry != "" {
		return c.Registry + "/" + img
	}
	return NormalizeImageRef(img)
}

// RuntimePath returns the path to the workspace runtime directory.
func (c WorkspaceConfig) RuntimePath() string {
	if c.RuntimeDir != "" {
		return absPath(c.RuntimeDir)
	}
	return DefaultRuntimeDir
}

// BridgeBinaryPath returns the host path mounted read-only into native
// workspace containers. runtime_dir remains a compatibility fallback.
func (c WorkspaceConfig) BridgeBinaryPath() string {
	if strings.TrimSpace(c.BridgePath) != "" {
		return absPath(c.BridgePath)
	}
	if strings.TrimSpace(c.RuntimeDir) != "" {
		return filepath.Join(c.RuntimePath(), "bridge")
	}
	return DefaultBridgePath
}

func (c WorkspaceConfig) DataRootPath() string {
	if strings.TrimSpace(c.DataRoot) != "" {
		return absPath(c.DataRoot)
	}
	return absPath(DefaultDataRoot)
}

func (c WorkspaceConfig) EffectiveImagePullPolicy() string {
	switch strings.TrimSpace(strings.ToLower(c.ImagePullPolicy)) {
	case ImagePullPolicyAlways:
		return ImagePullPolicyAlways
	case ImagePullPolicyNever:
		return ImagePullPolicyNever
	case ImagePullPolicyIfNotPresent, "":
		return ImagePullPolicyIfNotPresent
	default:
		return ImagePullPolicyIfNotPresent
	}
}

func expandHome(path string) string {
	if path == "~" {
		return homeDirOrDot()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDirOrDot(), path[2:])
	}
	return path
}

func absPath(path string) string {
	path = strings.TrimSpace(expandHome(path))
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

func homeDirOrDot() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return home
	}
	return "."
}

// NormalizeImageRef ensures an image reference is fully qualified for containerd.
func NormalizeImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	firstSlash := strings.Index(ref, "/")
	if firstSlash == -1 {
		return "docker.io/library/" + ref
	}
	firstSegment := ref[:firstSlash]
	if strings.Contains(firstSegment, ".") || strings.Contains(firstSegment, ":") || firstSegment == "localhost" {
		return ref
	}
	return "docker.io/" + ref
}

func WorkspaceImagePullCandidates(ref string) []string {
	normalized := NormalizeImageRef(ref)
	if normalized == "" {
		return nil
	}
	if normalized == NormalizeImageRef(DefaultWorkspaceImage) {
		return []string{normalized, DefaultWorkspaceMirrorImage}
	}
	return []string{normalized}
}

type PostgresConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	User     string `toml:"user"`
	Password string `toml:"password" json:"-"`
	Database string `toml:"database"`
	SSLMode  string `toml:"sslmode"`
}

type PGVectorConfig struct {
	Enabled  bool   `toml:"enabled"`
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	User     string `toml:"user"`
	Password string `toml:"password" json:"-"`
	Database string `toml:"database"`
	SSLMode  string `toml:"sslmode"`
}

func (c PGVectorConfig) PostgresConfig() PostgresConfig {
	host := strings.TrimSpace(c.Host)
	if host == "" {
		host = DefaultPGVectorHost
	}
	port := c.Port
	if port == 0 {
		port = DefaultPGVectorPort
	}
	user := strings.TrimSpace(c.User)
	if user == "" {
		user = DefaultPGVectorUser
	}
	database := strings.TrimSpace(c.Database)
	if database == "" {
		database = DefaultPGVectorDatabase
	}
	sslMode := strings.TrimSpace(c.SSLMode)
	if sslMode == "" {
		sslMode = DefaultPGVectorSSLMode
	}
	return PostgresConfig{
		Host:     host,
		Port:     port,
		User:     user,
		Password: c.Password,
		Database: database,
		SSLMode:  sslMode,
	}
}

const DefaultProvidersDir = "conf/providers"

type RegistryConfig struct {
	ProvidersDir string `toml:"providers_dir"`
}

// ProvidersPath returns the configured providers directory or the default.
func (c RegistryConfig) ProvidersPath() string {
	if c.ProvidersDir != "" {
		return absPath(c.ProvidersDir)
	}
	return absPath(DefaultProvidersDir)
}

const (
	DefaultSupermarketBaseURL     = "https://supermarket.memoh.ai"
	DefaultOAuthClientsConfigPath = "conf/oauth-clients.toml"
)

type SupermarketConfig struct {
	BaseURL string `toml:"base_url"`
}

func (c SupermarketConfig) GetBaseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultSupermarketBaseURL
}

type OAuthClientsConfig struct {
	ConfigPath string `toml:"config_path"`
}

func (c OAuthClientsConfig) Path() string {
	if strings.TrimSpace(c.ConfigPath) != "" {
		return absPath(c.ConfigPath)
	}
	return absPath(DefaultOAuthClientsConfigPath)
}

func Load(path string) (Config, error) {
	defaultWorkspace := WorkspaceConfig{
		DefaultImage: DefaultBaseImage,
		DataRoot:     DefaultDataRoot,
		CNIBinaryDir: DefaultCNIBinaryDir,
		CNIConfigDir: DefaultCNIConfigDir,
	}
	cfg := Config{
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
		Server: ServerConfig{
			Addr:          DefaultHTTPAddr,
			RPCListenAddr: DefaultServerRPCListenAddr,
		},
		Channel: ChannelConfig{
			Addr:          DefaultChannelHTTPAddr,
			RPCListenAddr: DefaultChannelRPCListenAddr,
		},
		InternalRPC: InternalRPCConfig{
			ServerTarget:  DefaultServerRPCTarget,
			ChannelTarget: DefaultChannelRPCTarget,
		},
		Admin: AdminConfig{
			Username: "admin",
			Password: "change-your-password-here",
			Email:    "you@example.com",
		},
		Auth: AuthConfig{
			JWTExpiresIn: DefaultJWTExpiresIn,
		},
		Agent: AgentConfig{
			ToolOutputMaxBytes:  DefaultAgentToolOutputBytes,
			ToolOutputMaxLines:  DefaultAgentToolOutputLines,
			SystemFilesMaxBytes: DefaultAgentSystemFilesBytes,
		},
		Timezone: DefaultTimezone,
		Database: DatabaseConfig{
			Driver: DefaultDatabaseDriver,
		},
		Container: ContainerConfig{
			Backend:         "",
			WorkspaceConfig: defaultWorkspace,
		},
		Containerd: ContainerdConfig{
			SocketPath: DefaultSocketPath,
			Namespace:  DefaultNamespace,
		},
		Workspace: defaultWorkspace,
		Postgres: PostgresConfig{
			Host:     DefaultPGHost,
			Port:     DefaultPGPort,
			User:     DefaultPGUser,
			Database: DefaultPGDatabase,
			SSLMode:  DefaultPGSSLMode,
		},
		SessionRuntime: SessionRuntimeConfig{
			Backend:       SessionRuntimeBackendMemory,
			StateTTL:      DefaultSessionRuntimeStateTTL,
			OwnerLeaseTTL: DefaultSessionRuntimeOwnerLeaseTTL,
			Redis: SessionRuntimeRedisConfig{
				URL:       DefaultSessionRuntimeRedisURL,
				KeyPrefix: DefaultSessionRuntimeRedisKeyPrefix,
			},
		},
	}

	if path == "" {
		path = DefaultConfigPath
	}
	path = filepath.Clean(path)

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			cfg.applyBridgeTLSEnvOverrides()
			if err := cfg.validate(); err != nil {
				return cfg, err
			}
			cfg.resolvePaths()
			return cfg, nil
		}
		return cfg, err
	}

	//nolint:gosec // config path is intentionally user-configurable
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	var raw struct {
		Container map[string]any `toml:"container"`
		Workspace map[string]any `toml:"workspace"`
		MCP       map[string]any `toml:"mcp"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return cfg, err
	}
	if raw.MCP != nil {
		if raw.Workspace != nil {
			return cfg, errors.New("config uses both [mcp] and [workspace]; remove [mcp] and move workspace fields into [container]")
		}
		return cfg, errors.New("config section [mcp] has been replaced by workspace fields in [container]; update your config.toml and restart")
	}

	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return cfg, err
	}
	if raw.Workspace != nil && containerHasWorkspaceFields(raw.Container) {
		return cfg, errors.New("config uses workspace fields in both [container] and [workspace]; move workspace fields into [container] and remove [workspace]")
	}
	if raw.Workspace != nil {
		cfg.Container.WorkspaceConfig = cfg.Workspace
	} else {
		cfg.Workspace = cfg.Container.WorkspaceConfig
	}
	cfg.applyBridgeTLSEnvOverrides()
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	cfg.resolvePaths()

	return cfg, nil
}

func (cfg Config) validate() error {
	if cfg.Database.DriverOrDefault() != DefaultDatabaseDriver {
		return fmt.Errorf("unsupported database driver %q", cfg.Database.DriverOrDefault())
	}
	if err := cfg.WebhookTunnel.Validate(); err != nil {
		return err
	}
	if err := cfg.SessionRuntime.Validate(); err != nil {
		return err
	}
	if err := cfg.ConnectIt.Validate(); err != nil {
		return err
	}
	return nil
}

// SplitChannelRuntime reports whether the channel runtime runs as a
// separate process reached over the internal RPC. Setting the shared
// secret opts into split mode (docker compose does); without it the
// server embeds the full channel runtime, preserving the pre-split
// all-in-one deployment for existing configs.
func (cfg Config) SplitChannelRuntime() bool {
	return strings.TrimSpace(cfg.InternalRPC.SharedSecret) != ""
}

// ValidateServerRuntime validates the settings required by the Server process.
// It is intentionally separate from Load so migration commands do not require
// runtime-to-runtime credentials.
func (cfg Config) ValidateServerRuntime() error {
	if strings.TrimSpace(cfg.Server.Addr) == "" {
		return errors.New("server.addr is required")
	}
	if strings.TrimSpace(cfg.Server.RPCListenAddr) == "" {
		return errors.New("server.rpc_listen_addr is required")
	}
	return cfg.InternalRPC.Validate()
}

// ValidateChannelRuntime validates the settings required by the Channel process.
func (cfg Config) ValidateChannelRuntime() error {
	if strings.TrimSpace(cfg.Channel.Addr) == "" {
		return errors.New("channel.addr is required")
	}
	if strings.TrimSpace(cfg.Channel.RPCListenAddr) == "" {
		return errors.New("channel.rpc_listen_addr is required")
	}
	return cfg.InternalRPC.Validate()
}

func (cfg *Config) applyBridgeTLSEnvOverrides() {
	if value := strings.TrimSpace(os.Getenv("MEMOH_INSTANCE_ID")); value != "" {
		cfg.InstanceID = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_BRIDGE_TLS_MODE")); value != "" {
		cfg.BridgeTLS.Mode = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_BRIDGE_TLS_SERVER_DIR")); value != "" {
		cfg.BridgeTLS.ServerDir = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_BRIDGE_TLS_BRIDGE_DIR")); value != "" {
		cfg.BridgeTLS.BridgeDir = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_BRIDGE_TLS_SERVER_NAME")); value != "" {
		cfg.BridgeTLS.ServerName = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_WEBHOOK_TUNNEL_MODE")); value != "" {
		cfg.WebhookTunnel.Mode = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_WEBHOOK_PUBLIC_BASE_URL")); value != "" {
		cfg.WebhookTunnel.PublicBaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR")); value != "" {
		cfg.WebhookTunnel.ListenAddr = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_CLOUDFLARED_BIN")); value != "" {
		cfg.WebhookTunnel.CloudflaredPath = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_WEBHOOK_TUNNEL_TARGET_URL")); value != "" {
		cfg.WebhookTunnel.TargetURL = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_WEBHOOK_TUNNEL_METRICS_ADDR")); value != "" {
		cfg.WebhookTunnel.MetricsAddr = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_WEBHOOK_TUNNEL_METRICS_URL")); value != "" {
		cfg.WebhookTunnel.MetricsURL = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_INTERNAL_RPC_SHARED_SECRET")); value != "" {
		cfg.InternalRPC.SharedSecret = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_INTERNAL_RPC_SERVER_TARGET")); value != "" {
		cfg.InternalRPC.ServerTarget = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_INTERNAL_RPC_CHANNEL_TARGET")); value != "" {
		cfg.InternalRPC.ChannelTarget = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_CONNECT_IT_BASE_URL")); value != "" {
		cfg.ConnectIt.BaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("MEMOH_CONNECT_IT_API_TOKEN")); value != "" {
		cfg.ConnectIt.APIToken = value
	}
}

func (cfg *Config) resolvePaths() {
	cfg.Container.DataRoot = cfg.Container.DataRootPath()
	cfg.Container.RuntimeDir = cfg.Container.RuntimePath()
	cfg.Container.BridgePath = cfg.Container.BridgeBinaryPath()
	cfg.Workspace = cfg.Container.WorkspaceConfig
	if strings.TrimSpace(cfg.Registry.ProvidersDir) != "" {
		cfg.Registry.ProvidersDir = cfg.Registry.ProvidersPath()
	}
	if strings.TrimSpace(cfg.OAuthClients.ConfigPath) != "" {
		cfg.OAuthClients.ConfigPath = cfg.OAuthClients.Path()
	}
}

func containerHasWorkspaceFields(values map[string]any) bool {
	for _, key := range []string{
		"registry",
		"default_image",
		"image_pull_policy",
		"snapshotter",
		"data_root",
		"cni_bin_dir",
		"cni_conf_dir",
		"bridge_path",
		"runtime_dir",
	} {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}
