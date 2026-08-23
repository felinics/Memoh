package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisOptions struct {
	URL       string
	KeyPrefix string
	StateTTL  time.Duration
}

type RedisBackend struct {
	client    *redis.Client
	keyPrefix string
	stateTTL  time.Duration

	subscriptionsMu sync.Mutex
	subscriptions   map[uint64]context.CancelFunc
	nextSubID       uint64
	subscriptionsWG sync.WaitGroup
	closed          bool
	closeOnce       sync.Once
	closeDone       chan struct{}
	closeErr        error
}

type redisHistoryResetState struct {
	Bot      *redisHistoryResetRecord           `json:"bot,omitempty"`
	Sessions map[string]redisHistoryResetRecord `json:"sessions,omitempty"`
}

type redisHistoryResetRecord struct {
	Token       string `json:"token"`
	ExpiresAtMS int64  `json:"expires_at_ms"`
}

// renewRedisLeaseScript also carries the lease index forward. The index score is
// the deadline the reaper condemns a run by, so it must move in the same atomic
// step as the deadline inside the snapshot; a renewal that updated one and not
// the other would either lose the run or condemn a live owner.
var renewRedisLeaseScript = redis.NewScript(`
local current_state = redis.call('GET', KEYS[1])
local current_ref = redis.call('GET', KEYS[2])
if not current_state or not current_ref then
  return -1
end
if current_state ~= ARGV[1] or current_ref ~= ARGV[2] then
  return 0
end
local now = redis.call('TIME')
local now_ms = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
local expires_at_ms = tonumber(ARGV[5])
if not expires_at_ms or expires_at_ms <= now_ms then
  return -1
end
redis.call('SET', KEYS[1], ARGV[3], 'PX', ARGV[6])
redis.call('SET', KEYS[2], ARGV[4], 'PXAT', ARGV[5])
if ARGV[7] ~= '' then
  redis.call('ZADD', KEYS[3], expires_at_ms, ARGV[7])
end
return 1
`)

// deleteRedisRunRefScript compares the identity fields rather than the whole
// stored blob: release paths reconstruct a ref from live state, which does not
// carry the fencing token. The token is read back from the stored ref so the
// lease index member can be removed exactly, without the caller having to still
// hold a token it may have already lost.
var deleteRedisRunRefScript = redis.NewScript(`
local current_ref = redis.call('GET', KEYS[1])
if not current_ref then
  return 0
end
local ok_ref, ref = pcall(cjson.decode, current_ref)
if not ok_ref then
  return 0
end
if ref.bot_id ~= ARGV[1] or ref.session_id ~= ARGV[2] or
   ref.run_id ~= ARGV[3] or ref.owner_id ~= ARGV[4] or ref.generation ~= ARGV[5] then
  return 0
end
redis.call('DEL', KEYS[1])
local token = tonumber(ref.fencing_token)
if token and token > 0 then
  redis.call('ZREM', KEYS[2], ARGV[1] .. '|' .. ARGV[2] .. '|' .. ARGV[3] .. '|' .. string.format('%d', math.floor(token)))
end
return 1
`)

var validateRedisRunOwnershipScript = redis.NewScript(`
local current_state = redis.call('GET', KEYS[1])
local current_ref = redis.call('GET', KEYS[2])
if not current_state or not current_ref then
  return 0
end

local ok_ref, ref = pcall(cjson.decode, current_ref)
local ok_state, state = pcall(cjson.decode, current_state)
if not ok_ref or not ok_state or not state.current_run_view then
  return 0
end

local run = state.current_run_view
if ref.bot_id ~= ARGV[1] or ref.session_id ~= ARGV[2] or
   ref.run_id ~= ARGV[3] or ref.owner_id ~= ARGV[4] or ref.generation ~= ARGV[5] or
   state.bot_id ~= ARGV[1] or state.session_id ~= ARGV[2] or
   run.run_id ~= ARGV[3] or run.owner_id ~= ARGV[4] or run.generation ~= ARGV[5] then
  return 0
end

if run.status ~= 'admitting' and run.status ~= 'running' and
   run.status ~= 'waiting_decision' and run.status ~= 'aborting' then
  return 0
end

-- PTTL and TIME are evaluated by Redis in this atomic script, so expiry cannot
-- pass between separate client reads. TIME is intentionally sampled here to
-- keep the ownership decision on the Redis server clock.
local now = redis.call('TIME')
local ttl = redis.call('PTTL', KEYS[2])
if not now or ttl <= 0 then
  return 0
end
return 1
`)

