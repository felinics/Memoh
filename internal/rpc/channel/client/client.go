package client

import (
	"context"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	"github.com/memohai/memoh/internal/rpc/channel/internal/codec"
)

type Client struct {
	admin    channelpb.ChannelAdminServiceClient
	delivery channelpb.ChannelDeliveryServiceClient
	status   channelpb.ChannelStatusServiceClient
	email    channelpb.ChannelEmailServiceClient
}

func New(conn grpc.ClientConnInterface) *Client {
	return &Client{
		admin: channelpb.NewChannelAdminServiceClient(conn), delivery: channelpb.NewChannelDeliveryServiceClient(conn),
		status: channelpb.NewChannelStatusServiceClient(conn), email: channelpb.NewChannelEmailServiceClient(conn),
	}
}

func (c *Client) UpsertChannelConfig(ctx context.Context, cmd channel.UpsertConfigCommand) (channel.Config, error) {
	provider, err := codec.ProviderConfigToProto(cmd.Config)
	if err != nil {
		return channel.Config{}, channel.NewDomainError(channel.ErrInvalidArgument, channel.ErrorDetail{Field: "config"})
	}
	self, err := codec.SelfIdentityToProtoForType(cmd.SelfIdentity, cmd.ChannelType)
	if err != nil {
		return channel.Config{}, channel.NewDomainError(channel.ErrInvalidArgument, channel.ErrorDetail{Field: "self_identity"})
	}
	routing, err := codec.RoutingToProto(cmd.Routing)
	if err != nil {
		return channel.Config{}, channel.NewDomainError(channel.ErrInvalidArgument, channel.ErrorDetail{Field: "routing"})
	}
	req := &channelpb.UpsertConfigRequest{TeamId: cmd.TeamID, BotId: cmd.BotID, ChannelType: codec.ChannelTypeToProto(cmd.ChannelType), Config: provider, ExternalIdentity: cmd.ExternalIdentity, SelfIdentity: self, Routing: routing, Disabled: cmd.Disabled}
	if cmd.VerifiedAt != nil {
		req.VerifiedAt = timestamppb.New(cmd.VerifiedAt.UTC())
	}
	resp, err := c.admin.UpsertConfig(ctx, req)
	if err != nil {
		return channel.Config{}, decode(err)
	}
	value, err := codec.ConfigFromProto(resp.GetConfig())
	if err != nil {
		return channel.Config{}, channel.NewDomainError(channel.ErrUnknown, channel.ErrorDetail{})
	}
	return value, nil
}

func (c *Client) SetChannelDisabled(ctx context.Context, cmd channel.SetDisabledCommand) (channel.Config, error) {
	resp, err := c.admin.SetStatus(ctx, &channelpb.SetStatusRequest{TeamId: cmd.TeamID, BotId: cmd.BotID, ChannelType: codec.ChannelTypeToProto(cmd.ChannelType), Disabled: cmd.Disabled})
	if err != nil {
		return channel.Config{}, decode(err)
	}
	value, err := codec.ConfigFromProto(resp.GetConfig())
	if err != nil {
		return channel.Config{}, channel.NewDomainError(channel.ErrUnknown, channel.ErrorDetail{})
	}
	return value, nil
}

func (c *Client) DeleteChannelConfig(ctx context.Context, cmd channel.DeleteChannelConfigCommand) error {
	_, err := c.admin.DeleteConfig(ctx, &channelpb.DeleteConfigRequest{TeamId: cmd.TeamID, BotId: cmd.BotID, ChannelType: codec.ChannelTypeToProto(cmd.ChannelType)})
	return decode(err)
}

func (c *Client) SetWebhookEndpoint(ctx context.Context, cmd channel.SetWebhookEndpointCommand) (channel.WebhookEndpoint, error) {
	resp, err := c.admin.SetWebhookEndpoint(ctx, &channelpb.SetWebhookEndpointRequest{TeamId: cmd.TeamID, BotId: cmd.BotID, ChannelType: codec.ChannelTypeToProto(cmd.ChannelType), Endpoint: cmd.Endpoint})
	if err != nil {
		return channel.WebhookEndpoint{}, decode(err)
	}
	return channel.WebhookEndpoint{Endpoint: resp.GetEndpoint()}, nil
}

