package codec

import (
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
)

// narrowEnum converts a protobuf enum (int32 on the wire) to a domain enum
// backed by uint8. The bound check is what makes the conversion safe: a peer
// that sends an out-of-range ordinal would otherwise silently truncate into a
// valid-looking domain value rather than being rejected.
func narrowEnum[Domain ~uint8, Wire ~int32](value Wire, highest Domain) (Domain, bool) {
	if value < 0 || int64(value) > int64(highest) {
		return 0, false
	}
	return Domain(value), true
}

// enumOrZero keeps the existing lenient decode behaviour for optional config
// enums: an unrecognized ordinal degrades to the zero (unspecified) value
// instead of truncating to an unrelated one.
func enumOrZero[Domain ~uint8, Wire ~int32](value Wire, highest Domain) Domain {
	converted, ok := narrowEnum(value, highest)
	if !ok {
		return 0
	}
	return converted
}

func ChannelTypeToProto(value channel.ChannelType) channelpb.ChannelType {
	return channelpb.ChannelType(value)
}

func ChannelTypeFromProto(value channelpb.ChannelType) (channel.ChannelType, error) {
	if value <= channelpb.ChannelType_CHANNEL_TYPE_UNSPECIFIED || value > channelpb.ChannelType_CHANNEL_TYPE_WEIXIN {
		return channel.ChannelTypeUnspecified, fmt.Errorf("invalid channel type %d", value)
	}
	return channel.ChannelType(value), nil
}

func IdentityProjectionToProto(value channel.IdentityProjection) *channelpb.IdentityProjection {
	return &channelpb.IdentityProjection{
		Id:               value.ID,
		Channel:          value.Channel,
		ChannelSubjectId: value.ChannelSubjectID,
		DisplayName:      value.DisplayName,
		AvatarUrl:        value.AvatarURL,
	}
}

func IdentityProjectionFromProto(value *channelpb.IdentityProjection) (channel.IdentityProjection, error) {
	if value == nil {
		return channel.IdentityProjection{}, errors.New("missing identity projection")
	}
	return channel.IdentityProjection{
		ID:               value.GetId(),
		Channel:          value.GetChannel(),
		ChannelSubjectID: value.GetChannelSubjectId(),
		DisplayName:      value.GetDisplayName(),
		AvatarURL:        value.GetAvatarUrl(),
	}, nil
}

func ConversationProjectionToProto(value channel.ConversationProjection) *channelpb.ConversationProjection {
	return &channelpb.ConversationProjection{
		RouteId:               value.RouteID,
		Channel:               value.Channel,
		ConversationType:      value.ConversationType,
		ConversationId:        value.ConversationID,
		ThreadId:              value.ThreadID,
		ConversationName:      value.ConversationName,
		ConversationAvatarUrl: value.ConversationAvatarURL,
	}
}

func ConversationProjectionFromProto(value *channelpb.ConversationProjection) (channel.ConversationProjection, error) {
	if value == nil {
		return channel.ConversationProjection{}, errors.New("missing conversation projection")
	}
	return channel.ConversationProjection{
		RouteID:               value.GetRouteId(),
		Channel:               value.GetChannel(),
		ConversationType:      value.GetConversationType(),
		ConversationID:        value.GetConversationId(),
		ThreadID:              value.GetThreadId(),
		ConversationName:      value.GetConversationName(),
		ConversationAvatarURL: value.GetConversationAvatarUrl(),
	}, nil
}

func ConfigToProto(value channel.Config) (*channelpb.ChannelConfig, error) {
	provider, err := ProviderConfigToProto(value.ProviderConfig)
	if err != nil {
		return nil, err
	}
	self, err := SelfIdentityToProtoForType(value.SelfIdentity, value.ChannelType)
	if err != nil {
		return nil, err
	}
	routing, err := RoutingToProto(value.Routing)
	if err != nil {
		return nil, err
	}
	result := &channelpb.ChannelConfig{
		Id: value.ID, TeamId: value.TeamID, BotId: value.BotID,
		ChannelType: ChannelTypeToProto(value.ChannelType), ProviderConfig: provider,
		ExternalIdentity: value.ExternalIdentity, SelfIdentity: self, Routing: routing,
		Disabled: value.Disabled, CreatedAt: timestamppb.New(value.CreatedAt.UTC()), UpdatedAt: timestamppb.New(value.UpdatedAt.UTC()),
	}
	if value.VerifiedAt != nil {
		result.VerifiedAt = timestamppb.New(value.VerifiedAt.UTC())
	}
	return result, nil
}

