package mem0

import (
	"context"
	"errors"
	"log/slog"

	"github.com/felinics/memoh/internal/mcp"
	adapters "github.com/felinics/memoh/internal/memory/adapters"
	storefs "github.com/felinics/memoh/internal/memory/storefs"
)

const Mem0Type = "mem0"

var errMem0Disabled = errors.New("mem0 provider is disabled")

// Mem0Provider is kept as a provider interface placeholder. The external Mem0
// integration logic has been removed; selecting this provider is a no-op for
// chat memory and returns an unsupported error for direct CRUD calls.
type Mem0Provider struct{}

func NewMem0Provider(_ *slog.Logger, _ map[string]any, _ *storefs.Service) (*Mem0Provider, error) {
	return &Mem0Provider{}, nil
}

func (*Mem0Provider) Type() string { return Mem0Type }

func (*Mem0Provider) OnBeforeChat(_ context.Context, _ adapters.BeforeChatRequest) (*adapters.BeforeChatResult, error) {
	return nil, nil
}

func (*Mem0Provider) OnAfterChat(_ context.Context, _ adapters.AfterChatRequest) error {
	return nil
}

func (*Mem0Provider) ListTools(_ context.Context, _ mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return nil, nil
}

func (*Mem0Provider) CallTool(_ context.Context, _ mcp.ToolSessionContext, _ string, _ map[string]any) (map[string]any, error) {
	return nil, mcp.ErrToolNotFound
}

func (*Mem0Provider) Add(_ context.Context, _ adapters.AddRequest) (adapters.SearchResponse, error) {
	return adapters.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) Search(_ context.Context, _ adapters.SearchRequest) (adapters.SearchResponse, error) {
	return adapters.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) GetAll(_ context.Context, _ adapters.GetAllRequest) (adapters.SearchResponse, error) {
	return adapters.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) Update(_ context.Context, _ adapters.UpdateRequest) (adapters.MemoryItem, error) {
	return adapters.MemoryItem{}, errMem0Disabled
}

func (*Mem0Provider) Delete(_ context.Context, _ string) (adapters.DeleteResponse, error) {
	return adapters.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) DeleteBatch(_ context.Context, _ []string) (adapters.DeleteResponse, error) {
	return adapters.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) DeleteAll(_ context.Context, _ adapters.DeleteAllRequest) (adapters.DeleteResponse, error) {
	return adapters.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) Compact(_ context.Context, _ map[string]any, _ float64, _ int) (adapters.CompactResult, error) {
	return adapters.CompactResult{}, errMem0Disabled
}

func (*Mem0Provider) Usage(_ context.Context, _ map[string]any) (adapters.UsageResponse, error) {
	return adapters.UsageResponse{}, errMem0Disabled
}

func (*Mem0Provider) Status(_ context.Context, _ string) (adapters.MemoryStatusResponse, error) {
	return adapters.MemoryStatusResponse{
		ProviderType:  Mem0Type,
		MemoryMode:    "disabled",
		CanManualSync: false,
		Compact: adapters.MemoryCompactCapability{
			Reason: errMem0Disabled.Error(),
		},
	}, nil
}

func (*Mem0Provider) Rebuild(_ context.Context, _ string) (adapters.RebuildResult, error) {
	return adapters.RebuildResult{}, errMem0Disabled
}