func (c *Client) ListIdentityProjections(ctx context.Context, ids []string) ([]channel.IdentityProjection, error) {
	if len(ids) == 0 {
		return []channel.IdentityProjection{}, nil
	}
	resp, err := c.admin.ListIdentityProjections(ctx, &channelpb.ListIdentityProjectionsRequest{
		IdentityIds: append([]string(nil), ids...),
	})
	if err != nil {
		return nil, decode(err)
	}
	items := make([]channel.IdentityProjection, 0, len(resp.GetIdentities()))
	for _, wire := range resp.GetIdentities() {
		item, err := codec.IdentityProjectionFromProto(wire)
		if err != nil {
			return nil, channel.NewDomainError(channel.ErrUnknown, channel.ErrorDetail{})
		}
		items = append(items, item)
	}
	return items, nil
}

func (c *Client) ListConversationProjections(ctx context.Context, request channel.ConversationProjectionRequest) ([]channel.ConversationProjection, error) {
	if len(request.RouteIDs) == 0 {
		return []channel.ConversationProjection{}, nil
	}
	resp, err := c.admin.ListConversationProjections(ctx, &channelpb.ListConversationProjectionsRequest{
		BotId: request.BotID, RouteIds: append([]string(nil), request.RouteIDs...), ChannelType: request.ChannelType,
	})
	if err != nil {
		return nil, decode(err)
	}
	items := make([]channel.ConversationProjection, 0, len(resp.GetProjections()))
	for _, value := range resp.GetProjections() {
		item, err := codec.ConversationProjectionFromProto(value)
		if err != nil {
			return nil, channel.NewDomainError(channel.ErrUnknown, channel.ErrorDetail{})
		}
		items = append(items, item)
	}
	return items, nil
}

func (c *Client) ReactToChannelMessage(ctx context.Context, cmd channel.ReactCommand) error {
	_, err := c.delivery.React(ctx, &channelpb.ReactRequest{TeamId: cmd.TeamID, BotId: cmd.BotID, ChannelType: codec.ChannelTypeToProto(cmd.ChannelType), Target: cmd.Target, MessageId: cmd.MessageID, Emoji: cmd.Emoji, Remove: cmd.Remove})
	return decode(err)
}

func (c *Client) ConnectionStatuses(ctx context.Context, teamID, botID string) ([]channel.ConnectionStatus, error) {
	resp, err := c.status.ListConnectionStatuses(ctx, &channelpb.ListConnectionStatusesRequest{TeamId: teamID, BotId: botID})
	if err != nil {
		return nil, decode(err)
	}
	result := make([]channel.ConnectionStatus, 0, len(resp.GetStatuses()))
	for _, item := range resp.GetStatuses() {
		value, err := codec.ConnectionStatusFromProto(item)
		if err != nil {
			return nil, channel.NewDomainError(channel.ErrUnknown, channel.ErrorDetail{})
		}
		result = append(result, value)
	}
	slices.SortFunc(result, func(a, b channel.ConnectionStatus) int {
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
	return result, nil
}

func (c *Client) TunnelStatus(ctx context.Context) (channel.TunnelStatus, error) {
	resp, err := c.status.GetTunnelStatus(ctx, &channelpb.GetTunnelStatusRequest{})
	if err != nil {
		return channel.TunnelStatus{}, decode(err)
	}
	value, err := codec.TunnelStatusFromProto(resp)
	if err != nil {
		return channel.TunnelStatus{}, channel.NewDomainError(channel.ErrUnknown, channel.ErrorDetail{})
	}
	return value, nil
}

func (c *Client) RefreshEmailProvider(ctx context.Context, cmd channel.RefreshEmailProviderCommand) error {
	_, err := c.email.RefreshProvider(ctx, &channelpb.RefreshProviderRequest{TeamId: cmd.TeamID, ProviderId: cmd.ProviderID})
	return decode(err)
}

func (c *Client) SendEmail(ctx context.Context, cmd channel.SendEmailCommand) (channel.SendEmailResult, error) {
	resp, err := c.email.SendEmail(ctx, &channelpb.SendEmailRequest{TeamId: cmd.TeamID, BotId: cmd.BotID, ProviderId: cmd.ProviderID, To: append([]string(nil), cmd.To...), Subject: cmd.Subject, Body: cmd.Body, Html: cmd.HTML})
	if err != nil {
		return channel.SendEmailResult{}, decode(err)
	}
	return channel.SendEmailResult{MessageID: resp.GetMessageId()}, nil
}
