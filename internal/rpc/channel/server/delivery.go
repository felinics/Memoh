package server

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	"github.com/memohai/memoh/internal/rpc/channel/internal/codec"
)

type DeliveryProvider interface {
	ReactToChannelMessage(context.Context, channel.ReactCommand) error
}

type Delivery struct {
	channelpb.UnimplementedChannelDeliveryServiceServer
	provider DeliveryProvider
}

func NewDelivery(provider DeliveryProvider) *Delivery { return &Delivery{provider: provider} }

func (s *Delivery) React(ctx context.Context, req *channelpb.ReactRequest) (*emptypb.Empty, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	if err := validateScopedType(req.GetTeamId(), req.GetBotId(), req.GetChannelType()); err != nil {
		return nil, codec.EncodeError(err)
	}
	if err := requiredBytes("target", req.GetTarget(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	if err := requiredBytes("message_id", req.GetMessageId(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	if len(req.GetEmoji()) > 64 || (!req.GetRemove() && clean(req.GetEmoji()) == "") {
		return nil, codec.EncodeError(invalid("emoji", 64))
	}
	typ, _ := codec.ChannelTypeFromProto(req.GetChannelType())
	err := s.provider.ReactToChannelMessage(ctx, channel.ReactCommand{TeamID: clean(req.GetTeamId()), BotID: clean(req.GetBotId()), ChannelType: typ, Target: clean(req.GetTarget()), MessageID: clean(req.GetMessageId()), Emoji: clean(req.GetEmoji()), Remove: req.GetRemove()})
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	return &emptypb.Empty{}, nil
}
