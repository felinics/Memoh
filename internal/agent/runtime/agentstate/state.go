// Package agentstate defines the publication and configuration guards used by
// process-local external runtimes. Native conversation files belong to the Agent.
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

// SessionPublicationHead identifies the last committed round observed by a
// warm runtime. An absent head is represented separately by Head's bool result.
type SessionPublicationHead struct {
	RunID string
}

// RuntimeConfigEpoch is the durable generation of process-affecting bot and
// session configuration. It is deliberately independent of publication head:
// credentials and workspace config may change while canonical chat history
// does not.
type RuntimeConfigEpoch struct {
	Bot     int64
	Session int64
}

// RuntimeStateStore fences warm processes against committed rounds and
// configuration changes. It does not store or restore native conversation files.
type RuntimeStateStore interface {
	RuntimeConfigEpoch(ctx context.Context, botID, sessionID string) (RuntimeConfigEpoch, error)
	GuardRuntimeSync(ctx context.Context, botID string, expectedBotEpoch int64, fn func(context.Context) error) error
	Head(ctx context.Context, botID, sessionID string) (SessionPublicationHead, bool, error)
}
