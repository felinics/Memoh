package application

import (
	"context"
	"errors"

	session "github.com/felinics/memoh/internal/chat/thread"
)

type fakeBackgroundSessionService struct {
	getFn                  func(ctx context.Context, sessionID string) (session.Thread, error)
	updateMetadataFn       func(ctx context.Context, sessionID string, metadata map[string]any) (session.Thread, error)
	mergeRuntimeMetadataFn func(ctx context.Context, sessionID, runtimeType string, delta map[string]any) (session.Thread, error)
}

func (f *fakeBackgroundSessionService) Get(ctx context.Context, sessionID string) (session.Thread, error) {
	if f == nil || f.getFn == nil {
		return session.Thread{}, errors.New("unexpected Get call")
	}
	return f.getFn(ctx, sessionID)
}

func (*fakeBackgroundSessionService) UpdateTitle(context.Context, string, string) (session.Thread, error) {
	return session.Thread{}, errors.New("unexpected UpdateTitle call")
}

func (f *fakeBackgroundSessionService) UpdateMetadata(ctx context.Context, sessionID string, metadata map[string]any) (session.Thread, error) {
	if f == nil || f.updateMetadataFn == nil {
		return session.Thread{}, errors.New("unexpected UpdateMetadata call")
	}
	return f.updateMetadataFn(ctx, sessionID, metadata)
}

func (f *fakeBackgroundSessionService) MergeRuntimeMetadata(ctx context.Context, sessionID, runtimeType string, delta map[string]any) (session.Thread, error) {
	if f == nil || f.mergeRuntimeMetadataFn == nil {
		return session.Thread{}, errors.New("unexpected MergeRuntimeMetadata call")
	}
	return f.mergeRuntimeMetadataFn(ctx, sessionID, runtimeType, delta)
}
