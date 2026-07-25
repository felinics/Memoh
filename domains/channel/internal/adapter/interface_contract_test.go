package adapter_test

import (
	localadapter "github.com/memohai/memoh/domains/api/http/chat/local"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/internal/adapter/dingtalk"
	"github.com/memohai/memoh/domains/channel/internal/adapter/discord"
	"github.com/memohai/memoh/domains/channel/internal/adapter/feishu"
	"github.com/memohai/memoh/domains/channel/internal/adapter/matrix"
	"github.com/memohai/memoh/domains/channel/internal/adapter/misskey"
	"github.com/memohai/memoh/domains/channel/internal/adapter/qq"
	"github.com/memohai/memoh/domains/channel/internal/adapter/telegram"
	"github.com/memohai/memoh/domains/channel/internal/adapter/wechatoa"
	"github.com/memohai/memoh/domains/channel/internal/adapter/wecom"
	"github.com/memohai/memoh/domains/channel/internal/adapter/weixin"
)

var (
	_ gateway.Sender = (*dingtalk.DingTalkAdapter)(nil)
	_ gateway.Sender = (*discord.DiscordAdapter)(nil)
	_ gateway.Sender = (*feishu.FeishuAdapter)(nil)
	_ gateway.Sender = (*localadapter.WebAdapter)(nil)
	_ gateway.Sender = (*matrix.MatrixAdapter)(nil)
	_ gateway.Sender = (*misskey.MisskeyAdapter)(nil)
	_ gateway.Sender = (*qq.QQAdapter)(nil)
	_ gateway.Sender = (*telegram.TelegramAdapter)(nil)
	_ gateway.Sender = (*wechatoa.WeChatOAAdapter)(nil)
	_ gateway.Sender = (*wecom.WeComAdapter)(nil)
	_ gateway.Sender = (*weixin.WeixinAdapter)(nil)

	_ gateway.StreamSender = (*dingtalk.DingTalkAdapter)(nil)
	_ gateway.StreamSender = (*discord.DiscordAdapter)(nil)
	_ gateway.StreamSender = (*feishu.FeishuAdapter)(nil)
	_ gateway.StreamSender = (*localadapter.WebAdapter)(nil)
	_ gateway.StreamSender = (*matrix.MatrixAdapter)(nil)
	_ gateway.StreamSender = (*misskey.MisskeyAdapter)(nil)
	_ gateway.StreamSender = (*qq.QQAdapter)(nil)
	_ gateway.StreamSender = (*telegram.TelegramAdapter)(nil)
	_ gateway.StreamSender = (*wechatoa.WeChatOAAdapter)(nil)
	_ gateway.StreamSender = (*wecom.WeComAdapter)(nil)
	_ gateway.StreamSender = (*weixin.WeixinAdapter)(nil)
)
