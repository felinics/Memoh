package containerd

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
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

func TestMapContainerdErrMarksUnavailable(t *testing.T) {
	err := mapContainerdErr(fmt.Errorf("connect containerd: %w", errdefs.ErrUnavailable))
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, ErrRuntime) {
		t.Fatalf("mapContainerdErr(unavailable) = %v, want ErrUnavailable and ErrRuntime", err)
	}
	if fault := errs.FaultOf(err); fault != apperror.FaultDependency {
		t.Fatalf("mapContainerdErr(unavailable) fault = %s, want dependency", fault)
	}

	err = mapContainerdErr(errors.New("snapshot unpack failed"))
	if errors.Is(err, ErrUnavailable) || !errors.Is(err, ErrRuntime) {
		t.Fatalf("mapContainerdErr(other) = %v, want ErrRuntime only", err)
	}
	if fault := errs.FaultOf(err); fault != apperror.FaultServer {
		t.Fatalf("mapContainerdErr(other) fault = %s, want server", fault)
	}
}

func TestMapContainerdErrClassifiesImagePullFailures(t *testing.T) {
	missing := mapContainerdErr(fmt.Errorf("docker.io/library/nope:latest: %w", errdefs.ErrNotFound))
	if !errors.Is(missing, ErrNotFound) {
		t.Fatalf("mapContainerdErr(not found) = %v, want ErrNotFound", missing)
	}
	invalid := mapContainerdErr(fmt.Errorf("parse reference: %w", errdefs.ErrInvalidArgument))
	if !errors.Is(invalid, ErrInvalidArgument) {
		t.Fatalf("mapContainerdErr(invalid argument) = %v, want ErrInvalidArgument", invalid)
	}
}

// A registry refusing a pull is how a missing repository is reported, so it is
// a not-found image rather than a runtime failure.
func TestMapContainerdErrTreatsRefusedPullAsNotFound(t *testing.T) {
	refused := fmt.Errorf("pull access denied, repository does not exist or may require authorization: %w", docker.ErrInvalidAuthorization)
	err := mapContainerdErr(fmt.Errorf("resolve image: %w", refused))
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrRuntime) {
		t.Fatalf("mapContainerdErr(refused pull) = %v, want ErrNotFound only", err)
	}
}
