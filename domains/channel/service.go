package channel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

type AdminProvider interface {
	UpsertChannelConfig(context.Context, UpsertConfigCommand) (Config, error)
	SetChannelDisabled(context.Context, SetDisabledCommand) (Config, error)
	DeleteChannelConfig(context.Context, DeleteChannelConfigCommand) error
	SetWebhookEndpoint(context.Context, SetWebhookEndpointCommand) (WebhookEndpoint, error)
}

type DeliveryProvider interface {
	ReactToChannelMessage(context.Context, ReactCommand) error
}

type StatusProvider interface {
	ConnectionStatuses(context.Context, string, string) ([]ConnectionStatus, error)
	TunnelStatus(context.Context) (TunnelStatus, error)
}

type EmailProvider interface {
	RefreshEmailProvider(context.Context, RefreshEmailProviderCommand) error
	SendEmail(context.Context, SendEmailCommand) (SendEmailResult, error)
}

type Dependencies struct {
	Admin         AdminProvider
	Delivery      DeliveryProvider
	Status        StatusProvider
	Email         EmailProvider
	Identity      IdentityReader
	Conversations ConversationProjectionReader
}

// Service is the in-process implementation of the Channel boundary contract.
// It validates and normalizes values before delegating to Channel-owned
// providers. Embedded composition injects it directly; split composition uses
// the RPC client implementing the same consumer ports.
type Service struct{ dependencies Dependencies }

func NewService(dependencies Dependencies) *Service { return &Service{dependencies: dependencies} }

func (a *Service) UpsertChannelConfig(ctx context.Context, cmd UpsertConfigCommand) (Config, error) {
	if err := validateScopedType(cmd.TeamID, cmd.BotID, cmd.ChannelType); err != nil {
		return Config{}, err
	}
	if cmd.Config == nil {
		return Config{}, invalid("config", 0)
	}
	if !providerMatches(cmd.ChannelType, cmd.Config) {
		return Config{}, invalid("config", 0)
	}
	if len(cmd.ExternalIdentity) > 512 {
		return Config{}, invalid("external_identity", 512)
	}
	if a.dependencies.Admin == nil {
		return Config{}, NewDomainError(ErrUnknown, ErrorDetail{})
	}
	value, err := a.dependencies.Admin.UpsertChannelConfig(ctx, cmd)
	return normalizeConfig(value), normalizeError(err)
}

func (a *Service) SetChannelDisabled(ctx context.Context, cmd SetDisabledCommand) (Config, error) {
	if err := validateScopedType(cmd.TeamID, cmd.BotID, cmd.ChannelType); err != nil {
		return Config{}, err
	}
	if a.dependencies.Admin == nil {
		return Config{}, NewDomainError(ErrUnknown, ErrorDetail{})
	}
	value, err := a.dependencies.Admin.SetChannelDisabled(ctx, cmd)
	return normalizeConfig(value), normalizeError(err)
}

func (a *Service) DeleteChannelConfig(ctx context.Context, cmd DeleteChannelConfigCommand) error {
	if err := validateScopedType(cmd.TeamID, cmd.BotID, cmd.ChannelType); err != nil {
		return err
	}
	if a.dependencies.Admin == nil {
		return NewDomainError(ErrUnknown, ErrorDetail{})
	}
	return normalizeError(a.dependencies.Admin.DeleteChannelConfig(ctx, cmd))
}

func (a *Service) SetWebhookEndpoint(ctx context.Context, cmd SetWebhookEndpointCommand) (WebhookEndpoint, error) {
	if err := validateScopedType(cmd.TeamID, cmd.BotID, cmd.ChannelType); err != nil {
		return WebhookEndpoint{}, err
	}
	if err := required("endpoint", cmd.Endpoint, 500); err != nil {
		return WebhookEndpoint{}, NewDomainError(ErrInvalidWebhook, ErrorDetail{Reason: ErrorReasonInvalidWebhook, Field: "endpoint", Limit: 500})
	}
	if a.dependencies.Admin == nil {
		return WebhookEndpoint{}, NewDomainError(ErrUnknown, ErrorDetail{})
	}
	value, err := a.dependencies.Admin.SetWebhookEndpoint(ctx, cmd)
	return value, normalizeError(err)
}

func (a *Service) ListIdentityProjections(ctx context.Context, ids []string) ([]IdentityProjection, error) {
	if len(ids) == 0 {
		return []IdentityProjection{}, nil
	}
	if a.dependencies.Identity == nil {
		return nil, NewDomainError(ErrUnknown, ErrorDetail{})
	}
	items, err := a.dependencies.Identity.ListIdentityProjections(ctx, append([]string(nil), ids...))
	if err != nil {
		return nil, normalizeError(err)
	}
	if items == nil {
		items = []IdentityProjection{}
	}
	return items, nil
}

