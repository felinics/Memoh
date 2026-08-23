package profile

import (
	"fmt"
	"path"
	"strings"
)

// RuntimeStoragePolicy is the complete persistent/runtime boundary for an ACP
// agent. Paths in Artifacts are relative to /data; RuntimePath values are
// relative to the process-owned runtime directory.
//
// This policy is deliberately internal to the server and is not exposed by
// PublicProfile. Adding an ACP agent without a complete policy is a developer
// error: Register validates the contract before the profile becomes usable.
type RuntimeStoragePolicy struct {
	AgentEnv []RuntimeEnvBinding
	Modes    map[string]RuntimeStorageMode
	// SessionRoots are process-local JSONL trees that may be restored from and
	// snapshotted to Memoh's database. They are relative to the UUID-owned
	// runtime directory and are never staged from or synchronized to /data.
	SessionRoots []string
	// SessionLocator identifies the audited on-disk transcript contract. The
	// client uses this profile-owned value to select only files belonging to the
	// requested ACP session and to validate the session ID stored in the primary
	// transcript.
	SessionLocator RuntimeSessionLocator
}

// RuntimeSessionLocator names an agent-specific, pinned JSONL layout. It is an
// internal launcher contract rather than part of the public ACP profile API.
type RuntimeSessionLocator string

const (
	RuntimeSessionLocatorNone          RuntimeSessionLocator = ""
	RuntimeSessionLocatorCodexRollout  RuntimeSessionLocator = "codex_rollout"
	RuntimeSessionLocatorClaudeProject RuntimeSessionLocator = "claude_project"
)

// RuntimeEnvBinding declares one environment variable owned by the runtime
// launcher. Exactly one of RuntimePath and Value must be set. RuntimePath is
// joined beneath the process-owned runtime directory; Value is a fixed literal
// such as /data, a container-local shared cache, or an agent mode selector.
type RuntimeEnvBinding struct {
	Name        string
	RuntimePath string
	Value       string
}

type RuntimeStorageMode struct {
	Artifacts []RuntimeArtifact
	Generated []RuntimeGeneratedFile
}

type RuntimeArtifactKind string

const (
	RuntimeArtifactFile RuntimeArtifactKind = "file"
	RuntimeArtifactTree RuntimeArtifactKind = "tree"
)

type RuntimeArtifactFilter string

const (
	RuntimeArtifactFilterNone   RuntimeArtifactFilter = ""
	RuntimeArtifactFilterDotEnv RuntimeArtifactFilter = "dotenv"
)

type RuntimeSyncStrategy string

const (
	// RuntimeSyncNone stages a durable file into the process runtime but never
	// copies agent-side mutations back to /data.
	RuntimeSyncNone RuntimeSyncStrategy = ""
	// RuntimeSyncCompareAndSwap writes a changed runtime copy only while the
	// durable copy still matches the last observed version.
	RuntimeSyncCompareAndSwap RuntimeSyncStrategy = "compare_and_swap"
	// RuntimeSyncCodexAuth compares auth.json last_refresh timestamps before
	// every write. This makes rolling OAuth refresh tokens monotonic across
	// concurrent ACP processes and the OAuth device-flow handler.
	RuntimeSyncCodexAuth RuntimeSyncStrategy = "codex_auth"
)

type RuntimeArtifact struct {
	PersistentPath string
	RuntimePath    string
	Kind           RuntimeArtifactKind
	Filter         RuntimeArtifactFilter
	Sync           RuntimeSyncStrategy
}

type RuntimeGeneratedFile struct {
	RuntimePath string
	Content     string
}

const claudeManagedSettings = `{
  "permissions": {
    "ask": [
      "Bash"
    ]
  }
}
`

