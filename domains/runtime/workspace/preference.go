package workspace

import (
	"encoding/json"
	"path"
	"strings"

	runtimedomain "github.com/memohai/memoh/domains/runtime"
)

const (
	workspaceMetadataKey                    = "workspace"
	workspaceImageMetadataKey               = "image"
	workspaceGPUMetadataKey                 = "gpu"
	workspaceGPUDevicesKey                  = "devices"
	workspaceSkillDiscoveryRootsMetadataKey = "skill_discovery_roots"
)

type WorkspaceGPUConfig struct {
	Devices []string `json:"devices,omitempty"`
}

func decodeBotMetadata(payload []byte) (map[string]any, error) {
	if len(payload) == 0 {
		return map[string]any{}, nil
	}
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
}

func workspaceSection(metadata map[string]any) map[string]any {
	raw, ok := metadata[workspaceMetadataKey]
	if !ok {
		return map[string]any{}
	}
	section, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return cloneAnyMap(section)
}

func workspaceImageFromMetadata(metadata map[string]any) string {
	section := workspaceSection(metadata)
	image, _ := section[workspaceImageMetadataKey].(string)
	return strings.TrimSpace(image)
}

func normalizeWorkspaceGPUDevices(devices []string) []string {
	if len(devices) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(devices))
	normalized := make([]string, 0, len(devices))
	for _, raw := range devices {
		device := strings.TrimSpace(raw)
		if device == "" {
			continue
		}
		if _, ok := seen[device]; ok {
			continue
		}
		seen[device] = struct{}{}
		normalized = append(normalized, device)
	}
	return normalized
}

func workspaceGPUFromMetadata(metadata map[string]any) (WorkspaceGPUConfig, bool) {
	section := workspaceSection(metadata)
	raw, ok := section[workspaceGPUMetadataKey]
	if !ok {
		return WorkspaceGPUConfig{}, false
	}

	gpuSection, ok := raw.(map[string]any)
	if !ok {
		return WorkspaceGPUConfig{}, true
	}

	var devices []string
	switch typed := gpuSection[workspaceGPUDevicesKey].(type) {
	case []string:
		devices = append(devices, typed...)
	case []any:
		for _, item := range typed {
			if device, ok := item.(string); ok {
				devices = append(devices, device)
			}
		}
	}

	return WorkspaceGPUConfig{Devices: normalizeWorkspaceGPUDevices(devices)}, true
}

func workspaceSkillDiscoveryRootsFromMetadata(metadata map[string]any) ([]string, bool) {
	section := workspaceSection(metadata)
	raw, ok := section[workspaceSkillDiscoveryRootsMetadataKey]
	if !ok {
		return nil, false
	}

	var roots []string
	switch typed := raw.(type) {
	case []string:
		roots = append(roots, typed...)
	case []any:
		for _, item := range typed {
			if root, ok := item.(string); ok {
				roots = append(roots, root)
			}
		}
	default:
		return []string{}, true
	}

	normalized := normalizeWorkspaceSkillDiscoveryRoots(roots)
	if normalized == nil {
		return []string{}, true
	}
	return normalized, true
}

func withWorkspaceImagePreference(metadata map[string]any, image string) map[string]any {
	next := cloneAnyMap(metadata)
	section := workspaceSection(next)
	section[workspaceImageMetadataKey] = strings.TrimSpace(image)
	next[workspaceMetadataKey] = section
	return next
}

func withoutWorkspaceImagePreference(metadata map[string]any) map[string]any {
	next := cloneAnyMap(metadata)
	section := workspaceSection(next)
	delete(section, workspaceImageMetadataKey)
	if len(section) == 0 {
		delete(next, workspaceMetadataKey)
		return next
	}
	next[workspaceMetadataKey] = section
	return next
}

func withWorkspaceGPUPreference(metadata map[string]any, gpu WorkspaceGPUConfig) map[string]any {
	next := cloneAnyMap(metadata)
	section := workspaceSection(next)
	section[workspaceGPUMetadataKey] = map[string]any{
		workspaceGPUDevicesKey: normalizeWorkspaceGPUDevices(gpu.Devices),
	}
	next[workspaceMetadataKey] = section
	return next
}

