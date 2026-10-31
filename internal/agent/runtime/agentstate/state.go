// Package agentstate defines the configuration-epoch and runtime-sync guards
// used by process-local external runtimes. Native conversation files belong to
// the Agent.
package agentstate

import (
	"context"
	"errors"
)

var (
	// ErrRuntimeConfigStale means the runtime process was created from an older
	// bot configuration generation. Retrying the write from that process would
	// let stale credentials or configuration replace the current generation.
	ErrRuntimeConfigStale = errors.New("runtime configuration generation is stale")
	// ErrRuntimeConfigResetInProgress is transient: a bot-scoped reset owns the
	// workspace configuration publication lock. Session-scoped history resets
	// deliberately do not block bot-global configuration synchronization.
	ErrRuntimeConfigResetInProgress = errors.New("bot runtime reset is in progress")
)

// RuntimeConfigEpoch is the durable generation of process-affecting bot and
// session configuration. Credentials and workspace config may change while
// canonical chat history does not, so a warm process compares the epoch it
// started from to tell whether it is still current.
type RuntimeConfigEpoch struct {
	Bot     int64
	Session int64
}

// RuntimeStateStore fences warm processes against configuration changes. It
// does not store or restore native conversation files.
type RuntimeStateStore interface {
	RuntimeConfigEpoch(ctx context.Context, botID, sessionID string) (RuntimeConfigEpoch, error)
}
