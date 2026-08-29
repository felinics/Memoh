package sessionruntime

import (
	"context"
	"testing"
	"time"

	chatview "github.com/felinics/memoh/internal/agent/view"
)

// reserveRuntimeRun writes one live reservation whose owner lease ends at
// expiresAt. A deadline already in the past models an owner that vanished: the
// reservation is what the reaper has to find.
func reserveRuntimeRun(ctx context.Context, t *testing.T, backend *RedisBackend, ref RunRef, expiresAt time.Time) {
	t.Helper()
	key := Key{BotID: ref.BotID, SessionID: ref.SessionID}
	_, changed, err := backend.StartRun(ctx, key, ref, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot.BotID = ref.BotID
		snapshot.SessionID = ref.SessionID
		snapshot.Epoch = "epoch-lease-index"
		snapshot.Seq++
		snapshot.UpdatedAt = time.Now()
		lease := expiresAt
		snapshot.CurrentRunView = &CurrentRunView{
			RunID:               ref.RunID,
			Generation:          ref.Generation,
			Status:              RunStatusRunning,
			OwnerID:             ref.OwnerID,
			OwnerLeaseExpiresAt: &lease,
			StartedAt:           time.Now(),
			UpdatedAt:           time.Now(),
			Messages:            []chatview.UIMessage{},
		}
		return snapshot, true, nil
	})
	if err != nil {
		t.Fatalf("reserve run %q: %v", ref.RunID, err)
	}
	if !changed {
		t.Fatalf("reserve run %q did not apply", ref.RunID)
	}
}

func leaseCandidateFor(ctx context.Context, t *testing.T, backend *RedisBackend, runID string) (LeaseCandidate, bool) {
	t.Helper()
	candidates, err := backend.ExpiredLeaseCandidates(ctx, 16)
	if err != nil {
		t.Fatalf("read expired lease candidates: %v", err)
	}
	for _, candidate := range candidates {
		if candidate.RunID == runID {
			return candidate, true
		}
	}
	return LeaseCandidate{}, false
}