func NewRedisBackend(ctx context.Context, opts RedisOptions) (*RedisBackend, error) {
	backend, err := newRedisBackend(opts)
	if err != nil {
		return nil, err
	}
	if err := backend.CheckHealth(ctx); err != nil {
		_ = backend.Close()
		return nil, err
	}
	return backend, nil
}

var errRedisBackendClosed = errors.New("session runtime redis backend is closed")

func newRedisBackend(opts RedisOptions) (*RedisBackend, error) {
	redisOpts, err := redis.ParseURL(strings.TrimSpace(opts.URL))
	if err != nil {
		return nil, err
	}
	redisOpts.ContextTimeoutEnabled = true
	client := redis.NewClient(redisOpts)
	ttl := opts.StateTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	prefix := strings.TrimSpace(opts.KeyPrefix)
	if prefix == "" {
		prefix = "memoh:session_runtime:"
	}
	return &RedisBackend{
		client:        client,
		keyPrefix:     prefix,
		stateTTL:      ttl,
		subscriptions: make(map[uint64]context.CancelFunc),
		closeDone:     make(chan struct{}),
	}, nil
}

func (b *RedisBackend) registerSubscription(cancel context.CancelFunc) (uint64, bool) {
	b.subscriptionsMu.Lock()
	defer b.subscriptionsMu.Unlock()
	if b.closed {
		return 0, false
	}
	b.nextSubID++
	id := b.nextSubID
	b.subscriptions[id] = cancel
	b.subscriptionsWG.Add(1)
	return id, true
}

func (b *RedisBackend) unregisterSubscription(id uint64) {
	b.subscriptionsMu.Lock()
	delete(b.subscriptions, id)
	b.subscriptionsMu.Unlock()
	b.subscriptionsWG.Done()
}

func (b *RedisBackend) CheckHealth(ctx context.Context) error {
	return b.client.Ping(ctx).Err()
}

func (b *RedisBackend) Now(ctx context.Context) (time.Time, error) {
	return b.client.Time(ctx).Result()
}

func (b *RedisBackend) Load(ctx context.Context, key Key) (Snapshot, bool, error) {
	data, err := b.client.Get(ctx, b.stateKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, false, err
	}
	return snapshot, true, nil
}

func (b *RedisBackend) Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error) {
	if update == nil {
		return Snapshot{}, false, errors.New("snapshot update is required")
	}
	stateKey := b.stateKey(key)
	for {
		var updated Snapshot
		var changed bool
		err := b.client.Watch(ctx, func(tx *redis.Tx) error {
			// current is freshly unmarshaled per attempt and next is
			// transaction-local, so neither needs a defensive clone.
			current, ok, err := loadRedisSnapshot(ctx, tx, stateKey)
			if err != nil {
				return err
			}
			next, apply, err := update(current, ok)
			if err != nil {
				return err
			}
			if !apply {
				updated = next
				changed = false
				return nil
			}
			data, err := json.Marshal(next)
			if err != nil {
				return err
			}
			if _, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, stateKey, data, b.stateTTL)
				return nil
			}); err != nil {
				return err
			}
			updated = next
			changed = true
			return nil
		}, stateKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return updated, changed, err
	}
}

func (b *RedisBackend) UpdateActiveRun(ctx context.Context, key Key, runID, generation string, update ActiveRunUpdate) (Snapshot, bool, error) {
	if update == nil {
		return Snapshot{}, false, errors.New("active run update is required")
	}
	runID = strings.TrimSpace(runID)
	generation = strings.TrimSpace(generation)
	if runID == "" || generation == "" {
		return Snapshot{}, false, errors.New("run_id and generation are required")
	}
	stateKey := b.stateKey(key)
	runKey := b.runKey(key, runID)
	for {
		var updated Snapshot
		var changed bool
		err := b.client.Watch(ctx, func(tx *redis.Tx) error {
			now, err := tx.Time(ctx).Result()
			if err != nil {
				return err
			}
			ref, ok, err := loadRedisRunRef(ctx, tx, runKey)
			if err != nil {
				return err
			}
			current, stateOK, err := loadRedisSnapshot(ctx, tx, stateKey)
			if err != nil {
				return err
			}
			if !ok || !stateOK || current.CurrentRunView == nil {
				return ErrRunOwnershipLost
			}
			run := current.CurrentRunView
			if run.RunID != runID || run.Generation != generation || ref.RunID != runID || ref.Generation != generation || ref.OwnerID != run.OwnerID || ref.BotID != key.BotID || ref.SessionID != key.SessionID || !isActiveRunStatus(run.Status) || run.OwnerLeaseExpiresAt == nil || !now.Before(*run.OwnerLeaseExpiresAt) {
				return ErrRunOwnershipLost
			}
			next, apply, err := update(current, now)
			if err != nil {
				return err
			}
			if !apply {
				updated = next
				changed = false
				return nil
			}
			data, err := json.Marshal(next)
			if err != nil {
				return err
			}
			if _, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, stateKey, data, b.stateTTL)
				return nil
			}); err != nil {
				return err
			}
			updated = next
			changed = true
			return nil
		}, stateKey, runKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return updated, changed, err
	}
}

