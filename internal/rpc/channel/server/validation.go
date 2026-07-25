package server

import (
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	"github.com/memohai/memoh/internal/rpc/channel/internal/codec"
)

func clean(value string) string { return strings.TrimSpace(value) }

func requiredBytes(field, value string, limit uint64) error {
	if clean(value) == "" || !utf8.ValidString(value) || uint64(len(value)) > limit {
		return invalid(field, limit)
	}
	return nil
}

func invalid(field string, limit uint64) error {
	return channel.NewDomainError(channel.ErrInvalidArgument, channel.ErrorDetail{Field: field, Limit: limit})
}

func validateScopedType(teamID, botID string, typ channelpb.ChannelType) error {
	if err := requiredBytes("team_id", teamID, 256); err != nil {
		return err
	}
	if err := requiredBytes("bot_id", botID, 256); err != nil {
		return err
	}
	if _, err := codec.ChannelTypeFromProto(typ); err != nil {
		return invalid("channel_type", 0)
	}
	return nil
}

func optionalTimestamp(value *timestamppb.Timestamp) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.CheckValid(); err != nil {
		return nil, err
	}
	result := value.AsTime().UTC()
	return &result, nil
}

func providerMatches(typ channel.ChannelType, config channel.ProviderConfig) bool {
	switch typ {
	case channel.ChannelTypeDingTalk:
		_, ok := config.(channel.DingTalkConfig)
		return ok
	case channel.ChannelTypeDiscord:
		_, ok := config.(channel.DiscordConfig)
		return ok
	case channel.ChannelTypeFeishu:
		_, ok := config.(channel.FeishuConfig)
		return ok
	case channel.ChannelTypeLine:
		_, ok := config.(channel.LineConfig)
		return ok
	case channel.ChannelTypeMatrix:
		_, ok := config.(channel.MatrixConfig)
		return ok
	case channel.ChannelTypeMisskey:
		_, ok := config.(channel.MisskeyConfig)
		return ok
	case channel.ChannelTypeQQ:
		_, ok := config.(channel.QQConfig)
		return ok
	case channel.ChannelTypeSlack:
		_, ok := config.(channel.SlackConfig)
		return ok
	case channel.ChannelTypeTelegram:
		_, ok := config.(channel.TelegramConfig)
		return ok
	case channel.ChannelTypeWeChatOA:
		_, ok := config.(channel.WeChatOAConfig)
		return ok
	case channel.ChannelTypeWeCom:
		_, ok := config.(channel.WeComConfig)
		return ok
	case channel.ChannelTypeWeixin:
		_, ok := config.(channel.WeixinConfig)
		return ok
	default:
		return false
	}
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
