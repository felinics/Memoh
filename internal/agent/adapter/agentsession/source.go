// Package agentsession adapts Chat thread metadata to the minimal descriptor
// consumed by the ACP runtime.
package agentsession

import (
	"context"

	acp "github.com/felinics/memoh/internal/agent/runtime/acp"
	"github.com/felinics/memoh/internal/chat/thread"
)

type Source struct {
	threads threadGetter
}

type threadGetter interface {
	Get(ctx context.Context, sessionID string) (thread.Thread, error)
	MergeRuntimeMetadata(ctx context.Context, sessionID, runtimeType string, delta map[string]any) (thread.Thread, error)
}

func NewSource(threads *thread.Service) *Source {
	return &Source{threads: threads}
}

func (s *Source) Get(ctx context.Context, sessionID string) (acp.SessionDescriptor, error) {
	item, err := s.threads.Get(ctx, sessionID)
	if err != nil {
		return acp.SessionDescriptor{}, err
	}
	return acp.SessionDescriptor{
		BotID:           item.BotID,
		SessionType:     item.Type,
		Metadata:        item.Metadata,
		RuntimeMetadata: item.RuntimeMetadata,
		IsACP:           thread.IsACPRuntime(item),
	}, nil
}

// SaveModelPreference is called under the ACP runtime operation lock, so
// an earlier setter cannot persist its state after a later setter or prompt.
func (s *Source) SaveModelPreference(ctx context.Context, sessionID, modelID, effort string) error {
	var model, reasoning any
	if modelID != "" {
		model = modelID
	}
	if effort != "" {
		reasoning = effort
	}
	_, err := s.threads.MergeRuntimeMetadata(ctx, sessionID, thread.RuntimeACPAgent, map[string]any{"acp_model_id": model, "acp_reasoning_effort": reasoning})
	return err
}
