package runtimefence

import (
	"context"
	"errors"
	"testing"
)

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()

	want := Fence{BotID: " bot-1 ", SessionID: " session-1 ", Token: 3}
	ctx := WithContext(context.Background(), want)
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("runtime fence missing from context")
	}
	if got.BotID != "bot-1" || got.SessionID != "session-1" || got.Token != want.Token {
		t.Fatalf("runtime fence = %#v", got)
	}
}

func TestInvalidFenceIsNotStored(t *testing.T) {
	t.Parallel()

	ctx := WithContext(context.Background(), Fence{BotID: "bot-1", SessionID: "session-1"})
	if _, ok := FromContext(ctx); ok {
		t.Fatal("invalid runtime fence was stored")
	}
}

func TestValidateScopeRejectsAnotherSession(t *testing.T) {
	t.Parallel()

	ctx := WithContext(context.Background(), Fence{BotID: "bot-1", SessionID: "session-1", Token: 2})
	if err := ValidateScope(ctx, "bot-1", "session-2"); !errors.Is(err, ErrStale) {
		t.Fatalf("ValidateScope() error = %v, want ErrStale", err)
	}
}
