package codex

import (
	"context"
	"errors"
	"fmt"

	"github.com/felinics/memoh/internal/agent/runtime/external"
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
		return external.Launcher{}, nil, fmt.Errorf("lease codex launcher: %w", err)
	}
	return launcher, lease, nil
}