func codexRuntimeStorage() RuntimeStoragePolicy {
	return RuntimeStoragePolicy{
		AgentEnv:       stateEnv("CODEX_HOME", "CODEX_SQLITE_HOME"),
		SessionRoots:   []string{"state/sessions"},
		SessionLocator: RuntimeSessionLocatorCodexRollout,
		Modes: map[string]RuntimeStorageMode{
			setupModeAPIKey: {
				Artifacts: []RuntimeArtifact{
					fileArtifact(".codex/config.toml", "state/config.toml", RuntimeSyncNone),
					fileArtifact(".codex/auth.json", "state/auth.json", RuntimeSyncNone),
				},
			},
			setupModeOAuth: {
				Artifacts: []RuntimeArtifact{
					fileArtifact(".codex/config.toml", "state/config.toml", RuntimeSyncNone),
					fileArtifact(".codex/auth.json", "state/auth.json", RuntimeSyncCodexAuth),
				},
			},
			setupModeSelf: {
				Artifacts: []RuntimeArtifact{
					fileArtifact(".codex/config.toml", "state/config.toml", RuntimeSyncCompareAndSwap),
					fileArtifact(".codex/auth.json", "state/auth.json", RuntimeSyncCodexAuth),
					fileArtifact(".codex/AGENTS.md", "state/AGENTS.md", RuntimeSyncCompareAndSwap),
					treeArtifact(".codex/agents", "state/agents", RuntimeSyncCompareAndSwap),
					treeArtifact(".codex/prompts", "state/prompts", RuntimeSyncCompareAndSwap),
					treeArtifact(".codex/rules", "state/rules", RuntimeSyncCompareAndSwap),
					treeArtifact(".codex/skills", "state/skills", RuntimeSyncCompareAndSwap),
				},
			},
		},
	}
}

func claudeCodeRuntimeStorage() RuntimeStoragePolicy {
	managed := RuntimeStorageMode{
		Generated: []RuntimeGeneratedFile{{
			RuntimePath: "state/settings.json",
			Content:     claudeManagedSettings,
		}},
	}
	return RuntimeStoragePolicy{
		AgentEnv: append(stateEnv("CLAUDE_CONFIG_DIR"), RuntimeEnvBinding{
			Name:  "CLAUDE_CODE_EAGER_FLUSH",
			Value: "1",
		}),
		SessionRoots:   []string{"state/projects"},
		SessionLocator: RuntimeSessionLocatorClaudeProject,
		Modes: map[string]RuntimeStorageMode{
			setupModeAPIKey: managed,
			setupModeOAuth:  managed,
			setupModeSelf: {
				Artifacts: []RuntimeArtifact{
					fileArtifact(".claude/settings.json", "state/settings.json", RuntimeSyncCompareAndSwap),
					fileArtifact(".claude/settings.local.json", "state/settings.local.json", RuntimeSyncCompareAndSwap),
					fileArtifact(".claude/.credentials.json", "state/.credentials.json", RuntimeSyncCompareAndSwap),
					// Claude 0.64.2 / Agent SDK 0.3.220 relocates this file under
					// CLAUDE_CONFIG_DIR. It mixes user choices with volatile UI and
					// session state, so keep it available to the process but never
					// write the mixed file back to /data.
					fileArtifact(".claude.json", "state/.claude.json", RuntimeSyncNone),
					fileArtifact(".claude/CLAUDE.md", "state/CLAUDE.md", RuntimeSyncCompareAndSwap),
					fileArtifact(".claude/keybindings.json", "state/keybindings.json", RuntimeSyncCompareAndSwap),
					fileArtifact(".claude/plugins/installed_plugins.json", "state/plugins/installed_plugins.json", RuntimeSyncCompareAndSwap),
					fileArtifact(".claude/plugins/known_marketplaces.json", "state/plugins/known_marketplaces.json", RuntimeSyncCompareAndSwap),
					treeArtifact(".claude/agents", "state/agents", RuntimeSyncCompareAndSwap),
					treeArtifact(".claude/commands", "state/commands", RuntimeSyncCompareAndSwap),
					treeArtifact(".claude/output-styles", "state/output-styles", RuntimeSyncCompareAndSwap),
					treeArtifact(".claude/rules", "state/rules", RuntimeSyncCompareAndSwap),
					treeArtifact(".claude/skills", "state/skills", RuntimeSyncCompareAndSwap),
				},
			},
		},
	}
}

