package workspace

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	WorkspaceResourceCPUMillicoresLabelKey = "memoh.workspace.resource.cpu_millicores"
	WorkspaceResourceMemoryBytesLabelKey   = "memoh.workspace.resource.memory_bytes"

	ResourceLimitStatusApplied         = "applied"
	ResourceLimitStatusNotCreated      = "not_created"
	ResourceLimitStatusPendingRecreate = "pending_recreate"
	ResourceLimitStatusUnsupported     = "unsupported"
)

type ResourceLimitCapability struct {
	HardLimitSupported bool
	SoftLimitSupported bool
}

type ResourceLimitCapabilities struct {
	CPU     ResourceLimitCapability
	Memory  ResourceLimitCapability
	Storage ResourceLimitCapability
}

type ResourceLimitObserved struct {
	CPUUsagePercent      float64
	MemoryUsageBytes     uint64
	MemoryLimitBytes     uint64
	StorageUsedBytes     uint64
	StorageOverSoftLimit bool
}

type ResourceLimitsResult struct {
	Desired          container.ResourceLimits
	Applied          container.ResourceLimits
	Capabilities     ResourceLimitCapabilities
	Observed         ResourceLimitObserved
	Status           string
	RequiresRecreate bool
	WorkspaceBackend string
	RuntimeBackend   string
}

func validateResourceLimits(limits container.ResourceLimits) error {
	if limits.CPUMillicores < 0 {
		return errors.New("cpu_millicores must be non-negative")
	}
	if limits.MemoryBytes < 0 {
		return errors.New("memory_bytes must be non-negative")
	}
	if limits.StorageBytes < 0 {
		return errors.New("storage_bytes must be non-negative")
	}
	return nil
}

func workspaceResourceLimitsFromRow(row dbsqlc.BotWorkspaceResourceLimit) container.ResourceLimits {
	return container.ResourceLimits{
		CPUMillicores: row.CpuMillicores,
		MemoryBytes:   row.MemoryBytes,
		StorageBytes:  row.StorageBytes,
	}
}

func (m *Manager) desiredResourceLimits(ctx context.Context, botID string) (container.ResourceLimits, error) {
	if m.queries == nil {
		return container.ResourceLimits{}, nil
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return container.ResourceLimits{}, err
	}
	row, err := m.queries.GetBotWorkspaceResourceLimits(ctx, pgBotID)
	if err == nil {
		return workspaceResourceLimitsFromRow(row), nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return container.ResourceLimits{}, nil
	}
	return container.ResourceLimits{}, err
}