func (b *RedisBackend) ReleaseRun(ctx context.Context, key Key, ref RunRef, update ActiveRunUpdate) (Snapshot, bool, error) {
	if update == nil {
		return Snapshot{}, false, errors.New("active run update is required")
	}
	ref.BotID = strings.TrimSpace(ref.BotID)
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.RunID = strings.TrimSpace(ref.RunID)
	ref.OwnerID = strings.TrimSpace(ref.OwnerID)
	ref.Generation = strings.TrimSpace(ref.Generation)
	if ref.BotID != strings.TrimSpace(key.BotID) || ref.SessionID != strings.TrimSpace(key.SessionID) || ref.RunID == "" || ref.OwnerID == "" || ref.Generation == "" {
		return Snapshot{}, false, ErrRunOwnershipLost
	}
	stateKey := b.stateKey(key)
	runKey := b.runKey(key, ref.RunID)
	for {
		var updated Snapshot
		var changed bool
		err := b.client.Watch(ctx, func(tx *redis.Tx) error {
			now, err := tx.Time(ctx).Result()
			if err != nil {
				return err
			}
			storedRef, ok, err := loadRedisRunRef(ctx, tx, runKey)
			if err != nil {
				return err
			}
			current, stateOK, err := loadRedisSnapshot(ctx, tx, stateKey)
			if err != nil {
				return err
			}
			if !ok || !stateOK || current.CurrentRunView == nil {
				return ErrRunOwnershipLost
			}
			run := current.CurrentRunView
			if !storedRef.identityMatches(ref) || run.RunID != ref.RunID || run.Generation != ref.Generation || run.OwnerID != ref.OwnerID || !isActiveRunStatus(run.Status) || run.OwnerLeaseExpiresAt == nil || !now.Before(*run.OwnerLeaseExpiresAt) {
				return ErrRunOwnershipLost
			}
			next, apply, err := update(current, now)
			if err != nil {
				return err
			}
			if !apply {
				updated = next
				changed = false
				return nil
			}
			data, err := json.Marshal(next)
			if err != nil {
				return err
			}
			if _, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, stateKey, data, b.stateTTL)
				pipe.Del(ctx, runKey)
				// The stored ref, not the caller's, carries the token this
				// reservation was indexed under.
				if storedRef.FencingToken > 0 {
					pipe.ZRem(ctx, b.leaseIndexKey(), encodeLeaseIndexMember(key, storedRef.RunID, storedRef.FencingToken))
				}
				return nil
			}); err != nil {
				return err
			}
			updated = next
			changed = true
			return nil
		}, stateKey, runKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return updated, changed, err
	}
}

