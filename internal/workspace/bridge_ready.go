package workspace

import (
	"context"
	"log/slog"
	"slices"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// OnNativeWorkspaceReady registers a lightweight notification after a native
// bridge has answered a readiness probe. Subscribers must enqueue bounded work
// rather than call back synchronously into workspace startup.
func (m *Manager) OnNativeWorkspaceReady(fn func(context.Context, string)) {
	if fn == nil {
		return
	}
	m.bridgeResetMu.Lock()
	m.bridgeReadyFns = append(m.bridgeReadyFns, fn)
	m.bridgeResetMu.Unlock()
}

func (m *Manager) notifyNativeWorkspaceReady(ctx context.Context, botID string) {
	m.bridgeResetMu.Lock()
	fns := slices.Clone(m.bridgeReadyFns)
	m.bridgeResetMu.Unlock()
	for _, fn := range fns {
		fn(ctx, botID)
	}
}

// OnNativeWorkspaceQuiescent registers bounded startup maintenance. The client
// is already connected; callbacks must not resolve it through Manager again.
// Bridge admission decides whether this lifetime is actually still unused.
func (m *Manager) OnNativeWorkspaceQuiescent(fn func(context.Context, string, *bridge.Client) error) {
	if fn == nil {
		return
	}
	m.bridgeResetMu.Lock()
	m.bridgeQuiescentFns = append(m.bridgeQuiescentFns, fn)
	m.bridgeResetMu.Unlock()
}

func (m *Manager) notifyNativeWorkspaceQuiescent(ctx context.Context, botID string, client *bridge.Client) {
	m.bridgeResetMu.Lock()
	fns := slices.Clone(m.bridgeQuiescentFns)
	m.bridgeResetMu.Unlock()
	for _, fn := range fns {
		if err := fn(ctx, botID, client); err != nil {
			m.logger.Warn("workspace startup maintenance deferred", slog.String("bot_id", botID), slog.Any("error", err))
		}
	}
}
