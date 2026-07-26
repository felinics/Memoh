package thread

import (
	"context"
	"errors"
	"testing"

	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
)

type runtimeFenceStore struct {
	fakeThreadStore
	titleFence    *RuntimeFence
	metadataFence *RuntimeFence
}

func (s *runtimeFenceStore) UpdateThreadTitle(_ context.Context, id, title string, fence *RuntimeFence) (Thread, error) {
	s.titleFence = fence
	return Thread{ID: id, Title: title}, nil
}

func (s *runtimeFenceStore) UpdateThreadMetadata(_ context.Context, id string, metadata map[string]any, fence *RuntimeFence) (Thread, error) {
	s.metadataFence = fence
	return Thread{ID: id, Metadata: metadata}, nil
}

func TestThreadUpdatesUseRuntimeFence(t *testing.T) {
	const botID = "11111111-1111-1111-1111-111111111111"
	const threadID = "22222222-2222-2222-2222-222222222222"
	store := &runtimeFenceStore{}
	svc := NewService(nil, store, nil, nil)
	ctx := runtimefence.WithContext(t.Context(), runtimefence.Fence{BotID: botID, SessionID: threadID, Token: 9})
	if _, err := svc.UpdateTitle(ctx, threadID, "fenced"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateMetadata(ctx, threadID, map[string]any{"fenced": true}); err != nil {
		t.Fatal(err)
	}
	if store.titleFence == nil || store.titleFence.Token != 9 ||
		store.metadataFence == nil || store.metadataFence.Token != 9 {
		t.Fatalf("title=%+v metadata=%+v", store.titleFence, store.metadataFence)
	}
}

func TestThreadUpdateRejectsMismatchedFenceScope(t *testing.T) {
	store := &runtimeFenceStore{}
	svc := NewService(nil, store, nil, nil)
	ctx := runtimefence.WithContext(t.Context(), runtimefence.Fence{
		BotID:     "11111111-1111-1111-1111-111111111111",
		SessionID: "22222222-2222-2222-2222-222222222222", Token: 3,
	})
	_, err := svc.UpdateMetadata(ctx, "33333333-3333-3333-3333-333333333333", map[string]any{})
	if !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("error = %v, want ErrStale", err)
	}
}
