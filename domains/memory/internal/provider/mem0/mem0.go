package mem0

import (
	"context"
	"errors"
	"log/slog"

	memorydomain "github.com/memohai/memoh/domains/memory"
	storefs "github.com/memohai/memoh/domains/memory/internal/store/fs"
	memreg "github.com/memohai/memoh/domains/memory/registry"
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

func (*Mem0Provider) OnBeforeChat(_ context.Context, _ memreg.BeforeChatRequest) (*memreg.BeforeChatResult, error) {
	return nil, nil
}

func (*Mem0Provider) OnAfterChat(_ context.Context, _ memreg.AfterChatRequest) error {
	return nil
}

func (*Mem0Provider) Add(_ context.Context, _ memreg.AddRequest) (memreg.SearchResponse, error) {
	return memreg.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) Search(_ context.Context, _ memreg.SearchRequest) (memreg.SearchResponse, error) {
	return memreg.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) GetAll(_ context.Context, _ memreg.GetAllRequest) (memreg.SearchResponse, error) {
	return memreg.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) Update(_ context.Context, _ memreg.UpdateRequest) (memorydomain.Item, error) {
	return memorydomain.Item{}, errMem0Disabled
}

func (*Mem0Provider) Delete(_ context.Context, _ string) (memreg.DeleteResponse, error) {
	return memreg.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) DeleteBatch(_ context.Context, _ []string) (memreg.DeleteResponse, error) {
	return memreg.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) DeleteAll(_ context.Context, _ memreg.DeleteAllRequest) (memreg.DeleteResponse, error) {
	return memreg.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) Compact(_ context.Context, _ map[string]any, _ float64, _ int) (memreg.CompactResult, error) {
	return memreg.CompactResult{}, errMem0Disabled
}

func (*Mem0Provider) Usage(_ context.Context, _ map[string]any) (memreg.UsageResponse, error) {
	return memreg.UsageResponse{}, errMem0Disabled
}

func (*Mem0Provider) Status(_ context.Context, _ string) (memreg.MemoryStatusResponse, error) {
	return memreg.MemoryStatusResponse{
		ProviderType:  Mem0Type,
		MemoryMode:    "disabled",
		CanManualSync: false,
		Compact: memreg.MemoryCompactCapability{
			Reason: errMem0Disabled.Error(),
		},
	}, nil
}

func (*Mem0Provider) Rebuild(_ context.Context, _ string) (memreg.RebuildResult, error) {
	return memreg.RebuildResult{}, errMem0Disabled
}
