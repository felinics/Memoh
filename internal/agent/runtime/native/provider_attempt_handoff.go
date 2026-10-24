package native

import (
	"errors"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

var errProviderAttemptNotPrepared = errors.New("provider attempt was not prepared")

type preparedProviderAttempt struct {
	// call is the attempt's position among its dispatch's calls, the index
	// providerAttemptState resolves the dispatch's committed steps by.
	call            int
	snapshot        contextfrag.StepSnapshot
	systemPrepended bool
	// admitted are the loop-owned dynamic messages the prepared payload
	// carries, with their positions in that payload.
	admitted []dynamicSourceRef
}

// providerAttemptHandoff publishes successful attempt state at the last
// Memoh-owned boundary before invoking the provider. Preparation may run well
// before that boundary, so it stages content-light metadata here instead of
// advancing hash, fork, retry, or durable-input state early.
type providerAttemptHandoff struct {
	mu  sync.Mutex
	cfg RunConfig
	// firstStep is the durable index of the step the dispatch's first call
	// produces. A dispatch rebuilt mid-run (an in-run compaction event, a
	// mid-stream retry) starts at the step it regenerates, and a decision
	// continuation at the step the run paused before.
	firstStep int
	pending   *preparedProviderAttempt
}

func newProviderAttemptHandoff(cfg RunConfig, firstStep int) *providerAttemptHandoff {
	return &providerAttemptHandoff{cfg: cfg, firstStep: firstStep}
}

// stepIndex is the durable index of the step a call of this dispatch produces:
// the index the step commits under and the one its audit is recorded with.
func (h *providerAttemptHandoff) stepIndex(call int) int {
	if h == nil {
		return call
	}
	return h.firstStep + call
}

func (h *providerAttemptHandoff) stage(
	call int,
	snapshot contextfrag.StepSnapshot,
	systemPrepended bool,
	admitted []dynamicSourceRef,
) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pending = &preparedProviderAttempt{
		call:            call,
		snapshot:        snapshot,
		systemPrepended: systemPrepended,
		admitted:        cloneDynamicSourceRefs(admitted),
	}
	h.mu.Unlock()
}

// admitted is where the staged attempt carries the loop-owned dynamic
// messages it admits.
func (h *providerAttemptHandoff) admitted() []dynamicSourceRef {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == nil {
		return nil
	}
	return cloneDynamicSourceRefs(h.pending.admitted)
}

// reject drops the staged attempt and removes provider-visibility admission
// for the boundary the attempt targeted: it will not be dispatched.
func (h *providerAttemptHandoff) reject() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pending = nil
	h.mu.Unlock()
	h.cfg.dynamicInputs.revoke()
}

// publish publishes the staged attempt for the request about to be dispatched.
//
//nolint:gocritic // hugeParam: the loop's request value is the frozen payload this publishes.
func (h *providerAttemptHandoff) publish(params sdk.Request) error {
	if h == nil {
		return errProviderAttemptNotPrepared
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == nil {
		return errProviderAttemptNotPrepared
	}

	pending := *h.pending
	if h.cfg.ForkContext != nil {
		if err := h.cfg.ForkContext.Store(params.Messages); err != nil {
			h.pending = nil
			h.cfg.dynamicInputs.revoke()
			return err
		}
	}

	h.cfg.dynamicInputs.commit(pending.admitted)
	hash := contextfrag.ProviderPayloadHash(params.System, params.Messages, params.Tools)
	h.cfg.providerAttemptState.store(&params, pending.call, pending.systemPrepended, pending.admitted)
	h.cfg.ContextMutations.SetFinalInputHash(hash)
	pending.snapshot.PostPrepareInputHash = hash
	h.cfg.ContextMutations.AppendStepSnapshot(pending.snapshot)
	h.pending = nil
	return nil
}
