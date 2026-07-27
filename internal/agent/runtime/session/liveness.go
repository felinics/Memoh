package sessionruntime

import (
	"context"
	"errors"
	"time"
)

// LeaseCandidate is one entry of the live backend's lease index whose deadline
// has passed. It carries the fencing token that was current when the lease was
// written, which is what lets the reaper apply a fenced transition without
// trusting anything else about the vanished owner.
type LeaseCandidate struct {
	Key          Key
	RunID        string
	FencingToken int64
	// ExpiresAt is the deadline recorded in the index, sampled on the backend's
	// clock rather than the reaper's.
	ExpiresAt time.Time
}

// LivenessBackend is the half of the runtime backend that answers "is an owner
// still alive". It is separate from Backend and DistributedBackend because
// liveness is the one thing PostgreSQL deliberately does not know: the ledger
// records ownership changes, never lease ticks, so only this interface can tell
// the reaper which runs to give up on.
//
// Both backends implement it. On a memory backend there is a single process and
// therefore no lease to expire, but there is still an incarnation to compare
// against and orphans to repair, which is exactly how a single-instance install
// recovers after a crash.
type LivenessBackend interface {
	// LivenessGeneration identifies this incarnation of the live backend. It is
	// created once and read by everyone, so a value different from the one
	// stamped on a run means the backend that claimed that run is gone and its
	// in-flight state cannot be recovered.
	LivenessGeneration(ctx context.Context) (string, error)

	// ExpiredLeaseCandidates returns up to limit entries whose lease deadline
	// has passed. The backend samples its own clock: a reaper with a skewed
	// clock must not be able to declare a healthy owner dead. Candidates are
	// returned, not claimed — the leader is single, so a visibility timeout
	// would add a second failure mode without removing one.
	ExpiredLeaseCandidates(ctx context.Context, limit int64) ([]LeaseCandidate, error)

	// ReleaseLeaseCandidate removes an index entry, but only while the entry
	// still carries the token the candidate was read with. A run reclaimed
	// between read and release keeps its new entry.
	ReleaseLeaseCandidate(ctx context.Context, candidate LeaseCandidate) (bool, error)

	// AcquireLeaderLease elects the single cluster-wide reaper. Renewal is the
	// same call: an existing holder re-acquires its own lease. Failover is safe
	// because every reaper duty is an idempotent fenced transition, so a new
	// leader repeating work the old one had started changes nothing.
	AcquireLeaderLease(ctx context.Context, ownerID string, ttl time.Duration) (bool, error)

	// ReleaseLeaderLease hands leadership back on graceful shutdown so a peer
	// takes over in one tick instead of one lease period.
	ReleaseLeaderLease(ctx context.Context, ownerID string) error
}

// LivenessGeneration returns this process's incarnation. A memory backend keeps
// live state in the heap, so its incarnation ends with the process: after a
// restart every run the ledger still shows as active belongs to a generation
// that no longer exists, which is precisely the signal the recovery sweep needs.
func (b *MemoryBackend) LivenessGeneration(context.Context) (string, error) {
	if b == nil {
		return "", errors.New("session runtime memory backend is unavailable")
	}
	return b.generation, nil
}

// ExpiredLeaseCandidates is always empty for a memory backend: one process owns
// every run it admitted, so a lease would only ever expire together with the
// process that was watching it.
func (*MemoryBackend) ExpiredLeaseCandidates(context.Context, int64) ([]LeaseCandidate, error) {
	return nil, nil
}

func (*MemoryBackend) ReleaseLeaseCandidate(context.Context, LeaseCandidate) (bool, error) {
	return false, nil
}

// AcquireLeaderLease always succeeds: a single process is trivially the leader.
func (*MemoryBackend) AcquireLeaderLease(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (*MemoryBackend) ReleaseLeaderLease(context.Context, string) error { return nil }
