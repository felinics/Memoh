package openviking

import (
	"context"
	"errors"
	"log/slog"

	memorydomain "github.com/memohai/memoh/domains/memory"
	memreg "github.com/memohai/memoh/domains/memory/registry"
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

func (*OpenVikingProvider) OnBeforeChat(_ context.Context, _ memreg.BeforeChatRequest) (*memreg.BeforeChatResult, error) {
	return nil, nil
}

func (*OpenVikingProvider) OnAfterChat(_ context.Context, _ memreg.AfterChatRequest) error {
	return nil
}

func (*OpenVikingProvider) Add(_ context.Context, _ memreg.AddRequest) (memreg.SearchResponse, error) {
	return memreg.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Search(_ context.Context, _ memreg.SearchRequest) (memreg.SearchResponse, error) {
	return memreg.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) GetAll(_ context.Context, _ memreg.GetAllRequest) (memreg.SearchResponse, error) {
	return memreg.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Update(_ context.Context, _ memreg.UpdateRequest) (memorydomain.Item, error) {
	return memorydomain.Item{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Delete(_ context.Context, _ string) (memreg.DeleteResponse, error) {
	return memreg.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) DeleteBatch(_ context.Context, _ []string) (memreg.DeleteResponse, error) {
	return memreg.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) DeleteAll(_ context.Context, _ memreg.DeleteAllRequest) (memreg.DeleteResponse, error) {
	return memreg.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Compact(_ context.Context, _ map[string]any, _ float64, _ int) (memreg.CompactResult, error) {
	return memreg.CompactResult{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Usage(_ context.Context, _ map[string]any) (memreg.UsageResponse, error) {
	return memreg.UsageResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Status(_ context.Context, _ string) (memreg.MemoryStatusResponse, error) {
	return memreg.MemoryStatusResponse{
		ProviderType:  OpenVikingType,
		MemoryMode:    "disabled",
		CanManualSync: false,
		Compact: memreg.MemoryCompactCapability{
			Reason: errOpenVikingDisabled.Error(),
		},
	}, nil
}

func (*OpenVikingProvider) Rebuild(_ context.Context, _ string) (memreg.RebuildResult, error) {
	return memreg.RebuildResult{}, errOpenVikingDisabled
}
