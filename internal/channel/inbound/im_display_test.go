package inbound

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/route"
)

type fakeIMDisplayOptionsReader struct {
	options IMDisplayOptions
	err     error
	botIDs  []string
}

func (r *fakeIMDisplayOptionsReader) IMDisplayOptions(_ context.Context, botID string) (IMDisplayOptions, error) {
	r.botIDs = append(r.botIDs, botID)
	return r.options, r.err
}

// optionRecordingSender records the options every reply stream is opened with.
type optionRecordingSender struct {
	fakeReplySender
	options []channel.StreamOptions
}

func (s *optionRecordingSender) OpenStream(ctx context.Context, target string, opts channel.StreamOptions) (channel.OutboundStream, error) {
	s.options = append(s.options, opts)
	return s.fakeReplySender.OpenStream(ctx, target, opts)
}

func TestResolveIMDisplayOptions(t *testing.T) {
	t.Parallel()

	shownWithReuse := IMDisplayOptions{ShowToolCalls: true, ReuseToolCallMessage: true}
	tests := []struct {
		name        string
		channelType channel.ChannelType
		botID       string
		reader      *fakeIMDisplayOptionsReader
		want        IMDisplayOptions
	}{
		{
			name:        "local channels receive every tool event",
			channelType: channel.ChannelType("web"),
			botID:       "bot-1",
			want:        IMDisplayOptions{ShowToolCalls: true},
		},
		{
			name:        "no reader hides tool calls",
			channelType: channel.ChannelTypeTelegram,
			botID:       "bot-1",
		},
		{
			name:        "blank bot hides tool calls",
			channelType: channel.ChannelTypeTelegram,
			botID:       " ",
			reader:      &fakeIMDisplayOptionsReader{options: shownWithReuse},
		},
		{
			name:        "failed lookup hides tool calls",
			channelType: channel.ChannelTypeTelegram,
			botID:       "bot-1",
			reader:      &fakeIMDisplayOptionsReader{options: shownWithReuse, err: errors.New("settings unavailable")},
		},
		{
			name:        "reuse only applies to shown tool calls",
			channelType: channel.ChannelTypeTelegram,
			botID:       "bot-1",
			reader:      &fakeIMDisplayOptionsReader{options: IMDisplayOptions{ReuseToolCallMessage: true}},
		},
		{
			name:        "shown tool calls with reuse",
			channelType: channel.ChannelTypeTelegram,
			botID:       "bot-1",
			reader:      &fakeIMDisplayOptionsReader{options: shownWithReuse},
			want:        shownWithReuse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			processor := &ChannelInboundProcessor{logger: slog.Default()}
			if tt.reader != nil {
				processor.SetIMDisplayOptions(tt.reader)
			}
			if got := processor.resolveIMDisplayOptions(context.Background(), tt.channelType, tt.botID); got != tt.want {
				t.Fatalf("resolveIMDisplayOptions() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHandleInboundOpensStreamWithToolCallMessageReuse(t *testing.T) {
	tests := []struct {
		name    string
		options IMDisplayOptions
		want    bool
	}{
		{name: "reuse with shown tool calls", options: IMDisplayOptions{ShowToolCalls: true, ReuseToolCallMessage: true}, want: true},
		{name: "reuse with hidden tool calls", options: IMDisplayOptions{ReuseToolCallMessage: true}, want: false},
		{name: "shown tool calls without reuse", options: IMDisplayOptions{ShowToolCalls: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
			chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
			gateway := &fakeChatGateway{
				resp: fakeChatResponse{
					Messages: []turn.ModelMessage{
						{Role: "assistant", Content: turn.NewTextContent("AI reply")},
					},
				},
			}
			processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
			reader := &fakeIMDisplayOptionsReader{options: tt.options}
			processor.SetIMDisplayOptions(reader)
			sender := &optionRecordingSender{}

			cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
			msg := channel.InboundMessage{
				BotID:       "bot-1",
				Channel:     channel.ChannelType("feishu"),
				Message:     channel.Message{Text: "hello"},
				ReplyTarget: "target-id",
				Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
				Conversation: channel.Conversation{
					ID:   "chat-1",
					Type: channel.ConversationTypePrivate,
				},
			}

			if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
				t.Fatalf("HandleInbound() error = %v", err)
			}
			if len(sender.options) != 1 {
				t.Fatalf("expected one reply stream, got %d", len(sender.options))
			}
			if got := sender.options[0].ReuseToolCallMessage; got != tt.want {
				t.Fatalf("ReuseToolCallMessage = %v, want %v", got, tt.want)
			}
			if len(reader.botIDs) != 1 || reader.botIDs[0] != "bot-1" {
				t.Fatalf("display options were looked up for %v, want [bot-1]", reader.botIDs)
			}
		})
	}
}
