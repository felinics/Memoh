package runtimefence

import (
	"context"

	dbstore "github.com/felinics/memoh/internal/db/store"
)

// Activator binds Activate to one persistence store so a caller can hand
// persistence ownership to a run without holding a database handle itself.
//
// The session runtime owns the decision of when a token takes over; it must not
// also own a connection to the store that records it. Keeping the binding here
// is what lets the runtime declare the capability as an interface over
// (bot, session, token) and stay free of dbstore.
type Activator struct {
	queries dbstore.Queries
}

func NewActivator(queries dbstore.Queries) *Activator {
	return &Activator{queries: queries}
}

// Activate promotes token to the session's persistence fence. Callers must have
// won the durable ownership claim first: activating a token whose run does not
// own the session would fence out the owner that legitimately holds an older
// one.
func (a *Activator) Activate(ctx context.Context, botID, sessionID string, token int64) error {
	if a == nil || a.queries == nil {
		return ErrTransactionsUnsupported
	}
	return Activate(ctx, a.queries, Fence{BotID: botID, SessionID: sessionID, Token: token})
}

func (a *Activator) ReclaimWaitingDecision(
	ctx context.Context,
	botID, sessionID, runID, ownerID, liveGeneration string,
	previousToken, newToken int64,
	decisions []PreservedDecision,
) error {
	if a == nil || a.queries == nil {
		return ErrTransactionsUnsupported
	}
	return ActivateWithOptions(ctx, a.queries, Fence{
		BotID: botID, SessionID: sessionID, Token: newToken,
	}, ActivationOptions{
		PreserveDecisions: decisions,
		ReclaimWaitingDecision: &WaitingDecisionReclaim{
			RunID: runID, OwnerID: ownerID, PreviousToken: previousToken,
			LiveGeneration: liveGeneration,
		},
	})
}
