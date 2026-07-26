package approval

import (
	"context"
	"errors"
	"testing"

	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
)

func TestApproveRejectsStaleRuntimeFenceBeforeMutation(t *testing.T) {
	persistence := &testPersistence{
		getRow:   Record{ID: testApprovalID, BotID: testBotID, SessionID: testSessionID, Status: StatusPending},
		fenceErr: runtimefence.ErrStale,
	}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID: testBotID, SessionID: testSessionID, Token: 1,
	})
	_, err := NewService(nil, persistence, nil).Approve(ctx, testApprovalID, "", "")
	if !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("Approve() error = %v", err)
	}
}
