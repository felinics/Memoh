package userruntime

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

func testBridgeClient(t *testing.T) *bridge.Client {
	t.Helper()
	client, err := bridge.Dial(context.Background(), "passthrough:///runtime-hub-test")
	if err != nil {
		t.Fatalf("bridge.Dial: %v", err)
	}
	return client
}

func TestHubReplacesOnlyAfterNewConnectionIsRegistered(t *testing.T) {
	t.Parallel()
	hub := NewHub(nil)
	var oldClosed atomic.Int32
	old := &Connection{
		RuntimeID:    "runtime-1",
		ConnectionID: "connection-old",
		Client:       testBridgeClient(t),
		Close:        func(string) { oldClosed.Add(1) },
	}
	if err := hub.registerGuarded(old, nil); err != nil {
		t.Fatalf("register old connection: %v", err)
	}

	var newClosed atomic.Int32
	current := &Connection{
		RuntimeID:    "runtime-1",
		ConnectionID: "connection-new",
		Client:       testBridgeClient(t),
		Close:        func(string) { newClosed.Add(1) },
	}
	if err := hub.registerGuarded(current, nil); err != nil {
		t.Fatalf("register current connection: %v", err)
	}
	if oldClosed.Load() != 1 {
		t.Fatal("replacement returned before canceling the old connection")
	}
	if got, ok := hub.Get("runtime-1"); !ok || got != current {
		t.Fatalf("current connection = %#v, %v", got, ok)
	}

	// The superseded handler can exit later without deleting the replacement.
	hub.unregister("runtime-1", old, "old handler exited")
	if got, ok := hub.Get("runtime-1"); !ok || got != current {
		t.Fatalf("old unregister removed current connection")
	}

	hub.Kick("runtime-1", "test")
	if newClosed.Load() != 1 {
		t.Fatal("kick returned before canceling the current connection")
	}
	if _, ok := hub.Get("runtime-1"); ok {
		t.Fatal("kicked connection remains online")
	}
	// All close paths are idempotent.
	current.close("again")
	if newClosed.Load() != 1 {
		t.Fatalf("idempotent close count = %d, want 1", newClosed.Load())
	}
}

func TestHubShutdownRemovesAndClosesAllConnections(t *testing.T) {
	t.Parallel()
	hub := NewHub(nil)
	var closed atomic.Int32
	for index, runtimeID := range []string{"runtime-1", "runtime-2"} {
		if err := hub.registerGuarded(&Connection{
			RuntimeID:    runtimeID,
			ConnectionID: fmt.Sprintf("connection-%d", index),
			Client:       testBridgeClient(t),
			Close:        func(string) { closed.Add(1) },
		}, nil); err != nil {
			t.Fatalf("register connection: %v", err)
		}
	}
	if err := hub.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	for _, runtimeID := range []string{"runtime-1", "runtime-2"} {
		if _, ok := hub.Get(runtimeID); ok {
			t.Fatalf("runtime %q remains online after shutdown", runtimeID)
		}
	}
	if closed.Load() != 2 {
		t.Fatalf("closed count = %d, want 2", closed.Load())
	}

	late := &Connection{
		RuntimeID:    "runtime-3",
		ConnectionID: "connection-3",
		Client:       testBridgeClient(t),
		Close:        func(string) { closed.Add(1) },
	}
	_ = hub.registerGuarded(late, nil)
	if closed.Load() != 3 {
		t.Fatal("late registration was not closed synchronously")
	}
	if _, ok := hub.Get("runtime-3"); ok {
		t.Fatal("late registration became visible after shutdown")
	}
}
