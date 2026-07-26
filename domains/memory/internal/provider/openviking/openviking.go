package openviking

import (
	"context"
	"errors"
	"log/slog"

	memorydomain "github.com/memohai/memoh/domains/memory"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
)

const OpenVikingType = "openviking"

var errOpenVikingDisabled = errors.New("openviking provider is disabled")

// OpenVikingProvider is kept as a provider interface placeholder. The external
// OpenViking integration logic has been removed; selecting this provider is a
// no-op for chat memory and returns an unsupported error for direct CRUD calls.
type OpenVikingProvider struct{}

func NewOpenVikingProvider(_ *slog.Logger, _ map[string]any) (*OpenVikingProvider, error) {
	return &OpenVikingProvider{}, nil
}

func (*OpenVikingProvider) Type() string { return OpenVikingType }

func (*OpenVikingProvider) OnBeforeChat(_ context.Context, _ memprovider.BeforeChatRequest) (*memprovider.BeforeChatResult, error) {
	return nil, nil
}

func (*OpenVikingProvider) OnAfterChat(_ context.Context, _ memprovider.AfterChatRequest) error {
	return nil
}

func (*OpenVikingProvider) Add(_ context.Context, _ memprovider.AddRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Search(_ context.Context, _ memprovider.SearchRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) GetAll(_ context.Context, _ memprovider.GetAllRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Update(_ context.Context, _ memprovider.UpdateRequest) (memorydomain.Item, error) {
	return memorydomain.Item{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Delete(_ context.Context, _ string) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) DeleteBatch(_ context.Context, _ []string) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) DeleteAll(_ context.Context, _ memprovider.DeleteAllRequest) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Compact(_ context.Context, _ map[string]any, _ float64, _ int) (memprovider.CompactResult, error) {
	return memprovider.CompactResult{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Usage(_ context.Context, _ map[string]any) (memprovider.UsageResponse, error) {
	return memprovider.UsageResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Status(_ context.Context, _ string) (memprovider.MemoryStatusResponse, error) {
	return memprovider.MemoryStatusResponse{
		ProviderType:  OpenVikingType,
		MemoryMode:    "disabled",
		CanManualSync: false,
		Compact: memprovider.MemoryCompactCapability{
			Reason: errOpenVikingDisabled.Error(),
		},
	}, nil
}

func (*OpenVikingProvider) Rebuild(_ context.Context, _ string) (memprovider.RebuildResult, error) {
	return memprovider.RebuildResult{}, errOpenVikingDisabled
}