func (a *Service) ListConversationProjections(ctx context.Context, request ConversationProjectionRequest) ([]ConversationProjection, error) {
	if err := required("bot_id", request.BotID, 256); err != nil {
		return nil, err
	}
	request.BotID = strings.TrimSpace(request.BotID)
	request.ChannelType = strings.TrimSpace(request.ChannelType)
	if len(request.RouteIDs) > 1000 {
		return nil, invalid("route_ids", 1000)
	}
	request.RouteIDs = append([]string(nil), request.RouteIDs...)
	for i := range request.RouteIDs {
		request.RouteIDs[i] = strings.TrimSpace(request.RouteIDs[i])
		if err := required("route_id", request.RouteIDs[i], 256); err != nil {
			return nil, err
		}
	}
	if request.ChannelType != "" {
		if err := required("channel_type", request.ChannelType, 64); err != nil {
			return nil, err
		}
	}
	if len(request.RouteIDs) == 0 {
		return []ConversationProjection{}, nil
	}
	if a.dependencies.Conversations == nil {
		return nil, NewDomainError(ErrUnknown, ErrorDetail{})
	}
	items, err := a.dependencies.Conversations.ListConversationProjections(ctx, request)
	if err != nil {
		return nil, normalizeError(err)
	}
	if items == nil {
		items = []ConversationProjection{}
	}
	return items, nil
}

func (a *Service) ReactToChannelMessage(ctx context.Context, cmd ReactCommand) error {
	if err := validateScopedType(cmd.TeamID, cmd.BotID, cmd.ChannelType); err != nil {
		return err
	}
	if err := required("target", cmd.Target, 256); err != nil {
		return err
	}
	if err := required("message_id", cmd.MessageID, 256); err != nil {
		return err
	}
	if len(cmd.Emoji) > 64 || (!cmd.Remove && strings.TrimSpace(cmd.Emoji) == "") {
		return invalid("emoji", 64)
	}
	cmd.TeamID = strings.TrimSpace(cmd.TeamID)
	cmd.BotID = strings.TrimSpace(cmd.BotID)
	cmd.Target = strings.TrimSpace(cmd.Target)
	cmd.MessageID = strings.TrimSpace(cmd.MessageID)
	cmd.Emoji = strings.TrimSpace(cmd.Emoji)
	if a.dependencies.Delivery == nil {
		return NewDomainError(ErrUnknown, ErrorDetail{})
	}
	return normalizeError(a.dependencies.Delivery.ReactToChannelMessage(ctx, cmd))
}

func (a *Service) ConnectionStatuses(ctx context.Context, teamID, botID string) ([]ConnectionStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := required("team_id", teamID, 256); err != nil {
		return nil, err
	}
	if err := required("bot_id", botID, 256); err != nil {
		return nil, err
	}
	if a.dependencies.Status == nil {
		return nil, NewDomainError(ErrUnknown, ErrorDetail{})
	}
	items, err := a.dependencies.Status.ConnectionStatuses(ctx, strings.TrimSpace(teamID), strings.TrimSpace(botID))
	if err != nil {
		return nil, normalizeError(err)
	}
	if items == nil {
		items = []ConnectionStatus{}
	}
	for index := range items {
		items[index].UpdatedAt = items[index].UpdatedAt.UTC()
		items[index].LastError = truncate(items[index].LastError, 4<<10)
	}
	slices.SortFunc(items, compareStatus)
	return items, nil
}

func (a *Service) TunnelStatus(ctx context.Context) (TunnelStatus, error) {
	if err := ctx.Err(); err != nil {
		return TunnelStatus{}, err
	}
	if a.dependencies.Status == nil {
		return TunnelStatus{}, NewDomainError(ErrUnknown, ErrorDetail{})
	}
	value, err := a.dependencies.Status.TunnelStatus(ctx)
	if err != nil {
		return TunnelStatus{}, normalizeError(err)
	}
	value.PublicBaseURL = truncate(value.PublicBaseURL, 2<<10)
	value.Error = truncate(value.Error, 4<<10)
	return value, nil
}

func (a *Service) RefreshEmailProvider(ctx context.Context, cmd RefreshEmailProviderCommand) error {
	if err := required("team_id", cmd.TeamID, 256); err != nil {
		return err
	}
	if err := required("provider_id", cmd.ProviderID, 256); err != nil {
		return err
	}
	if a.dependencies.Email == nil {
		return NewDomainError(ErrUnknown, ErrorDetail{})
	}
	return normalizeError(a.dependencies.Email.RefreshEmailProvider(ctx, cmd))
}

