// Package channel defines the stable values and commands owned by the Channel capsule.
package channel

// ChannelType identifies a remotely supported external messaging iam.
type ChannelType uint8

const (
	ChannelTypeUnspecified ChannelType = iota
	ChannelTypeDingTalk
	ChannelTypeDiscord
	ChannelTypeFeishu
	ChannelTypeLine
	ChannelTypeMatrix
	ChannelTypeMisskey
	ChannelTypeQQ
	ChannelTypeSlack
	ChannelTypeTelegram
	ChannelTypeWeChatOA
	ChannelTypeWeCom
	ChannelTypeWeixin
)

func (t ChannelType) String() string {
	switch t {
	case ChannelTypeDingTalk:
		return "dingtalk"
	case ChannelTypeDiscord:
		return "discord"
	case ChannelTypeFeishu:
		return "feishu"
	case ChannelTypeLine:
		return "line"
	case ChannelTypeMatrix:
		return "matrix"
	case ChannelTypeMisskey:
		return "misskey"
	case ChannelTypeQQ:
		return "qq"
	case ChannelTypeSlack:
		return "slack"
	case ChannelTypeTelegram:
		return "telegram"
	case ChannelTypeWeChatOA:
		return "wechat_oa"
	case ChannelTypeWeCom:
		return "wecom"
	case ChannelTypeWeixin:
		return "weixin"
	default:
		return ""
	}
}
