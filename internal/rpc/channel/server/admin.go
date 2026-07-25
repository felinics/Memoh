package server

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	"github.com/memohai/memoh/internal/rpc/channel/internal/codec"
)

type AdminProvider interface {
	UpsertChannelConfig(context.Context, channel.UpsertConfigCommand) (channel.Config, error)
	SetChannelDisabled(context.Context, channel.SetDisabledCommand) (channel.Config, error)
	DeleteChannelConfig(context.Context, channel.DeleteChannelConfigCommand) error
	SetWebhookEndpoint(context.Context, channel.SetWebhookEndpointCommand) (channel.WebhookEndpoint, error)
}

type Admin struct {
	channelpb.UnimplementedChannelAdminServiceServer
	provider    AdminProvider
	identities  channel.IdentityReader
	projections channel.ConversationProjectionReader
}

func NewAdmin(provider AdminProvider, identities channel.IdentityReader, projections channel.ConversationProjectionReader) *Admin {
	return &Admin{provider: provider, identities: identities, projections: projections}
}

func (s *Admin) UpsertConfig(ctx context.Context, req *channelpb.UpsertConfigRequest) (*channelpb.ChannelConfigResponse, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	cmd, err := decodeUpsert(req)
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	value, err := s.provider.UpsertChannelConfig(ctx, cmd)
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	wire, err := codec.ConfigToProto(value)
	if err != nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	return &channelpb.ChannelConfigResponse{Config: wire}, nil
}

func (s *Admin) SetStatus(ctx context.Context, req *channelpb.SetStatusRequest) (*channelpb.ChannelConfigResponse, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	if err := validateScopedType(req.GetTeamId(), req.GetBotId(), req.GetChannelType()); err != nil {
		return nil, codec.EncodeError(err)
	}
	typ, _ := codec.ChannelTypeFromProto(req.GetChannelType())
	value, err := s.provider.SetChannelDisabled(ctx, channel.SetDisabledCommand{TeamID: clean(req.GetTeamId()), BotID: clean(req.GetBotId()), ChannelType: typ, Disabled: req.GetDisabled()})
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	wire, err := codec.ConfigToProto(value)
	if err != nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	return &channelpb.ChannelConfigResponse{Config: wire}, nil
}

func (s *Admin) DeleteConfig(ctx context.Context, req *channelpb.DeleteConfigRequest) (*emptypb.Empty, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	if err := validateScopedType(req.GetTeamId(), req.GetBotId(), req.GetChannelType()); err != nil {
		return nil, codec.EncodeError(err)
	}
	typ, _ := codec.ChannelTypeFromProto(req.GetChannelType())
	if err := s.provider.DeleteChannelConfig(ctx, channel.DeleteChannelConfigCommand{TeamID: clean(req.GetTeamId()), BotID: clean(req.GetBotId()), ChannelType: typ}); err != nil {
		return nil, codec.EncodeError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Admin) SetWebhookEndpoint(ctx context.Context, req *channelpb.SetWebhookEndpointRequest) (*channelpb.SetWebhookEndpointResponse, error) {
	if s.provider == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	if err := validateScopedType(req.GetTeamId(), req.GetBotId(), req.GetChannelType()); err != nil {
		return nil, codec.EncodeError(err)
	}
	if err := requiredBytes("endpoint", req.GetEndpoint(), 500); err != nil {
		return nil, codec.EncodeError(channel.NewDomainError(channel.ErrInvalidWebhook, channel.ErrorDetail{Reason: channel.ErrorReasonInvalidWebhook, Field: "endpoint", Limit: 500}))
	}
	typ, _ := codec.ChannelTypeFromProto(req.GetChannelType())
	value, err := s.provider.SetWebhookEndpoint(ctx, channel.SetWebhookEndpointCommand{TeamID: clean(req.GetTeamId()), BotID: clean(req.GetBotId()), ChannelType: typ, Endpoint: clean(req.GetEndpoint())})
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	return &channelpb.SetWebhookEndpointResponse{Endpoint: value.Endpoint}, nil
}

func (s *Admin) ListIdentityProjections(ctx context.Context, req *channelpb.ListIdentityProjectionsRequest) (*channelpb.ListIdentityProjectionsResponse, error) {
	if s.identities == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	ids := req.GetIdentityIds()
	if len(ids) > 1000 {
		return nil, codec.EncodeError(invalid("identity_ids", 1000))
	}
	if len(ids) == 0 {
		return &channelpb.ListIdentityProjectionsResponse{Identities: []*channelpb.IdentityProjection{}}, nil
	}
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = clean(id)
		if _, err := uuid.Parse(id); err != nil {
			return nil, codec.EncodeError(invalid("identity_ids", 0))
		}
		normalized = append(normalized, id)
	}
	items, err := s.identities.ListIdentityProjections(ctx, normalized)
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	response := &channelpb.ListIdentityProjectionsResponse{
		Identities: make([]*channelpb.IdentityProjection, 0, len(items)),
	}
	for _, item := range items {
		response.Identities = append(response.Identities, codec.IdentityProjectionToProto(item))
	}
	return response, nil
}