func (m *Manager) SetResourceLimits(ctx context.Context, botID string, limits container.ResourceLimits) (*ResourceLimitsResult, error) {
	if err := validateBotID(botID); err != nil {
		return nil, err
	}
	if err := validateResourceLimits(limits); err != nil {
		return nil, err
	}
	if m.queries == nil {
		return nil, container.ErrNotSupported
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	if _, err := m.queries.UpsertBotWorkspaceResourceLimits(ctx, dbsqlc.UpsertBotWorkspaceResourceLimitsParams{
		BotID:         pgBotID,
		CpuMillicores: limits.CPUMillicores,
		MemoryBytes:   limits.MemoryBytes,
		StorageBytes:  limits.StorageBytes,
	}); err != nil {
		return nil, err
	}
	return m.GetResourceLimits(ctx, botID)
}

func (m *Manager) GetResourceLimits(ctx context.Context, botID string) (*ResourceLimitsResult, error) {
	if err := validateBotID(botID); err != nil {
		return nil, err
	}
	desired, err := m.desiredResourceLimits(ctx, botID)
	if err != nil {
		return nil, err
	}

	workspaceBackend := bridge.WorkspaceBackendContainer

	result := &ResourceLimitsResult{
		Desired:          desired,
		WorkspaceBackend: workspaceBackend,
		Status:           ResourceLimitStatusNotCreated,
	}

	containerID, err := m.ContainerID(ctx, botID)
	if err != nil {
		if errors.Is(err, ErrContainerNotFound) {
			result.Capabilities = ResourceLimitCapabilitiesFor(workspaceBackend, "")
			return result, nil
		}
		return nil, err
	}

	info, err := m.service.GetContainer(ctx, containerID)
	if err != nil {
		if container.IsNotFound(err) {
			result.Capabilities = ResourceLimitCapabilitiesFor(workspaceBackend, "")
			return result, nil
		}
		return nil, err
	}

	result.RuntimeBackend = info.Runtime.Name
	result.Capabilities = ResourceLimitCapabilitiesFor(workspaceBackend, info.Runtime.Name)
	result.Applied = resourceLimitsFromLabels(info.Labels)

	if result.Capabilities.CPU.HardLimitSupported ||
		result.Capabilities.Memory.HardLimitSupported ||
		result.Capabilities.Storage.HardLimitSupported {
		result.RequiresRecreate = hardLimitDiffers(desired, result.Applied, result.Capabilities)
	}
	switch {
	case unsupportedHardLimitsRequested(desired, result.Capabilities):
		result.Status = ResourceLimitStatusUnsupported
	case result.RequiresRecreate:
		result.Status = ResourceLimitStatusPendingRecreate
	default:
		result.Status = ResourceLimitStatusApplied
	}

	if metrics, err := m.GetContainerMetrics(ctx, botID); err == nil && metrics != nil {
		if metrics.CPU != nil {
			result.Observed.CPUUsagePercent = metrics.CPU.UsagePercent
		}
		if metrics.Memory != nil {
			result.Observed.MemoryUsageBytes = metrics.Memory.UsageBytes
			result.Observed.MemoryLimitBytes = metrics.Memory.LimitBytes
		}
		if metrics.Storage != nil {
			result.Observed.StorageUsedBytes = metrics.Storage.UsedBytes
			result.Observed.StorageOverSoftLimit = desired.StorageBytes > 0 && metrics.Storage.UsedBytes > uint64(desired.StorageBytes)
		}
	}
	return result, nil
}

func ResourceLimitCapabilitiesFor(workspaceBackend, runtimeBackend string) ResourceLimitCapabilities {
	workspaceBackend = strings.ToLower(strings.TrimSpace(workspaceBackend))
	runtimeBackend = strings.ToLower(strings.TrimSpace(runtimeBackend))

	caps := ResourceLimitCapabilities{
		Storage: ResourceLimitCapability{SoftLimitSupported: true},
	}
	switch {
	case strings.Contains(runtimeBackend, "apple"):
		return caps
	case workspaceBackend == "vm" || runtimeBackend == "vm":
		caps.CPU.HardLimitSupported = true
		caps.Memory.HardLimitSupported = true
		caps.Storage.HardLimitSupported = true
		return caps
	default:
		caps.CPU.HardLimitSupported = true
		caps.Memory.HardLimitSupported = true
		return caps
	}
}

func resourceLimitLabels(limits container.ResourceLimits) map[string]string {
	return map[string]string{
		WorkspaceResourceCPUMillicoresLabelKey: strconv.FormatInt(limits.CPUMillicores, 10),
		WorkspaceResourceMemoryBytesLabelKey:   strconv.FormatInt(limits.MemoryBytes, 10),
	}
}

func resourceLimitsFromLabels(labels map[string]string) container.ResourceLimits {
	if len(labels) == 0 {
		return container.ResourceLimits{}
	}
	return container.ResourceLimits{
		CPUMillicores: parseResourceLimitLabel(labels[WorkspaceResourceCPUMillicoresLabelKey]),
		MemoryBytes:   parseResourceLimitLabel(labels[WorkspaceResourceMemoryBytesLabelKey]),
	}
}

func parseResourceLimitLabel(value string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func hardLimitDiffers(desired, applied container.ResourceLimits, caps ResourceLimitCapabilities) bool {
	if caps.CPU.HardLimitSupported && desired.CPUMillicores != applied.CPUMillicores {
		return true
	}
	if caps.Memory.HardLimitSupported && desired.MemoryBytes != applied.MemoryBytes {
		return true
	}
	if caps.Storage.HardLimitSupported && desired.StorageBytes != applied.StorageBytes {
		return true
	}
	return false
}

func unsupportedHardLimitsRequested(limits container.ResourceLimits, caps ResourceLimitCapabilities) bool {
	if limits.CPUMillicores > 0 && !caps.CPU.HardLimitSupported {
		return true
	}
	if limits.MemoryBytes > 0 && !caps.Memory.HardLimitSupported {
		return true
	}
	if limits.StorageBytes > 0 && !caps.Storage.HardLimitSupported && !caps.Storage.SoftLimitSupported {
		return true
	}
	return false
}

func (m *Manager) resourceLimitsForCreate(ctx context.Context, botID string) (container.ResourceLimits, error) {
	limits, err := m.desiredResourceLimits(ctx, botID)
	if err != nil {
		return container.ResourceLimits{}, err
	}
	if err := validateResourceLimits(limits); err != nil {
		return container.ResourceLimits{}, fmt.Errorf("invalid saved resource limits: %w", err)
	}
	return limits, nil
}
