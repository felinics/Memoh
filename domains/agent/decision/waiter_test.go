package decision

import (
	"context"
	"testing"
	"time"
)

func TestAwaitResolvesInstantlyViaNotify(t *testing.T) {
	t.Parallel()
	w := NewWaiter[string]()
	polled := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		<-polled
		w.Notify("d1", "approved")
	}()
	start := time.Now()
	seenPoll := false
	got, err := w.Await(ctx, "d1", time.Hour, func(context.Context) (string, bool, error) {
		if !seenPoll {
			seenPoll = true
			close(polled)
		}
		return "", false, nil
	})
	if err != nil || got != "approved" {
		t.Fatalf("Await = %q, %v", got, err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("Await took the fallback path; broadcast must resolve instantly")
	}
}

func TestAwaitImmediatePollShortCircuits(t *testing.T) {
	t.Parallel()
	w := NewWaiter[string]()
	got, err := w.Await(context.Background(), "d1", time.Hour, func(context.Context) (string, bool, error) {
		return "already-decided", true, nil
	})
	if err != nil || got != "already-decided" {
		t.Fatalf("Await = %q, %v", got, err)
	}
}

func TestAwaitFallbackPollCoversMissedBroadcast(t *testing.T) {
	t.Parallel()
	w := NewWaiter[string]()
	calls := 0
	got, err := w.Await(context.Background(), "d1", 10*time.Millisecond, func(context.Context) (string, bool, error) {
		calls++
		if calls >= 3 {
			return "decided-elsewhere", true, nil
		}
		return "", false, nil
	})
	if err != nil || got != "decided-elsewhere" {
		t.Fatalf("Await = %q, %v (calls=%d)", got, err, calls)
	}
}

func TestAwaitDrainsNotificationWhenContextCancels(t *testing.T) {
	t.Parallel()
	w := NewWaiter[string]()
	ctx, cancel := context.WithCancel(context.Background())
	notified := false
	got, err := w.Await(ctx, "d1", time.Hour, func(context.Context) (string, bool, error) {
		if !notified {
			notified = true
			w.Notify("d1", "decided-at-cancel")
			cancel()
		}
		return "", false, nil
	})
	if err != nil || got != "decided-at-cancel" {
		t.Fatalf("Await = %q, %v; want notification to win cancellation", got, err)
	}
}

func TestRegisterRefcounting(t *testing.T) {
	t.Parallel()
	w := NewWaiter[int]()
	r1 := w.Register("d1")
	r2 := w.Register("d1")
	if !w.Has("d1") {
		t.Fatalf("Has = false after two registers")
	}
	r1()
	if !w.Has("d1") {
		t.Fatalf("Has = false after one of two releases")
	}
	r2()
	if w.Has("d1") {
		t.Fatalf("Has = true after all releases")
	}
	if w.Has("other") {
		t.Fatalf("ids must be independent")
	}
}
