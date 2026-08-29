package db

import "errors"

var (
	ErrNotFound             = errors.New("database record not found")
	ErrLastActiveAdmin      = errors.New("team must retain at least one active admin")
	ErrWorkspaceTargetInUse = errors.New("workspace target is referenced by a workdir")
	// ErrCommitOutcomeUnknown means a transaction's COMMIT acknowledgement was
	// lost. Callers that would perform destructive compensation must reconcile
	// an idempotency/publication key on a fresh connection first.
	ErrCommitOutcomeUnknown = errors.New("database transaction commit outcome is unknown")
)
