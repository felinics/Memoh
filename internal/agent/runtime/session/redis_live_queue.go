package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const redisQueueMaxRetries = 8

func (b *RedisBackend) ensureQueueOpen() error {
	if b == nil || b.client == nil {
		return ErrLiveQueueUnavailable
	}
	b.subscriptionsMu.Lock()
	closed := b.closed
	b.subscriptionsMu.Unlock()
	if closed {
		return ErrLiveQueueUnavailable
	}
	return nil
}

func waitRedisQueueRetry(ctx context.Context, attempt int) error {
	if attempt >= redisQueueMaxRetries {
		return ErrQueueAdmissionOverloaded
	}
	delay := time.Duration(1<<min(attempt, 6)) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func redisWatch(ctx context.Context, b *RedisBackend, keys []string, fn func(*redis.Tx) error) error {
	if err := b.ensureQueueOpen(); err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err := b.client.Watch(ctx, fn, keys...)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
		if err := waitRedisQueueRetry(ctx, attempt); err != nil {
			return err
		}
		if err := b.ensureQueueOpen(); err != nil {
			return err
		}
	}
}

func loadRedisJSON[T any](ctx context.Context, cmd redis.Cmdable, key string) (T, error) {
	var value T
	data, err := cmd.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	return value, json.Unmarshal(data, &value)
}

func storeRedisJSON(ctx context.Context, tx *redis.Tx, key string, value any, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, key, data, ttl)
		return nil
	})
	return err
}

// compactableQueueState is implemented by both queue documents so the generic
// mutation helpers can bound terminal items before writing the document back.
type compactableQueueState interface{ compact() }

func redisMutate[T any, R any](ctx context.Context, b *RedisBackend, key string, mutate func(*T, time.Time) (R, error)) (R, error) {
	return redisMutateWithKeys(ctx, b, []string{key}, key, func(_ *redis.Tx, state *T, now time.Time) (R, error) {
		return mutate(state, now)
	})
}