func ConfigFromProto(value *channelpb.ChannelConfig) (channel.Config, error) {
	if value == nil {
		return channel.Config{}, errors.New("missing channel config")
	}
	typ, err := ChannelTypeFromProto(value.GetChannelType())
	if err != nil {
		return channel.Config{}, err
	}
	provider, err := ProviderConfigFromProto(value.GetProviderConfig())
	if err != nil {
		return channel.Config{}, err
	}
	self, err := SelfIdentityFromProto(value.GetSelfIdentity())
	if err != nil {
		return channel.Config{}, err
	}
	routing, err := RoutingFromProto(value.GetRouting())
	if err != nil {
		return channel.Config{}, err
	}
	createdAt, err := timestamp(value.GetCreatedAt(), true)
	if err != nil {
		return channel.Config{}, fmt.Errorf("created_at: %w", err)
	}
	updatedAt, err := timestamp(value.GetUpdatedAt(), true)
	if err != nil {
		return channel.Config{}, fmt.Errorf("updated_at: %w", err)
	}
	verifiedAt, err := optionalTimestamp(value.GetVerifiedAt())
	if err != nil {
		return channel.Config{}, fmt.Errorf("verified_at: %w", err)
	}
	return channel.Config{
		ID: value.GetId(), TeamID: value.GetTeamId(), BotID: value.GetBotId(), ChannelType: typ,
		ProviderConfig: provider, ExternalIdentity: value.GetExternalIdentity(), SelfIdentity: self,
		Routing: routing, Disabled: value.GetDisabled(), VerifiedAt: verifiedAt,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func ProviderConfigToProto(value channel.ProviderConfig) (*channelpb.ProviderConfig, error) {
	switch value := value.(type) {
	case channel.DingTalkConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Dingtalk{Dingtalk: &channelpb.DingTalkConfig{AppKey: value.AppKey, AppSecret: value.AppSecret}}}, nil
	case channel.DiscordConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Discord{Discord: &channelpb.DiscordConfig{BotToken: value.BotToken}}}, nil
	case channel.FeishuConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Feishu{Feishu: &channelpb.FeishuConfig{AppId: value.AppID, AppSecret: value.AppSecret, EncryptKey: value.EncryptKey, VerificationToken: value.VerificationToken, Region: channelpb.FeishuRegion(value.Region), InboundMode: channelpb.FeishuInboundMode(value.InboundMode)}}}, nil
	case channel.LineConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Line{Line: &channelpb.LineConfig{ChannelSecret: value.ChannelSecret, ChannelAccessToken: value.ChannelAccessToken}}}, nil
	case channel.MatrixConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Matrix{Matrix: &channelpb.MatrixConfig{HomeserverUrl: value.HomeserverURL, AccessToken: value.AccessToken, UserId: value.UserID, SyncTimeout: durationpb.New(value.SyncTimeout), AutoJoinInvites: value.AutoJoinInvites}}}, nil
	case channel.MisskeyConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Misskey{Misskey: &channelpb.MisskeyConfig{InstanceUrl: value.InstanceURL, AccessToken: value.AccessToken}}}, nil
	case channel.QQConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Qq{Qq: &channelpb.QQConfig{AppId: value.AppID, AppSecret: value.AppSecret, MarkdownSupport: value.MarkdownSupport, EnableInputHint: value.EnableInputHint}}}, nil
	case channel.SlackConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Slack{Slack: &channelpb.SlackConfig{BotToken: value.BotToken, AppToken: value.AppToken}}}, nil
	case channel.TelegramConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Telegram{Telegram: &channelpb.TelegramConfig{BotToken: value.BotToken, ApiBaseUrl: value.APIBaseURL, HttpProxy: proxyToProto(value.HTTPProxy)}}}, nil
	case channel.WeChatOAConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_WechatOa{WechatOa: &channelpb.WeChatOAConfig{AppId: value.AppID, AppSecret: value.AppSecret, Token: value.Token, EncodingAesKey: value.EncodingAESKey, EncryptionMode: channelpb.EncryptionMode(value.EncryptionMode), HttpProxy: proxyToProto(value.HTTPProxy)}}}, nil
	case channel.WeComConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Wecom{Wecom: &channelpb.WeComConfig{BotId: value.BotID, Credential: value.Credential, WsUrl: value.WSURL, Heartbeat: durationpb.New(value.Heartbeat), AckTimeout: durationpb.New(value.ACKTimeout), WriteTimeout: durationpb.New(value.WriteTimeout), ReadTimeout: durationpb.New(value.ReadTimeout)}}}, nil
	case channel.WeixinConfig:
		return &channelpb.ProviderConfig{Provider: &channelpb.ProviderConfig_Weixin{Weixin: &channelpb.WeixinConfig{Token: value.Token, BaseUrl: value.BaseURL, PollTimeout: durationpb.New(value.PollTimeout), EnableTyping: value.EnableTyping}}}, nil
	default:
		return nil, fmt.Errorf("unsupported provider config %T", value)
	}
}

