package containerd

import (
	"context"
	"testing"

	"github.com/opencontainers/runtime-spec/specs-go"
)

func TestSpecOptsFromResourceLimitsSetsLinuxResources(t *testing.T) {
	t.Parallel()

	spec := specs.Spec{Linux: &specs.Linux{}}
	for _, opt := range specOptsFromResourceLimits(ResourceLimits{
		CPUMillicores: 500,
		MemoryBytes:   256 * 1024 * 1024,
	}) {
		if err := opt(context.Background(), nil, nil, &spec); err != nil {
			t.Fatalf("apply resource limit spec opt: %v", err)
		}
	}

	if spec.Linux.Resources == nil {
		t.Fatal("linux resources were not set")
	}
	if spec.Linux.Resources.CPU == nil {
		t.Fatal("cpu resources were not set")
	}
	if spec.Linux.Resources.CPU.Period == nil || *spec.Linux.Resources.CPU.Period != 100_000 {
		t.Fatalf("cpu period = %v, want 100000", spec.Linux.Resources.CPU.Period)
	}
	if spec.Linux.Resources.CPU.Quota == nil || *spec.Linux.Resources.CPU.Quota != 50_000 {
		t.Fatalf("cpu quota = %v, want 50000", spec.Linux.Resources.CPU.Quota)
	}
	if spec.Linux.Resources.Memory == nil {
		t.Fatal("memory resources were not set")
	}
	if spec.Linux.Resources.Memory.Limit == nil || *spec.Linux.Resources.Memory.Limit != 256*1024*1024 {
		t.Fatalf("memory limit = %v, want 268435456", spec.Linux.Resources.Memory.Limit)
	}
}

func TestSpecOptsFromResourceLimitsSkipsUnlimitedValues(t *testing.T) {
	t.Parallel()

	opts := specOptsFromResourceLimits(ResourceLimits{})
	if len(opts) != 0 {
		t.Fatalf("spec opts count = %d, want 0", len(opts))
	}
}