// redisMutateWithKeys runs one optimistic transaction over a queue document.
// keys is the WATCH set; the document itself must be part of it. Callers may
// read other keys inside mutate without watching them: the queue document is
// the serialization point for queue decisions, and run ownership is watched
// through the run key where a decision depends on it. The session state key is
// deliberately never watched here because it is rewritten on every streamed
// runtime delta and would make queue transactions fail under normal output.
func redisMutateWithKeys[T any, R any](ctx context.Context, b *RedisBackend, keys []string, key string, mutate func(*redis.Tx, *T, time.Time) (R, error)) (R, error) {
	var zero R
	if err := b.ensureQueueOpen(); err != nil {
		return zero, err
	}
	var result R
	err := redisWatch(ctx, b, keys, func(tx *redis.Tx) error {
		state, err := loadRedisJSON[T](ctx, tx, key)
		if err != nil {
			return err
		}
		now, err := tx.Time(ctx).Result()
		if err != nil {
			return err
		}
		result, err = mutate(tx, &state, now.UTC())
		if err != nil {
			return err
		}
		if compactable, ok := any(&state).(compactableQueueState); ok {
			compactable.compact()
		}
		return storeRedisJSON(ctx, tx, key, state, b.stateTTL)
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}

func (b *RedisBackend) EnqueueSteer(ctx context.Context, key Key, itemID, invocationID string, payload []byte) (SteerItem, error) {
	if err := b.ensureQueueOpen(); err != nil {
		return SteerItem{}, err
	}
	if err := validateQueueKey(key); err != nil || strings.TrimSpace(itemID) == "" || strings.TrimSpace(invocationID) == "" || validatePayload(payload) != nil {
		return SteerItem{}, ErrQueueInvalidReference
	}
	stateKey, queueKey := b.stateKey(key), b.steerQueueKey(key)
	var item SteerItem
	// The state key is read but not watched: a run that terminalizes between
	// this read and EXEC is closed by CloseSteerRun, which serializes on the
	// queue key and rejects the item or has already sealed the run ID.
	err := redisWatch(ctx, b, []string{queueKey}, func(tx *redis.Tx) error {
		queue, err := loadRedisJSON[steerQueueState](ctx, tx, queueKey)
		if err != nil {
			return err
		}
		if replay, ok, replayErr := replaySteer(queue, invocationID, payload); ok {
			item = replay
			return replayErr
		}
		snapshot, ok, err := loadRedisSnapshot(ctx, tx, stateKey)
		if err != nil {
			return err
		}
		run, active := activeRun(snapshot, ok)
		if !active || queue.ClosedRunID == run.RunID {
			return ErrQueueNoActiveRun
		}
		if !SteerRunAvailable(run) {
			return ErrQueueSteerUnsupported
		}
		now, err := tx.Time(ctx).Result()
		if err != nil {
			return err
		}
		item, err = queue.enqueue(key, itemID, invocationID, run.RunID, payload, now.UTC())
		if err != nil {
			return err
		}
		return storeRedisJSON(ctx, tx, queueKey, queue, b.stateTTL)
	})
	return item, err
}

func (b *RedisBackend) EnqueueFollowUp(ctx context.Context, key Key, itemID, invocationID string, payload []byte) (FollowUpItem, error) {
	if err := b.ensureQueueOpen(); err != nil {
		return FollowUpItem{}, err
	}
	if err := validateQueueKey(key); err != nil || strings.TrimSpace(itemID) == "" || strings.TrimSpace(invocationID) == "" || validatePayload(payload) != nil {
		return FollowUpItem{}, ErrQueueInvalidReference
	}
	stateKey, queueKey := b.stateKey(key), b.followUpQueueKey(key)
	var item FollowUpItem
	// The state key is read but not watched; the application re-checks for an
	// active run after a successful enqueue and starts the follow-up itself
	// when the terminal observer has already passed.
	err := redisWatch(ctx, b, []string{queueKey}, func(tx *redis.Tx) error {
		queue, err := loadRedisJSON[followUpQueueState](ctx, tx, queueKey)
		if err != nil {
			return err
		}
		if replay, ok, replayErr := replayFollowUp(queue, invocationID, payload); ok {
			item = replay
			return replayErr
		}
		snapshot, ok, err := loadRedisSnapshot(ctx, tx, stateKey)
		if err != nil {
			return err
		}
		run, active := activeRun(snapshot, ok)
		if !active {
			return ErrQueueNoActiveRun
		}
		now, err := tx.Time(ctx).Result()
		if err != nil {
			return err
		}
		item, err = queue.enqueue(key, itemID, invocationID, run.RunID, payload, now.UTC())
		if err != nil {
			return err
		}
		return storeRedisJSON(ctx, tx, queueKey, queue, b.stateTTL)
	})
	return item, err
}

func (b *RedisBackend) PendingQueues(ctx context.Context, key Key, limit int) ([]SteerItem, []FollowUpItem, error) {
	if err := b.ensureQueueOpen(); err != nil {
		return nil, nil, err
	}
	if err := validateQueueKey(key); err != nil {
		return nil, nil, err
	}
	pipe := b.client.Pipeline()
	steerCmd := pipe.Get(ctx, b.steerQueueKey(key))
	followCmd := pipe.Get(ctx, b.followUpQueueKey(key))
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, nil, err
	}
	var steers steerQueueState
	if data, getErr := steerCmd.Bytes(); getErr == nil {
		if err := json.Unmarshal(data, &steers); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(getErr, redis.Nil) {
		return nil, nil, getErr
	}
	var follows followUpQueueState
	if data, getErr := followCmd.Bytes(); getErr == nil {
		if err := json.Unmarshal(data, &follows); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(getErr, redis.Nil) {
		return nil, nil, getErr
	}
	return pendingSteers(steers, limit), pendingFollowUps(follows, limit), nil
}

func (b *RedisBackend) ReorderSteer(ctx context.Context, key Key, item, before SteerPendingRef) ([]SteerItem, error) {
	if err := validateQueueKey(key); err != nil {
		return nil, err
	}
	return redisMutate(ctx, b, b.steerQueueKey(key), func(state *steerQueueState, now time.Time) ([]SteerItem, error) {
		items, err := reorderSteerState(state, item, before)
		state.UpdatedAt = now
		return items, err
	})
}

func (b *RedisBackend) ReorderFollowUp(ctx context.Context, key Key, item, before FollowUpPendingRef) ([]FollowUpItem, error) {
	if err := validateQueueKey(key); err != nil {
		return nil, err
	}
	return redisMutate(ctx, b, b.followUpQueueKey(key), func(state *followUpQueueState, now time.Time) ([]FollowUpItem, error) {
		items, err := reorderFollowUpState(state, item, before)
		state.UpdatedAt = now
		return items, err
	})
}

func (b *RedisBackend) UpdateSteer(ctx context.Context, key Key, itemID SteerItemID, payload []byte) (SteerItem, error) {
	if err := validateQueueKey(key); err != nil || itemID == "" || validatePayload(payload) != nil {
		return SteerItem{}, ErrQueueInvalidReference
	}
	return redisMutate(ctx, b, b.steerQueueKey(key), func(state *steerQueueState, now time.Time) (SteerItem, error) {
		return state.edit(itemID, payload, now)
	})
}

func (b *RedisBackend) UpdateFollowUp(ctx context.Context, key Key, itemID FollowUpItemID, payload []byte) (FollowUpItem, error) {
	if err := validateQueueKey(key); err != nil || itemID == "" || validatePayload(payload) != nil {
		return FollowUpItem{}, ErrQueueInvalidReference
	}
	return redisMutate(ctx, b, b.followUpQueueKey(key), func(state *followUpQueueState, now time.Time) (FollowUpItem, error) {
		return state.edit(itemID, payload, now)
	})
}

func (b *RedisBackend) CancelSteer(ctx context.Context, key Key, itemID SteerItemID) error {
	if err := validateQueueKey(key); err != nil || itemID == "" {
		return ErrQueueInvalidReference
	}
	_, err := redisMutate(ctx, b, b.steerQueueKey(key), func(state *steerQueueState, now time.Time) (struct{}, error) {
		return struct{}{}, state.cancel(itemID, now)
	})
	return err
}

func (b *RedisBackend) CancelFollowUp(ctx context.Context, key Key, itemID FollowUpItemID) error {
	if err := validateQueueKey(key); err != nil || itemID == "" {
		return ErrQueueInvalidReference
	}
	_, err := redisMutate(ctx, b, b.followUpQueueKey(key), func(state *followUpQueueState, now time.Time) (struct{}, error) {
		return struct{}{}, state.cancel(itemID, now)
	})
	return err
}

func (b *RedisBackend) PromoteFollowUpToSteer(ctx context.Context, key Key, ref FollowUpPendingRef) (PromoteFollowUpResult, error) {
	if err := b.ensureQueueOpen(); err != nil {
		return PromoteFollowUpResult{}, err
	}
	stateKey, steerKey, followKey := b.stateKey(key), b.steerQueueKey(key), b.followUpQueueKey(key)
	if err := validateQueueKey(key); err != nil || ref.ItemID == "" {
		return PromoteFollowUpResult{}, ErrQueueInvalidReference
	}
	var result PromoteFollowUpResult
	err := redisWatch(ctx, b, []string{steerKey, followKey}, func(tx *redis.Tx) error {
		snapshot, ok, err := loadRedisSnapshot(ctx, tx, stateKey)
		if err != nil {
			return err
		}
		run, active := activeRun(snapshot, ok)
		steers, err := loadRedisJSON[steerQueueState](ctx, tx, steerKey)
		if err != nil {
			return err
		}
		if !active || steers.ClosedRunID == run.RunID {
			return ErrQueueNoActiveRun
		}
		if !SteerRunAvailable(run) {
			return ErrQueueSteerUnsupported
		}
		follows, err := loadRedisJSON[followUpQueueState](ctx, tx, followKey)
		if err != nil {
			return err
		}
		now, err := tx.Time(ctx).Result()
		if err != nil {
			return err
		}
		result, err = steers.promote(&follows, key, run.RunID, ref, now.UTC(), uuid.NewString())
		if err != nil {
			return err
		}
		steerData, err := json.Marshal(steers)
		if err != nil {
			return err
		}
		followData, err := json.Marshal(follows)
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, steerKey, steerData, b.stateTTL)
			pipe.Set(ctx, followKey, followData, b.stateTTL)
			return nil
		})
		return err
	})
	return result, err
}

