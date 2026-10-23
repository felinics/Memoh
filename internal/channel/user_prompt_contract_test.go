package channel_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/dingtalk"
	"github.com/felinics/memoh/internal/channel/adapters/discord"
	"github.com/felinics/memoh/internal/channel/adapters/feishu"
	"github.com/felinics/memoh/internal/channel/adapters/line"
	localadapter "github.com/felinics/memoh/internal/channel/adapters/local"
	"github.com/felinics/memoh/internal/channel/adapters/matrix"
	"github.com/felinics/memoh/internal/channel/adapters/misskey"
	"github.com/felinics/memoh/internal/channel/adapters/qq"
	"github.com/felinics/memoh/internal/channel/adapters/slack"
	"github.com/felinics/memoh/internal/channel/adapters/telegram"
	"github.com/felinics/memoh/internal/channel/adapters/wechatoa"
	"github.com/felinics/memoh/internal/channel/adapters/wecom"
	"github.com/felinics/memoh/internal/channel/adapters/weixin"
)

// promptRecorder stands in for an adapter's transport: it keeps the real
// adapter's descriptor, so the shared layer makes the same routing decision
// as in production, and records what would reach the platform.
type promptRecorder struct {
	channel.Adapter
	mu       sync.Mutex
	messages []channel.Message
	events   []channel.StreamEvent
}

func (r *promptRecorder) Send(_ context.Context, _ channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, msg.LogicalMessage().Message)
	return nil
}

func (r *promptRecorder) OpenStream(context.Context, channel.ChannelConfig, string, channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	return &recorderStream{r: r}, nil
}

type recorderStream struct{ r *promptRecorder }

func (s *recorderStream) Push(_ context.Context, event channel.PreparedStreamEvent) error {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.r.events = append(s.r.events, event.LogicalEvent())
	return nil
}

func (*recorderStream) Close(context.Context) error { return nil }

func approvalEvent() channel.StreamEvent {
	return channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: &channel.StreamToolCall{
		Name: "exec", CallID: "call-1", ApprovalID: "approval-1", ShortID: 7,
		Input: map[string]any{"command": "pwd"},
	}}
}

func askUserEvent() channel.StreamEvent {
	return channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: &channel.StreamToolCall{
		Name: "ask_user", CallID: "call-2",
		Actions: []channel.Action{{Type: "user_input", Value: "respond:input-1"}},
		Input: map[string]any{"payload": map[string]any{"questions": []any{map[string]any{
			"text": "Pick one", "kind": "single_select",
			"options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}},
		}}}},
	}}
}

// TestAdaptersSurfaceApprovalAndUserInputPrompts keeps every adapter from
// silently dropping a request the user has to answer. An adapter either
// declares the native capability, in which case the shared layer hands the
// event to it, or it receives the prompt as a plain message.
func TestAdaptersSurfaceApprovalAndUserInputPrompts(t *testing.T) {
	t.Parallel()

	adapters := []channel.Adapter{
		dingtalk.NewDingTalkAdapter(nil),
		discord.NewDiscordAdapter(nil),
		feishu.NewFeishuAdapter(nil),
		line.NewAdapter(nil),
		matrix.NewMatrixAdapter(nil),
		misskey.NewMisskeyAdapter(nil),
		qq.NewQQAdapter(nil),
		slack.NewSlackAdapter(nil),
		telegram.NewTelegramAdapter(nil),
		wechatoa.NewWeChatOAAdapter(nil),
		wecom.NewWeComAdapter(nil),
		weixin.NewWeixinAdapter(nil),
	}
	cases := []struct {
		name   string
		event  channel.StreamEvent
		native func(channel.ChannelCapabilities) bool
		hints  []string
	}{
		{"approval", approvalEvent(), func(c channel.ChannelCapabilities) bool { return c.NativeApproval }, []string{"/approve 7", "/reject 7"}},
		{"ask_user", askUserEvent(), func(c channel.ChannelCapabilities) bool { return c.NativeUserInput }, []string{"Pick one", "1) A"}},
	}
	for _, adapter := range adapters {
		for _, tc := range cases {
			t.Run(string(adapter.Type())+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				rec := &promptRecorder{Adapter: adapter}
				reg := channel.NewRegistry()
				reg.MustRegister(rec)
				manager := channel.NewManager(nil, reg, nil, nil)
				cfg := channel.ChannelConfig{BotID: "bot-1", ChannelType: adapter.Type()}
				stream, err := channel.NewReplySenderForTest(manager, cfg, adapter.Type()).OpenStream(context.Background(), "target-1", channel.StreamOptions{})
				if err != nil {
					t.Fatalf("open stream: %v", err)
				}
				if err := stream.Push(context.Background(), tc.event); err != nil {
					t.Fatalf("push: %v", err)
				}
				rec.mu.Lock()
				defer rec.mu.Unlock()
				if tc.native(adapter.Descriptor().Capabilities) {
					if len(rec.events) != 1 || rec.events[0].Type != channel.StreamEventToolCallStart || len(rec.messages) != 0 {
						t.Fatalf("native adapter: events=%#v messages=%#v, want the event forwarded and no plain message", rec.events, rec.messages)
					}
					return
				}
				if len(rec.events) != 0 || len(rec.messages) != 1 {
					t.Fatalf("plain adapter: events=%#v messages=%#v, want one plain message", rec.events, rec.messages)
				}
				text := rec.messages[0].PlainText()
				for _, want := range tc.hints {
					if !strings.Contains(text, want) {
						t.Fatalf("prompt missing %q:\n%s", want, text)
					}
				}
			})
		}
	}
}

// The Web channel hands both events to the route hub, where the web UI
// renders them.
func TestLocalAdapterForwardsApprovalAndUserInputEvents(t *testing.T) {
	t.Parallel()

	hub := localadapter.NewRouteHub()
	adapter := localadapter.NewWebAdapter(hub)
	reg := channel.NewRegistry()
	reg.MustRegister(adapter)
	manager := channel.NewManager(nil, reg, nil, nil)
	cfg := channel.ChannelConfig{BotID: "bot-1", ChannelType: adapter.Type()}
	_, events, cancel := hub.Subscribe("target-1")
	defer cancel()
	stream, err := channel.NewReplySenderForTest(manager, cfg, adapter.Type()).OpenStream(context.Background(), "target-1", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	for _, event := range []channel.StreamEvent{approvalEvent(), askUserEvent()} {
		if err := stream.Push(context.Background(), event); err != nil {
			t.Fatalf("push: %v", err)
		}
		select {
		case got := <-events:
			if got.Event.Type != channel.StreamEventToolCallStart || got.Event.ToolCall == nil || got.Event.ToolCall.CallID != event.ToolCall.CallID {
				t.Fatalf("hub event = %#v, want %s forwarded", got.Event, event.ToolCall.CallID)
			}
		case <-time.After(time.Second):
			t.Fatalf("hub did not receive %s", event.ToolCall.CallID)
		}
	}
}
