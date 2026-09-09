package workspace

import "slices"

// OnBridgeReset registers fn to run each time the bridge connection for a bot
// is evicted from the pool — after a container restart, rebuild, snapshot
// replace, or a failed readiness probe. The callback receives the bot ID and
// runs synchronously on the resetting goroutine, so it must be cheap and must
// not call back into the Manager's bridge path. Use it to invalidate per-bot
// state derived from the workspace container process. A nil fn is ignored.
func (m *Manager) OnBridgeReset(fn func(botID string)) {
	if fn == nil {
		return
	}
	m.bridgeResetMu.Lock()
	m.bridgeResetFns = append(m.bridgeResetFns, fn)
	m.bridgeResetMu.Unlock()
}

// resetBridge evicts the pooled bridge client for botID and then notifies
// OnBridgeReset subscribers in registration order. It is the single
// invalidation point for anything cached against a workspace container
// process; every code path that used to call grpcPool.Remove directly goes
// through here.
func (m *Manager) resetBridge(botID string) {
	m.grpcPool.Remove(botID)

	m.bridgeResetMu.Lock()
	fns := slices.Clone(m.bridgeResetFns)
	m.bridgeResetMu.Unlock()
	for _, fn := range fns {
		fn(botID)
	}
}
