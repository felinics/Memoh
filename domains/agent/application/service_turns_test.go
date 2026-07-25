package application

import (
	"context"
	"testing"
	"time"
)

func TestEnterSessionTurnSerializesSameSession(t *testing.T) {
	resolver := &Service{}
	releaseFirst := resolver.enterSessionTurn(context.Background(), "bot-1", "session-1")

	enteredSecond := make(chan struct{})
	releaseSecondCh := make(chan func(), 1)
	go func() {
		releaseSecond := resolver.enterSessionTurn(context.Background(), "bot-1", "session-1")
		close(enteredSecond)
		releaseSecondCh <- releaseSecond
	}()

	select {
	case <-enteredSecond:
		t.Fatal("second same-session turn entered before the first released")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()

	select {
	case <-enteredSecond:
	case <-time.After(time.Second):
		t.Fatal("second same-session turn did not enter after the first released")
	}

	releaseSecond := <-releaseSecondCh
	releaseSecond()
}

func TestTryEnterIdleSessionTurnRejectsActiveSession(t *testing.T) {
	resolver := &Service{}
	release := resolver.enterSessionTurn(context.Background(), "bot-1", "session-1")
	defer release()

	if releaseIdle, ok := resolver.tryEnterIdleSessionTurn(context.Background(), "bot-1", "session-1"); ok {
		releaseIdle()
		t.Fatal("tryEnterIdleSessionTurn entered while a turn was active")
	}
}

func TestSessionTurnActiveTracksActiveTurn(t *testing.T) {
	resolver := &Service{}
	if resolver.SessionTurnActive("bot-1", "session-1") {
		t.Fatal("session should not be active before entering a turn")
	}

	release := resolver.enterSessionTurn(context.Background(), "bot-1", "session-1")
	if !resolver.SessionTurnActive(" bot-1 ", " session-1 ") {
		t.Fatal("session should be active while a turn is entered")
	}
	release()
	if resolver.SessionTurnActive("bot-1", "session-1") {
		t.Fatal("session should not be active after release")
	}
}

func TestTryEnterIdleSessionTurnReentersAfterRelease(t *testing.T) {
	resolver := &Service{}
	release, ok := resolver.tryEnterIdleSessionTurn(context.Background(), "bot-1", "session-1")
	if !ok {
		t.Fatal("tryEnterIdleSessionTurn rejected an idle session")
	}
	release()

	releaseAgain, ok := resolver.tryEnterIdleSessionTurn(context.Background(), "bot-1", "session-1")
	if !ok {
		t.Fatal("tryEnterIdleSessionTurn rejected the session after release")
	}
	releaseAgain()
}