func ProviderConfigFromProto(value *channelpb.ProviderConfig) (channel.ProviderConfig, error) {
	if value == nil {
		return nil, errors.New("missing provider config")
	}
	switch value := value.Provider.(type) {
	case *channelpb.ProviderConfig_Dingtalk:
		v := value.Dingtalk
		return channel.DingTalkConfig{AppKey: v.GetAppKey(), AppSecret: v.GetAppSecret()}, nil
	case *channelpb.ProviderConfig_Discord:
		return channel.DiscordConfig{BotToken: value.Discord.GetBotToken()}, nil
	case *channelpb.ProviderConfig_Feishu:
		v := value.Feishu
		if v.GetRegion() <= 0 || v.GetRegion() > channelpb.FeishuRegion_FEISHU_REGION_LARK || v.GetInboundMode() <= 0 || v.GetInboundMode() > channelpb.FeishuInboundMode_FEISHU_INBOUND_MODE_WEBHOOK {
			return nil, errors.New("invalid feishu enum")
		}
		return channel.FeishuConfig{AppID: v.GetAppId(), AppSecret: v.GetAppSecret(), EncryptKey: v.GetEncryptKey(), VerificationToken: v.GetVerificationToken(), Region: enumOrZero(v.GetRegion(), channel.FeishuRegionLark), InboundMode: enumOrZero(v.GetInboundMode(), channel.FeishuInboundModeWebhook)}, nil
	case *channelpb.ProviderConfig_Line:
		v := value.Line
		return channel.LineConfig{ChannelSecret: v.GetChannelSecret(), ChannelAccessToken: v.GetChannelAccessToken()}, nil
	case *channelpb.ProviderConfig_Matrix:
		v := value.Matrix
		d, err := duration(v.GetSyncTimeout())
		if err != nil {
			return nil, fmt.Errorf("sync_timeout: %w", err)
		}
		return channel.MatrixConfig{HomeserverURL: v.GetHomeserverUrl(), AccessToken: v.GetAccessToken(), UserID: v.GetUserId(), SyncTimeout: d, AutoJoinInvites: v.GetAutoJoinInvites()}, nil
	case *channelpb.ProviderConfig_Misskey:
		v := value.Misskey
		return channel.MisskeyConfig{InstanceURL: v.GetInstanceUrl(), AccessToken: v.GetAccessToken()}, nil
	case *channelpb.ProviderConfig_Qq:
		v := value.Qq
		return channel.QQConfig{AppID: v.GetAppId(), AppSecret: v.GetAppSecret(), MarkdownSupport: v.GetMarkdownSupport(), EnableInputHint: v.GetEnableInputHint()}, nil
	case *channelpb.ProviderConfig_Slack:
		v := value.Slack
		return channel.SlackConfig{BotToken: v.GetBotToken(), AppToken: v.GetAppToken()}, nil
	case *channelpb.ProviderConfig_Telegram:
		v := value.Telegram
		return channel.TelegramConfig{BotToken: v.GetBotToken(), APIBaseURL: v.GetApiBaseUrl(), HTTPProxy: proxyFromProto(v.GetHttpProxy())}, nil
	case *channelpb.ProviderConfig_WechatOa:
		v := value.WechatOa
		if v.GetEncryptionMode() <= 0 || v.GetEncryptionMode() > channelpb.EncryptionMode_ENCRYPTION_MODE_SAFE {
			return nil, errors.New("invalid encryption mode")
		}
		return channel.WeChatOAConfig{AppID: v.GetAppId(), AppSecret: v.GetAppSecret(), Token: v.GetToken(), EncodingAESKey: v.GetEncodingAesKey(), EncryptionMode: enumOrZero(v.GetEncryptionMode(), channel.EncryptionModeSafe), HTTPProxy: proxyFromProto(v.GetHttpProxy())}, nil
	case *channelpb.ProviderConfig_Wecom:
		v := value.Wecom
		heartbeat, err := duration(v.GetHeartbeat())
		if err != nil {
			return nil, fmt.Errorf("heartbeat: %w", err)
		}
		ack, err := duration(v.GetAckTimeout())
		if err != nil {
			return nil, fmt.Errorf("ack_timeout: %w", err)
		}
		write, err := duration(v.GetWriteTimeout())
		if err != nil {
			return nil, fmt.Errorf("write_timeout: %w", err)
		}
		read, err := duration(v.GetReadTimeout())
		if err != nil {
			return nil, fmt.Errorf("read_timeout: %w", err)
		}
		return channel.WeComConfig{BotID: v.GetBotId(), Credential: v.GetCredential(), WSURL: v.GetWsUrl(), Heartbeat: heartbeat, ACKTimeout: ack, WriteTimeout: write, ReadTimeout: read}, nil
	case *channelpb.ProviderConfig_Weixin:
		v := value.Weixin
		poll, err := duration(v.GetPollTimeout())
		if err != nil {
			return nil, fmt.Errorf("poll_timeout: %w", err)
		}
		return channel.WeixinConfig{Token: v.GetToken(), BaseURL: v.GetBaseUrl(), PollTimeout: poll, EnableTyping: v.GetEnableTyping()}, nil
	default:
		return nil, errors.New("missing provider config variant")
	}
}