func (b *RedisBackend) ClaimNextSteer(ctx context.Context, handle RunHandle, sealIfEmpty bool) (SteerItem, SteerClaimRef, bool, error) {
	if err := b.ensureQueueOpen(); err != nil {
		return SteerItem{}, SteerClaimRef{}, false, err
	}
	handle = handle.normalized()
	if !handle.valid() || handle.OwnerID == "" || handle.FencingToken <= 0 {
		return SteerItem{}, SteerClaimRef{}, false, ErrQueueInvalidReference
	}
	key := handle.key()
	stateKey, runKey, queueKey := b.stateKey(key), b.runKey(key, handle.RunID), b.steerQueueKey(key)
	var item SteerItem
	var claim SteerClaimRef
	var claimed bool
	// Ownership is watched through the run key, which the finishing owner
	// deletes; the state key is only read for the projected run status.
	err := redisWatch(ctx, b, []string{runKey, queueKey}, func(tx *redis.Tx) error {
		snapshot, ok, err := loadRedisSnapshot(ctx, tx, stateKey)
		if err != nil {
			return err
		}
		ref, refOK, err := loadRedisRunRef(ctx, tx, runKey)
		if err != nil {
			return err
		}
		if !ok || !refOK || !runMatchesHandle(snapshot.CurrentRunView, handle) || ref.FencingToken != handle.FencingToken || ref.OwnerID != handle.OwnerID || ref.Generation != handle.Generation || !isActiveRunStatus(snapshot.CurrentRunView.Status) {
			return ErrRunOwnershipLost
		}
		if !SteerRunAvailable(snapshot.CurrentRunView) {
			return ErrQueueSteerUnsupported
		}
		state, err := loadRedisJSON[steerQueueState](ctx, tx, queueKey)
		if err != nil {
			return err
		}
		now, err := tx.Time(ctx).Result()
		if err != nil {
			return err
		}
		item, claim, claimed = state.claimNext(handle, sealIfEmpty, now.UTC())
		return storeRedisJSON(ctx, tx, queueKey, state, b.stateTTL)
	})
	return item, claim, claimed, err
}

