package workspace

import (
	"context"
	"errors"
	"path"
	"strings"

	runtimedomain "github.com/memohai/memoh/domains/runtime"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/config"
)

const (
	workspaceMetadataKey                    = "workspace"
	workspaceImageMetadataKey               = "image"
	workspaceGPUMetadataKey                 = "gpu"
	workspaceGPUDevicesKey                  = "devices"
	workspaceSkillDiscoveryRootsMetadataKey = "skill_discovery_roots"
)

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

func workspaceGPUFromMetadata(metadata map[string]any) (runtimeworkspace.WorkspaceGPUConfig, bool) {
	section := workspaceSection(metadata)
	raw, ok := section[workspaceGPUMetadataKey]
	if !ok {
		return runtimeworkspace.WorkspaceGPUConfig{}, false
	}

	gpuSection, ok := raw.(map[string]any)
	if !ok {
		return runtimeworkspace.WorkspaceGPUConfig{}, true
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

	return runtimeworkspace.WorkspaceGPUConfig{Devices: normalizeWorkspaceGPUDevices(devices)}, true
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

func withWorkspaceGPUPreference(metadata map[string]any, gpu runtimeworkspace.WorkspaceGPUConfig) map[string]any {
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

// runtimeworkspace.DecodeWorkspacePreferences converts the API-owned bot metadata payload into
// Runtime's narrow projection. It exists for the transitional legacy SQLC
// adapter; new API owner adapters should return runtimeworkspace.WorkspacePreferences directly.
// runtimeworkspace.PatchWorkspaceImagePreference and runtimeworkspace.PatchWorkspaceGPUPreference preserve all
// unrelated metadata. They are only used by the transitional legacy adapter.
//
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

func (m *Manager) botWorkspaceImagePreference(ctx context.Context, botID string) (string, error) {
	if m.profiles == nil {
		return "", nil
	}
	preferences, found, err := m.profiles.LookupWorkspacePreferences(ctx, botID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	return strings.TrimSpace(preferences.Image), nil
}

func (m *Manager) RememberWorkspaceImage(ctx context.Context, botID, image string) error {
	if m.profiles == nil {
		return nil
	}
	return m.profiles.SetWorkspaceImagePreference(ctx, botID, config.NormalizeImageRef(image))
}

func (m *Manager) ClearWorkspaceImagePreference(ctx context.Context, botID string) error {
	if m.profiles == nil {
		return nil
	}
	return m.profiles.ClearWorkspaceImagePreference(ctx, botID)
}

func (m *Manager) botWorkspaceGPUPreference(ctx context.Context, botID string) (runtimeworkspace.WorkspaceGPUConfig, bool, error) {
	if m.profiles == nil {
		return runtimeworkspace.WorkspaceGPUConfig{}, false, nil
	}
	preferences, found, err := m.profiles.LookupWorkspacePreferences(ctx, botID)
	if err != nil {
		return runtimeworkspace.WorkspaceGPUConfig{}, false, err
	}
	if !found {
		return runtimeworkspace.WorkspaceGPUConfig{}, false, nil
	}
	return preferences.GPU, preferences.HasGPU, nil
}

func (m *Manager) botWorkspaceSkillDiscoveryRootsPreference(ctx context.Context, botID string) ([]string, bool, error) {
	if m.profiles == nil {
		return nil, false, nil
	}
	preferences, found, err := m.profiles.LookupWorkspacePreferences(ctx, botID)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	return append([]string(nil), preferences.SkillDiscoveryRoots...), preferences.HasSkillDiscoveryRoots, nil
}

func (m *Manager) RememberWorkspaceGPU(ctx context.Context, botID string, gpu runtimeworkspace.WorkspaceGPUConfig) error {
	if m.profiles == nil {
		return nil
	}
	gpu.Devices = normalizeWorkspaceGPUDevices(gpu.Devices)
	return m.profiles.SetWorkspaceGPUPreference(ctx, botID, gpu)
}

func (m *Manager) ClearWorkspaceGPUPreference(ctx context.Context, botID string) error {
	if m.profiles == nil {
		return nil
	}
	return m.profiles.ClearWorkspaceGPUPreference(ctx, botID)
}

func (m *Manager) ResolveWorkspaceImage(ctx context.Context, botID string) (string, error) {
	return m.resolveWorkspaceImage(ctx, botID)
}

func (m *Manager) ResolveWorkspaceGPU(ctx context.Context, botID string) (runtimeworkspace.WorkspaceGPUConfig, error) {
	return m.resolveWorkspaceGPU(ctx, botID)
}

func (m *Manager) ResolveWorkspaceSkillDiscoveryRoots(ctx context.Context, botID string) ([]string, error) {
	return m.resolveWorkspaceSkillDiscoveryRoots(ctx, botID)
}

func (m *Manager) resolveWorkspaceImage(ctx context.Context, botID string) (string, error) {
	if m.containers != nil {
		if validateBotID(botID) == nil {
			row, dbErr := m.containers.FindContainer(ctx, botID)
			if dbErr == nil && strings.TrimSpace(row.Image) != "" {
				return config.NormalizeImageRef(strings.TrimSpace(row.Image)), nil
			}
			if dbErr != nil && !errors.Is(dbErr, runtimeworkspace.ErrRecordNotFound) {
				return "", dbErr
			}
		}
	}

	preferredImage, err := m.botWorkspaceImagePreference(ctx, botID)
	if err != nil {
		return "", err
	}
	if preferredImage != "" {
		return config.NormalizeImageRef(preferredImage), nil
	}

	return m.imageRef(), nil
}

func (m *Manager) resolveWorkspaceGPU(ctx context.Context, botID string) (runtimeworkspace.WorkspaceGPUConfig, error) {
	preferredGPU, hasPreference, err := m.botWorkspaceGPUPreference(ctx, botID)
	if err != nil {
		return runtimeworkspace.WorkspaceGPUConfig{}, err
	}
	if hasPreference {
		preferredGPU.Devices = normalizeWorkspaceGPUDevices(preferredGPU.Devices)
		return preferredGPU, nil
	}

	return runtimeworkspace.WorkspaceGPUConfig{}, nil
}

func (m *Manager) resolveWorkspaceSkillDiscoveryRoots(ctx context.Context, botID string) ([]string, error) {
	roots, hasPreference, err := m.botWorkspaceSkillDiscoveryRootsPreference(ctx, botID)
	if err != nil {
		return nil, err
	}
	if !hasPreference {
		return nil, nil
	}
	return roots, nil
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