func SelfIdentityToProto(value channel.ChannelSelfIdentity) (*channelpb.ChannelSelfIdentity, error) {
	if value == nil {
		return nil, nil
	}
	switch value := value.(type) {
	case channel.DingTalkIdentity:
		return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_Dingtalk{Dingtalk: &channelpb.DingTalkIdentity{AppKey: value.AppKey, Name: value.Name}}}, nil
	case channel.FeishuIdentity:
		return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_Feishu{Feishu: &channelpb.FeishuIdentity{OpenId: value.OpenID, Name: value.Name, AvatarUrl: value.AvatarURL}}}, nil
	case channel.LineIdentity:
		return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_Line{Line: &channelpb.LineIdentity{BotUserId: value.BotUserID, BasicId: value.BasicID, DisplayName: value.DisplayName}}}, nil
	case channel.UserIdentity:
		return nil, errors.New("user identity requires an explicit provider variant")
	case channel.SlackIdentity:
		return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_Slack{Slack: &channelpb.SlackIdentity{UserId: value.UserID, BotId: value.BotID, TeamId: value.TeamID, Username: value.Username, Team: value.Team}}}, nil
	case channel.WeChatOAIdentity:
		return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_WechatOa{WechatOa: &channelpb.WeChatOAIdentity{AppId: value.AppID}}}, nil
	case channel.WeComIdentity:
		return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_Wecom{Wecom: &channelpb.WeComIdentity{BotId: value.BotID, AibotId: value.AIBotID}}}, nil
	case channel.EmptyIdentity:
		return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_Empty{Empty: &channelpb.EmptyIdentity{}}}, nil
	default:
		return nil, fmt.Errorf("unsupported self identity %T", value)
	}
}