func (b *RedisBackend) StartRun(ctx context.Context, key Key, ref RunRef, update SnapshotUpdate) (Snapshot, bool, error) {
	if update == nil {
		return Snapshot{}, false, errors.New("snapshot update is required")
	}
	runID := strings.TrimSpace(ref.RunID)
	ref.Generation = strings.TrimSpace(ref.Generation)
	if runID == "" || ref.Generation == "" {
		return Snapshot{}, false, errors.New("run_id and generation are required")
	}
	ref.RunID = runID
	refData, err := json.Marshal(ref)
	if err != nil {
		return Snapshot{}, false, err
	}
	stateKey := b.stateKey(key)
	runKey := b.runKey(key, runID)
	resetKey := b.historyResetKey(key.BotID)
	for {
		var updated Snapshot
		var changed bool
		err := b.client.Watch(ctx, func(tx *redis.Tx) error {
			now, err := tx.Time(ctx).Result()
			if err != nil {
				return err
			}
			resetState, err := loadRedisHistoryResetState(ctx, tx, resetKey)
			if err != nil {
				return err
			}
			purgeRedisHistoryResetState(&resetState, now.UnixMilli())
			if _, blocked := effectiveRedisHistoryReset(resetState, key.BotID, key.SessionID, now.UnixMilli()); blocked {
				return ErrHistoryResetInProgress
			}
			if existing, ok, err := loadRedisRunRef(ctx, tx, runKey); err != nil {
				return err
			} else if ok {
				return fmt.Errorf("run_id %q is already registered for session %q", runID, existing.SessionID)
			}
			current, ok, err := loadRedisSnapshot(ctx, tx, stateKey)
			if err != nil {
				return err
			}
			next, apply, err := update(current, ok)
			if err != nil {
				return err
			}
			if !apply {
				updated = next
				changed = false
				return nil
			}
			stateData, err := json.Marshal(next)
			if err != nil {
				return err
			}
			leaseExpiry := streamLeaseExpiry(next, runID, time.Now().Add(b.stateTTL))
			if _, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, stateKey, stateData, b.stateTTL)
				pipe.Set(ctx, runKey, refData, 0)
				pipe.PExpireAt(ctx, runKey, leaseExpiry)
				// The index entry is what makes this reservation reapable. It is
				// written in the same transaction as the reservation itself, so
				// there is no window in which an owner holds a run that no
				// reaper can find.
				if ref.FencingToken > 0 {
					pipe.ZAdd(ctx, b.leaseIndexKey(), redis.Z{
						Score:  float64(leaseExpiry.UnixMilli()),
						Member: encodeLeaseIndexMember(key, runID, ref.FencingToken),
					})
				}
				return nil
			}); err != nil {
				return err
			}
			updated = next
			changed = true
			return nil
		}, stateKey, runKey, resetKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return updated, changed, err
	}
}

func (b *RedisBackend) AcquireHistoryReset(ctx context.Context, scope ResetScope, token string, ttl time.Duration) (ResetLease, bool, error) {
	scope = scope.normalized()
	token = strings.TrimSpace(token)
	if !scope.valid() || token == "" || ttl <= 0 {
		return ResetLease{}, false, errors.New("reset scope, token, and positive ttl are required")
	}
	key := b.historyResetKey(scope.BotID)
	for {
		var acquired ResetLease
		var ok bool
		err := b.client.Watch(ctx, func(tx *redis.Tx) error {
			now, err := tx.Time(ctx).Result()
			if err != nil {
				return err
			}
			state, err := loadRedisHistoryResetState(ctx, tx, key)
			if err != nil {
				return err
			}
			nowMS := now.UnixMilli()
			purgeRedisHistoryResetState(&state, nowMS)
			if _, blocked := effectiveRedisHistoryReset(state, scope.BotID, scope.SessionID, nowMS); blocked {
				return nil
			}
			expiresAt := now.Add(ttl)
			record := redisHistoryResetRecord{Token: token, ExpiresAtMS: expiresAt.UnixMilli()}
			if scope.SessionID == "" {
				if len(state.Sessions) != 0 {
					return nil
				}
				state.Bot = &record
			} else {
				if state.Sessions == nil {
					state.Sessions = make(map[string]redisHistoryResetRecord)
				}
				state.Sessions[scope.SessionID] = record
			}
			if err := storeRedisHistoryResetState(ctx, tx, key, state, nowMS); err != nil {
				return err
			}
			acquired = ResetLease{Scope: scope, Token: token, ExpiresAt: expiresAt}
			ok = true
			return nil
		}, key)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return acquired, ok, err
	}
}