func hermesRuntimeStorage() RuntimeStoragePolicy {
	return RuntimeStoragePolicy{
		AgentEnv: append(hermesStateEnv(),
			RuntimeEnvBinding{Name: "HERMES_REAL_HOME", Value: "/data"},
			RuntimeEnvBinding{Name: "TERMINAL_HOME_MODE", Value: "real"},
			RuntimeEnvBinding{Name: "MEMOH_HERMES_RUNTIME_DIR", Value: "/tmp/memoh-acp-cache/hermes"},
			RuntimeEnvBinding{Name: "UV_CACHE_DIR", Value: "/tmp/memoh-acp-cache/hermes/uv-cache"},
			RuntimeEnvBinding{Name: "UV_TOOL_DIR", Value: "/tmp/memoh-acp-cache/hermes/uv-tools"},
			RuntimeEnvBinding{Name: "UV_PYTHON_INSTALL_DIR", Value: "/tmp/memoh-acp-cache/hermes/python"},
		),
		Modes: map[string]RuntimeStorageMode{
			setupModeAPIKey: {
				Artifacts: []RuntimeArtifact{
					fileArtifact(".memoh-hermes/config.yaml", "state/config.yaml", RuntimeSyncNone),
					fileArtifact(".memoh-hermes/.env", "state/.env", RuntimeSyncNone),
				},
			},
			setupModeSelf: {
				Artifacts: []RuntimeArtifact{
					fileArtifact(".hermes/config.yaml", "state/config.yaml", RuntimeSyncCompareAndSwap),
					// Runtime ownership variables are filtered while staging. Keep the
					// source file read-only so filtering cannot reorder or erase user
					// shell configuration on writeback.
					dotEnvArtifact(".hermes/.env", "state/.env", RuntimeSyncNone),
					fileArtifact(".hermes/auth.json", "state/auth.json", RuntimeSyncCompareAndSwap),
					fileArtifact(".hermes/.anthropic_oauth.json", "state/.anthropic_oauth.json", RuntimeSyncCompareAndSwap),
					fileArtifact(".hermes/SOUL.md", "state/SOUL.md", RuntimeSyncCompareAndSwap),
					treeArtifact(".hermes/auth", "state/auth", RuntimeSyncCompareAndSwap),
					treeArtifact(".hermes/hooks", "state/hooks", RuntimeSyncCompareAndSwap),
					treeArtifact(".hermes/mcp-tokens", "state/mcp-tokens", RuntimeSyncCompareAndSwap),
					treeArtifact(".hermes/skills", "state/skills", RuntimeSyncCompareAndSwap),
				},
			},
		},
	}
}

func hermesStateEnv() []RuntimeEnvBinding {
	env := stateEnv("HERMES_HOME")
	for i := range env {
		if env[i].Name == "HOME" {
			env[i] = RuntimeEnvBinding{Name: "HOME", RuntimePath: "home"}
			break
		}
	}
	return env
}

func stateEnv(stateNames ...string) []RuntimeEnvBinding {
	env := []RuntimeEnvBinding{
		{Name: "HOME", Value: "/data"},
		{Name: "TMPDIR", RuntimePath: "tmp"},
		{Name: "NPM_CONFIG_CACHE", Value: "/tmp/memoh-acp-cache/npm"},
	}
	for _, name := range stateNames {
		runtimePath := "state"
		if name == "CODEX_SQLITE_HOME" {
			runtimePath = "sqlite"
		}
		env = append(env, RuntimeEnvBinding{Name: name, RuntimePath: runtimePath})
	}
	return env
}