func SelfIdentityToProtoForType(value channel.ChannelSelfIdentity, typ channel.ChannelType) (*channelpb.ChannelSelfIdentity, error) {
	if user, ok := value.(channel.UserIdentity); ok {
		wire := &channelpb.UserIdentity{UserId: user.UserID, Username: user.Username, Name: user.Name, AvatarUrl: user.AvatarURL}
		switch typ {
		case channel.ChannelTypeMisskey:
			return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_Misskey{Misskey: wire}}, nil
		case channel.ChannelTypeTelegram:
			return &channelpb.ChannelSelfIdentity{Identity: &channelpb.ChannelSelfIdentity_Telegram{Telegram: wire}}, nil
		default:
			return nil, fmt.Errorf("user identity is invalid for channel type %d", typ)
		}
	}
	return SelfIdentityToProto(value)
}

func SelfIdentityFromProto(value *channelpb.ChannelSelfIdentity) (channel.ChannelSelfIdentity, error) {
	if value == nil {
		return nil, nil
	}
	switch value := value.Identity.(type) {
	case *channelpb.ChannelSelfIdentity_Dingtalk:
		v := value.Dingtalk
		return channel.DingTalkIdentity{AppKey: v.GetAppKey(), Name: v.GetName()}, nil
	case *channelpb.ChannelSelfIdentity_Feishu:
		v := value.Feishu
		return channel.FeishuIdentity{OpenID: v.GetOpenId(), Name: v.GetName(), AvatarURL: v.GetAvatarUrl()}, nil
	case *channelpb.ChannelSelfIdentity_Line:
		v := value.Line
		return channel.LineIdentity{BotUserID: v.GetBotUserId(), BasicID: v.GetBasicId(), DisplayName: v.GetDisplayName()}, nil
	case *channelpb.ChannelSelfIdentity_Misskey:
		v := value.Misskey
		return channel.UserIdentity{UserID: v.GetUserId(), Username: v.GetUsername(), Name: v.GetName(), AvatarURL: v.GetAvatarUrl()}, nil
	case *channelpb.ChannelSelfIdentity_Telegram:
		v := value.Telegram
		return channel.UserIdentity{UserID: v.GetUserId(), Username: v.GetUsername(), Name: v.GetName(), AvatarURL: v.GetAvatarUrl()}, nil
	case *channelpb.ChannelSelfIdentity_Slack:
		v := value.Slack
		return channel.SlackIdentity{UserID: v.GetUserId(), BotID: v.GetBotId(), TeamID: v.GetTeamId(), Username: v.GetUsername(), Team: v.GetTeam()}, nil
	case *channelpb.ChannelSelfIdentity_WechatOa:
		return channel.WeChatOAIdentity{AppID: value.WechatOa.GetAppId()}, nil
	case *channelpb.ChannelSelfIdentity_Wecom:
		v := value.Wecom
		return channel.WeComIdentity{BotID: v.GetBotId(), AIBotID: v.GetAibotId()}, nil
	case *channelpb.ChannelSelfIdentity_Empty:
		return channel.EmptyIdentity{}, nil
	default:
		return nil, errors.New("missing self identity variant")
	}
}

func RoutingToProto(value channel.ChannelRoutingState) (*channelpb.ChannelRoutingState, error) {
	if value == nil {
		return nil, nil
	}
	switch value := value.(type) {
	case channel.MatrixRoutingState:
		return &channelpb.ChannelRoutingState{State: &channelpb.ChannelRoutingState_Matrix{Matrix: &channelpb.MatrixRoutingState{SinceToken: value.SinceToken}}}, nil
	case channel.EmptyRoutingState:
		return &channelpb.ChannelRoutingState{State: &channelpb.ChannelRoutingState_Empty{Empty: &channelpb.EmptyRoutingState{}}}, nil
	default:
		return nil, fmt.Errorf("unsupported routing state %T", value)
	}
}

