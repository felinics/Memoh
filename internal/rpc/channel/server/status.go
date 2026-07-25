package server

import (
	"context"
	"slices"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	"github.com/memohai/memoh/internal/rpc/channel/internal/codec"
)

type StatusProvider interface {
	ConnectionStatuses(context.Context, string, string) ([]channel.ConnectionStatus, error)
	TunnelStatus(context.Context) (channel.TunnelStatus, error)
}

type Status struct {
	channelpb.UnimplementedChannelStatusServiceServer
	provider StatusProvider
}

func NewStatus(provider StatusProvider) *Status { return &Status{provider: provider} }

func (s *Status) ListConnectionStatuses(ctx context.Context, req *channelpb.ListConnectionStatusesRequest) (*channelpb.ListConnectionStatusesResponse, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	if err := requiredBytes("team_id", req.GetTeamId(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	if err := requiredBytes("bot_id", req.GetBotId(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	items, err := s.provider.ConnectionStatuses(ctx, clean(req.GetTeamId()), clean(req.GetBotId()))
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	items = append([]channel.ConnectionStatus(nil), items...)
	slices.SortFunc(items, func(a, b channel.ConnectionStatus) int {
		if a.ChannelType != b.ChannelType {
			return int(a.ChannelType) - int(b.ChannelType)
		}
		if a.ConfigID < b.ConfigID {
			return -1
		}
		if a.ConfigID > b.ConfigID {
			return 1
		}
		return 0
	})
	result := make([]*channelpb.ConnectionStatus, 0, len(items))
	for _, item := range items {
		item.LastError = truncateUTF8(item.LastError, 4<<10)
		item.UpdatedAt = item.UpdatedAt.UTC()
		result = append(result, codec.ConnectionStatusToProto(item))
	}
	return &channelpb.ListConnectionStatusesResponse{Statuses: result}, nil
}

func (s *Status) GetTunnelStatus(ctx context.Context, _ *channelpb.GetTunnelStatusRequest) (*channelpb.GetTunnelStatusResponse, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	value, err := s.provider.TunnelStatus(ctx)
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	value.PublicBaseURL = truncateUTF8(value.PublicBaseURL, 2<<10)
	value.Error = truncateUTF8(value.Error, 4<<10)
	if value.Mode <= channel.TunnelModeUnspecified || value.Mode > channel.TunnelModeManaged || value.Status <= channel.TunnelStateUnspecified || value.Status > channel.TunnelStateError {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	return codec.TunnelStatusToProto(value), nil
}
