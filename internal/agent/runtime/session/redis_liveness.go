package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// The lease index is a single sorted set scored by lease deadline in
// milliseconds on the Redis clock. Members encode the run identity plus the
// fencing token that was current when the lease was written:
//
//	<bot_id>|<session_id>|<run_id>|<fencing_token>
//
// Carrying the token in the member is what makes the index reaper-consumable. A
// member is immutable for the life of one ownership, so a reclaim writes a new
// member and the stale one is recognisable rather than actionable.
//
// The write side belongs to the reservation itself: StartRun adds the member in
// the same transaction that reserves the run, RenewLease moves its score in the
// same script that extends the lease, and ReleaseRun and DeleteRunRef remove
// it from the stored ref's token. Runs started through the pre-ledger entry
// points carry no token and are deliberately absent — there is no durable row
// for a reaper to transition.
const leaseIndexMemberFields = 4

// expiredLeaseCandidatesScript samples TIME inside the script so a reaper with a
// skewed clock cannot condemn a live owner. Candidates are returned rather than
// claimed: the leader is single and every duty is an idempotent fenced
// transition, so a visibility timeout would only add a way to lose candidates.
var expiredLeaseCandidatesScript = redis.NewScript(`
local now = redis.call('TIME')
local now_ms = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
return redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now_ms, 'WITHSCORES', 'LIMIT', 0, ARGV[1])
`)

// acquireLeaderLeaseScript both elects and renews. It must be one script: a GET
// followed by a separate SET would let a peer take the lease in between and then
// be silently overwritten.
var acquireLeaderLeaseScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
if current == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0
`)

// releaseLeaderLeaseScript compares before deleting so a process that already
// lost leadership cannot delete its successor's lease.
var releaseLeaderLeaseScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == ARGV[1] then
  redis.call('DEL', KEYS[1])
  return 1
end
return 0
`)

func (b *RedisBackend) leaseIndexKey() string {
	return b.keyPrefix + "leases"
}

func (b *RedisBackend) livenessGenerationKey() string {
	return b.keyPrefix + "generation"
}

func (b *RedisBackend) leaderLeaseKey() string {
	return b.keyPrefix + "reaper_leader"
}

// LivenessGeneration reads the shared incarnation marker, creating it with SET
// NX when absent so concurrently starting processes converge on one value
// instead of each minting its own. The marker lives and dies with the Redis
// dataset: a flushed or non-persistent Redis comes back without it, the next SET
// NX writes a new value, and every run stamped with the old one is
// unrecoverable by definition.
func (b *RedisBackend) LivenessGeneration(ctx context.Context) (string, error) {
	if b == nil || b.client == nil {
		return "", errRedisBackendClosed
	}
	key := b.livenessGenerationKey()
	generation, err := b.client.Get(ctx, key).Result()
	if err == nil && strings.TrimSpace(generation) != "" {
		return generation, nil
	}
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", fmt.Errorf("read session runtime liveness generation: %w", err)
	}
	// No expiry: the marker must outlive every lease, because its absence is the
	// condition being detected.
	created, err := b.client.SetNX(ctx, key, uuid.NewString(), 0).Result()
	if err != nil {
		return "", fmt.Errorf("create session runtime liveness generation: %w", err)
	}
	generation, err = b.client.Get(ctx, key).Result()
	if err != nil {
		return "", fmt.Errorf("read session runtime liveness generation: %w", err)
	}
	if strings.TrimSpace(generation) == "" {
		return "", fmt.Errorf("session runtime liveness generation is empty after create (created=%t)", created)
	}
	return generation, nil
}

func (b *RedisBackend) ExpiredLeaseCandidates(ctx context.Context, limit int64) ([]LeaseCandidate, error) {
	if b == nil || b.client == nil {
		return nil, errRedisBackendClosed
	}
	if limit <= 0 {
		return nil, nil
	}
	raw, err := expiredLeaseCandidatesScript.Run(ctx, b.client, []string{b.leaseIndexKey()}, limit).Slice()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("read expired session runtime leases: %w", err)
	}
	candidates := make([]LeaseCandidate, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		member, ok := raw[i].(string)
		if !ok {
			return nil, fmt.Errorf("unexpected session runtime lease member type %T", raw[i])
		}
		expiresAt, err := leaseScoreTime(raw[i+1])
		if err != nil {
			return nil, err
		}
		candidate, err := decodeLeaseIndexMember(member, expiresAt)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// ReleaseLeaseCandidate drops the index member only while it still carries the
// token it was read with. Because the token is part of the member string, the
// compare is free: a run reclaimed between read and release has a different
// member and this ZREM removes nothing.
func (b *RedisBackend) ReleaseLeaseCandidate(ctx context.Context, candidate LeaseCandidate) (bool, error) {
	if b == nil || b.client == nil {
		return false, errRedisBackendClosed
	}
	member := encodeLeaseIndexMember(candidate.Key, candidate.RunID, candidate.FencingToken)
	removed, err := b.client.ZRem(ctx, b.leaseIndexKey(), member).Result()
	if err != nil {
		return false, fmt.Errorf("release expired session runtime lease: %w", err)
	}
	return removed > 0, nil
}

func (b *RedisBackend) AcquireLeaderLease(ctx context.Context, ownerID string, ttl time.Duration) (bool, error) {
	if b == nil || b.client == nil {
		return false, errRedisBackendClosed
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return false, errors.New("session runtime reaper leader lease requires an owner id")
	}
	if ttl <= 0 {
		return false, errors.New("session runtime reaper leader lease requires a positive ttl")
	}
	held, err := acquireLeaderLeaseScript.Run(ctx, b.client, []string{b.leaderLeaseKey()}, ownerID, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("acquire session runtime reaper leader lease: %w", err)
	}
	return held == 1, nil
}

func (b *RedisBackend) ReleaseLeaderLease(ctx context.Context, ownerID string) error {
	if b == nil || b.client == nil {
		return errRedisBackendClosed
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil
	}
	if _, err := releaseLeaderLeaseScript.Run(ctx, b.client, []string{b.leaderLeaseKey()}, ownerID).Int64(); err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return fmt.Errorf("release session runtime reaper leader lease: %w", err)
	}
	return nil
}

func encodeLeaseIndexMember(key Key, runID string, fencingToken int64) string {
	return strings.Join([]string{
		strings.TrimSpace(key.BotID),
		strings.TrimSpace(key.SessionID),
		strings.TrimSpace(runID),
		strconv.FormatInt(fencingToken, 10),
	}, "|")
}

func decodeLeaseIndexMember(member string, expiresAt time.Time) (LeaseCandidate, error) {
	parts := strings.Split(member, "|")
	if len(parts) != leaseIndexMemberFields {
		return LeaseCandidate{}, fmt.Errorf("malformed session runtime lease member %q", member)
	}
	token, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return LeaseCandidate{}, fmt.Errorf("malformed session runtime lease token in %q: %w", member, err)
	}
	return LeaseCandidate{
		Key:          Key{BotID: parts[0], SessionID: parts[1]},
		RunID:        parts[2],
		FencingToken: token,
		ExpiresAt:    expiresAt,
	}, nil
}

func leaseScoreTime(value any) (time.Time, error) {
	switch score := value.(type) {
	case string:
		ms, err := strconv.ParseFloat(score, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("malformed session runtime lease score %q: %w", score, err)
		}
		return time.UnixMilli(int64(ms)).UTC(), nil
	case int64:
		return time.UnixMilli(score).UTC(), nil
	case float64:
		return time.UnixMilli(int64(score)).UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("unexpected session runtime lease score type %T", value)
	}
}