func (b *RedisBackend) RenewHistoryReset(ctx context.Context, lease ResetLease, ttl time.Duration) (ResetLease, bool, error) {
	if !lease.valid() || ttl <= 0 {
		return ResetLease{}, false, errors.New("valid reset lease and positive ttl are required")
	}
	key := b.historyResetKey(lease.Scope.BotID)
	for {
		var renewed ResetLease
		var ok bool
		err := b.client.Watch(ctx, func(tx *redis.Tx) error {
			now, err := tx.Time(ctx).Result()
			if err != nil {
				return err
			}
			state, err := loadRedisHistoryResetState(ctx, tx, key)
			if err != nil {
				return err
			}
			nowMS := now.UnixMilli()
			purgeRedisHistoryResetState(&state, nowMS)
			record := redisHistoryResetRecord{}
			if lease.Scope.SessionID == "" {
				if state.Bot == nil || state.Bot.Token != strings.TrimSpace(lease.Token) || len(state.Sessions) != 0 {
					return nil
				}
				record = *state.Bot
			} else {
				if state.Bot != nil {
					return nil
				}
				var found bool
				record, found = state.Sessions[lease.Scope.SessionID]
				if !found || record.Token != strings.TrimSpace(lease.Token) {
					return nil
				}
			}
			expiresAt := now.Add(ttl)
			record.ExpiresAtMS = expiresAt.UnixMilli()
			if lease.Scope.SessionID == "" {
				state.Bot = &record
			} else {
				state.Sessions[lease.Scope.SessionID] = record
			}
			if err := storeRedisHistoryResetState(ctx, tx, key, state, nowMS); err != nil {
				return err
			}
			renewed = ResetLease{Scope: lease.Scope, Token: lease.Token, ExpiresAt: expiresAt}
			ok = true
			return nil
		}, key)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return renewed, ok, err
	}
}

func (b *RedisBackend) ReleaseHistoryReset(ctx context.Context, lease ResetLease) (bool, error) {
	if !lease.Scope.valid() || strings.TrimSpace(lease.Token) == "" {
		return false, nil
	}
	key := b.historyResetKey(lease.Scope.BotID)
	for {
		var released bool
		err := b.client.Watch(ctx, func(tx *redis.Tx) error {
			now, err := tx.Time(ctx).Result()
			if err != nil {
				return err
			}
			state, err := loadRedisHistoryResetState(ctx, tx, key)
			if err != nil {
				return err
			}
			nowMS := now.UnixMilli()
			purgeRedisHistoryResetState(&state, nowMS)
			if lease.Scope.SessionID == "" {
				if state.Bot == nil || state.Bot.Token != strings.TrimSpace(lease.Token) {
					return nil
				}
				state.Bot = nil
			} else {
				record, found := state.Sessions[lease.Scope.SessionID]
				if !found || record.Token != strings.TrimSpace(lease.Token) {
					return nil
				}
				delete(state.Sessions, lease.Scope.SessionID)
			}
			if err := storeRedisHistoryResetState(ctx, tx, key, state, nowMS); err != nil {
				return err
			}
			released = true
			return nil
		}, key)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return released, err
	}
}

