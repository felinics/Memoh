package application

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

func TestActiveTurnDrainWaitsForCanceledTurnCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var turns activeTurnTracker
		end, err := turns.begin()
		if err != nil {
			t.Fatal(err)
		}
		endSecond, err := turns.begin()
		if err != nil {
			t.Fatal(err)
		}
		finished := make(chan error, 1)
		go func() { finished <- turns.drain(t.Context()) }()
		synctest.Wait()
		select {
		case err := <-finished:
			t.Fatalf("drain returned before the turn finished: %v", err)
		default:
		}
		if _, err := turns.begin(); !errors.Is(err, sessionruntime.ErrManagerClosed) {
			t.Fatalf("new turn admitted during drain: %v", err)
		}
		end()
		select {
		case err := <-finished:
			t.Fatalf("drain returned while another turn was active: %v", err)
		default:
		}
		endSecond()
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	})
}

func TestActiveTurnDrainHonorsShutdownDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var turns activeTurnTracker
		end, err := turns.begin()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		finished := make(chan error, 1)
		go func() { finished <- turns.drain(ctx) }()
		synctest.Wait()
		time.Sleep(time.Second)
		if err := <-finished; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("drain result = %v, want shutdown deadline", err)
		}
		end()
	})
}
