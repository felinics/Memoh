package application

import (
	"context"
	"testing"
	"time"
)

func TestRecordToolCallExtendsIdleTimeoutBeforeFirstDeadline(t *testing.T) {
	ctx, idle := withIdleTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(idle.Stop)

	idle.RecordToolCall()
	time.Sleep(30 * time.Millisecond)

	select {
	case <-ctx.Done():
		t.Fatal("idle timeout fired at the base deadline after recording a tool call")
	default:
	}
	if idle.DidFire() {
		t.Fatal("idle timeout marked itself fired at the base deadline")
	}
}