func (b *RedisBackend) EffectiveHistoryReset(ctx context.Context, scope ResetScope) (ResetLease, bool, error) {
	scope = scope.normalized()
	if !scope.valid() {
		return ResetLease{}, false, nil
	}
	now, err := b.client.Time(ctx).Result()
	if err != nil {
		return ResetLease{}, false, err
	}
	data, err := b.client.Get(ctx, b.historyResetKey(scope.BotID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return ResetLease{}, false, nil
	}
	if err != nil {
		return ResetLease{}, false, err
	}
	var state redisHistoryResetState
	if err := json.Unmarshal(data, &state); err != nil {
		return ResetLease{}, false, err
	}
	lease, blocked := effectiveRedisHistoryReset(state, scope.BotID, scope.SessionID, now.UnixMilli())
	if !blocked {
		return ResetLease{}, false, nil
	}
	return lease, true, nil
}

func (b *RedisBackend) RenewLease(ctx context.Context, key Key, runID, ownerID, generation string, renewedAt, expiresAt time.Time) error {
	runID = strings.TrimSpace(runID)
	ownerID = strings.TrimSpace(ownerID)
	generation = strings.TrimSpace(generation)
	if runID == "" || ownerID == "" || generation == "" {
		return nil
	}
	if renewedAt.IsZero() || !renewedAt.Before(expiresAt) {
		return ErrRunOwnershipLost
	}
	stateKey := b.stateKey(key)
	runKey := b.runKey(key, runID)
	for {
		stateData, err := b.client.Get(ctx, stateKey).Bytes()
		if errors.Is(err, redis.Nil) {
			return ErrRunOwnershipLost
		}
		if err != nil {
			return err
		}
		refData, err := b.client.Get(ctx, runKey).Bytes()
		if errors.Is(err, redis.Nil) {
			return ErrRunOwnershipLost
		}
		if err != nil {
			return err
		}
		var ref RunRef
		if err := json.Unmarshal(refData, &ref); err != nil {
			return err
		}
		if ref.OwnerID != ownerID || ref.BotID != key.BotID || ref.SessionID != key.SessionID || ref.RunID != runID || ref.Generation != generation {
			return ErrRunOwnershipLost
		}
		var snapshot Snapshot
		if err := json.Unmarshal(stateData, &snapshot); err != nil {
			return err
		}
		if snapshot.CurrentRunView == nil {
			return ErrRunOwnershipLost
		}
		run := snapshot.CurrentRunView
		if run.RunID != runID || run.OwnerID != ownerID || run.Generation != generation || run.OwnerLeaseExpiresAt == nil {
			return ErrRunOwnershipLost
		}
		if !isActiveRunStatus(run.Status) {
			return nil
		}
		run.OwnerLeaseExpiresAt = &expiresAt
		nextStateData, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		leaseMember := ""
		if ref.FencingToken > 0 {
			leaseMember = encodeLeaseIndexMember(key, runID, ref.FencingToken)
		}
		result, err := renewRedisLeaseScript.Run(ctx, b.client, []string{stateKey, runKey, b.leaseIndexKey()},
			stateData, refData, nextStateData, refData, expiresAt.UnixMilli(), b.stateTTL.Milliseconds(), leaseMember,
		).Int64()
		if err != nil {
			return err
		}
		switch result {
		case 1:
			return nil
		case 0:
			continue
		default:
			return ErrRunOwnershipLost
		}
	}
}

func (b *RedisBackend) ValidateRunOwnership(ctx context.Context, key Key, ref RunRef) error {
	ref.BotID = strings.TrimSpace(ref.BotID)
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.RunID = strings.TrimSpace(ref.RunID)
	ref.OwnerID = strings.TrimSpace(ref.OwnerID)
	ref.Generation = strings.TrimSpace(ref.Generation)
	if ref.BotID != strings.TrimSpace(key.BotID) || ref.SessionID != strings.TrimSpace(key.SessionID) || ref.RunID == "" || ref.OwnerID == "" || ref.Generation == "" {
		return ErrRunOwnershipLost
	}
	valid, err := validateRedisRunOwnershipScript.Run(ctx, b.client, []string{b.stateKey(key), b.runKey(key, ref.RunID)},
		ref.BotID, ref.SessionID, ref.RunID, ref.OwnerID, ref.Generation,
	).Int64()
	if err != nil {
		return err
	}
	if valid != 1 {
		return ErrRunOwnershipLost
	}
	return nil
}

func (b *RedisBackend) Publish(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, b.sessionChannel(Key{BotID: event.BotID, SessionID: event.SessionID}), data).Err()
}

func (b *RedisBackend) Subscribe(ctx context.Context, key Key) (Subscription, error) {
	subCtx, cancel := context.WithCancel(ctx)
	pubsub := b.client.Subscribe(subCtx, b.sessionChannel(key))
	if _, err := pubsub.Receive(subCtx); err != nil {
		cancel()
		_ = pubsub.Close()
		return Subscription{}, err
	}
	subscriptionID, registered := b.registerSubscription(cancel)
	if !registered {
		cancel()
		_ = pubsub.Close()
		return Subscription{}, errRedisBackendClosed
	}
	ch := make(chan Event, 64)
	done := make(chan struct{})
	go func() {
		defer func() {
			_ = pubsub.Close()
			cancel()
			close(ch)
			b.unregisterSubscription(subscriptionID)
			close(done)
		}()
		for {
			msg, err := pubsub.ReceiveMessage(subCtx)
			if err != nil {
				if subCtx.Err() != nil {
					return
				}
				enqueueRuntimeEvent(ch, Event{
					Type:      EventRuntimeDropped,
					BotID:     key.BotID,
					SessionID: key.SessionID,
					Message:   "runtime pubsub receive interrupted",
				})
				if !waitRedisSubscriptionRetry(subCtx) {
					return
				}
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				enqueueRuntimeEvent(ch, Event{
					Type:      EventRuntimeDropped,
					BotID:     key.BotID,
					SessionID: key.SessionID,
					Message:   "runtime pubsub event is invalid",
				})
				continue
			}
			enqueueRuntimeEvent(ch, event)
		}
	}()
	var closeOnce sync.Once
	return Subscription{
		C: ch,
		Close: func() {
			closeOnce.Do(func() {
				cancel()
				_ = pubsub.Close()
			})
			<-done
		},
	}, nil
}