func (s *Admin) ListConversationProjections(ctx context.Context, req *channelpb.ListConversationProjectionsRequest) (*channelpb.ListConversationProjectionsResponse, error) {
	if s.projections == nil {
		return nil, codec.EncodeError(channel.ErrUnknown)
	}
	if err := requiredBytes("bot_id", req.GetBotId(), 256); err != nil {
		return nil, codec.EncodeError(err)
	}
	ids := req.GetRouteIds()
	if len(ids) > 1000 {
		return nil, codec.EncodeError(invalid("route_ids", 1000))
	}
	if len(ids) == 0 {
		return &channelpb.ListConversationProjectionsResponse{Projections: []*channelpb.ConversationProjection{}}, nil
	}
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = clean(id)
		if _, err := uuid.Parse(id); err != nil {
			return nil, codec.EncodeError(invalid("route_ids", 0))
		}
		normalized = append(normalized, id)
	}
	channelType := clean(req.GetChannelType())
	if channelType != "" {
		if err := requiredBytes("channel_type", channelType, 64); err != nil {
			return nil, codec.EncodeError(err)
		}
	}
	items, err := s.projections.ListConversationProjections(ctx, channel.ConversationProjectionRequest{
		BotID: clean(req.GetBotId()), RouteIDs: normalized, ChannelType: channelType,
	})
	if err != nil {
		return nil, codec.EncodeError(err)
	}
	response := &channelpb.ListConversationProjectionsResponse{
		Projections: make([]*channelpb.ConversationProjection, 0, len(items)),
	}
	for _, item := range items {
		response.Projections = append(response.Projections, codec.ConversationProjectionToProto(item))
	}
	return response, nil
}

func decodeUpsert(req *channelpb.UpsertConfigRequest) (channel.UpsertConfigCommand, error) {
	if err := validateScopedType(req.GetTeamId(), req.GetBotId(), req.GetChannelType()); err != nil {
		return channel.UpsertConfigCommand{}, err
	}
	if len(req.GetExternalIdentity()) > 512 {
		return channel.UpsertConfigCommand{}, invalid("external_identity", 512)
	}
	typ, _ := codec.ChannelTypeFromProto(req.GetChannelType())
	provider, err := codec.ProviderConfigFromProto(req.GetConfig())
	if err != nil {
		return channel.UpsertConfigCommand{}, invalid("config", 0)
	}
	if !providerMatches(typ, provider) {
		return channel.UpsertConfigCommand{}, invalid("config", 0)
	}
	self, err := codec.SelfIdentityFromProto(req.GetSelfIdentity())
	if err != nil {
		return channel.UpsertConfigCommand{}, invalid("self_identity", 0)
	}
	routing, err := codec.RoutingFromProto(req.GetRouting())
	if err != nil {
		return channel.UpsertConfigCommand{}, invalid("routing", 0)
	}
	verified, err := optionalTimestamp(req.GetVerifiedAt())
	if err != nil {
		return channel.UpsertConfigCommand{}, invalid("verified_at", 0)
	}
	return channel.UpsertConfigCommand{TeamID: clean(req.GetTeamId()), BotID: clean(req.GetBotId()), ChannelType: typ, Config: provider, ExternalIdentity: req.GetExternalIdentity(), SelfIdentity: self, Routing: routing, Disabled: req.Disabled, VerifiedAt: verified}, nil
}
