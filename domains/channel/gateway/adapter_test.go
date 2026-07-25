package gateway

import (
	"context"
	"errors"
	"testing"
)

// TestBaseConnectionMarkStopped verifies the path adapters use to
// signal that a long-lived connection died on its own. Manager.refresh
// hangs Running() off this flag, so a goroutine that gives up on a
// terminal protocol error must flip it to false; otherwise the channel
// shows healthy until manual intervention.
func TestBaseConnectionMarkStopped(t *testing.T) {
	t.Parallel()
	var stopped bool
	conn := NewConnection(ChannelConfig{ID: "cfg"}, func(context.Context) error {
		stopped = true
		return nil
	})

	if !conn.Running() {
		t.Fatal("new connection should report Running=true")
	}

	conn.MarkStopped()

	if conn.Running() {
		t.Fatal("after MarkStopped Running should be false")
	}
	if stopped {
		t.Fatal("MarkStopped must not invoke the stop function")
	}
}

// TestBaseConnectionStopFailureKeepsRunning verifies that a stop call
// which errors leaves Running=true. Manager keys retry decisions off
// the flag, so a half-shutdown must not look like a clean exit.
func TestBaseConnectionStopFailureKeepsRunning(t *testing.T) {
	t.Parallel()
	stopErr := errors.New("boom")
	conn := NewConnection(ChannelConfig{ID: "cfg"}, func(context.Context) error {
		return stopErr
	})

	if err := conn.Stop(context.Background()); !errors.Is(err, stopErr) {
		t.Fatalf("Stop returned %v, want %v", err, stopErr)
	}
	if !conn.Running() {
		t.Fatal("Running should stay true when stop returned an error")
	}
}