func (b *RedisBackend) LoadRunRef(ctx context.Context, key Key, runID string) (RunRef, bool, error) {
	data, err := b.client.Get(ctx, b.runKey(key, runID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return RunRef{}, false, nil
	}
	if err != nil {
		return RunRef{}, false, err
	}
	var ref RunRef
	if err := json.Unmarshal(data, &ref); err != nil {
		return RunRef{}, false, err
	}
	return ref, true, nil
}

func (b *RedisBackend) DeleteRunRef(ctx context.Context, ref RunRef) (bool, error) {
	ref.BotID = strings.TrimSpace(ref.BotID)
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.RunID = strings.TrimSpace(ref.RunID)
	ref.OwnerID = strings.TrimSpace(ref.OwnerID)
	ref.Generation = strings.TrimSpace(ref.Generation)
	if ref.RunID == "" || ref.Generation == "" {
		return false, nil
	}
	key := Key{BotID: ref.BotID, SessionID: ref.SessionID}
	deleted, err := deleteRedisRunRefScript.Run(ctx, b.client,
		[]string{b.runKey(key, ref.RunID), b.leaseIndexKey()},
		ref.BotID, ref.SessionID, ref.RunID, ref.OwnerID, ref.Generation,
	).Int64()
	return deleted == 1, err
}

func (b *RedisBackend) PublishCommand(ctx context.Context, ownerID string, command Command) error {
	data, err := json.Marshal(command)
	if err != nil {
		return err
	}
	count, err := b.client.Publish(ctx, b.commandChannel(ownerID), data).Result()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrCommandOwnerUnavailable
	}
	return nil
}

func (b *RedisBackend) SubscribeCommands(ctx context.Context, ownerID string) (CommandSubscription, error) {
	subCtx, cancel := context.WithCancel(ctx)
	pubsub := b.client.Subscribe(subCtx, b.commandChannel(ownerID))
	if _, err := pubsub.Receive(subCtx); err != nil {
		cancel()
		_ = pubsub.Close()
		return CommandSubscription{}, err
	}
	subscriptionID, registered := b.registerSubscription(cancel)
	if !registered {
		cancel()
		_ = pubsub.Close()
		return CommandSubscription{}, errRedisBackendClosed
	}
	ch := make(chan Command, 64)
	done := make(chan struct{})
	go func() {
		defer func() {
			_ = pubsub.Close()
			cancel()
			close(ch)
			b.unregisterSubscription(subscriptionID)
			close(done)
		}()
		for {
			msg, err := pubsub.ReceiveMessage(subCtx)
			if err != nil {
				if !waitRedisSubscriptionRetry(subCtx) {
					return
				}
				continue
			}
			var command Command
			if err := json.Unmarshal([]byte(msg.Payload), &command); err != nil {
				continue
			}
			select {
			case ch <- command:
			case <-subCtx.Done():
				return
			}
		}
	}()
	var closeOnce sync.Once
	return CommandSubscription{
		C: ch,
		Close: func() {
			closeOnce.Do(func() {
				cancel()
				_ = pubsub.Close()
			})
			<-done
		},
	}, nil
}

func (b *RedisBackend) StoreCommandResult(ctx context.Context, result Command, ttl time.Duration) error {
	commandID := strings.TrimSpace(result.ID)
	if strings.TrimSpace(result.Type) != CommandResult || commandID == "" {
		return errors.New("command result type and id are required")
	}
	if ttl <= 0 {
		return errors.New("command result ttl must be positive")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return b.client.SetNX(ctx, b.commandResultKey(commandID), data, ttl).Err()
}

func (b *RedisBackend) LoadCommandResult(ctx context.Context, commandID string) (Command, bool, error) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return Command{}, false, nil
	}
	data, err := b.client.Get(ctx, b.commandResultKey(commandID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Command{}, false, nil
	}
	if err != nil {
		return Command{}, false, err
	}
	var result Command
	if err := json.Unmarshal(data, &result); err != nil {
		return Command{}, false, err
	}
	if strings.TrimSpace(result.Type) != CommandResult || strings.TrimSpace(result.ID) != commandID {
		return Command{}, false, errors.New("stored runtime command result is invalid")
	}
	return result, true, nil
}

func (b *RedisBackend) Close() error {
	b.closeOnce.Do(func() {
		b.subscriptionsMu.Lock()
		b.closed = true
		cancels := make([]context.CancelFunc, 0, len(b.subscriptions))
		for _, cancel := range b.subscriptions {
			cancels = append(cancels, cancel)
		}
		b.subscriptionsMu.Unlock()

		for _, cancel := range cancels {
			cancel()
		}
		b.closeErr = b.client.Close()
		b.subscriptionsWG.Wait()
		close(b.closeDone)
	})
	<-b.closeDone
	return b.closeErr
}

