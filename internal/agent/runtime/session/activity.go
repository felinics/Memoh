package sessionruntime

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