// The lease index is the only thing that can tell the reaper an owner is gone,
// because the ledger deliberately records no heartbeat. These cases pin the
// write side: a reservation appears, a renewal moves its deadline, and release
// removes it (SR-OWN-002).
func runRedisLeaseIndexContract(t *testing.T, redisURL string) {
	t.Helper()

	prefix := uniqueRuntimeBackendPrefix("lease-index")
	newBackend := func() *RedisBackend {
		backend, err := NewRedisBackend(context.Background(), RedisOptions{
			URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute,
		})
		if err != nil {
			t.Fatalf("redis backend: %v", err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		return backend
	}
	backend := newBackend()
	ctx := context.Background()

	t.Run("live lease is not a candidate", func(t *testing.T) {
		ref := RunRef{
			BotID: testBotID, SessionID: "session-lease-live", RunID: "run-lease-live",
			OwnerID: "owner-lease-live", Generation: "gen-live", FencingToken: 7,
		}
		reserveRuntimeRun(ctx, t, backend, ref, time.Now().Add(time.Minute))
		if candidate, found := leaseCandidateFor(ctx, t, backend, ref.RunID); found {
			t.Fatalf("live lease reported as expired: %+v", candidate)
		}
	})

	t.Run("expired lease carries run and token", func(t *testing.T) {
		ref := RunRef{
			BotID: testBotID, SessionID: "session-lease-expired", RunID: "run-lease-expired",
			OwnerID: "owner-lease-expired", Generation: "gen-expired", FencingToken: 11,
		}
		deadline := time.Now().Add(-time.Minute)
		reserveRuntimeRun(ctx, t, backend, ref, deadline)

		candidate, found := leaseCandidateFor(ctx, t, backend, ref.RunID)
		if !found {
			t.Fatal("expired lease is missing from the index")
		}
		if candidate.FencingToken != ref.FencingToken {
			t.Fatalf("candidate token = %d, want %d", candidate.FencingToken, ref.FencingToken)
		}
		if candidate.Key != (Key{BotID: ref.BotID, SessionID: ref.SessionID}) {
			t.Fatalf("candidate key = %+v, want the reserved session", candidate.Key)
		}
		if diff := candidate.ExpiresAt.Sub(deadline); diff > time.Second || diff < -time.Second {
			t.Fatalf("candidate deadline = %v, want ~%v", candidate.ExpiresAt, deadline)
		}

		// A stale token cannot release the entry: that is what stops a reaper
		// holding an old read from clearing a reclaimed run.
		stale := candidate
		stale.FencingToken = candidate.FencingToken - 1
		if released, err := backend.ReleaseLeaseCandidate(ctx, stale); err != nil || released {
			t.Fatalf("stale release = (%t, %v), want (false, nil)", released, err)
		}
		if released, err := backend.ReleaseLeaseCandidate(ctx, candidate); err != nil || !released {
			t.Fatalf("release = (%t, %v), want (true, nil)", released, err)
		}
		if released, err := backend.ReleaseLeaseCandidate(ctx, candidate); err != nil || released {
			t.Fatalf("repeat release = (%t, %v), want (false, nil)", released, err)
		}
		if _, found := leaseCandidateFor(ctx, t, backend, ref.RunID); found {
			t.Fatal("released candidate is still indexed")
		}
	})

	t.Run("renewal moves the deadline", func(t *testing.T) {
		ref := RunRef{
			BotID: testBotID, SessionID: "session-lease-renew", RunID: "run-lease-renew",
			OwnerID: "owner-lease-renew", Generation: "gen-renew", FencingToken: 13,
		}
		key := Key{BotID: ref.BotID, SessionID: ref.SessionID}
		reserveRuntimeRun(ctx, t, backend, ref, time.Now().Add(150*time.Millisecond))

		renewedAt, err := backend.Now(ctx)
		if err != nil {
			t.Fatalf("backend time: %v", err)
		}
		if err := backend.RenewLease(ctx, key, ref.RunID, ref.OwnerID, ref.Generation, renewedAt, renewedAt.Add(time.Minute)); err != nil {
			t.Fatalf("renew lease: %v", err)
		}
		// Past the original deadline the run must still be absent from the
		// expired set, which is only true if the renewal moved the index score
		// rather than just the snapshot.
		time.Sleep(250 * time.Millisecond)
		if candidate, found := leaseCandidateFor(ctx, t, backend, ref.RunID); found {
			t.Fatalf("renewed lease reported as expired: %+v", candidate)
		}
	})

	t.Run("release removes the entry", func(t *testing.T) {
		ref := RunRef{
			BotID: testBotID, SessionID: "session-lease-release", RunID: "run-lease-release",
			OwnerID: "owner-lease-release", Generation: "gen-release", FencingToken: 17,
		}
		key := Key{BotID: ref.BotID, SessionID: ref.SessionID}
		reserveRuntimeRun(ctx, t, backend, ref, time.Now().Add(200*time.Millisecond))

		_, changed, err := backend.ReleaseRun(ctx, key, ref, func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
			snapshot.Seq++
			snapshot.UpdatedAt = now
			snapshot.CurrentRunView.Status = RunStatusCompleted
			snapshot.CurrentRunView.UpdatedAt = now
			return snapshot, true, nil
		})
		if err != nil || !changed {
			t.Fatalf("release run = (%t, %v), want (true, nil)", changed, err)
		}
		time.Sleep(300 * time.Millisecond)
		if candidate, found := leaseCandidateFor(ctx, t, backend, ref.RunID); found {
			t.Fatalf("released run is still indexed: %+v", candidate)
		}
	})

	t.Run("stream ref deletion removes the entry without the caller's token", func(t *testing.T) {
		ref := RunRef{
			BotID: testBotID, SessionID: "session-lease-delete", RunID: "run-lease-delete",
			OwnerID: "owner-lease-delete", Generation: "gen-delete", FencingToken: 19,
		}
		reserveRuntimeRun(ctx, t, backend, ref, time.Now().Add(time.Minute))

		// Release paths rebuild the ref from live state, which does not carry a
		// token. Identity must be enough to delete, and the stored token is what
		// clears the index.
		reconstructed := RunRef{
			BotID: ref.BotID, SessionID: ref.SessionID, RunID: ref.RunID,
			OwnerID: ref.OwnerID, Generation: ref.Generation,
		}
		deleted, err := backend.DeleteRunRef(ctx, reconstructed)
		if err != nil || !deleted {
			t.Fatalf("delete stream ref = (%t, %v), want (true, nil)", deleted, err)
		}
		candidates, err := backend.ExpiredLeaseCandidates(ctx, 16)
		if err != nil {
			t.Fatalf("read expired lease candidates: %v", err)
		}
		for _, candidate := range candidates {
			if candidate.RunID == ref.RunID {
				t.Fatalf("deleted reservation is still indexed: %+v", candidate)
			}
		}
	})

	// The whole point of the index is that a reaper can act on it. This is the
	// one case that exercises both halves against a real backend: the member
	// written by a reservation must decode into a candidate whose token matches
	// the durable row, or every fenced transition would silently apply to
	// nothing.
	t.Run("reaper condemns a run from the real index", func(t *testing.T) {
		const runID = "run-lease-reaped"
		generation, err := backend.LivenessGeneration(ctx)
		if err != nil {
			t.Fatalf("liveness generation: %v", err)
		}
		ref := RunRef{
			BotID: testBotID, SessionID: "session-lease-reaped", RunID: runID,
			OwnerID: "owner-lease-reaped", Generation: "gen-reaped", FencingToken: 23,
		}
		reserveRuntimeRun(ctx, t, backend, ref, time.Now().Add(-time.Minute))

		runs := newFakeLedger()
		runs.insertClaimed(runID, ref.SessionID, ref.FencingToken, generation)
		reaper := newTestReaperWithLiveness(t, runs, backend, generation)

		reaper.tick(ctx)

		if got := runs.state(runID); got != "lost" {
			t.Fatalf("state = %q, want lost", got)
		}
		if got := runs.errorCode(runID); got != runErrorOwnerLeaseExpired {
			t.Fatalf("error code = %q, want %q", got, runErrorOwnerLeaseExpired)
		}
		if _, found := leaseCandidateFor(ctx, t, backend, runID); found {
			t.Fatal("condemned run is still indexed")
		}
	})

	t.Run("a run without a ledger token is not indexed", func(t *testing.T) {
		ref := RunRef{
			BotID: testBotID, SessionID: "session-lease-untokened", RunID: "run-lease-untokened",
			OwnerID: "owner-lease-untokened", Generation: "gen-untokened",
		}
		reserveRuntimeRun(ctx, t, backend, ref, time.Now().Add(-time.Minute))
		if candidate, found := leaseCandidateFor(ctx, t, backend, ref.RunID); found {
			t.Fatalf("run with no durable row was indexed: %+v", candidate)
		}
	})
}
