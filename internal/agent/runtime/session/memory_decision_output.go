package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// memoryDecisionOutput is one command's append-only raw output log. Entries are
// stored as the exact bytes the producer committed; readers receive copies.
type memoryDecisionOutput struct {
	events  []json.RawMessage
	bytes   int
	done    bool
	failed  bool
	claimed bool

	released   bool
	claimToken string
	claimUntil time.Time
	run        DecisionOutputRun
}

func (l *memoryDecisionOutput) activeClaim(now time.Time) bool {
	return l.released || (l.claimed && (l.claimUntil.IsZero() || now.Before(l.claimUntil)))
}

func (l *memoryDecisionOutput) state(now time.Time) DecisionOutputState {
	return DecisionOutputState{
		Exists: true, Length: len(l.events), Bytes: l.bytes,
		Done: l.done, Failed: l.failed, Claimed: l.activeClaim(now), Released: l.released, Run: l.run,
	}
}

func (r DecisionOutputRef) String() string {
	return r.topic().String()
}

func (b *MemoryBackend) touchDecisionOutputLocked(k string, now time.Time) {
	b.decisionOutputExpiresAt[k] = now.Add(b.stateTTL)
	b.scheduleCleanupLocked(b.decisionOutputExpiresAt[k])
}

func (b *MemoryBackend) decisionOutputLocked(ref DecisionOutputRef, now time.Time) *memoryDecisionOutput {
	k := ref.String()
	log, ok := b.decisionOutputs[k]
	if !ok {
		log = &memoryDecisionOutput{}
		b.decisionOutputs[k] = log
	}
	b.touchDecisionOutputLocked(k, now)
	return log
}

func (b *MemoryBackend) InitDecisionOutput(ctx context.Context, ref DecisionOutputRef, run DecisionOutputRun) error {
	return b.withDecisionOutput(ctx, ref, "", false, func(_ string, _ *memoryDecisionOutput, _ bool, _ string, now time.Time) {
		l := b.decisionOutputLocked(ref, now)
		mergeDecisionOutputRun(&l.run.SessionID, &l.run.RunID, &l.run.Generation, run)
	})
}

func (b *MemoryBackend) DiscardDecisionOutput(ctx context.Context, ref DecisionOutputRef) error {
	return b.withDecisionOutput(ctx, ref, "", false, func(k string, log *memoryDecisionOutput, ok bool, _ string, now time.Time) {
		if ok && len(log.events) == 0 && !log.done && !log.failed && !log.released && !log.activeClaim(now) {
			delete(b.decisionOutputs, k)
			delete(b.decisionOutputExpiresAt, k)
		}
	})
}

