package external

import (
	"context"

	"github.com/felinics/memoh/internal/workspace/payloadlease"
)

// LauncherLeaseProvider is an execution boundary. Read-only ResolveLauncher
// deliberately does not acquire leases or start any workspace command holder.
type LauncherLeaseProvider interface {
	AcquireLauncher(context.Context, string, string) (Launcher, *payloadlease.Lease, error)
}

// AcquireLauncher keeps compatibility with resolvers which only discover
// image/PATH commands. Managed dependency services implement the lease port.
func AcquireLauncher(ctx context.Context, resolver LauncherResolver, botID, depID string) (Launcher, *payloadlease.Lease, error) {
	if provider, ok := resolver.(LauncherLeaseProvider); ok {
		return provider.AcquireLauncher(ctx, botID, depID)
	}
	launcher, err := resolver.ResolveLauncher(ctx, botID, depID)
	return launcher, nil, err
}