func (a *Service) SendEmail(ctx context.Context, cmd SendEmailCommand) (SendEmailResult, error) {
	if err := required("team_id", cmd.TeamID, 256); err != nil {
		return SendEmailResult{}, err
	}
	if err := required("bot_id", cmd.BotID, 256); err != nil {
		return SendEmailResult{}, err
	}
	if len(cmd.ProviderID) > 256 {
		return SendEmailResult{}, invalid("provider_id", 256)
	}
	if len(cmd.To) == 0 || len(cmd.To) > 100 {
		return SendEmailResult{}, invalid("to", 100)
	}
	for _, recipient := range cmd.To {
		if err := required("to", recipient, 998); err != nil {
			return SendEmailResult{}, err
		}
	}
	if len(cmd.Subject) > 998 {
		return SendEmailResult{}, invalid("subject", 998)
	}
	if len(cmd.Body) > 1<<20 {
		return SendEmailResult{}, invalid("body", 1<<20)
	}
	if a.dependencies.Email == nil {
		return SendEmailResult{}, NewDomainError(ErrUnknown, ErrorDetail{})
	}
	value, err := a.dependencies.Email.SendEmail(ctx, cmd)
	return value, normalizeError(err)
}

func validateScopedType(teamID, botID string, typ ChannelType) error {
	if err := required("team_id", teamID, 256); err != nil {
		return err
	}
	if err := required("bot_id", botID, 256); err != nil {
		return err
	}
	if typ <= ChannelTypeUnspecified || typ > ChannelTypeWeixin {
		return invalid("channel_type", 0)
	}
	return nil
}

func required(field, value string, limit uint64) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || uint64(len(value)) > limit {
		return invalid(field, limit)
	}
	return nil
}

func invalid(field string, limit uint64) error {
	return NewDomainError(ErrInvalidArgument, ErrorDetail{Field: field, Limit: limit})
}

func compareStatus(a, b ConnectionStatus) int {
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
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func normalizeConfig(value Config) Config {
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if value.VerifiedAt != nil {
		verified := value.VerifiedAt.UTC()
		value.VerifiedAt = &verified
	}
	return value
}

func providerMatches(typ ChannelType, config ProviderConfig) bool {
	switch typ {
	case ChannelTypeDingTalk:
		_, ok := config.(DingTalkConfig)
		return ok
	case ChannelTypeDiscord:
		_, ok := config.(DiscordConfig)
		return ok
	case ChannelTypeFeishu:
		_, ok := config.(FeishuConfig)
		return ok
	case ChannelTypeLine:
		_, ok := config.(LineConfig)
		return ok
	case ChannelTypeMatrix:
		_, ok := config.(MatrixConfig)
		return ok
	case ChannelTypeMisskey:
		_, ok := config.(MisskeyConfig)
		return ok
	case ChannelTypeQQ:
		_, ok := config.(QQConfig)
		return ok
	case ChannelTypeSlack:
		_, ok := config.(SlackConfig)
		return ok
	case ChannelTypeTelegram:
		_, ok := config.(TelegramConfig)
		return ok
	case ChannelTypeWeChatOA:
		_, ok := config.(WeChatOAConfig)
		return ok
	case ChannelTypeWeCom:
		_, ok := config.(WeComConfig)
		return ok
	case ChannelTypeWeixin:
		_, ok := config.(WeixinConfig)
		return ok
	default:
		return false
	}
}

func normalizeError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var domain *DomainError
	if errors.As(err, &domain) {
		return err
	}
	for _, candidate := range []struct {
		cause  error
		reason ErrorReason
	}{
		{ErrInvalidArgument, ErrorReasonUnspecified},
		{ErrTeamNotServed, ErrorReasonUnspecified},
		{ErrForbidden, ErrorReasonUnspecified},
		{ErrConfigNotFound, ErrorReasonConfigNotFound},
		{ErrDiscoveryFailed, ErrorReasonDiscoveryFailed},
		{ErrEnableFailed, ErrorReasonEnableFailed},
		{ErrInvalidWebhook, ErrorReasonInvalidWebhook},
		{ErrWebhookUnsupported, ErrorReasonWebhookUnsupported},
		{ErrPayloadTooLarge, ErrorReasonPayloadTooLarge},
		{ErrProviderFailed, ErrorReasonProviderFailed},
	} {
		if errors.Is(err, candidate.cause) {
			return NewDomainError(candidate.cause, ErrorDetail{Reason: candidate.reason})
		}
	}
	return NewDomainError(ErrUnknown, ErrorDetail{})
}
