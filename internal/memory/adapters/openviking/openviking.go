package openviking

import (
	"context"
	"errors"
	"log/slog"

	"github.com/felinics/memoh/internal/mcp"
	adapters "github.com/felinics/memoh/internal/memory/adapters"
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

func (*OpenVikingProvider) OnBeforeChat(_ context.Context, _ adapters.BeforeChatRequest) (*adapters.BeforeChatResult, error) {
	return nil, nil
}

func (*OpenVikingProvider) OnAfterChat(_ context.Context, _ adapters.AfterChatRequest) error {
	return nil
}

func (*OpenVikingProvider) ListTools(_ context.Context, _ mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return nil, nil
}

func (*OpenVikingProvider) CallTool(_ context.Context, _ mcp.ToolSessionContext, _ string, _ map[string]any) (map[string]any, error) {
	return nil, mcp.ErrToolNotFound
}

func (*OpenVikingProvider) Add(_ context.Context, _ adapters.AddRequest) (adapters.SearchResponse, error) {
	return adapters.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Search(_ context.Context, _ adapters.SearchRequest) (adapters.SearchResponse, error) {
	return adapters.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) GetAll(_ context.Context, _ adapters.GetAllRequest) (adapters.SearchResponse, error) {
	return adapters.SearchResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Update(_ context.Context, _ adapters.UpdateRequest) (adapters.MemoryItem, error) {
	return adapters.MemoryItem{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Delete(_ context.Context, _ string) (adapters.DeleteResponse, error) {
	return adapters.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) DeleteBatch(_ context.Context, _ []string) (adapters.DeleteResponse, error) {
	return adapters.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) DeleteAll(_ context.Context, _ adapters.DeleteAllRequest) (adapters.DeleteResponse, error) {
	return adapters.DeleteResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Compact(_ context.Context, _ map[string]any, _ float64, _ int) (adapters.CompactResult, error) {
	return adapters.CompactResult{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Usage(_ context.Context, _ map[string]any) (adapters.UsageResponse, error) {
	return adapters.UsageResponse{}, errOpenVikingDisabled
}

func (*OpenVikingProvider) Status(_ context.Context, _ string) (adapters.MemoryStatusResponse, error) {
	return adapters.MemoryStatusResponse{
		ProviderType:  OpenVikingType,
		MemoryMode:    "disabled",
		CanManualSync: false,
		Compact: adapters.MemoryCompactCapability{
			Reason: errOpenVikingDisabled.Error(),
		},
	}, nil
}

func (*OpenVikingProvider) Rebuild(_ context.Context, _ string) (adapters.RebuildResult, error) {
	return adapters.RebuildResult{}, errOpenVikingDisabled
}
