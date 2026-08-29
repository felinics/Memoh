package application

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestAbortReconciliationPrefersResumedRunSnapshotOverPausedMetadata(t *testing.T) {
	staleMetadata, err := json.Marshal(map[string]any{
		contextfrag.MetadataContextLifecycleKey: contextfrag.LifecycleSnapshot{Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	finalSnapshot, err := json.Marshal(contextfrag.LifecycleSnapshot{Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	queries := newAbortedLifecycleQueries(t)
	queries.existing = &sqlc.ContextLifecycle{Snapshot: finalSnapshot}
	queries.existingAfter = 2
	queries.metadata = staleMetadata
	service := &Service{queries: queries, contextLifecycles: queries}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	service.reconcileAbortedContextLifecycle(
		ctx,
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
	)

	upserts := queries.recordedUpserts()
	if len(upserts) != 1 || !bytes.Equal(upserts[0].Snapshot, finalSnapshot) {
		t.Fatalf("aborted upserts = %#v, want resumed snapshot %s", upserts, finalSnapshot)
	}
	if bytes.Contains(upserts[0].Snapshot, []byte(`"version":1`)) {
		t.Fatalf("aborted lifecycle used stale paused metadata: %s", upserts[0].Snapshot)
	}
}

func TestAbortReconciliationRechecksPendingDecisionBeforeMetadataFallback(t *testing.T) {
	staleMetadata, err := json.Marshal(map[string]any{
		contextfrag.MetadataContextLifecycleKey: contextfrag.LifecycleSnapshot{Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	finalSnapshot, err := json.Marshal(contextfrag.LifecycleSnapshot{Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	queries := newAbortedLifecycleQueries(t)
	queries.existing = &sqlc.ContextLifecycle{Snapshot: finalSnapshot}
	queries.existingAfter = 4
	queries.metadata = staleMetadata
	queries.pending = true
	queries.pendingUntil = 1
	service := &Service{queries: queries, contextLifecycles: queries}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	service.reconcileAbortedContextLifecycle(
		ctx,
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
	)

	upserts := queries.recordedUpserts()
	if len(upserts) != 1 || !bytes.Equal(upserts[0].Snapshot, finalSnapshot) {
		t.Fatalf("aborted upserts = %#v, want resumed snapshot %s", upserts, finalSnapshot)
	}
	queries.mu.Lock()
	pendingReads := queries.pendingReads
	queries.mu.Unlock()
	if pendingReads < 2 {
		t.Fatalf("pending decision reads = %d, want a recheck", pendingReads)
	}
}
