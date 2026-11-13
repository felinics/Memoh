package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var initRedisDecisionOutputScript = redis.NewScript(`
redis.call('HSETNX', KEYS[1], 'init', '1')
for i, k in ipairs({'session_id', 'run_id', 'generation'}) do if ARGV[i+1] ~= '' then redis.call('HSETNX', KEYS[1], k, ARGV[i+1]) end end
redis.call('PEXPIRE', KEYS[1], ARGV[1]); return 1`)

var discardRedisDecisionOutputScript = redis.NewScript(`
local t = redis.call('TIME'); local now_ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local until_ms = tonumber(redis.call('HGET', KEYS[2], 'claim_until_ms') or '0')
local active = redis.call('HGET', KEYS[2], 'claimed') == '1' and (until_ms == 0 or now_ms < until_ms)
if redis.call('LLEN', KEYS[1]) == 0 and redis.call('HGET', KEYS[2], 'done') ~= '1' and redis.call('HGET', KEYS[2], 'failed') ~= '1' and redis.call('HGET', KEYS[2], 'released') ~= '1' and not active then
  redis.call('DEL', KEYS[2]); return 1
end
return 0`)

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
if len > 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[6])
end
redis.call('PEXPIRE', KEYS[2], ARGV[6])
local claimed = redis.call('HGET', KEYS[2], 'claimed') == '1'
local released = redis.call('HGET', KEYS[2], 'released') == '1'
return {code, len, bytes, done and 1 or 0, failed and 1 or 0, claimed and 1 or 0, released and 1 or 0}
`)

// Result codes: 0 unavailable (missing/released/split-TTL corrupt), 1 acquired, 2 busy.
var claimRedisDecisionOutputScript = redis.NewScript(`
local len = redis.call('LLEN', KEYS[1])
if (len == 0 and redis.call('EXISTS', KEYS[2]) == 0) or redis.call('HGET', KEYS[2], 'released') == '1' then return 0 end
if (redis.call('HGET', KEYS[2], 'done') == '1' or redis.call('HGET', KEYS[2], 'failed') == '1') and tonumber(redis.call('HGET', KEYS[2], 'bytes') or '0') > 0 and len == 0 then return 0 end
local t = redis.call('TIME'); local now_ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local cur, until_ms = redis.call('HGET', KEYS[2], 'claim_token') or '', tonumber(redis.call('HGET', KEYS[2], 'claim_until_ms') or '0')
if redis.call('HGET', KEYS[2], 'claimed') == '1' and (until_ms == 0 or now_ms < until_ms) and (cur == '' or cur ~= ARGV[1]) then return 2 end
local lease = tonumber(ARGV[3]) or 0
redis.call('HSET', KEYS[2], 'claimed', '1', 'claim_token', ARGV[1], 'claim_until_ms', tostring(lease > 0 and (now_ms + lease) or 0))
redis.call('PEXPIRE', KEYS[2], ARGV[2]); if len > 0 then redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return 1`)

// ARGV[3] == '-1' unclaims; ARGV[3] >= '0' renews an active claim.
var mutateClaimRedisDecisionOutputScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 0 or redis.call('HGET', KEYS[2], 'released') == '1' or (redis.call('HGET', KEYS[2], 'claim_token') or '') ~= ARGV[1] then return 0 end
local lease = tonumber(ARGV[3]) or 0
if lease < 0 then
  redis.call('HSET', KEYS[2], 'claimed', '0', 'claim_token', '', 'claim_until_ms', '0')
elseif redis.call('HGET', KEYS[2], 'claimed') ~= '1' then
  return 0
elseif lease > 0 then
  local t = redis.call('TIME')
  redis.call('HSET', KEYS[2], 'claim_until_ms', tostring(tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000) + lease))
end
redis.call('PEXPIRE', KEYS[2], ARGV[2]); if redis.call('LLEN', KEYS[1]) > 0 then redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return 1`)

var releaseRedisDecisionOutputScript = redis.NewScript(`
if (ARGV[2] or '') ~= '' and redis.call('EXISTS', KEYS[2]) == 1 and (redis.call('HGET', KEYS[2], 'claim_token') or '') ~= ARGV[2] then return 0 end
if redis.call('DEL', KEYS[1]) > 0 or redis.call('EXISTS', KEYS[2]) == 1 then
  redis.call('HSET', KEYS[2], 'released', '1', 'claimed', '1', 'claim_token', '', 'claim_until_ms', '0')
  redis.call('PEXPIRE', KEYS[2], ARGV[1])
end
return 1`)

func (b *RedisBackend) decisionOutputListKey(ref DecisionOutputRef) string {
	return b.keyPrefix + "decision_output:" + ref.String()
}

func (b *RedisBackend) decisionOutputMetaKey(ref DecisionOutputRef) string {
	return b.keyPrefix + "decision_output_meta:" + ref.String()
}