func fileArtifact(persistentPath, runtimePath string, sync RuntimeSyncStrategy) RuntimeArtifact {
	return RuntimeArtifact{
		PersistentPath: persistentPath,
		RuntimePath:    runtimePath,
		Kind:           RuntimeArtifactFile,
		Sync:           sync,
	}
}

func treeArtifact(persistentPath, runtimePath string, sync RuntimeSyncStrategy) RuntimeArtifact {
	return RuntimeArtifact{
		PersistentPath: persistentPath,
		RuntimePath:    runtimePath,
		Kind:           RuntimeArtifactTree,
		Sync:           sync,
	}
}

func dotEnvArtifact(persistentPath, runtimePath string, sync RuntimeSyncStrategy) RuntimeArtifact {
	artifact := fileArtifact(persistentPath, runtimePath, sync)
	artifact.Filter = RuntimeArtifactFilterDotEnv
	return artifact
}

func validateRuntimeStorage(p Profile) error {
	policy := p.RuntimeStorage
	if len(policy.AgentEnv) == 0 {
		return fmt.Errorf("profile %q has no runtime environment policy", p.ID)
	}
	seenEnv := make(map[string]struct{}, len(policy.AgentEnv))
	for _, binding := range policy.AgentEnv {
		name := strings.TrimSpace(binding.Name)
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return fmt.Errorf("profile %q has invalid runtime environment name %q", p.ID, binding.Name)
		}
		if _, duplicate := seenEnv[name]; duplicate {
			return fmt.Errorf("profile %q declares runtime environment %q more than once", p.ID, name)
		}
		seenEnv[name] = struct{}{}
		hasRuntimePath := strings.TrimSpace(binding.RuntimePath) != ""
		hasValue := strings.TrimSpace(binding.Value) != ""
		if hasRuntimePath == hasValue {
			return fmt.Errorf("profile %q runtime environment %q must set exactly one path source", p.ID, name)
		}
		if hasRuntimePath && !safeRelativeRuntimePath(binding.RuntimePath) {
			return fmt.Errorf("profile %q runtime environment %q escapes the runtime root", p.ID, name)
		}
		if hasValue && strings.ContainsAny(binding.Value, "\x00\r\n") {
			return fmt.Errorf("profile %q runtime environment %q has an invalid fixed value", p.ID, name)
		}
	}

	switch policy.SessionLocator {
	case RuntimeSessionLocatorNone:
		if len(policy.SessionRoots) != 0 {
			return fmt.Errorf("profile %q declares session roots without a session locator", p.ID)
		}
	case RuntimeSessionLocatorCodexRollout, RuntimeSessionLocatorClaudeProject:
		if len(policy.SessionRoots) == 0 {
			return fmt.Errorf("profile %q declares session locator %q without session roots", p.ID, policy.SessionLocator)
		}
	default:
		return fmt.Errorf("profile %q has invalid session locator %q", p.ID, policy.SessionLocator)
	}

	for _, setupMode := range p.SetupModes {
		mode := NormalizeAgentID(setupMode)
		storageMode, ok := policy.Modes[mode]
		if !ok {
			return fmt.Errorf("profile %q has no runtime storage policy for setup mode %q", p.ID, mode)
		}
		if err := validateRuntimeStorageMode(p.ID, mode, storageMode); err != nil {
			return err
		}
		if err := validateSessionRoots(p.ID, mode, policy.SessionRoots, storageMode); err != nil {
			return err
		}
	}
	return nil
}

