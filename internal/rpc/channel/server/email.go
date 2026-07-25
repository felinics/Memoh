package server

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	"github.com/memohai/memoh/internal/rpc/channel/internal/codec"
)

type EmailProvider interface {
	RefreshEmailProvider(context.Context, channel.RefreshEmailProviderCommand) error
	SendEmail(context.Context, channel.SendEmailCommand) (channel.SendEmailResult, error)
}

type Email struct {
	channelpb.UnimplementedChannelEmailServiceServer
	provider EmailProvider
}

func NewEmail(provider EmailProvider) *Email { return &Email{provider: provider} }

func (s *Email) RefreshProvider(ctx context.Context, req *channelpb.RefreshProviderRequest) (*emptypb.Empty, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	if err := requiredBytes("team_id", req.GetTeamId(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	if err := requiredBytes("provider_id", req.GetProviderId(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	if err := s.provider.RefreshEmailProvider(ctx, channel.RefreshEmailProviderCommand{TeamID: clean(req.GetTeamId()), ProviderID: clean(req.GetProviderId())}); err != nil {
		return nil, codec.EncodeError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Email) SendEmail(ctx context.Context, req *channelpb.SendEmailRequest) (*channelpb.SendEmailResponse, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	if err := requiredBytes("team_id", req.GetTeamId(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	if err := requiredBytes("bot_id", req.GetBotId(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	if len(req.GetProviderId()) > 256 {
		return nil, codec.EncodeError(invalid("provider_id", 256))
	}
	if len(req.GetTo()) == 0 || len(req.GetTo()) > 100 {
		return nil, codec.EncodeError(invalid("to", 100))
	}
	for _, recipient := range req.GetTo() {
		if err := requiredBytes("to", recipient, 998); err != nil {
			return nil, codec.EncodeError(err)
		}
	}
	if len(req.GetSubject()) > 998 {
		return nil, codec.EncodeError(invalid("subject", 998))
	}
	if len(req.GetBody()) > 1<<20 {
		return nil, codec.EncodeError(invalid("body", 1<<20))
	}
	value, err := s.provider.SendEmail(ctx, channel.SendEmailCommand{TeamID: clean(req.GetTeamId()), BotID: clean(req.GetBotId()), ProviderID: clean(req.GetProviderId()), To: append([]string(nil), req.GetTo()...), Subject: req.GetSubject(), Body: req.GetBody(), HTML: req.GetHtml()})
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	return &channelpb.SendEmailResponse{MessageId: value.MessageID}, nil
}
