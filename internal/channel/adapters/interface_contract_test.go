package adapters_test

import (
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/dingtalk"
	"github.com/felinics/memoh/internal/channel/adapters/discord"
	"github.com/felinics/memoh/internal/channel/adapters/feishu"
	localadapter "github.com/felinics/memoh/internal/channel/adapters/local"
	"github.com/felinics/memoh/internal/channel/adapters/matrix"
	"github.com/felinics/memoh/internal/channel/adapters/misskey"
	"github.com/felinics/memoh/internal/channel/adapters/qq"
	"github.com/felinics/memoh/internal/channel/adapters/telegram"
	"github.com/felinics/memoh/internal/channel/adapters/wechatoa"
	"github.com/felinics/memoh/internal/channel/adapters/wecom"
	"github.com/felinics/memoh/internal/channel/adapters/weixin"
)

var (
	_ channel.Sender = (*dingtalk.DingTalkAdapter)(nil)
	_ channel.Sender = (*discord.DiscordAdapter)(nil)
	_ channel.Sender = (*feishu.FeishuAdapter)(nil)
	_ channel.Sender = (*localadapter.WebAdapter)(nil)
	_ channel.Sender = (*matrix.MatrixAdapter)(nil)
	_ channel.Sender = (*misskey.MisskeyAdapter)(nil)
	_ channel.Sender = (*qq.QQAdapter)(nil)
	_ channel.Sender = (*telegram.TelegramAdapter)(nil)
	_ channel.Sender = (*wechatoa.WeChatOAAdapter)(nil)
	_ channel.Sender = (*wecom.WeComAdapter)(nil)
	_ channel.Sender = (*weixin.WeixinAdapter)(nil)

	_ channel.StreamSender = (*dingtalk.DingTalkAdapter)(nil)
	_ channel.StreamSender = (*discord.DiscordAdapter)(nil)
	_ channel.StreamSender = (*feishu.FeishuAdapter)(nil)
	_ channel.StreamSender = (*localadapter.WebAdapter)(nil)
	_ channel.StreamSender = (*matrix.MatrixAdapter)(nil)
	_ channel.StreamSender = (*misskey.MisskeyAdapter)(nil)
	_ channel.StreamSender = (*qq.QQAdapter)(nil)
	_ channel.StreamSender = (*telegram.TelegramAdapter)(nil)
	_ channel.StreamSender = (*wechatoa.WeChatOAAdapter)(nil)
	_ channel.StreamSender = (*wecom.WeComAdapter)(nil)
	_ channel.StreamSender = (*weixin.WeixinAdapter)(nil)
)
