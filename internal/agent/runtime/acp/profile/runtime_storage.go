package profile

import (
	"fmt"
	"path"
	"strings"
)

// RuntimeStoragePolicy is the persistent/runtime boundary for an ACP agent:
// the launcher-owned environment plus the setup modes that may start a
// process. All durable agent state lives directly under HOME=/data; the
// process-owned runtime directory holds only ephemeral state and is removed
// when the process exits.
//
// This policy is deliberately internal to the server and is not exposed by
// PublicProfile. Adding an ACP agent without a complete policy is a developer
// error: Register validates the contract before the profile becomes usable.
type RuntimeStoragePolicy struct {
	AgentEnv []RuntimeEnvBinding
	Modes    map[string]RuntimeStorageMode
}

// RuntimeEnvBinding declares one environment variable owned by the runtime
// launcher. Exactly one of RuntimePath and Value must be set. RuntimePath is
// joined beneath the process-owned runtime directory; Value is a fixed literal
// such as /data, a container-local shared cache, or an agent mode selector.
type RuntimeEnvBinding struct {
	Name        string
	RuntimePath string
	Value       string
}

// RuntimeStorageMode marks a setup mode as allowed to start a process. It
// carries no per-mode staging configuration: agents own their durable state
// under /data directly.
type RuntimeStorageMode struct{}

func genericACPRuntimeStorage() RuntimeStoragePolicy {
	return RuntimeStoragePolicy{
		AgentEnv: stateEnv(),
		Modes: map[string]RuntimeStorageMode{
			setupModeAPIKey: {},
		},
	}
}

func stateEnv(stateNames ...string) []RuntimeEnvBinding {
	env := []RuntimeEnvBinding{
		{Name: "HOME", Value: "/data"},
		{Name: "TMPDIR", RuntimePath: "tmp"},
		{Name: "NPM_CONFIG_CACHE", Value: "/tmp/memoh-acp-cache/npm"},
	}
	for _, name := range stateNames {
		env = append(env, RuntimeEnvBinding{Name: name, RuntimePath: "state"})
	}
	return env
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

	for _, setupMode := range p.SetupModes {
		mode := NormalizeAgentID(setupMode)
		if _, ok := policy.Modes[mode]; !ok {
			return fmt.Errorf("profile %q has no runtime storage policy for setup mode %q", p.ID, mode)
		}
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