func RoutingFromProto(value *channelpb.ChannelRoutingState) (channel.ChannelRoutingState, error) {
	if value == nil {
		return nil, nil
	}
	switch value := value.State.(type) {
	case *channelpb.ChannelRoutingState_Matrix:
		return channel.MatrixRoutingState{SinceToken: value.Matrix.GetSinceToken()}, nil
	case *channelpb.ChannelRoutingState_Empty:
		return channel.EmptyRoutingState{}, nil
	default:
		return nil, errors.New("missing routing state variant")
	}
}

func ConnectionStatusToProto(value channel.ConnectionStatus) *channelpb.ConnectionStatus {
	return &channelpb.ConnectionStatus{ConfigId: value.ConfigID, BotId: value.BotID, ChannelType: ChannelTypeToProto(value.ChannelType), Running: value.Running, LastError: value.LastError, UpdatedAt: timestamppb.New(value.UpdatedAt.UTC())}
}

func ConnectionStatusFromProto(value *channelpb.ConnectionStatus) (channel.ConnectionStatus, error) {
	if value == nil {
		return channel.ConnectionStatus{}, errors.New("missing connection status")
	}
	typ, err := ChannelTypeFromProto(value.GetChannelType())
	if err != nil {
		return channel.ConnectionStatus{}, err
	}
	updated, err := timestamp(value.GetUpdatedAt(), true)
	if err != nil {
		return channel.ConnectionStatus{}, err
	}
	return channel.ConnectionStatus{ConfigID: value.GetConfigId(), BotID: value.GetBotId(), ChannelType: typ, Running: value.GetRunning(), LastError: value.GetLastError(), UpdatedAt: updated}, nil
}

func TunnelStatusToProto(value channel.TunnelStatus) *channelpb.GetTunnelStatusResponse {
	return &channelpb.GetTunnelStatusResponse{Enabled: value.Enabled, Mode: channelpb.TunnelMode(value.Mode), Status: channelpb.TunnelState(value.Status), PublicBaseUrl: value.PublicBaseURL, Error: value.Error}
}

func TunnelStatusFromProto(value *channelpb.GetTunnelStatusResponse) (channel.TunnelStatus, error) {
	if value == nil {
		return channel.TunnelStatus{}, errors.New("missing tunnel status")
	}
	if value.GetMode() <= 0 || value.GetMode() > channelpb.TunnelMode_TUNNEL_MODE_MANAGED {
		return channel.TunnelStatus{}, errors.New("invalid tunnel mode")
	}
	if value.GetStatus() <= 0 || value.GetStatus() > channelpb.TunnelState_TUNNEL_STATE_ERROR {
		return channel.TunnelStatus{}, errors.New("invalid tunnel state")
	}
	return channel.TunnelStatus{Enabled: value.GetEnabled(), Mode: enumOrZero(value.GetMode(), channel.TunnelModeManaged), Status: enumOrZero(value.GetStatus(), channel.TunnelStateError), PublicBaseURL: value.GetPublicBaseUrl(), Error: value.GetError()}, nil
}

func proxyToProto(value *channel.HTTPProxy) *channelpb.HttpProxy {
	if value == nil {
		return nil
	}
	return &channelpb.HttpProxy{Url: value.URL}
}

func proxyFromProto(value *channelpb.HttpProxy) *channel.HTTPProxy {
	if value == nil {
		return nil
	}
	return &channel.HTTPProxy{URL: value.GetUrl()}
}

func duration(value *durationpb.Duration) (time.Duration, error) {
	if value == nil {
		return 0, nil
	}
	if err := value.CheckValid(); err != nil {
		return 0, err
	}
	result := value.AsDuration()
	if result < 0 {
		return 0, errors.New("negative duration")
	}
	return result, nil
}

func timestamp(value *timestamppb.Timestamp, required bool) (time.Time, error) {
	if value == nil {
		if required {
			return time.Time{}, errors.New("missing timestamp")
		}
		return time.Time{}, nil
	}
	if err := value.CheckValid(); err != nil {
		return time.Time{}, err
	}
	return value.AsTime().UTC(), nil
}

func optionalTimestamp(value *timestamppb.Timestamp) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	result, err := timestamp(value, false)
	if err != nil {
		return nil, err
	}
	return &result, nil
}
