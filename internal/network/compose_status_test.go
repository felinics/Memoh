package network

import "testing"

func TestComposeBotStatusKeepsEveryOverlayState(t *testing.T) {
	states := []OverlayState{
		OverlayStateReady, OverlayStateStarting, OverlayStateNeedsLogin, OverlayStateDegraded,
		OverlayStateStopped, OverlayStateMissing, OverlayStateUnsupported, OverlayStateDisabled,
		OverlayStateWorkspaceMissing, OverlayStateNeedsConfig, OverlayStateUnauthorized,
	}
	for _, state := range states {
		got := composeBotStatus(BotOverlayConfig{Provider: "tailscale"}, OverlayStatus{State: state})
		if got.State != state || got.Title == "" || got.Title == "Unknown" {
			t.Errorf("state %s composed as %s %q", state, got.State, got.Title)
		}
	}
	if got := composeBotStatus(BotOverlayConfig{}, OverlayStatus{State: "bogus"}); got.State != OverlayStateUnknown {
		t.Errorf("unrecognised state composed as %s", got.State)
	}
}