func (b *RedisBackend) InitDecisionOutput(ctx context.Context, ref DecisionOutputRef, run DecisionOutputRun) error {
	return initRedisDecisionOutputScript.Run(ctx, b.client, []string{b.decisionOutputMetaKey(ref)},
		b.stateTTL.Milliseconds(), strings.TrimSpace(run.SessionID), strings.TrimSpace(run.RunID), strings.TrimSpace(run.Generation)).Err()
}

func (b *RedisBackend) DiscardDecisionOutput(ctx context.Context, ref DecisionOutputRef) error {
	return discardRedisDecisionOutputScript.Run(ctx, b.client, b.decisionOutputKeys(ref)).Err()
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
	if len(raw) != 7 {
		return DecisionOutputState{}, fmt.Errorf("decision output append returned %d fields", len(raw))
	}
	state := DecisionOutputState{
		Exists: true, Length: int(raw[1]), Bytes: int(raw[2]),
		Done: raw[3] == 1, Failed: raw[4] == 1, Claimed: raw[5] == 1, Released: raw[6] == 1,
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
		nowCmd *redis.TimeCmd
		length *redis.IntCmd
		meta   *redis.MapStringStringCmd
		events *redis.StringSliceCmd
	)
	// MULTI/EXEC keeps length, markers and the tail consistent with each other;
	// a reader must never see a Done marker without the entries before it.
	if _, err := b.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		nowCmd = pipe.Time(ctx)
		length = pipe.LLen(ctx, listKey)
		meta = pipe.HGetAll(ctx, metaKey)
		events = pipe.LRange(ctx, listKey, int64(from), -1)
		return nil
	}); err != nil {
		return DecisionOutputPage{}, err
	}
	fields := meta.Val()
	released := fields["released"] == "1"
	claimed := released
	if !claimed && fields["claimed"] == "1" {
		claimUntil, _ := strconv.ParseInt(fields["claim_until_ms"], 10, 64)
		claimed = claimUntil == 0 || nowCmd.Val().UnixMilli() < claimUntil
	}
	page := DecisionOutputPage{DecisionOutputState: DecisionOutputState{
		Exists:  length.Val() > 0 || len(fields) > 0,
		Length:  int(length.Val()),
		Done:    fields["done"] == "1",
		Failed:  fields["failed"] == "1",
		Claimed: claimed,
	}}
	page.Released = released
	page.Run = DecisionOutputRun{SessionID: fields["session_id"], RunID: fields["run_id"], Generation: fields["generation"]}
	if bytes, err := strconv.Atoi(fields["bytes"]); err == nil {
		page.Bytes = bytes
	}
	for _, raw := range events.Val() {
		page.Events = append(page.Events, json.RawMessage(raw))
	}
	return page, nil
}

func decisionOutputLeaseMS(leaseTTL time.Duration) int64 {
	if leaseTTL <= 0 {
		return 0
	}
	return max(leaseTTL.Milliseconds(), 1)
}

func (b *RedisBackend) decisionOutputKeys(ref DecisionOutputRef) []string {
	return []string{b.decisionOutputListKey(ref), b.decisionOutputMetaKey(ref)}
}

func (b *RedisBackend) runClaimScript(ctx context.Context, script *redis.Script, ref DecisionOutputRef, rawToken string, lease int64) (int64, error) {
	if tok := strings.TrimSpace(rawToken); tok != "" {
		return script.Run(ctx, b.client, b.decisionOutputKeys(ref), tok, b.stateTTL.Milliseconds(), lease).Int64()
	}
	return 0, errors.New("decision output claim token is required")
}

func (b *RedisBackend) ClaimDecisionOutput(ctx context.Context, ref DecisionOutputRef, token string, leaseTTL time.Duration) (DecisionOutputClaimOutcome, error) {
	v, err := b.runClaimScript(ctx, claimRedisDecisionOutputScript, ref, token, decisionOutputLeaseMS(leaseTTL))
	if v == 1 || v == 2 {
		return DecisionOutputClaimOutcome(v), err
	}
	return DecisionOutputClaimUnavailable, err
}

func (b *RedisBackend) RenewDecisionOutputClaim(ctx context.Context, ref DecisionOutputRef, token string, leaseTTL time.Duration) (bool, error) {
	v, err := b.runClaimScript(ctx, mutateClaimRedisDecisionOutputScript, ref, token, decisionOutputLeaseMS(leaseTTL))
	return v == 1, err
}

func (b *RedisBackend) UnclaimDecisionOutput(ctx context.Context, ref DecisionOutputRef, token string) error {
	_, err := b.runClaimScript(ctx, mutateClaimRedisDecisionOutputScript, ref, token, -1)
	return err
}

func (b *RedisBackend) ReleaseDecisionOutput(ctx context.Context, ref DecisionOutputRef, token string) error {
	return releaseRedisDecisionOutputScript.Run(ctx, b.client, b.decisionOutputKeys(ref), b.stateTTL.Milliseconds(), strings.TrimSpace(token)).Err()
}
