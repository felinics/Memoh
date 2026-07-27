package sessionruntime

import "context"

// FenceActivator hands durable persistence ownership to one run's fencing
// token. It is the second half of a claim: the ledger decides who owns the run,
// and this decides whose writes PostgreSQL will still accept (SR-OWN-002).
//
// The token is the same one the claim wrote to session_runs, which is the whole
// point of drawing it from a shared monotonic sequence. Two independent
// orderings — one for run ownership, one for persistence — could disagree, and
// the disagreement would surface as a superseded owner still writing history.
//
// It is expressed over (bot, session, token) rather than over a store handle so
// the runtime never learns that PostgreSQL exists;
// runtimefence.NewActivator satisfies it.
type FenceActivator interface {
	Activate(ctx context.Context, botID, sessionID string, token int64) error
}

// DecisionFenceActivator advances a parked run's persistence fence while
// preserving the one pending decision that will resume it. Implementations
// must update the decision row to token in the same transaction that activates
// the session fence.
type DecisionFenceActivator interface {
	ReclaimWaitingDecision(
		ctx context.Context,
		botID, sessionID, runID, ownerID, liveGeneration string,
		previousToken, newToken int64,
		decisionKind, decisionID string,
	) error
}
