package input

import (
	"context"
	"errors"
	"testing"

	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
)

func TestCancelRejectsStaleRuntimeFenceBeforeMutation(t *testing.T) {
	persistence := &lifecyclePersistence{fenceErr: runtimefence.ErrStale}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID: storeTestBotID, SessionID: storeTestSessionID, Token: 1,
	})
	_, err := NewService(nil, persistence).Cancel(ctx, CancelInput{
		RequestID: "33333333-3333-3333-3333-333333333333",
	})
	if !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("Cancel() error = %v", err)
	}
}
