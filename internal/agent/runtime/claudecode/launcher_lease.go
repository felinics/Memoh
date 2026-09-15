package claudecode

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/workspace/payloadlease"
)

func (d *Driver) acquireLauncher(ctx context.Context, botID string) (external.Launcher, *payloadlease.Lease, error) {
	if d.launchers == nil {
		launcher, err := d.resolveLauncher(ctx, botID)
		return launcher, nil, err
	}
	launcher, lease, err := external.AcquireLauncher(ctx, d.launchers, botID, dependencyID)
	if err != nil {
		var missing *external.DependencyMissingError
		if errors.As(err, &missing) {
			return external.Launcher{}, nil, dependencyMissingFeedback(missing)
		}
		return external.Launcher{}, nil, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	return launcher, lease, nil
}
