package native

import (
	"errors"
	"fmt"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

var errProviderAttemptNotPrepared = errors.New("provider attempt was not prepared")

type preparedProviderAttempt struct {
	snapshot          contextfrag.StepSnapshot
	systemPrepended   bool
	reselectionDetail string
	protectedPruned   int
	// admitted are the loop-owned dynamic messages the prepared payload
	// carries, with their positions in that payload.
	admitted []dynamicSourceRef
}

// providerAttemptHandoff publishes successful attempt state at the last
// Memoh-owned boundary before invoking the provider. Preparation may run well
// before that boundary, so it stages content-light metadata here instead of
// advancing hash, fork, retry, or durable-input state early.
type providerAttemptHandoff struct {
	mu      sync.Mutex
	cfg     RunConfig
	pending *preparedProviderAttempt
}

func newProviderAttemptHandoff(cfg RunConfig) *providerAttemptHandoff {
	return &providerAttemptHandoff{cfg: cfg}
}

func (h *providerAttemptHandoff) stage(
	snapshot contextfrag.StepSnapshot,
	systemPrepended bool,
	reselectionDetail string,
	protectedPruned int,
	admitted []dynamicSourceRef,
) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pending = &preparedProviderAttempt{
		snapshot:          snapshot,
		systemPrepended:   systemPrepended,
		reselectionDetail: reselectionDetail,
		protectedPruned:   protectedPruned,
		admitted:          cloneDynamicSourceRefs(admitted),
	}
	h.mu.Unlock()
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
	h.cfg.providerAttemptState.store(&params, pending.snapshot.StepIndex, pending.systemPrepended, pending.admitted)
	h.cfg.ContextMutations.SetFinalInputHash(hash)
	pending.snapshot.PostPrepareInputHash = hash
	h.cfg.ContextMutations.AppendStepSnapshot(pending.snapshot)
	if pending.reselectionDetail != "" {
		h.cfg.ContextMutations.Record(contextfrag.MutationLoopStepReselection, pending.reselectionDetail)
	}
	if pending.protectedPruned > 0 {
		h.cfg.ContextMutations.Record(
			contextfrag.MutationMidTaskPrune,
			fmt.Sprintf("truncated=%d", pending.protectedPruned),
		)
	}
	h.pending = nil
	return nil
}
