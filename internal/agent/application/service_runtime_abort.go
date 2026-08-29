package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	abortedContextLifecycleRetryInterval    = 25 * time.Millisecond
	abortedContextLifecycleMetadataFallback = 500 * time.Millisecond
)

type runtimeAbortController interface {
	AbortControl(ctx context.Context, botID, sessionID, runID, controlID string) (bool, error)
}

type assistantContextLifecycleStore interface {
	GetLatestAssistantContextLifecycleByRunID(
		context.Context,
		pgtype.UUID,
	) (sqlc.GetLatestAssistantContextLifecycleByRunIDRow, error)
}

// AbortRuntimeRun routes an abort through the durable runtime and then
// reconciles its lifecycle asynchronously. The acknowledgement remains owned
// by AbortControl; audit persistence failures are counted and logged only.
//
//nolint:contextcheck // reconciliation intentionally outlives the acknowledgement request.
func (s *Service) AbortRuntimeRun(
	ctx context.Context,
	botID, sessionID, runID, controlID string,
) (bool, error) {
	if s == nil || s.abortRuntime == nil {
		return false, errors.New("session runtime abort is not configured")
	}
	ctx = nonNilContext(ctx)
	applied, err := s.abortRuntime.AbortControl(ctx, botID, sessionID, runID, controlID)
	if applied || err != nil {
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contextLifecycleWriteTimeout)
		go func() {
			defer cancel()
			s.reconcileAbortedContextLifecycle(reconcileCtx, runID, botID, sessionID)
		}()
	}
	return applied, err
}

func (s *Service) reconcileAbortedContextLifecycle(
	ctx context.Context,
	runID, botID, sessionID string,
) {
	if s == nil || s.queries == nil || s.contextLifecycles == nil {
		return
	}
	runUUID, botUUID, sessionUUID, err := parseContextLifecycleIDs(runID, botID, sessionID)
	if err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, contextLifecycleStatusAborted)
		return
	}

	var (
		pendingErr         error
		pendingKnown       bool
		pendingDecision    bool
		metadataFallbackAt time.Time
	)
	for {
		run, runErr := s.queries.GetSessionRun(ctx, runUUID)
		switch {
		case runErr == nil:
			if run.BotID != botUUID || run.SessionID != sessionUUID {
				s.recordContextLifecyclePersistenceError(
					errors.New("aborted session run identity does not match request"),
					runID,
					botID,
					sessionID,
					contextLifecycleStatusAborted,
				)
				return
			}
			state := ledger.State(run.State)
			if state.Terminal() && state != ledger.StateAborted {
				return
			}
			if state == ledger.StateAborted {
				snapshot, ready, snapshotErr := s.existingAbortedContextLifecycleSnapshot(ctx, runUUID)
				if snapshotErr != nil {
					s.recordContextLifecyclePersistenceError(snapshotErr, runID, botID, sessionID, contextLifecycleStatusAborted)
					return
				}
				if ready {
					s.upsertAbortedContextLifecycle(ctx, runID, botID, sessionID, runUUID, botUUID, sessionUUID, snapshot)
					return
				}

				_, currentPending, decisionErr := s.PendingRuntimeDecision(ctx, runID)
				if decisionErr != nil {
					s.recordContextLifecyclePersistenceError(decisionErr, runID, botID, sessionID, contextLifecycleStatusAborted)
					return
				}
				if !pendingKnown || pendingDecision && !currentPending {
					metadataFallbackAt = time.Now().Add(abortedContextLifecycleMetadataFallback)
				}
				pendingKnown = true
				pendingDecision = currentPending
				if !metadataFallbackAt.IsZero() && !time.Now().Before(metadataFallbackAt) {
					snapshot, ready, snapshotErr = s.assistantContextLifecycleSnapshot(ctx, runUUID)
					if snapshotErr != nil {
						s.recordContextLifecyclePersistenceError(snapshotErr, runID, botID, sessionID, contextLifecycleStatusAborted)
						return
					}
					if !ready {
						snapshot, snapshotErr = json.Marshal(minimalContextLifecycleSnapshot())
						if snapshotErr != nil {
							s.recordContextLifecyclePersistenceError(snapshotErr, runID, botID, sessionID, contextLifecycleStatusAborted)
							return
						}
					}
					s.upsertAbortedContextLifecycle(ctx, runID, botID, sessionID, runUUID, botUUID, sessionUUID, snapshot)
					return
				}
				pendingErr = errors.New("aborted run has no recoverable context lifecycle snapshot")
			} else {
				pendingErr = fmt.Errorf("session run has not reached aborted state: %s", state)
			}
		case errors.Is(runErr, pgx.ErrNoRows):
			pendingErr = errors.New("aborted session run is not visible")
		default:
			s.recordContextLifecyclePersistenceError(runErr, runID, botID, sessionID, contextLifecycleStatusAborted)
			return
		}

		if !waitContextLifecycleRetry(ctx) {
			if pendingErr == nil {
				pendingErr = context.Cause(ctx)
			}
			s.recordContextLifecyclePersistenceError(pendingErr, runID, botID, sessionID, contextLifecycleStatusAborted)
			return
		}
	}
}

