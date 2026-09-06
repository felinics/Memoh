package sessionruntime

import (
	"context"
	"encoding/json"
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
}

func (l *memoryDecisionOutput) state() DecisionOutputState {
	return DecisionOutputState{
		Exists: true, Length: len(l.events), Bytes: l.bytes,
		Done: l.done, Failed: l.failed, Claimed: l.claimed,
	}
}

func (r DecisionOutputRef) String() string {
	return r.topic().String()
}

func (b *MemoryBackend) decisionOutputLocked(ref DecisionOutputRef, now time.Time) *memoryDecisionOutput {
	k := ref.String()
	log, ok := b.decisionOutputs[k]
	if !ok {
		log = &memoryDecisionOutput{}
		b.decisionOutputs[k] = log
	}
	b.decisionOutputExpiresAt[k] = now.Add(b.stateTTL)
	b.scheduleCleanupLocked(b.decisionOutputExpiresAt[k])
	return log
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
	state := log.state()
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
		state = log.state()
		state.Applied, state.Exceeded = true, true
		return state, nil
	default:
		log.events = append(log.events, append(json.RawMessage(nil), payload...))
		log.bytes += len(payload)
	}
	state = log.state()
	state.Applied = true
	return state, nil
}

func (b *MemoryBackend) ReadDecisionOutput(ctx context.Context, ref DecisionOutputRef, from int) (DecisionOutputPage, error) {
	if err := contextError(ctx); err != nil {
		return DecisionOutputPage{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purgeExpiredLocked(time.Now())
	log, ok := b.decisionOutputs[ref.String()]
	if !ok {
		return DecisionOutputPage{}, nil
	}
	page := DecisionOutputPage{DecisionOutputState: log.state()}
	if from < 0 {
		from = 0
	}
	for _, raw := range log.events[min(from, len(log.events)):] {
		page.Events = append(page.Events, append(json.RawMessage(nil), raw...))
	}
	return page, nil
}

func (b *MemoryBackend) ClaimDecisionOutput(ctx context.Context, ref DecisionOutputRef) (bool, error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.purgeExpiredLocked(now)
	log := b.decisionOutputLocked(ref, now)
	if log.claimed {
		return false, nil
	}
	log.claimed = true
	return true, nil
}

func (b *MemoryBackend) ReleaseDecisionOutput(ctx context.Context, ref DecisionOutputRef) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if log, ok := b.decisionOutputs[ref.String()]; ok {
		log.events, log.bytes = nil, 0
	}
	return nil
}

func exceedsDecisionOutputLimits(bytes, events, next int, limits DecisionOutputLimits) bool {
	return limits.MaxBytes > 0 && bytes+next > limits.MaxBytes ||
		limits.MaxEvents > 0 && events >= limits.MaxEvents
}