func validateSessionRoots(profileID, mode string, roots []string, storage RuntimeStorageMode) error {
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if !safeRelativeRuntimePath(root) {
			return fmt.Errorf("profile %q has unsafe session root %q", profileID, root)
		}
		for existing := range seen {
			if runtimePathsOverlap(root, existing) {
				return fmt.Errorf("profile %q session root %q overlaps session root %q", profileID, root, existing)
			}
		}
		seen[root] = struct{}{}
		for _, artifact := range storage.Artifacts {
			if runtimePathsOverlap(root, artifact.RuntimePath) {
				return fmt.Errorf("profile %q mode %q session root %q overlaps runtime artifact %q", profileID, mode, root, artifact.RuntimePath)
			}
		}
		for _, generated := range storage.Generated {
			if runtimePathsOverlap(root, generated.RuntimePath) {
				return fmt.Errorf("profile %q mode %q session root %q overlaps generated file %q", profileID, mode, root, generated.RuntimePath)
			}
		}
	}
	return nil
}

func runtimePathsOverlap(first, second string) bool {
	first = path.Clean(first)
	second = path.Clean(second)
	return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

func validateRuntimeStorageMode(profileID, mode string, storage RuntimeStorageMode) error {
	seenPersistent := make(map[string]struct{}, len(storage.Artifacts))
	seenRuntime := make(map[string]struct{}, len(storage.Artifacts)+len(storage.Generated))
	for _, artifact := range storage.Artifacts {
		if !safeRelativeRuntimePath(artifact.PersistentPath) {
			return fmt.Errorf("profile %q mode %q has unsafe persistent path %q", profileID, mode, artifact.PersistentPath)
		}
		if !safeRelativeRuntimePath(artifact.RuntimePath) {
			return fmt.Errorf("profile %q mode %q has unsafe runtime path %q", profileID, mode, artifact.RuntimePath)
		}
		if artifact.Kind != RuntimeArtifactFile && artifact.Kind != RuntimeArtifactTree {
			return fmt.Errorf("profile %q mode %q has invalid artifact kind %q", profileID, mode, artifact.Kind)
		}
		if artifact.Filter != RuntimeArtifactFilterNone && artifact.Filter != RuntimeArtifactFilterDotEnv {
			return fmt.Errorf("profile %q mode %q has invalid artifact filter %q", profileID, mode, artifact.Filter)
		}
		if artifact.Kind == RuntimeArtifactTree && artifact.Filter != RuntimeArtifactFilterNone {
			return fmt.Errorf("profile %q mode %q applies a file filter to tree %q", profileID, mode, artifact.PersistentPath)
		}
		if artifact.Filter != RuntimeArtifactFilterNone && artifact.Sync != RuntimeSyncNone {
			return fmt.Errorf("profile %q mode %q writes filtered artifact %q back to durable storage", profileID, mode, artifact.PersistentPath)
		}
		switch artifact.Sync {
		case RuntimeSyncNone, RuntimeSyncCompareAndSwap, RuntimeSyncCodexAuth:
		default:
			return fmt.Errorf("profile %q mode %q has invalid sync strategy %q", profileID, mode, artifact.Sync)
		}
		if _, duplicate := seenPersistent[artifact.PersistentPath]; duplicate {
			return fmt.Errorf("profile %q mode %q repeats persistent path %q", profileID, mode, artifact.PersistentPath)
		}
		if _, duplicate := seenRuntime[artifact.RuntimePath]; duplicate {
			return fmt.Errorf("profile %q mode %q repeats runtime path %q", profileID, mode, artifact.RuntimePath)
		}
		seenPersistent[artifact.PersistentPath] = struct{}{}
		seenRuntime[artifact.RuntimePath] = struct{}{}
	}
	for _, generated := range storage.Generated {
		if !safeRelativeRuntimePath(generated.RuntimePath) {
			return fmt.Errorf("profile %q mode %q has unsafe generated path %q", profileID, mode, generated.RuntimePath)
		}
		if _, duplicate := seenRuntime[generated.RuntimePath]; duplicate {
			return fmt.Errorf("profile %q mode %q repeats runtime path %q", profileID, mode, generated.RuntimePath)
		}
		seenRuntime[generated.RuntimePath] = struct{}{}
	}
	return nil
}

func safeRelativeRuntimePath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../") && cleaned == value
}