func (s *Service) existingAbortedContextLifecycleSnapshot(
	ctx context.Context,
	runID pgtype.UUID,
) ([]byte, bool, error) {
	existing, err := s.contextLifecycles.GetContextLifecycleByRunID(ctx, runID)
	if err == nil {
		return existing.Snapshot, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	return nil, false, nil
}

func (s *Service) assistantContextLifecycleSnapshot(
	ctx context.Context,
	runID pgtype.UUID,
) ([]byte, bool, error) {
	var (
		assistantID pgtype.UUID
		metadata    []byte
		err         error
	)
	if store, ok := s.contextLifecycles.(assistantContextLifecycleStore); ok {
		row, rowErr := store.GetLatestAssistantContextLifecycleByRunID(ctx, runID)
		assistantID = row.ID
		metadata = row.Metadata
		err = rowErr
	} else {
		metadata, err = s.contextLifecycles.GetLatestAssistantContextLifecycleMetadataByRunID(ctx, runID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	snapshot, ok := contextLifecycleSnapshotFromMetadata(metadata)
	if !ok {
		return nil, false, nil
	}
	if assistantID.Valid {
		snapshot.AssistantMessageID = assistantID.String()
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func contextLifecycleSnapshotFromMetadata(raw []byte) (contextfrag.LifecycleSnapshot, bool) {
	if len(raw) == 0 {
		return contextfrag.LifecycleSnapshot{}, false
	}
	var metadata struct {
		ContextLifecycle *contextfrag.LifecycleSnapshot `json:"context_lifecycle"`
	}
	if json.Unmarshal(raw, &metadata) != nil ||
		metadata.ContextLifecycle == nil ||
		metadata.ContextLifecycle.Version <= 0 {
		return contextfrag.LifecycleSnapshot{}, false
	}
	return *metadata.ContextLifecycle, true
}

func (s *Service) upsertAbortedContextLifecycle(
	ctx context.Context,
	runID, botID, sessionID string,
	runUUID, botUUID, sessionUUID pgtype.UUID,
	snapshot []byte,
) {
	_, err := s.contextLifecycles.UpsertAbortedContextLifecycle(
		ctx,
		sqlc.UpsertAbortedContextLifecycleParams{
			RunID:     runUUID,
			BotID:     botUUID,
			SessionID: sessionUUID,
			Snapshot:  snapshot,
		},
	)
	if err != nil {
		s.recordContextLifecyclePersistenceError(err, runID, botID, sessionID, contextLifecycleStatusAborted)
	}
}

func waitContextLifecycleRetry(ctx context.Context) bool {
	timer := time.NewTimer(abortedContextLifecycleRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
