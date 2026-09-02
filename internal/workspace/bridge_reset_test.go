package workspace

import (
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

func newBridgeResetTestManager() *Manager {
	return &Manager{grpcPool: bridge.NewPool(func(string) string { return "" })}
}

func TestResetBridgeNotifiesCallbacksInOrder(t *testing.T) {
	m := newBridgeResetTestManager()

	var calls []string
	m.OnBridgeReset(func(botID string) { calls = append(calls, "first:"+botID) })
	m.OnBridgeReset(func(botID string) { calls = append(calls, "second:"+botID) })
	m.OnBridgeReset(nil)

	m.resetBridge("bot-a")
	m.resetBridge("bot-b")

	want := []string{"first:bot-a", "second:bot-a", "first:bot-b", "second:bot-b"}
	if !slices.Equal(calls, want) {
		t.Fatalf("callbacks = %v, want %v", calls, want)
	}
}

func TestOnBridgeResetDoesNotReplayPastResets(t *testing.T) {
	m := newBridgeResetTestManager()

	m.resetBridge("bot-a")

	var calls []string
	m.OnBridgeReset(func(botID string) { calls = append(calls, botID) })
	if len(calls) != 0 {
		t.Fatalf("callback replayed past reset: %v", calls)
	}

	m.resetBridge("bot-b")
	if !slices.Equal(calls, []string{"bot-b"}) {
		t.Fatalf("callbacks = %v, want [bot-b]", calls)
	}
}

func TestResetBridgeCallbackMayRegisterAnother(t *testing.T) {
	m := newBridgeResetTestManager()

	var calls []string
	m.OnBridgeReset(func(botID string) {
		calls = append(calls, "outer:"+botID)
		// Registering from inside a callback must not deadlock; the new
		// callback only sees subsequent resets.
		m.OnBridgeReset(func(botID string) { calls = append(calls, "inner:"+botID) })
	})

	m.resetBridge("bot-a")
	if !slices.Equal(calls, []string{"outer:bot-a"}) {
		t.Fatalf("callbacks after first reset = %v", calls)
	}
}
