package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// The log is a Redis list plus a small hash of markers. One append is one RPUSH
// and two HSETs evaluated atomically; the list is never rewritten. Both keys
// carry the live-state TTL so an abandoned log expires with the run state.
//
// Result codes: 0 applied, 1 already terminal, 2 replayed seq, 3 sequence gap,
// 4 limit exceeded (log marked failed).
var appendRedisDecisionOutputScript = redis.NewScript(`
local len = redis.call('LLEN', KEYS[1])
local done = redis.call('HGET', KEYS[2], 'done') == '1'
local failed = redis.call('HGET', KEYS[2], 'failed') == '1'
local bytes = tonumber(redis.call('HGET', KEYS[2], 'bytes') or '0')
local seq = tonumber(ARGV[1])
local max_bytes = tonumber(ARGV[4])
local max_events = tonumber(ARGV[5])
local code
if done or failed then
  code = 1
elseif seq <= len then
  code = 2
elseif seq ~= len + 1 then
  code = 3
elseif ARGV[3] == '1' then
  redis.call('HSET', KEYS[2], 'done', '1')
  done = true
  code = 0
elseif (max_bytes > 0 and bytes + #ARGV[2] > max_bytes) or (max_events > 0 and len >= max_events) then
  redis.call('HSET', KEYS[2], 'failed', '1')
  failed = true
  code = 4
else
  redis.call('RPUSH', KEYS[1], ARGV[2])
  len = len + 1
  bytes = bytes + #ARGV[2]
  redis.call('HSET', KEYS[2], 'bytes', bytes)
  code = 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[6])
redis.call('PEXPIRE', KEYS[2], ARGV[6])
local claimed = redis.call('HGET', KEYS[2], 'claimed') == '1'
return {code, len, bytes, done and 1 or 0, failed and 1 or 0, claimed and 1 or 0}
`)

func (b *RedisBackend) decisionOutputListKey(ref DecisionOutputRef) string {
	return b.keyPrefix + "decision_output:" + ref.String()
}

func (b *RedisBackend) decisionOutputMetaKey(ref DecisionOutputRef) string {
	return b.keyPrefix + "decision_output_meta:" + ref.String()
}

func (b *RedisBackend) AppendDecisionOutput(ctx context.Context, ref DecisionOutputRef, seq int64, payload json.RawMessage, limits DecisionOutputLimits) (DecisionOutputState, error) {
	closing := "0"
	if payload == nil {
		closing = "1"
	}
	raw, err := appendRedisDecisionOutputScript.Run(ctx, b.client,
		[]string{b.decisionOutputListKey(ref), b.decisionOutputMetaKey(ref)},
		seq, string(payload), closing, limits.MaxBytes, limits.MaxEvents, b.stateTTL.Milliseconds(),
	).Int64Slice()
	if err != nil {
		return DecisionOutputState{}, err
	}
	if len(raw) != 6 {
		return DecisionOutputState{}, fmt.Errorf("decision output append returned %d fields", len(raw))
	}
	state := DecisionOutputState{
		Exists: true, Length: int(raw[1]), Bytes: int(raw[2]),
		Done: raw[3] == 1, Failed: raw[4] == 1, Claimed: raw[5] == 1,
	}
	switch raw[0] {
	case 0:
		state.Applied = true
	case 3:
		return state, ErrDecisionOutputSequenceGap
	case 4:
		state.Applied, state.Exceeded = true, true
	}
	return state, nil
}

func (b *RedisBackend) ReadDecisionOutput(ctx context.Context, ref DecisionOutputRef, from int) (DecisionOutputPage, error) {
	if from < 0 {
		from = 0
	}
	listKey, metaKey := b.decisionOutputListKey(ref), b.decisionOutputMetaKey(ref)
	var (
		length *redis.IntCmd
		meta   *redis.MapStringStringCmd
		events *redis.StringSliceCmd
	)
	// MULTI/EXEC keeps length, markers and the tail consistent with each other;
	// a reader must never see a Done marker without the entries before it.
	if _, err := b.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		length = pipe.LLen(ctx, listKey)
		meta = pipe.HGetAll(ctx, metaKey)
		events = pipe.LRange(ctx, listKey, int64(from), -1)
		return nil
	}); err != nil {
		return DecisionOutputPage{}, err
	}
	fields := meta.Val()
	page := DecisionOutputPage{DecisionOutputState: DecisionOutputState{
		Exists:  length.Val() > 0 || len(fields) > 0,
		Length:  int(length.Val()),
		Done:    fields["done"] == "1",
		Failed:  fields["failed"] == "1",
		Claimed: fields["claimed"] == "1",
	}}
	if bytes, err := strconv.Atoi(fields["bytes"]); err == nil {
		page.Bytes = bytes
	}
	for _, raw := range events.Val() {
		page.Events = append(page.Events, json.RawMessage(raw))
	}
	return page, nil
}

func (b *RedisBackend) ClaimDecisionOutput(ctx context.Context, ref DecisionOutputRef) (bool, error) {
	metaKey := b.decisionOutputMetaKey(ref)
	var claimed *redis.BoolCmd
	if _, err := b.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		claimed = pipe.HSetNX(ctx, metaKey, "claimed", "1")
		pipe.PExpire(ctx, metaKey, b.stateTTL)
		return nil
	}); err != nil {
		return false, err
	}
	return claimed.Val(), nil
}

func (b *RedisBackend) ReleaseDecisionOutput(ctx context.Context, ref DecisionOutputRef) error {
	// The meta hash keeps its own TTL; only the entries are reclaimed.
	return b.client.Del(ctx, b.decisionOutputListKey(ref)).Err()
}
