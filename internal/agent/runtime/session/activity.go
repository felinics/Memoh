package sessionruntime

import "context"

// SetAdmissionObserver installs the lightweight activity invalidation sink.
// The composition root owns its transport; Runtime does not depend on the
// bot activity hub or carry conversation contents through that channel.
func (m *Manager) SetAdmissionObserver(observer func(botID, sessionID string)) {
	m.mu.Lock()
	m.admissionObserver = observer
	m.mu.Unlock()
}

func (m *Manager) observeAdmission(botID, sessionID string) {
	m.mu.Lock()
	observer := m.admissionObserver
	m.mu.Unlock()
	if observer != nil {
		observer(botID, sessionID)
	}
}

// SetWaitingDecisionObserver installs the sink told when a run durably parks
// in waiting_decision, i.e. it now needs someone to approve or answer. It is
// called synchronously, without the manager lock, only after the ledger
// accepted the transition.
func (m *Manager) SetWaitingDecisionObserver(observer func(ctx context.Context, botID, sessionID string)) {
	m.mu.Lock()
	m.waitingDecisionObserver = observer
	m.mu.Unlock()
}

func (m *Manager) observeWaitingDecision(ctx context.Context, botID, sessionID string) {
	m.mu.Lock()
	observer := m.waitingDecisionObserver
	m.mu.Unlock()
	if observer != nil {
		observer(ctx, botID, sessionID)
	}
}