func (b *MemoryBackend) AppendDecisionOutput(ctx context.Context, ref DecisionOutputRef, seq int64, payload json.RawMessage, limits DecisionOutputLimits) (DecisionOutputState, error) {
	if err := contextError(ctx); err != nil {
		return DecisionOutputState{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.purgeExpiredLocked(now)
	log := b.decisionOutputLocked(ref, now)
	state := log.state(now)
	switch {
	case log.done || log.failed:
		return state, nil
	case seq <= int64(len(log.events)):
		return state, nil
	case seq != int64(len(log.events))+1:
		return state, ErrDecisionOutputSequenceGap
	case payload == nil:
		log.done = true
	case exceedsDecisionOutputLimits(log.bytes, len(log.events), len(payload), limits):
		log.failed = true
		state = log.state(now)
		state.Applied, state.Exceeded = true, true
		return state, nil
	default:
		log.events = append(log.events, append(json.RawMessage(nil), payload...))
		log.bytes += len(payload)
	}
	state = log.state(now)
	state.Applied = true
	return state, nil
}

func (b *MemoryBackend) ReadDecisionOutput(ctx context.Context, ref DecisionOutputRef, from int) (DecisionOutputPage, error) {
	if err := contextError(ctx); err != nil {
		return DecisionOutputPage{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.purgeExpiredLocked(now)
	log, ok := b.decisionOutputs[ref.String()]
	if !ok {
		return DecisionOutputPage{}, nil
	}
	page := DecisionOutputPage{DecisionOutputState: log.state(now)}
	if from < 0 {
		from = 0
	}
	for _, raw := range log.events[min(from, len(log.events)):] {
		page.Events = append(page.Events, append(json.RawMessage(nil), raw...))
	}
	return page, nil
}

func (b *MemoryBackend) withDecisionOutput(ctx context.Context, ref DecisionOutputRef, rawToken string, requireToken bool, fn func(k string, log *memoryDecisionOutput, ok bool, token string, now time.Time)) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	token := strings.TrimSpace(rawToken)
	if requireToken && token == "" {
		return errors.New("decision output claim token is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.purgeExpiredLocked(now)
	k := ref.String()
	log, ok := b.decisionOutputs[k]
	fn(k, log, ok, token, now)
	return nil
}

func (b *MemoryBackend) ClaimDecisionOutput(ctx context.Context, ref DecisionOutputRef, token string, leaseTTL time.Duration) (DecisionOutputClaimOutcome, error) {
	outcome := DecisionOutputClaimUnavailable
	err := b.withDecisionOutput(ctx, ref, token, true, func(k string, log *memoryDecisionOutput, ok bool, tok string, now time.Time) {
		if !ok || log.released || ((log.done || log.failed) && log.bytes > 0 && len(log.events) == 0) {
			return
		}
		if log.activeClaim(now) && log.claimToken != tok {
			outcome = DecisionOutputClaimBusy
			return
		}
		log.claimed, log.claimToken, log.claimUntil = true, tok, time.Time{}
		if leaseTTL > 0 {
			log.claimUntil = now.Add(leaseTTL)
		}
		b.touchDecisionOutputLocked(k, now)
		outcome = DecisionOutputClaimAcquired
	})
	return outcome, err
}

func (b *MemoryBackend) RenewDecisionOutputClaim(ctx context.Context, ref DecisionOutputRef, token string, leaseTTL time.Duration) (bool, error) {
	renewed := false
	err := b.withDecisionOutput(ctx, ref, token, true, func(k string, log *memoryDecisionOutput, ok bool, tok string, now time.Time) {
		if ok && !log.released && log.claimed && log.claimToken == tok {
			if leaseTTL > 0 {
				log.claimUntil = now.Add(leaseTTL)
			}
			b.touchDecisionOutputLocked(k, now)
			renewed = true
		}
	})
	return renewed, err
}

func (b *MemoryBackend) UnclaimDecisionOutput(ctx context.Context, ref DecisionOutputRef, token string) error {
	return b.withDecisionOutput(ctx, ref, token, true, func(k string, log *memoryDecisionOutput, ok bool, tok string, now time.Time) {
		if ok && !log.released && log.claimToken == tok {
			log.claimed, log.claimToken, log.claimUntil = false, "", time.Time{}
			b.touchDecisionOutputLocked(k, now)
		}
	})
}

func (b *MemoryBackend) ReleaseDecisionOutput(ctx context.Context, ref DecisionOutputRef, token string) error {
	return b.withDecisionOutput(ctx, ref, token, false, func(k string, log *memoryDecisionOutput, ok bool, tok string, now time.Time) {
		if ok && (tok == "" || log.claimToken == tok) {
			log.events, log.bytes = nil, 0
			log.released, log.claimed, log.claimToken, log.claimUntil = true, true, "", time.Time{}
			b.touchDecisionOutputLocked(k, now)
		}
	})
}

func exceedsDecisionOutputLimits(bytes, events, next int, limits DecisionOutputLimits) bool {
	return limits.MaxBytes > 0 && bytes+next > limits.MaxBytes ||
		limits.MaxEvents > 0 && events >= limits.MaxEvents
}