func loadRedisSnapshot(ctx context.Context, tx *redis.Tx, key string) (Snapshot, bool, error) {
	data, err := tx.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, false, err
	}
	return snapshot, true, nil
}

func waitRedisSubscriptionRetry(ctx context.Context) bool {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func loadRedisRunRef(ctx context.Context, tx *redis.Tx, key string) (RunRef, bool, error) {
	data, err := tx.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return RunRef{}, false, nil
	}
	if err != nil {
		return RunRef{}, false, err
	}
	var ref RunRef
	if err := json.Unmarshal(data, &ref); err != nil {
		return RunRef{}, false, err
	}
	return ref, true, nil
}

func loadRedisHistoryResetState(ctx context.Context, tx *redis.Tx, key string) (redisHistoryResetState, error) {
	data, err := tx.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return redisHistoryResetState{}, nil
	}
	if err != nil {
		return redisHistoryResetState{}, err
	}
	var state redisHistoryResetState
	if err := json.Unmarshal(data, &state); err != nil {
		return redisHistoryResetState{}, err
	}
	return state, nil
}

func storeRedisHistoryResetState(ctx context.Context, tx *redis.Tx, key string, state redisHistoryResetState, nowMS int64) error {
	maxExpiry := int64(0)
	if state.Bot != nil {
		maxExpiry = state.Bot.ExpiresAtMS
	}
	for _, record := range state.Sessions {
		if record.ExpiresAtMS > maxExpiry {
			maxExpiry = record.ExpiresAtMS
		}
	}
	if maxExpiry <= nowMS {
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, key)
			return nil
		})
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, key, data, time.Duration(maxExpiry-nowMS)*time.Millisecond)
		return nil
	})
	return err
}

func purgeRedisHistoryResetState(state *redisHistoryResetState, nowMS int64) {
	if state == nil {
		return
	}
	if state.Bot != nil && state.Bot.ExpiresAtMS <= nowMS {
		state.Bot = nil
	}
	for sessionID, record := range state.Sessions {
		if record.ExpiresAtMS <= nowMS {
			delete(state.Sessions, sessionID)
		}
	}
}

// effectiveRedisHistoryReset decodes the per-bot state blob into leases and
// applies the shared effectiveResetLease precedence rule, so the Redis reader
// cannot drift from the memory and PostgreSQL readers.
func effectiveRedisHistoryReset(state redisHistoryResetState, botID, sessionID string, nowMS int64) (ResetLease, bool) {
	var bot *ResetLease
	if state.Bot != nil && state.Bot.ExpiresAtMS > nowMS {
		lease := ResetLease{
			Scope: ResetScope{BotID: botID}, Token: state.Bot.Token,
			ExpiresAt: time.UnixMilli(state.Bot.ExpiresAtMS).UTC(),
		}
		bot = &lease
	}
	sessions := make([]ResetLease, 0, len(state.Sessions))
	for id, record := range state.Sessions {
		if record.ExpiresAtMS > nowMS {
			sessions = append(sessions, ResetLease{
				Scope: ResetScope{BotID: botID, SessionID: id}, Token: record.Token,
				ExpiresAt: time.UnixMilli(record.ExpiresAtMS).UTC(),
			})
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Scope.SessionID < sessions[j].Scope.SessionID
	})
	return effectiveResetLease(ResetScope{BotID: botID, SessionID: sessionID}, bot, sessions)
}

func (b *RedisBackend) stateKey(key Key) string {
	return b.keyPrefix + "state:" + key.String()
}

func (b *RedisBackend) runKey(key Key, runID string) string {
	return b.keyPrefix + "run:" + key.String() + ":" + strings.TrimSpace(runID)
}

func (b *RedisBackend) sessionChannel(key Key) string {
	return b.keyPrefix + "events:" + key.String()
}

func (b *RedisBackend) commandChannel(ownerID string) string {
	return b.keyPrefix + "commands:" + strings.TrimSpace(ownerID)
}

func (b *RedisBackend) commandResultKey(commandID string) string {
	return b.keyPrefix + "command_result:" + strings.TrimSpace(commandID)
}

func (b *RedisBackend) historyResetKey(botID string) string {
	return b.keyPrefix + "history_reset:" + strings.TrimSpace(botID)
}
