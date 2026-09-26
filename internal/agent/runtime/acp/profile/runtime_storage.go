package profile

import (
	"fmt"
	"path"
	"strings"
)

// RuntimeStoragePolicy is the persistent/runtime boundary for an ACP agent:
// the launcher-owned environment. All durable agent state lives directly under
// HOME=/data; the process-owned runtime directory holds only ephemeral state
// and is removed when the process exits.
//
// This policy is deliberately internal to the server and is not exposed by
// PublicProfile. Adding an ACP agent without a complete policy is a developer
// error: Register validates the contract before the profile becomes usable.
type RuntimeStoragePolicy struct {
	AgentEnv []RuntimeEnvBinding
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

func genericACPRuntimeStorage() RuntimeStoragePolicy {
	return RuntimeStoragePolicy{
		AgentEnv: []RuntimeEnvBinding{
			{Name: "HOME", Value: "/data"},
			{Name: "TMPDIR", RuntimePath: "tmp"},
			{Name: "NPM_CONFIG_CACHE", Value: "/tmp/memoh-acp-cache/npm"},
		},
	}
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
