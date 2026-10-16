package decision

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// ContinuationCheckpoint records accepted native decisions. A tool marked
// executing may have produced effects even if its result never reached history.
// Recovery reconstructs context from the decision row; it never replays a tool.
type ContinuationCheckpoint struct {
	Kind  string `json:"kind"`
	Phase string `json:"phase"`
}

type nativeContinuationKey struct{}

// WithNativeContinuation is set by the application after runtime classification.
// Inline waiters must keep their original waiting/last-answer semantics.
func WithNativeContinuation(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeContinuationKey{}, true)
}

// ResumeNativeContinuation must run inside the decision response's fenced
// transaction: either both the answer and running checkpoint commit, or neither.
func ResumeNativeContinuation(ctx context.Context, queries dbstore.Queries, botID, sessionID, runID, decisionID pgtype.UUID, token pgtype.Int8, kind string) error {
	native, _ := ctx.Value(nativeContinuationKey{}).(bool)
	if !native || !runID.Valid || !token.Valid || token.Int64 <= 0 {
		return nil
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok || fence.Token != token.Int64 {
		return runtimefence.ErrStale
	}
	if err := runtimefence.ValidateScope(ctx, botID.String(), sessionID.String()); err != nil {
		return err
	}
	_, err := queries.AcceptSessionRunDecisionContinuation(ctx, sqlc.AcceptSessionRunDecisionContinuationParams{
		BotID: botID, SessionID: sessionID, RunID: runID,
		FencingToken: token.Int64, DecisionID: decisionID.String(), DecisionKind: kind,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimefence.ErrStale
	}
	if err != nil {
		return fmt.Errorf("accept native decision continuation: %w", err)
	}
	return nil
}