func (b *RedisBackend) ApplySteer(ctx context.Context, key Key, ref SteerClaimRef) error {
	if err := validateSteerClaim(key, ref); err != nil {
		return err
	}
	stateKey, runKey, queueKey := b.stateKey(key), b.runKey(key, ref.RunID), b.steerQueueKey(key)
	_, err := redisMutateWithKeys(ctx, b, []string{runKey, queueKey}, queueKey, func(tx *redis.Tx, state *steerQueueState, now time.Time) (struct{}, error) {
		snapshot, ok, err := loadRedisSnapshot(ctx, tx, stateKey)
		if err != nil {
			return struct{}{}, err
		}
		run, runOK, err := loadRedisRunRef(ctx, tx, runKey)
		if err != nil {
			return struct{}{}, err
		}
		if !ok || !runOK || !runMatchesSteerClaim(snapshot.CurrentRunView, ref) || run.FencingToken != ref.FencingToken || run.OwnerID != ref.OwnerID || run.Generation != ref.Generation {
			return struct{}{}, ErrRunOwnershipLost
		}
		return struct{}{}, state.apply(ref, now)
	})
	return err
}

func (b *RedisBackend) CloseSteerRun(ctx context.Context, key Key, runID string) error {
	runID = strings.TrimSpace(runID)
	if err := validateQueueKey(key); err != nil || runID == "" {
		return ErrQueueInvalidReference
	}
	_, err := redisMutate(ctx, b, b.steerQueueKey(key), func(state *steerQueueState, now time.Time) (struct{}, error) {
		closeSteerRun(state, runID, now)
		return struct{}{}, nil
	})
	return err
}

