// Package acpsession adapts Chat thread metadata to the minimal descriptor
// consumed by the ACP runtime.
package acpsession

import (
	"context"
	"errors"

	acp "github.com/memohai/memoh/internal/agent/runtime/acp"
	"github.com/memohai/memoh/internal/chat/thread"
)

type Source struct {
	threads threadGetter
}

type threadGetter interface {
	Get(ctx context.Context, sessionID string) (thread.Thread, error)
}

func NewSource(threads *thread.Service) *Source {
	return &Source{threads: threads}
}

func (s *Source) Get(ctx context.Context, sessionID string) (acp.SessionDescriptor, error) {
	if s == nil || s.threads == nil {
		return acp.SessionDescriptor{}, errors.New("thread service unavailable")
	}
	item, err := s.threads.Get(ctx, sessionID)
	if err != nil {
		return acp.SessionDescriptor{}, err
	}
	return acp.SessionDescriptor{
		BotID:           item.BotID,
		BotAgentID:      item.BotAgentID,
		SessionType:     item.Type,
		Metadata:        item.Metadata,
		RuntimeMetadata: item.RuntimeMetadata,
		IsACP:           thread.IsACPRuntime(item),
	}, nil
}