//nolint:unused // Kept for tests and upcoming metadata plumbing.
func withWorkspaceSkillDiscoveryRoots(metadata map[string]any, roots []string) map[string]any {
	next := cloneAnyMap(metadata)
	section := workspaceSection(next)
	normalized := normalizeWorkspaceSkillDiscoveryRoots(roots)
	if normalized == nil {
		normalized = []string{}
	}
	section[workspaceSkillDiscoveryRootsMetadataKey] = normalized
	next[workspaceMetadataKey] = section
	return next
}

func withoutWorkspaceGPUPreference(metadata map[string]any) map[string]any {
	next := cloneAnyMap(metadata)
	section := workspaceSection(next)
	delete(section, workspaceGPUMetadataKey)
	if len(section) == 0 {
		delete(next, workspaceMetadataKey)
		return next
	}
	next[workspaceMetadataKey] = section
	return next
}

// DecodeWorkspacePreferences converts the API-owned bot metadata payload into
// Runtime's narrow projection. It exists for the transitional legacy SQLC
// adapter; new API owner adapters should return WorkspacePreferences directly.
func DecodeWorkspacePreferences(payload []byte) (WorkspacePreferences, error) {
	metadata, err := decodeBotMetadata(payload)
	if err != nil {
		return WorkspacePreferences{}, err
	}
	gpu, hasGPU := workspaceGPUFromMetadata(metadata)
	roots, hasRoots := workspaceSkillDiscoveryRootsFromMetadata(metadata)
	return WorkspacePreferences{
		Image:                  workspaceImageFromMetadata(metadata),
		GPU:                    gpu,
		HasGPU:                 hasGPU,
		SkillDiscoveryRoots:    roots,
		HasSkillDiscoveryRoots: hasRoots,
	}, nil
}

// PatchWorkspaceImagePreference and PatchWorkspaceGPUPreference preserve all
// unrelated metadata. They are only used by the transitional legacy adapter.
func PatchWorkspaceImagePreference(payload []byte, image *string) ([]byte, error) {
	metadata, err := decodeBotMetadata(payload)
	if err != nil {
		return nil, err
	}
	if image == nil {
		metadata = withoutWorkspaceImagePreference(metadata)
	} else {
		metadata = withWorkspaceImagePreference(metadata, *image)
	}
	return json.Marshal(metadata)
}

func PatchWorkspaceGPUPreference(payload []byte, gpu *WorkspaceGPUConfig) ([]byte, error) {
	metadata, err := decodeBotMetadata(payload)
	if err != nil {
		return nil, err
	}
	if gpu == nil {
		metadata = withoutWorkspaceGPUPreference(metadata)
	} else {
		metadata = withWorkspaceGPUPreference(metadata, *gpu)
	}
	return json.Marshal(metadata)
}

//nolint:unused // Kept for tests and upcoming metadata plumbing.
func withoutWorkspaceSkillDiscoveryRoots(metadata map[string]any) map[string]any {
	next := cloneAnyMap(metadata)
	section := workspaceSection(next)
	delete(section, workspaceSkillDiscoveryRootsMetadataKey)
	if len(section) == 0 {
		delete(next, workspaceMetadataKey)
		return next
	}
	next[workspaceMetadataKey] = section
	return next
}

// SkillDiscoveryRootsFromMetadata returns configured skill discovery roots from
// API-owned bot metadata, or nil when the preference is absent.
func SkillDiscoveryRootsFromMetadata(metadata map[string]any) []string {
	roots, ok := workspaceSkillDiscoveryRootsFromMetadata(metadata)
	if !ok {
		return nil
	}
	return roots
}

func normalizeWorkspaceSkillDiscoveryRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}

	managedDir := path.Join(runtimedomain.DefaultDataMount, "skills")
	indexDir := path.Join(runtimedomain.DefaultDataMount, ".memoh", "skills")
	legacyDir := path.Join(runtimedomain.DefaultDataMount, ".skills")
	seen := make(map[string]struct{}, len(roots))
	normalized := make([]string, 0, len(roots))
	for _, raw := range roots {
		root := path.Clean(strings.TrimSpace(raw))
		if root == "" || !strings.HasPrefix(root, "/") {
			continue
		}
		if root == managedDir || root == indexDir || root == legacyDir {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		normalized = append(normalized, root)
	}
	return normalized
}