func (b *RedisBackend) ReleaseSteer(ctx context.Context, key Key, ref SteerClaimRef) error {
	if err := validateSteerClaim(key, ref); err != nil {
		return err
	}
	stateKey, runKey, queueKey := b.stateKey(key), b.runKey(key, ref.RunID), b.steerQueueKey(key)
	_, err := redisMutateWithKeys(ctx, b, []string{runKey, queueKey}, queueKey, func(tx *redis.Tx, state *steerQueueState, now time.Time) (struct{}, error) {
		snapshot, ok, err := loadRedisSnapshot(ctx, tx, stateKey)
		if err != nil {
			return struct{}{}, err
		}
		run, runOK, err := loadRedisRunRef(ctx, tx, runKey)
		if err != nil {
			return struct{}{}, err
		}
		if !ok || !runOK || !runMatchesSteerClaim(snapshot.CurrentRunView, ref) || run.FencingToken != ref.FencingToken || run.OwnerID != ref.OwnerID || run.Generation != ref.Generation {
			return struct{}{}, ErrRunOwnershipLost
		}
		return struct{}{}, state.release(ref, now)
	})
	return err
}

func (b *RedisBackend) ClaimNextFollowUp(ctx context.Context, key Key, triggerRunID string) (FollowUpItem, FollowUpClaimRef, bool, error) {
	if err := b.ensureQueueOpen(); err != nil {
		return FollowUpItem{}, FollowUpClaimRef{}, false, err
	}
	if err := validateQueueKey(key); err != nil {
		return FollowUpItem{}, FollowUpClaimRef{}, false, err
	}
	triggerRunID = strings.TrimSpace(triggerRunID)
	if triggerRunID == "" {
		return FollowUpItem{}, FollowUpClaimRef{}, false, ErrQueueInvalidReference
	}
	type result struct {
		item    FollowUpItem
		claim   FollowUpClaimRef
		claimed bool
	}
	claimed, err := redisMutate(ctx, b, b.followUpQueueKey(key), func(state *followUpQueueState, now time.Time) (result, error) {
		item, claim, ok := state.claimNext(triggerRunID, now)
		return result{item: item, claim: claim, claimed: ok}, nil
	})
	return claimed.item, claimed.claim, claimed.claimed, err
}

func (b *RedisBackend) ApplyFollowUp(ctx context.Context, key Key, ref FollowUpClaimRef) error {
	if err := validateFollowUpClaim(key, ref); err != nil {
		return err
	}
	_, err := redisMutate(ctx, b, b.followUpQueueKey(key), func(state *followUpQueueState, now time.Time) (struct{}, error) {
		return struct{}{}, state.apply(ref, now)
	})
	return err
}

func (b *RedisBackend) ReleaseFollowUp(ctx context.Context, key Key, ref FollowUpClaimRef) error {
	if err := validateFollowUpClaim(key, ref); err != nil {
		return err
	}
	_, err := redisMutate(ctx, b, b.followUpQueueKey(key), func(state *followUpQueueState, now time.Time) (struct{}, error) {
		return struct{}{}, state.release(ref, now)
	})
	return err
}

func (b *RedisBackend) RejectFollowUp(ctx context.Context, key Key, ref FollowUpClaimRef, errorCode string) error {
	if err := validateFollowUpClaim(key, ref); err != nil {
		return err
	}
	if strings.TrimSpace(errorCode) == "" {
		return ErrQueueInvalidReference
	}
	_, err := redisMutate(ctx, b, b.followUpQueueKey(key), func(state *followUpQueueState, now time.Time) (struct{}, error) {
		return struct{}{}, state.reject(ref, errorCode, now)
	})
	return err
}

func (b *RedisBackend) steerQueueKey(key Key) string {
	return b.keyPrefix + "steer_queue:" + key.String()
}

func (b *RedisBackend) followUpQueueKey(key Key) string {
	return b.keyPrefix + "follow_up_queue:" + key.String()
}

var _ LiveQueueBackend = (*RedisBackend)(nil)
