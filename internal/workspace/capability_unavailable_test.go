package workspace

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestNativeTargetFailureKeepsIdentityOnlyForUnavailableRuntime(t *testing.T) {
	target := ResolvedWorkspaceTarget{TargetID: WorkspaceTargetNative, Kind: WorkspaceTargetNative}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"container missing": {ErrContainerNotFound, true},
		"bridge down":       {fmt.Errorf("mcp client: %w", bridge.ErrUnavailable), true},
		"provider timeout":  {fmt.Errorf("workspace info: %w", status.Error(codes.DeadlineExceeded, "sandbox provider timed out")), true},
		"provider down":     {fmt.Errorf("workspace client: %w", status.Error(codes.Unavailable, "runtime unreachable")), true},
		"runtime down":      {fmt.Errorf("workspace client: %w", ctr.ErrUnavailable), true},
		"caller deadline":   {fmt.Errorf("load bot container: %w", context.DeadlineExceeded), false},
		"runtime refused":   {fmt.Errorf("workspace client: %w", ctr.ErrConflict), false},
		"other failure":     {errors.New("permission denied"), false},
	} {
		got, err := nativeTargetFailure(target, tc.err)
		if unavailable := errors.Is(err, ErrCapabilityUnavailable); unavailable != tc.want {
			t.Errorf("%s: ErrCapabilityUnavailable = %v, want %v", name, unavailable, tc.want)
		}
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: cause lost: %v", name, err)
		}
		if (got.TargetID != "") != tc.want {
			t.Errorf("%s: target = %+v", name, got)
		}
	}
}
