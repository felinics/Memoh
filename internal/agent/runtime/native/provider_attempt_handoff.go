package native

import (
	"errors"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

var errProviderAttemptNotPrepared = errors.New("provider attempt was not prepared")

type preparedProviderAttempt struct {
	snapshot          contextfrag.StepSnapshot
	systemPrepended   bool
	reselectionDetail string
	provenance        preparedMessageProvenance
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
	provenanceValues ...preparedMessageProvenance,
) {
	if h == nil {
		return
	}
	var provenance preparedMessageProvenance
	if len(provenanceValues) > 0 {
		provenance = provenanceValues[0]
	}
	h.mu.Lock()
	h.pending = &preparedProviderAttempt{
		snapshot:          snapshot,
		systemPrepended:   systemPrepended,
		reselectionDetail: reselectionDetail,
		provenance:        clonePreparedMessageProvenance(provenance),
	}
	h.mu.Unlock()
}

func (h *providerAttemptHandoff) reject(provenanceValues ...preparedMessageProvenance) {
	if h == nil {
		return
	}
	h.mu.Lock()
	var provenance preparedMessageProvenance
	if h.pending != nil {
		provenance = h.pending.provenance
	}
	if len(provenanceValues) > 0 {
		provenance = provenanceValues[0]
	}
	h.pending = nil
	h.mu.Unlock()
	h.cfg.preparedStepMessages.revoke(provenance)
}

func (h *providerAttemptHandoff) publish(params sdk.GenerateParams) error {
	if h == nil {
		return errProviderAttemptNotPrepared
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == nil {
		return errProviderAttemptNotPrepared
	}

	pending := *h.pending
	if pending.provenance.known && len(pending.provenance.messageIndexes) != len(params.Messages) {
		h.pending = nil
		h.cfg.preparedStepMessages.revoke(pending.provenance)
		return errProviderAttemptNotPrepared
	}
	if h.cfg.ForkContext != nil {
		if err := h.cfg.ForkContext.Store(params.Messages); err != nil {
			h.pending = nil
			h.cfg.preparedStepMessages.revoke(pending.provenance)
			return err
		}
	}

	h.cfg.preparedStepMessages.reconcile(params.Messages, pending.provenance)
	hash := contextfrag.ProviderPayloadHash(params.System, params.Messages, params.Tools)
	h.cfg.providerAttemptState.store(&params, pending.snapshot.StepIndex, pending.systemPrepended, pending.provenance)
	h.cfg.ContextMutations.SetFinalInputHash(hash)
	pending.snapshot.PostPrepareInputHash = hash
	h.cfg.ContextMutations.AppendStepSnapshot(pending.snapshot)
	if pending.reselectionDetail != "" {
		h.cfg.ContextMutations.Record(contextfrag.MutationLoopStepReselection, pending.reselectionDetail)
	}
	h.pending = nil
	return nil
}
