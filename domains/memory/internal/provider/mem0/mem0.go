package mem0

import (
	"context"
	"errors"
	"log/slog"

	memorydomain "github.com/memohai/memoh/domains/memory"
	storefs "github.com/memohai/memoh/domains/memory/internal/store/fs"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
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

func (*Mem0Provider) OnBeforeChat(_ context.Context, _ memprovider.BeforeChatRequest) (*memprovider.BeforeChatResult, error) {
	return nil, nil
}

func (*Mem0Provider) OnAfterChat(_ context.Context, _ memprovider.AfterChatRequest) error {
	return nil
}

func (*Mem0Provider) Add(_ context.Context, _ memprovider.AddRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) Search(_ context.Context, _ memprovider.SearchRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) GetAll(_ context.Context, _ memprovider.GetAllRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, errMem0Disabled
}

func (*Mem0Provider) Update(_ context.Context, _ memprovider.UpdateRequest) (memorydomain.Item, error) {
	return memorydomain.Item{}, errMem0Disabled
}

func (*Mem0Provider) Delete(_ context.Context, _ string) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) DeleteBatch(_ context.Context, _ []string) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) DeleteAll(_ context.Context, _ memprovider.DeleteAllRequest) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, errMem0Disabled
}

func (*Mem0Provider) Compact(_ context.Context, _ map[string]any, _ float64, _ int) (memprovider.CompactResult, error) {
	return memprovider.CompactResult{}, errMem0Disabled
}

func (*Mem0Provider) Usage(_ context.Context, _ map[string]any) (memprovider.UsageResponse, error) {
	return memprovider.UsageResponse{}, errMem0Disabled
}

func (*Mem0Provider) Status(_ context.Context, _ string) (memprovider.MemoryStatusResponse, error) {
	return memprovider.MemoryStatusResponse{
		ProviderType:  Mem0Type,
		MemoryMode:    "disabled",
		CanManualSync: false,
		Compact: memprovider.MemoryCompactCapability{
			Reason: errMem0Disabled.Error(),
		},
	}, nil
}

func (*Mem0Provider) Rebuild(_ context.Context, _ string) (memprovider.RebuildResult, error) {
	return memprovider.RebuildResult{}, errMem0Disabled
}
