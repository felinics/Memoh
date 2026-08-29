package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/felinics/memoh/internal/channel/channeltest"
)

type streamValidationAdapter struct {
	channelType    ChannelType
	outboundPolicy OutboundPolicy
	noMarkdown     bool // when true, advertise a plain-text-only channel
	richText       bool
	noAttachments  bool
	dynamicCaps    *ChannelCapabilities
}

func (a *streamValidationAdapter) Type() ChannelType {
	return a.channelType
}

func (a *streamValidationAdapter) Descriptor() Descriptor {
	return Descriptor{
		Type:        a.channelType,
		DisplayName: "stream-validation",
		Capabilities: ChannelCapabilities{
			Text:           true,
			Markdown:       !a.noMarkdown,
			RichText:       a.richText,
			Attachments:    !a.noAttachments,
			Streaming:      true,
			BlockStreaming: true,
			Buttons:        true,
			Threads:        true,
		},
		OutboundPolicy: a.outboundPolicy,
	}
}

func (a *streamValidationAdapter) ResolveOutboundCapabilities(_ ChannelConfig, _ string, base ChannelCapabilities) ChannelCapabilities {
	if a.dynamicCaps != nil {
		return *a.dynamicCaps
	}
	return base
}

func newStreamValidationRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register(&streamValidationAdapter{channelType: ChannelType("test")}); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	return registry
}

type recordingPreparedSender struct {
	msg PreparedOutboundMessage
}

func (s *recordingPreparedSender) Send(_ context.Context, _ ChannelConfig, msg PreparedOutboundMessage) error {
	s.msg = msg
	return nil
}

type targetResolvingAdapter struct {
	channelType    ChannelType
	outboundPolicy OutboundPolicy
	sent           []OutboundMessage
	openedTarget   string
	openedStream   *recordingStream
	dynamicCaps    *ChannelCapabilities
	validate       func(PreparedOutboundMessage) error
}

func (a *targetResolvingAdapter) Type() ChannelType { return a.channelType }

func (a *targetResolvingAdapter) Descriptor() Descriptor {
	return Descriptor{
		Type:        a.channelType,
		DisplayName: "target-resolving",
		Capabilities: ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			URLButtons:     true,
			Attachments:    true,
			Streaming:      true,
			BlockStreaming: true,
		},
		OutboundPolicy: a.outboundPolicy,
	}
}

func (*targetResolvingAdapter) NormalizeTarget(raw string) string { return strings.TrimSpace(raw) }

func (*targetResolvingAdapter) ResolveTarget(_ map[string]any) (string, error) {
	return "", errors.New("not used")
}

func (*targetResolvingAdapter) ResolveOutboundTarget(_ context.Context, _ ChannelConfig, target string) (string, error) {
	if strings.TrimSpace(target) == "alias" {
		return "channel-target", nil
	}
	return strings.TrimSpace(target), nil
}

func (a *targetResolvingAdapter) ResolveOutboundCapabilities(_ ChannelConfig, target string, base ChannelCapabilities) ChannelCapabilities {
	if a.dynamicCaps != nil {
		return *a.dynamicCaps
	}
	caps := base
	if target == "channel-target" {
		caps.Markdown = false
		caps.Attachments = false
	}
	return caps
}

func (a *targetResolvingAdapter) Send(_ context.Context, _ ChannelConfig, msg PreparedOutboundMessage) error {
	a.sent = append(a.sent, msg.LogicalMessage())
	return nil
}

func (a *targetResolvingAdapter) ValidatePreparedOutbound(_ context.Context, _ ChannelConfig, _ string, msg PreparedOutboundMessage) error {
	if a.validate == nil {
		return nil
	}
	return a.validate(msg)
}

func (a *targetResolvingAdapter) OpenStream(_ context.Context, _ ChannelConfig, target string, _ StreamOptions) (PreparedOutboundStream, error) {
	a.openedTarget = target
	a.openedStream = &recordingStream{}
	return a.openedStream, nil
}

func TestValidateStreamEventSupportedTypes(t *testing.T) {
	t.Parallel()

	registry := newStreamValidationRegistry(t)
	channelType := ChannelType("test")
	tests := []struct {
		name  string
		event StreamEvent
	}{
		{name: "status", event: StreamEvent{Type: StreamEventStatus, Status: StreamStatusStarted}},
		{name: "delta", event: StreamEvent{Type: StreamEventDelta, Delta: "hello"}},
		{name: "phase start", event: StreamEvent{Type: StreamEventPhaseStart, Phase: StreamPhaseText}},
		{name: "phase end", event: StreamEvent{Type: StreamEventPhaseEnd, Phase: StreamPhaseText}},
		{name: "tool start", event: StreamEvent{Type: StreamEventToolCallStart, ToolCall: &StreamToolCall{Name: "search"}}},
		{name: "tool end", event: StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &StreamToolCall{Name: "search"}}},
		{name: "attachment", event: StreamEvent{Type: StreamEventAttachment, Attachments: []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}}}},
		{name: "agent start", event: StreamEvent{Type: StreamEventAgentStart}},
		{name: "agent end", event: StreamEvent{Type: StreamEventAgentEnd}},
		{name: "processing started", event: StreamEvent{Type: StreamEventProcessingStarted}},
		{name: "processing completed", event: StreamEvent{Type: StreamEventProcessingCompleted}},
		{name: "processing failed", event: StreamEvent{Type: StreamEventProcessingFailed, Error: "failed"}},
		{name: "final", event: StreamEvent{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{Text: "done"}}}},
		{name: "error", event: StreamEvent{Type: StreamEventError, Error: "boom"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateStreamEvent(registry, channelType, tt.event); err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		})
	}
}

func TestValidateStreamEventInvalidPayload(t *testing.T) {
	t.Parallel()

	registry := newStreamValidationRegistry(t)
	channelType := ChannelType("test")
	tests := []struct {
		name  string
		event StreamEvent
	}{
		{name: "missing status", event: StreamEvent{Type: StreamEventStatus}},
		{name: "missing tool call payload", event: StreamEvent{Type: StreamEventToolCallStart}},
		{name: "empty attachment payload", event: StreamEvent{Type: StreamEventAttachment}},
		{name: "processing failed missing error", event: StreamEvent{Type: StreamEventProcessingFailed}},
		{name: "missing final payload", event: StreamEvent{Type: StreamEventFinal}},
		{name: "missing error payload", event: StreamEvent{Type: StreamEventError}},
		{name: "unsupported type", event: StreamEvent{Type: StreamEventType("unknown")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateStreamEvent(registry, channelType, tt.event); err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

func TestValidateStreamEventRejectsUnsupportedAttachments(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("no-stream-attachments")
	registry := NewRegistry()
	if err := registry.Register(&streamValidationAdapter{channelType: channelType, noAttachments: true}); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}

	err := validateStreamEvent(registry, channelType, StreamEvent{
		Type:        StreamEventAttachment,
		Attachments: []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}},
	})
	if err == nil || !strings.Contains(err.Error(), "attachments") {
		t.Fatalf("expected attachment capability error, got %v", err)
	}
}

func TestManagerStreamAttachmentUsesDynamicOutboundCapabilities(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("dynamic-stream-attachments")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType: channelType,
		dynamicCaps: &ChannelCapabilities{
			Text:           true,
			Streaming:      true,
			BlockStreaming: true,
			Attachments:    false,
		},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      &recordingStream{},
		channelType: channelType,
		target:      "chat-1",
		policy:      manager.resolveOutboundPolicy(channelType),
	}

	err := stream.Push(context.Background(), StreamEvent{
		Type:        StreamEventAttachment,
		Attachments: []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}},
	})
	if err == nil || !strings.Contains(err.Error(), "attachments") {
		t.Fatalf("expected dynamic attachment capability error, got %v", err)
	}
}

func TestManagerSendResolvesOutboundTargetBeforeCapabilities(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("target-resolving-send")
	adapter := &targetResolvingAdapter{channelType: channelType}
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := NewManager(nil, registry, &fakeConfigStore{
		effectiveConfig: ChannelConfig{BotID: "bot-1", ChannelType: channelType},
	}, nil)

	err := manager.Send(context.Background(), "bot-1", channelType, SendRequest{
		Target: "alias",
		Message: Message{
			Text:   "Hello **world**",
			Format: MessageFormatMarkdown,
		},
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if len(adapter.sent) != 1 {
		t.Fatalf("expected one send, got %d", len(adapter.sent))
	}
	got := adapter.sent[0]
	if got.Target != "channel-target" {
		t.Fatalf("expected resolved target, got %q", got.Target)
	}
	if got.Message.Format != MessageFormatPlain || got.Message.Text != "Hello world" {
		t.Fatalf("expected markdown downgraded using resolved target caps, got %+v", got.Message)
	}

	err = manager.Send(context.Background(), "bot-1", channelType, SendRequest{
		Target: "alias",
		Message: Message{
			Text:        "with attachment",
			Attachments: []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "attachments") {
		t.Fatalf("expected attachment rejection using resolved target caps, got %v", err)
	}
}

func TestManagerSendAppliesRichTextCapabilityBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		richText   bool
		wantFormat MessageFormat
		wantParts  bool
		wantText   string
	}{
		{
			name:       "rich capable preserves parts",
			richText:   true,
			wantFormat: MessageFormatRich,
			wantParts:  true,
		},
		{
			name:       "markdown capable without rich downgrades parts",
			richText:   false,
			wantFormat: MessageFormatMarkdown,
			wantText:   "**Hello**",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			channelType := ChannelType("manager-rich-boundary-" + strings.ReplaceAll(tc.name, " ", "-"))
			adapter := &targetResolvingAdapter{
				channelType: channelType,
				dynamicCaps: &ChannelCapabilities{
					Text:     true,
					Markdown: true,
					RichText: tc.richText,
				},
			}
			registry := NewRegistry()
			if err := registry.Register(adapter); err != nil {
				t.Fatalf("register adapter failed: %v", err)
			}
			manager := NewManager(nil, registry, &fakeConfigStore{
				effectiveConfig: ChannelConfig{BotID: "bot-1", ChannelType: channelType},
			}, nil)

			err := manager.Send(context.Background(), "bot-1", channelType, SendRequest{
				Target: "chat-1",
				Message: Message{
					Parts: []MessagePart{{
						Type:   MessagePartText,
						Text:   "Hello",
						Styles: []MessageTextStyle{MessageStyleBold},
					}},
				},
			})
			if err != nil {
				t.Fatalf("Send failed: %v", err)
			}
			if len(adapter.sent) != 1 {
				t.Fatalf("expected one send, got %d", len(adapter.sent))
			}
			got := adapter.sent[0].Message
			if got.Format != tc.wantFormat {
				t.Fatalf("format = %q, want %q; msg=%+v", got.Format, tc.wantFormat, got)
			}
			if tc.wantParts {
				if len(got.Parts) != 1 || got.Text != "" {
					t.Fatalf("expected parts preserved without text, got %+v", got)
				}
			} else {
				if len(got.Parts) != 0 || got.Text != tc.wantText {
					t.Fatalf("expected parts downgraded to %q, got %+v", tc.wantText, got)
				}
			}
		})
	}
}

func TestManagerSendValidatesAllSplitMessagesBeforeSending(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("manager-preflight-split")
	adapter := &targetResolvingAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{MediaOrder: OutboundOrderTextFirst},
		dynamicCaps: &ChannelCapabilities{
			Text:        true,
			Attachments: false,
		},
	}
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := NewManager(nil, registry, &fakeConfigStore{
		effectiveConfig: ChannelConfig{BotID: "bot-1", ChannelType: channelType},
	}, nil)

	err := manager.Send(context.Background(), "bot-1", channelType, SendRequest{
		Target: "chat-1",
		Message: Message{
			Text:        "visible first",
			Attachments: []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "attachments") {
		t.Fatalf("expected attachment capability error, got %v", err)
	}
	if len(adapter.sent) != 0 {
		t.Fatalf("manager must not send text before later split item fails validation, got %+v", adapter.sent)
	}
}

func TestReplySenderValidatesAllSplitMessagesBeforeSending(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("reply-preflight-split")
	adapter := &targetResolvingAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{MediaOrder: OutboundOrderTextFirst},
		dynamicCaps: &ChannelCapabilities{
			Text:        true,
			Attachments: false,
		},
	}
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := NewManager(nil, registry, &fakeConfigStore{
		effectiveConfig: ChannelConfig{BotID: "bot-1", ChannelType: channelType},
	}, nil)
	sender := manager.newReplySender(ChannelConfig{BotID: "bot-1", ChannelType: channelType}, channelType)

	err := sender.Send(context.Background(), OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Text:        "visible first",
			Attachments: []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "attachments") {
		t.Fatalf("expected attachment capability error, got %v", err)
	}
	if len(adapter.sent) != 0 {
		t.Fatalf("reply sender must not send text before later split item fails validation, got %+v", adapter.sent)
	}
}

func TestReplyStreamResolvesOutboundTargetBeforeCapabilities(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("target-resolving-stream")
	adapter := &targetResolvingAdapter{channelType: channelType}
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := NewManager(nil, registry, &fakeConfigStore{
		effectiveConfig: ChannelConfig{BotID: "bot-1", ChannelType: channelType},
	}, nil)
	sender := manager.newReplySender(ChannelConfig{BotID: "bot-1", ChannelType: channelType}, channelType)

	stream, err := sender.OpenStream(context.Background(), "alias", StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	if adapter.openedTarget != "channel-target" {
		t.Fatalf("expected stream opened on resolved target, got %q", adapter.openedTarget)
	}

	err = stream.Push(context.Background(), StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:   "Hello **world**",
				Format: MessageFormatMarkdown,
			},
		},
	})
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	events := adapter.openedStream.Events()
	if len(events) != 1 {
		t.Fatalf("expected one stream event, got %d", len(events))
	}
	got := events[0].Final.Message
	if got.Format != MessageFormatPlain || got.Text != "Hello world" {
		t.Fatalf("expected stream final downgraded using resolved target caps, got %+v", got)
	}
}

func TestReplyStreamFinalAttachmentActionsSurviveSendBoundary(t *testing.T) {
	t.Parallel()

	channelType := ChannelTypeTelegram
	adapter := &targetResolvingAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: 50},
	}
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := NewManager(nil, registry, nil, nil)
	manager.attachmentStore = channeltest.NewMemoryAttachmentStore()
	sender := manager.newReplySender(ChannelConfig{BotID: "bot-1", ChannelType: channelType}, channelType)

	stream, err := sender.OpenStream(context.Background(), "chat-1", StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	err = stream.Push(context.Background(), StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:        strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 40),
				Format:      MessageFormatPlain,
				Attachments: []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}},
				Actions: []Action{{
					Label: "Open",
					URL:   "https://example.com",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	var attachmentMsg *OutboundMessage
	for i := range adapter.sent {
		if len(adapter.sent[i].Message.Attachments) > 0 {
			attachmentMsg = &adapter.sent[i]
		}
	}
	if attachmentMsg == nil {
		t.Fatalf("expected attachment send after split, got %+v", adapter.sent)
	}
	if len(attachmentMsg.Message.Actions) != 1 || attachmentMsg.Message.Actions[0].URL != "https://example.com" {
		t.Fatalf("expected actions to survive on attachment message, got %+v", attachmentMsg.Message.Actions)
	}
	for _, sent := range adapter.sent {
		if len(sent.Message.Attachments) == 0 && len(sent.Message.Actions) > 0 {
			t.Fatalf("text overflow send should not steal attachment actions: %+v", sent.Message)
		}
	}
}

func TestSendWithConfigUsesDynamicOutboundCapabilities(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("dynamic-caps-send")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType: channelType,
		dynamicCaps: &ChannelCapabilities{
			Text: true,
		},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	sender := &recordingPreparedSender{}

	err := manager.sendWithConfig(context.Background(), sender, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: channelType,
	}, OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Text:   "Hello **world**",
			Format: MessageFormatMarkdown,
		},
	}, manager.resolveOutboundPolicy(channelType))
	if err != nil {
		t.Fatalf("sendWithConfig returned error: %v", err)
	}
	got := sender.msg.Message.Message
	if got.Format != MessageFormatPlain {
		t.Fatalf("expected dynamic plain downgrade, got format %q", got.Format)
	}
	if strings.Contains(got.Text, "**") || got.Text != "Hello world" {
		t.Fatalf("expected stripped plain text, got %q", got.Text)
	}
}

func TestManagerSendPreflightsAdapterValidationBeforeSplitSends(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("adapter-preflight")
	registry := NewRegistry()
	adapter := &targetResolvingAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: 40},
		dynamicCaps: &ChannelCapabilities{
			Text:    true,
			Buttons: true,
		},
		validate: func(msg PreparedOutboundMessage) error {
			if len(msg.Message.Message.Actions) > 0 {
				return errors.New("platform block limit")
			}
			return nil
		},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := NewManager(nil, registry, &fakeConfigStore{
		effectiveConfig: ChannelConfig{BotID: "bot-1", ChannelType: channelType},
	}, nil)

	err := manager.Send(context.Background(), "bot-1", channelType, SendRequest{
		Target: "chat-1",
		Message: Message{
			Text: strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30),
			Actions: []Action{{
				Label: "Open",
				URL:   "https://example.com",
			}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "platform block limit") {
		t.Fatalf("expected adapter preflight error, got %v", err)
	}
	if len(adapter.sent) != 0 {
		t.Fatalf("expected no partial sends after adapter preflight failure, got %+v", adapter.sent)
	}
}

// recordingStream captures events pushed to it.
type recordingStream struct {
	mu     sync.Mutex
	events []StreamEvent
}

func (r *recordingStream) Push(_ context.Context, event PreparedStreamEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event.LogicalEvent())
	return nil
}

func (*recordingStream) Close(_ context.Context) error { return nil }

func (r *recordingStream) Events() []StreamEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]StreamEvent, len(r.events))
	copy(out, r.events)
	return out
}

type failingFinalStream struct {
	recordingStream
}

func (f *failingFinalStream) Push(ctx context.Context, event PreparedStreamEvent) error {
	if event.Type == StreamEventFinal {
		return context.DeadlineExceeded
	}
	return f.recordingStream.Push(ctx, event)
}

func newChunkingTestStream(t *testing.T, chunkLimit int) (*managerOutboundStream, *recordingStream, *[]OutboundMessage) {
	t.Helper()
	channelType := ChannelType("telegram")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: chunkLimit},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}

	rec := &recordingStream{}
	var sent []OutboundMessage
	var mu sync.Mutex

	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      rec,
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
		send: func(_ context.Context, msg OutboundMessage) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, msg)
			return nil
		},
	}
	return stream, rec, &sent
}

func TestBuildOutboundMessagesWithCaps_RechunksAfterURLActionDowngrade(t *testing.T) {
	t.Parallel()

	msgs, err := buildOutboundMessagesWithCaps(OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Text:   strings.Repeat("a", 18),
			Format: MessageFormatPlain,
			Actions: []Action{{
				Label: "Open detailed report",
				URL:   "https://example.com/reports/1234567890",
			}},
		},
	}, OutboundPolicy{TextChunkLimit: 40}, ChannelCapabilities{Text: true, Markdown: true}, true)
	if err != nil {
		t.Fatalf("buildOutboundMessagesWithCaps failed: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected URL action downgrade to be rechunked, got %d message(s)", len(msgs))
	}
	for i, msg := range msgs {
		if len(msg.Message.Actions) != 0 {
			t.Fatalf("chunk %d still has actions after downgrade: %+v", i, msg.Message.Actions)
		}
		if got := runeLen(msg.Message.Text); got > 40 {
			t.Fatalf("chunk %d exceeds limit after downgrade: %d", i, got)
		}
	}
}

func TestBuildOutboundMessagesWithCaps_RichPartsUseRichTextChunkLimit(t *testing.T) {
	t.Parallel()

	msgs, err := buildOutboundMessagesWithCaps(OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Format: MessageFormatRich,
			Parts: []MessagePart{{
				Type:   MessagePartText,
				Text:   strings.Repeat("r", 80),
				Styles: []MessageTextStyle{MessageStyleBold},
			}},
		},
	}, OutboundPolicy{TextChunkLimit: 50, RichTextChunkLimit: 200}, ChannelCapabilities{Text: true, Markdown: true, RichText: true}, true)
	if err != nil {
		t.Fatalf("buildOutboundMessagesWithCaps failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected rich parts within rich limit to stay as one message, got %d", len(msgs))
	}
	if len(msgs[0].Message.Parts) != 1 || msgs[0].Message.Format != MessageFormatRich {
		t.Fatalf("expected rich parts preserved, got %+v", msgs[0].Message)
	}
}

func TestPushFinalWithChunking_ShortText(t *testing.T) {
	t.Parallel()
	stream, rec, sent := newChunkingTestStream(t, 2000)

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{Text: "short text", Format: MessageFormatPlain},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 stream event, got %d", len(events))
	}
	if events[0].Final.Message.Text != "short text" {
		t.Fatalf("expected original text, got %q", events[0].Final.Message.Text)
	}
	if len(*sent) != 0 {
		t.Fatalf("expected no overflow sends, got %d", len(*sent))
	}
}

func TestManagerStreamSendsPlainTextUserInputAfterBufferedPreamble(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("plain-user-input")
	registry := NewRegistry()
	registry.MustRegister(&streamValidationAdapter{
		channelType: channelType,
		dynamicCaps: &ChannelCapabilities{Text: true, BlockStreaming: true},
	})
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	first := &recordingStream{}
	second := &recordingStream{}
	var sent []OutboundMessage
	reopenCalls := 0
	stream := &managerOutboundStream{
		manager: manager, config: ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream: first, channelType: channelType, target: "target-1",
		reply:  &ReplyRef{MessageID: "source-1"},
		policy: manager.resolveOutboundPolicy(channelType),
		send: func(_ context.Context, msg OutboundMessage) error {
			sent = append(sent, msg)
			return nil
		},
		reopen: func(context.Context) (PreparedOutboundStream, error) {
			reopenCalls++
			return second, nil
		},
	}
	ctx := context.Background()
	if err := stream.Push(ctx, StreamEvent{Type: StreamEventDelta, Delta: "先说一句。", Phase: StreamPhaseText}); err != nil {
		t.Fatalf("push preamble: %v", err)
	}
	if err := stream.Push(ctx, StreamEvent{Type: StreamEventToolCallStart, ToolCall: &StreamToolCall{
		Name: "ask_user", Locale: "zh", Actions: []Action{{Type: "user_input", Value: "respond:input-1"}},
		Input: map[string]any{"payload": map[string]any{"questions": []any{map[string]any{
			"text": "请选择", "kind": "single_select", "options": []any{map[string]any{"label": "甲"}, map[string]any{"label": "乙"}},
		}}}},
	}}); err != nil {
		t.Fatalf("push user input: %v", err)
	}
	firstEvents := first.Events()
	if len(firstEvents) != 2 || firstEvents[1].Type != StreamEventFinal || reopenCalls != 1 {
		t.Fatalf("preamble stream was not split: events=%#v reopen=%d", firstEvents, reopenCalls)
	}
	if len(sent) != 1 {
		t.Fatalf("plain prompt sends = %d, want 1", len(sent))
	}
	if sent[0].Message.Reply == nil || sent[0].Message.Reply.MessageID != "source-1" {
		t.Fatalf("plain prompt reply = %#v, want source-1", sent[0].Message.Reply)
	}
	prompt := sent[0].Message.PlainText()
	for _, want := range []string{"请选择", "1) 甲", "回复选项序号或选项文字。"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("plain prompt missing %q:\n%s", want, prompt)
		}
	}
	if len(second.Events()) != 1 || second.Events()[0].Type != StreamEventStatus {
		t.Fatalf("reopened stream events = %#v", second.Events())
	}
}

func TestManagerStreamFinalUsesDynamicOutboundCapabilities(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("dynamic-caps-stream")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType: channelType,
		dynamicCaps: &ChannelCapabilities{
			Text:           true,
			Streaming:      true,
			BlockStreaming: true,
		},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	rec := &recordingStream{}
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      rec,
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
	}

	err := stream.Push(context.Background(), StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:   "Hello **world**",
				Format: MessageFormatMarkdown,
			},
		},
	})
	if err != nil {
		t.Fatalf("Push returned error: %v", err)
	}
	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected one prepared event, got %d", len(events))
	}
	got := events[0].Final.Message
	if got.Format != MessageFormatPlain {
		t.Fatalf("expected dynamic plain downgrade, got format %q", got.Format)
	}
	if strings.Contains(got.Text, "**") || got.Text != "Hello world" {
		t.Fatalf("expected stripped plain text, got %q", got.Text)
	}
}

func TestPushFinalWithChunking_LongText(t *testing.T) {
	t.Parallel()
	stream, rec, sent := newChunkingTestStream(t, 100)

	lines := make([]string, 0, 10)
	for i := range 10 {
		line := strings.Repeat("x", 20)
		if i > 0 {
			line = strings.Repeat("y", 20)
		}
		lines = append(lines, line)
	}
	longText := strings.Join(lines, "\n")

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{Text: longText, Format: MessageFormatPlain},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 stream event (first chunk), got %d", len(events))
	}
	firstChunk := events[0].Final.Message.Text
	if runeLen(firstChunk) > 100 {
		t.Fatalf("first chunk exceeds limit: %d runes", runeLen(firstChunk))
	}
	if len(*sent) == 0 {
		t.Fatal("expected overflow sends, got none")
	}
	for i, msg := range *sent {
		if runeLen(msg.Message.Text) > 100 {
			t.Fatalf("overflow chunk %d exceeds limit: %d runes", i, runeLen(msg.Message.Text))
		}
	}

	var reconstructed strings.Builder
	reconstructed.WriteString(firstChunk)
	for _, msg := range *sent {
		reconstructed.WriteString("\n")
		reconstructed.WriteString(msg.Message.Text)
	}
	if strings.TrimSpace(reconstructed.String()) != strings.TrimSpace(longText) {
		t.Fatal("reconstructed text does not match original")
	}
}

func TestPushFinalWithChunking_AttachmentsSeparated(t *testing.T) {
	t.Parallel()
	stream, rec, sent := newChunkingTestStream(t, 50)

	longText := strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30)
	attachments := []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}}

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:        longText,
				Format:      MessageFormatPlain,
				Attachments: attachments,
			},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 stream event, got %d", len(events))
	}
	if len(events[0].Final.Message.Attachments) != 0 {
		t.Fatal("first chunk should not contain attachments")
	}

	hasAttachment := false
	for _, msg := range *sent {
		if len(msg.Message.Attachments) > 0 {
			hasAttachment = true
			if msg.Message.Attachments[0].URL != "https://example.com/img.png" {
				t.Fatal("attachment URL mismatch")
			}
		}
	}
	if !hasAttachment {
		t.Fatal("expected attachments in overflow sends")
	}
}

func TestPushFinalWithChunking_PreflightsOverflowBeforeFirstChunk(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("stream-preflight")
	registry := NewRegistry()
	adapter := &targetResolvingAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: 50},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry}
	rec := &recordingStream{}
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      rec,
		channelType: channelType,
		target:      "chat-1",
		policy:      manager.resolveOutboundPolicy(channelType),
		sender:      adapter,
	}

	err := stream.Push(context.Background(), StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{Message: Message{
			Text:   strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30),
			Format: MessageFormatPlain,
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  "https://example.com/img.png",
			}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "attachment store is not configured") {
		t.Fatalf("expected attachment preflight error, got %v", err)
	}
	if len(rec.Events()) != 0 {
		t.Fatalf("expected no first chunk pushed after preflight failure, got %+v", rec.Events())
	}
	if len(adapter.sent) != 0 {
		t.Fatalf("expected no fallback sends after preflight failure, got %+v", adapter.sent)
	}
}

func TestBuildOutboundMessages_InlineTextWithMediaMovesTextToCaption(t *testing.T) {
	t.Parallel()

	msgs, err := buildOutboundMessages(OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Text: "test.jpg from QQ",
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  "https://example.com/test.jpg",
			}},
		},
	}, OutboundPolicy{
		TextChunkLimit:      100,
		MediaOrder:          OutboundOrderTextFirst,
		InlineTextWithMedia: true,
	})
	if err != nil {
		t.Fatalf("buildOutboundMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(msgs))
	}
	if got := strings.TrimSpace(msgs[0].Message.Text); got != "" {
		t.Fatalf("expected inline caption to suppress standalone text, got %q", got)
	}
	if len(msgs[0].Message.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(msgs[0].Message.Attachments))
	}
	if got := msgs[0].Message.Attachments[0].Caption; got != "test.jpg from QQ" {
		t.Fatalf("unexpected attachment caption: %q", got)
	}
}

func TestBuildOutboundMessages_AttachmentOnlyActionsStayOnAttachment(t *testing.T) {
	t.Parallel()

	msgs, err := buildOutboundMessages(OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Attachments: []Attachment{{Type: AttachmentImage, URL: "https://example.com/test.jpg"}},
			Actions: []Action{{
				Label: "Open",
				URL:   "https://example.com",
			}},
		},
	}, OutboundPolicy{})
	if err != nil {
		t.Fatalf("buildOutboundMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected one attachment message, got %d: %+v", len(msgs), msgs)
	}
	got := msgs[0].Message
	if len(got.Attachments) != 1 {
		t.Fatalf("expected attachment preserved, got %+v", got)
	}
	if len(got.Actions) != 1 || got.Actions[0].Label != "Open" {
		t.Fatalf("expected actions on attachment message, got %+v", got.Actions)
	}
	if strings.TrimSpace(got.Text) != "" || len(got.Parts) != 0 {
		t.Fatalf("attachment-only actions should not create text body, got %+v", got)
	}
}

func TestBuildOutboundMessagesWithCaps_OversizedRichPartsDegradeToMarkdownChunks(t *testing.T) {
	t.Parallel()

	msgs, err := buildOutboundMessagesWithCaps(OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Format: MessageFormatRich,
			Parts: []MessagePart{
				{Type: MessagePartText, Text: strings.Repeat("alpha ", 6)},
				{Type: MessagePartText, Text: strings.Repeat("beta ", 6)},
				{Type: MessagePartText, Text: strings.Repeat("gamma ", 6)},
			},
		},
	}, OutboundPolicy{
		TextChunkLimit: 50,
		ChunkerMode:    ChunkerModeMarkdown,
	}, ChannelCapabilities{Text: true, Markdown: true, RichText: true}, true)
	if err != nil {
		t.Fatalf("buildOutboundMessagesWithCaps failed: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected oversized rich body to split, got %d message(s)", len(msgs))
	}
	for i, msg := range msgs {
		if len(msg.Message.Parts) != 0 {
			t.Fatalf("chunk %d should not keep rich parts: %+v", i, msg.Message.Parts)
		}
		if msg.Message.Format != MessageFormatMarkdown {
			t.Fatalf("chunk %d format = %q, want markdown", i, msg.Message.Format)
		}
		if got := runeLen(msg.Message.Text); got > 50 {
			t.Fatalf("chunk %d text length = %d, want <= 50: %q", i, got, msg.Message.Text)
		}
	}
}

func TestBuildOutboundMessagesWithCaps_OversizedRichOnlyCapsDegradeToPlainChunks(t *testing.T) {
	t.Parallel()

	msgs, err := buildOutboundMessagesWithCaps(OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Format: MessageFormatRich,
			Parts: []MessagePart{
				{Type: MessagePartText, Text: strings.Repeat("alpha ", 6), Styles: []MessageTextStyle{MessageStyleBold}},
				{Type: MessagePartLink, Text: "docs", URL: "https://example.test/docs"},
				{Type: MessagePartText, Text: strings.Repeat("gamma ", 6)},
			},
		},
	}, OutboundPolicy{
		TextChunkLimit: 50,
	}, ChannelCapabilities{Text: true, RichText: true}, true)
	if err != nil {
		t.Fatalf("buildOutboundMessagesWithCaps failed: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected oversized rich body to split, got %d message(s)", len(msgs))
	}
	for i, msg := range msgs {
		if len(msg.Message.Parts) != 0 {
			t.Fatalf("chunk %d should not keep rich parts: %+v", i, msg.Message.Parts)
		}
		if msg.Message.Format != MessageFormatPlain {
			t.Fatalf("chunk %d format = %q, want plain", i, msg.Message.Format)
		}
		if strings.Contains(msg.Message.Text, "**") || strings.Contains(msg.Message.Text, "[docs](") {
			t.Fatalf("chunk %d should be plain text, got %q", i, msg.Message.Text)
		}
		if got := runeLen(msg.Message.Text); got > 50 {
			t.Fatalf("chunk %d text length = %d, want <= 50: %q", i, got, msg.Message.Text)
		}
	}
}

func TestPushFinalWithChunking_NonFinalPassthrough(t *testing.T) {
	t.Parallel()
	stream, rec, sent := newChunkingTestStream(t, 100)

	event := StreamEvent{
		Type:   StreamEventStatus,
		Status: StreamStatusStarted,
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != StreamEventStatus {
		t.Fatalf("expected status event, got %s", events[0].Type)
	}
	if len(*sent) != 0 {
		t.Fatalf("expected no overflow sends, got %d", len(*sent))
	}
}

func TestPushFinalWithChunking_MarkdownFormat(t *testing.T) {
	t.Parallel()
	stream, rec, sent := newChunkingTestStream(t, 100)

	para1 := strings.Repeat("a", 60)
	para2 := strings.Repeat("b", 60)
	longText := para1 + "\n\n" + para2

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{Text: longText, Format: MessageFormatMarkdown},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 stream event, got %d", len(events))
	}
	if events[0].Final.Message.Format != MessageFormatMarkdown {
		t.Fatal("first chunk should preserve markdown format")
	}
	if len(*sent) != 1 {
		t.Fatalf("expected 1 overflow send, got %d", len(*sent))
	}
	if (*sent)[0].Message.Format != MessageFormatMarkdown {
		t.Fatal("overflow chunk should preserve markdown format")
	}
}

func TestPushFinalWithChunking_ShortRichPartsStayOnPreparedStream(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("richstream")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: 20},
		richText:       true,
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	rec := &recordingStream{}
	var sent []OutboundMessage
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      rec,
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
		send: func(_ context.Context, msg OutboundMessage) error {
			sent = append(sent, msg)
			return nil
		},
	}

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Format: MessageFormatRich,
				Parts: []MessagePart{
					{Type: MessagePartText, Text: "short rich", Styles: []MessageTextStyle{MessageStyleBold}},
				},
			},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected rich final to stay on prepared stream, got %d events", len(events))
	}
	if got := events[0].Final.Message; got.Format != MessageFormatRich || len(got.Parts) != 1 {
		t.Fatalf("expected rich parts preserved on prepared stream, got %+v", got)
	}
	if len(sent) != 0 {
		t.Fatalf("expected no overflow plain sends for rich parts, got %d", len(sent))
	}
}

func TestPushFinalWithChunking_OversizedRichPartsDegradeToMarkdownChunks(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("richstream-oversized")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: 50, ChunkerMode: ChunkerModeMarkdown},
		richText:       true,
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	rec := &recordingStream{}
	var sent []OutboundMessage
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      rec,
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
		send: func(_ context.Context, msg OutboundMessage) error {
			sent = append(sent, msg)
			return nil
		},
	}

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Format: MessageFormatRich,
				Parts: []MessagePart{
					{Type: MessagePartText, Text: strings.Repeat("alpha ", 6)},
					{Type: MessagePartText, Text: strings.Repeat("beta ", 6)},
					{Type: MessagePartText, Text: strings.Repeat("gamma ", 6)},
				},
			},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected first chunk on prepared stream, got %d events", len(events))
	}
	first := events[0].Final.Message
	if len(first.Parts) != 0 || first.Format != MessageFormatMarkdown {
		t.Fatalf("first chunk should be markdown text, got %+v", first)
	}
	if len(sent) == 0 {
		t.Fatal("expected overflow chunks sent through fallback sender")
	}
	for i, msg := range sent {
		if len(msg.Message.Parts) != 0 {
			t.Fatalf("overflow chunk %d should not keep parts: %+v", i, msg.Message.Parts)
		}
		if msg.Message.Format != MessageFormatMarkdown {
			t.Fatalf("overflow chunk %d format = %q, want markdown", i, msg.Message.Format)
		}
		if got := runeLen(msg.Message.Text); got > 50 {
			t.Fatalf("overflow chunk %d text length = %d, want <= 50: %q", i, got, msg.Message.Text)
		}
	}
}

func TestPushFinal_CoercesMarkdownToPlainForNoMarkdownChannel(t *testing.T) {
	t.Parallel()
	channelType := ChannelType("plainstream")
	registry := NewRegistry()
	if err := registry.Register(&streamValidationAdapter{channelType: channelType, noMarkdown: true}); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	rec := &recordingStream{}
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      rec,
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
	}

	event := StreamEvent{
		Type:  StreamEventFinal,
		Final: &StreamFinalizePayload{Message: Message{Text: "**bold** and `code`", Format: MessageFormatMarkdown}},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	evs := rec.Events()
	if len(evs) == 0 || evs[0].Final == nil {
		t.Fatalf("expected a final event, got %+v", evs)
	}
	got := evs[0].Final.Message
	if got.Format != MessageFormatPlain {
		t.Errorf("adapter Format = %q, want Plain (downgraded for no-markdown channel)", got.Format)
	}
	if strings.Contains(got.Text, "**") || strings.Contains(got.Text, "`") {
		t.Errorf("inline markup not stripped from payload: %q", got.Text)
	}
	// The shared input event must NOT be mutated: it may be fanned out to other
	// (Markdown-capable) channels via tee, which would lose their markup.
	if event.Final.Message.Format != MessageFormatMarkdown || !strings.Contains(event.Final.Message.Text, "**") {
		t.Errorf("shared input event was mutated by coercion: %+v", event.Final.Message)
	}
}

func TestPushFinal_NormalizesMixedTextAndPartsBeforePlainDowngrade(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("plainstream-mixed")
	registry := NewRegistry()
	if err := registry.Register(&streamValidationAdapter{channelType: channelType, noMarkdown: true}); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	rec := &recordingStream{}
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      rec,
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
	}

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{Message: Message{
			Text:   "intro",
			Format: MessageFormatRich,
			Parts: []MessagePart{
				{Type: MessagePartHeading, Text: "Title"},
				{Type: MessagePartLink, Text: "docs", URL: "https://example.test/docs"},
			},
		}},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	evs := rec.Events()
	if len(evs) != 1 || evs[0].Final == nil {
		t.Fatalf("expected a final event, got %+v", evs)
	}
	got := evs[0].Final.Message
	if got.Format != MessageFormatPlain {
		t.Fatalf("Format = %q, want plain", got.Format)
	}
	want := "intro\n\nTitle\n\ndocs (https://example.test/docs)"
	if got.Text != want {
		t.Fatalf("Text mismatch\n  got:  %q\n  want: %q", got.Text, want)
	}
	if len(got.Parts) != 0 {
		t.Fatalf("plain stream payload should clear parts, got %#v", got.Parts)
	}
	if event.Final.Message.Text != "intro" || len(event.Final.Message.Parts) != 2 {
		t.Fatalf("input event was mutated: %+v", event.Final.Message)
	}
}

func TestPushFinalWithChunking_ActionsOnLastChunk(t *testing.T) {
	t.Parallel()
	stream, rec, sent := newChunkingTestStream(t, 50)

	longText := strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30)
	actions := []Action{{Type: "button", Label: "Click me", URL: "https://example.com"}}

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:    longText,
				Format:  MessageFormatPlain,
				Actions: actions,
			},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 stream event, got %d", len(events))
	}
	if len(events[0].Final.Message.Actions) != 0 {
		t.Fatal("first chunk should NOT have actions when there are overflow chunks")
	}
	if len(*sent) == 0 {
		t.Fatal("expected overflow sends")
	}
	lastSent := (*sent)[len(*sent)-1]
	if len(lastSent.Message.Actions) != 1 || lastSent.Message.Actions[0].Label != "Click me" {
		t.Fatal("actions should be on the last overflow message")
	}
}

func TestPushFinalWithChunking_ActionsOnAttachmentMsg(t *testing.T) {
	t.Parallel()
	stream, rec, sent := newChunkingTestStream(t, 50)

	longText := strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30)
	actions := []Action{{Type: "button", Label: "OK"}}
	attachments := []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}}

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:        longText,
				Format:      MessageFormatPlain,
				Actions:     actions,
				Attachments: attachments,
			},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events[0].Final.Message.Actions) != 0 {
		t.Fatal("first chunk should NOT have actions")
	}

	var attachmentMsg *OutboundMessage
	for i := range *sent {
		if len((*sent)[i].Message.Attachments) > 0 {
			attachmentMsg = &(*sent)[i]
		}
	}
	if attachmentMsg == nil {
		t.Fatal("expected attachment message in overflow")
		return
	}
	if len(attachmentMsg.Message.Actions) != 1 || attachmentMsg.Message.Actions[0].Label != "OK" {
		t.Fatal("actions should be on the attachment (last) message")
	}

	for _, msg := range *sent {
		if len(msg.Message.Attachments) == 0 && len(msg.Message.Actions) > 0 {
			t.Fatal("text-only overflow chunks should NOT have actions when attachment message exists")
		}
	}
}

func TestPushFinalWithChunking_ThreadPropagated(t *testing.T) {
	t.Parallel()
	stream, rec, sent := newChunkingTestStream(t, 50)

	longText := strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30)
	thread := &ThreadRef{ID: "thread-123"}

	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:   longText,
				Format: MessageFormatPlain,
				Thread: thread,
			},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 stream event, got %d", len(events))
	}

	if len(*sent) == 0 {
		t.Fatal("expected overflow sends")
	}
	for i, msg := range *sent {
		if msg.Message.Thread == nil || msg.Message.Thread.ID != "thread-123" {
			t.Fatalf("overflow chunk %d should have thread propagated, got %+v", i, msg.Message.Thread)
		}
	}
}

func TestPushFinalWithChunking_FirstChunkPushFailureFallback(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("telegram")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: 100},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}

	rec := &failingFinalStream{}
	var sent []OutboundMessage
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      rec,
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
		send: func(_ context.Context, msg OutboundMessage) error {
			sent = append(sent, msg)
			return nil
		},
	}

	longText := strings.Repeat("a", 80) + "\n" + strings.Repeat("b", 80) + "\n" + strings.Repeat("c", 80)
	event := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{Text: longText, Format: MessageFormatPlain},
		},
	}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	if len(rec.Events()) != 0 {
		t.Fatalf("expected no stream events after first chunk push failure, got %d", len(rec.Events()))
	}
	if len(sent) < 2 {
		t.Fatalf("expected fallback sends for all chunks, got %d", len(sent))
	}

	var reconstructed strings.Builder
	for i, msg := range sent {
		if i > 0 {
			reconstructed.WriteString("\n")
		}
		reconstructed.WriteString(msg.Message.Text)
	}
	if strings.TrimSpace(reconstructed.String()) != strings.TrimSpace(longText) {
		t.Fatal("fallback reconstructed text does not match original")
	}
}

// reopenableStream wraps a list of recordingStreams; each reopen returns the next.
type reopenableStream struct {
	streams []*recordingStream
	idx     int
}

func (r *reopenableStream) current() *recordingStream {
	if r.idx >= len(r.streams) {
		return nil
	}
	return r.streams[r.idx]
}

func (r *reopenableStream) reopen(_ context.Context) (PreparedOutboundStream, error) {
	r.idx++
	if r.idx >= len(r.streams) {
		return nil, errors.New("no more streams")
	}
	return r.streams[r.idx], nil
}

func newDeltaSplitTestStream(t *testing.T, chunkLimit int, streamCount int) (*managerOutboundStream, *reopenableStream, *[]OutboundMessage) {
	t.Helper()
	channelType := ChannelType("telegram")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: chunkLimit},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}

	streams := make([]*recordingStream, streamCount)
	for i := range streams {
		streams[i] = &recordingStream{}
	}
	reo := &reopenableStream{streams: streams}

	var sent []OutboundMessage
	var mu sync.Mutex

	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      streams[0],
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
		send: func(_ context.Context, msg OutboundMessage) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, msg)
			return nil
		},
		reopen: reo.reopen,
	}
	return stream, reo, &sent
}

func TestPushDelta_NoSplitUnderLimit(t *testing.T) {
	t.Parallel()
	stream, reo, _ := newDeltaSplitTestStream(t, 100, 2)

	for i := range 5 {
		_ = i
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("a", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	events := reo.streams[0].Events()
	if len(events) != 5 {
		t.Fatalf("expected 5 delta events on stream 0, got %d", len(events))
	}
	if stream.splitCount != 0 {
		t.Fatal("expected no splits")
	}
}

func TestPushDelta_SplitsAtLimit(t *testing.T) {
	t.Parallel()
	stream, reo, _ := newDeltaSplitTestStream(t, 100, 3)

	for range 12 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("x", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	if stream.splitCount != 1 {
		t.Fatalf("expected 1 split, got %d", stream.splitCount)
	}

	s0 := reo.streams[0].Events()
	hasFinal := false
	deltaCount := 0
	for _, e := range s0 {
		if e.Type == StreamEventFinal {
			hasFinal = true
		}
		if e.Type == StreamEventDelta {
			deltaCount++
		}
	}
	if !hasFinal {
		t.Fatal("stream 0 should have received a Final event for finalization")
	}
	if deltaCount != 10 {
		t.Fatalf("stream 0 should have 10 deltas (100 runes), got %d", deltaCount)
	}

	s1 := reo.streams[1].Events()
	hasStarted := false
	deltaCount1 := 0
	for _, e := range s1 {
		if e.Type == StreamEventStatus && e.Status == StreamStatusStarted {
			hasStarted = true
		}
		if e.Type == StreamEventDelta {
			deltaCount1++
		}
	}
	if !hasStarted {
		t.Fatal("continuation stream should receive Status(Started) before deltas")
	}
	if deltaCount1 != 2 {
		t.Fatalf("stream 1 should have 2 deltas (overflow), got %d", deltaCount1)
	}
}

func TestPushDelta_MultipleSplits(t *testing.T) {
	t.Parallel()
	stream, reo, _ := newDeltaSplitTestStream(t, 50, 5)

	for range 15 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("y", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	if stream.splitCount != 2 {
		t.Fatalf("expected 2 splits for 150 runes / 50 limit, got %d", stream.splitCount)
	}

	for i := 0; i <= 2; i++ {
		events := reo.streams[i].Events()
		if len(events) == 0 {
			t.Fatalf("stream %d has no events", i)
		}
	}
}

func TestPushDelta_FinalAfterSplitUsesBuffer(t *testing.T) {
	t.Parallel()
	stream, reo, sent := newDeltaSplitTestStream(t, 50, 3)

	for range 8 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("z", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	if stream.splitCount == 0 {
		t.Fatal("expected at least 1 split")
	}

	finalEvent := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:   strings.Repeat("z", 80),
				Format: MessageFormatPlain,
			},
		},
	}
	if err := stream.Push(context.Background(), finalEvent); err != nil {
		t.Fatalf("Final Push failed: %v", err)
	}

	lastStream := reo.current()
	events := lastStream.Events()
	hasFinal := false
	for _, e := range events {
		if e.Type == StreamEventFinal {
			hasFinal = true
			if !e.Final.Message.IsEmpty() {
				t.Fatal("after split, Final should have empty message (adapter uses buffer)")
			}
		}
	}
	if !hasFinal {
		t.Fatal("last stream should have received a Final event")
	}
	_ = sent
}

func TestPushDelta_FinalWithAttachmentsAfterSplit(t *testing.T) {
	t.Parallel()
	stream, _, sent := newDeltaSplitTestStream(t, 50, 3)

	for range 8 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("w", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	attachments := []Attachment{{Type: AttachmentImage, URL: "https://example.com/img.png"}}
	actions := []Action{{Type: "button", Label: "OK"}}
	finalEvent := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:        strings.Repeat("w", 80),
				Format:      MessageFormatPlain,
				Attachments: attachments,
				Actions:     actions,
			},
		},
	}
	if err := stream.Push(context.Background(), finalEvent); err != nil {
		t.Fatalf("Final Push failed: %v", err)
	}

	if len(*sent) != 1 {
		t.Fatalf("expected 1 attachment send, got %d", len(*sent))
	}
	if len((*sent)[0].Message.Attachments) != 1 {
		t.Fatal("expected attachments forwarded via send")
	}
	if len((*sent)[0].Message.Actions) != 1 || (*sent)[0].Message.Actions[0].Label != "OK" {
		t.Fatal("expected actions forwarded with attachments")
	}
}

func TestPushDelta_FinalWithActionsAfterSplitStaysOnBufferedFinal(t *testing.T) {
	t.Parallel()
	stream, reo, sent := newDeltaSplitTestStream(t, 50, 3)

	for range 8 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("a", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	actions := []Action{{Type: "button", Label: "Open", URL: "https://example.com"}}
	finalEvent := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:    strings.Repeat("a", 80),
				Format:  MessageFormatPlain,
				Actions: actions,
			},
		},
	}
	if err := stream.Push(context.Background(), finalEvent); err != nil {
		t.Fatalf("Final Push failed: %v", err)
	}

	if len(*sent) != 0 {
		t.Fatalf("expected actions to stay on stream final, got %d fallback sends", len(*sent))
	}
	lastStream := reo.current()
	events := lastStream.Events()
	for _, event := range events {
		if event.Type == StreamEventFinal {
			if len(event.Final.Message.Actions) != 1 || event.Final.Message.Actions[0].Label != "Open" {
				t.Fatalf("expected actions on buffered final, got %+v", event.Final.Message.Actions)
			}
			return
		}
	}
	t.Fatal("last stream should have received a Final event")
}

func TestPushDelta_FinalWithDowngradedURLActionsAfterSplitSendsLinks(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("split-url-actions-no-buttons")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: 50},
		dynamicCaps: &ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			Streaming:      true,
			BlockStreaming: true,
		},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	streams := []*recordingStream{{}, {}, {}}
	reo := &reopenableStream{streams: streams}
	var sent []OutboundMessage
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      streams[0],
		channelType: channelType,
		target:      "chat-1",
		policy:      manager.resolveOutboundPolicy(channelType),
		send: func(_ context.Context, msg OutboundMessage) error {
			sent = append(sent, msg)
			return nil
		},
		reopen: reo.reopen,
	}

	for range 8 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("a", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}
	if stream.splitCount == 0 {
		t.Fatal("expected at least 1 split")
	}

	finalText := strings.Repeat("a", 80)
	err := stream.Push(context.Background(), StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text:   finalText,
				Format: MessageFormatPlain,
				Actions: []Action{{
					Label: "Open",
					URL:   "https://example.com",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Final Push failed: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected one fallback link send, got %d", len(sent))
	}
	got := sent[0].Message.PlainText()
	if !strings.Contains(got, "Open") || !strings.Contains(got, "https://example.com") {
		t.Fatalf("expected downgraded URL action link, got %q", got)
	}
	if strings.Contains(got, finalText) {
		t.Fatalf("fallback link send duplicated streamed final body: %q", got)
	}
}

func TestPushDelta_FinalWithPartsAfterSplitDoesNotDuplicateStreamedBody(t *testing.T) {
	t.Parallel()

	channelType := ChannelType("rich-split")
	registry := NewRegistry()
	adapter := &streamValidationAdapter{
		channelType:    channelType,
		outboundPolicy: OutboundPolicy{TextChunkLimit: 50},
		richText:       true,
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register adapter failed: %v", err)
	}
	manager := &Manager{registry: registry, attachmentStore: channeltest.NewMemoryAttachmentStore()}
	streams := []*recordingStream{{}, {}, {}}
	reo := &reopenableStream{streams: streams}
	var sent []OutboundMessage
	stream := &managerOutboundStream{
		manager:     manager,
		config:      ChannelConfig{BotID: "bot-1", ChannelType: channelType},
		stream:      streams[0],
		channelType: channelType,
		policy:      manager.resolveOutboundPolicy(channelType),
		send: func(_ context.Context, msg OutboundMessage) error {
			sent = append(sent, msg)
			return nil
		},
		reopen: reo.reopen,
	}

	for range 8 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("r", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}
	if stream.splitCount == 0 {
		t.Fatal("expected at least 1 split")
	}

	finalEvent := StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Format: MessageFormatRich,
				Parts: []MessagePart{
					{Type: MessagePartText, Text: strings.Repeat("r", 80), Styles: []MessageTextStyle{MessageStyleBold}},
				},
			},
		},
	}
	if err := stream.Push(context.Background(), finalEvent); err != nil {
		t.Fatalf("Final Push failed: %v", err)
	}

	if len(sent) != 0 {
		t.Fatalf("expected no duplicate rich final send after split, got %d", len(sent))
	}
}

func TestPushDelta_NoSplitWhenNoLimit(t *testing.T) {
	t.Parallel()
	stream, reo, _ := newDeltaSplitTestStream(t, 0, 2)

	for range 20 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("a", 100),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	if stream.splitCount != 0 {
		t.Fatal("expected no splits when TextChunkLimit is 0")
	}

	events := reo.streams[0].Events()
	if len(events) != 20 {
		t.Fatalf("expected all 20 deltas on stream 0, got %d", len(events))
	}
}

func TestPushDelta_ReasoningPhaseNotCounted(t *testing.T) {
	t.Parallel()
	stream, _, _ := newDeltaSplitTestStream(t, 50, 2)

	for range 10 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("r", 100),
			Phase: StreamPhaseReasoning,
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}

	if stream.splitCount != 0 {
		t.Fatal("reasoning deltas should not trigger splits")
	}
	if stream.deltaRunes != 0 {
		t.Fatal("reasoning deltas should not be counted")
	}
}

func TestPushDelta_SplitsAtNaturalBreak(t *testing.T) {
	t.Parallel()
	// limit=100, softLimit = 100 - 100/4 = 75
	stream, reo, _ := newDeltaSplitTestStream(t, 100, 3)

	// Push 70 runes — under soft limit.
	if err := stream.Push(context.Background(), StreamEvent{
		Type:  StreamEventDelta,
		Delta: strings.Repeat("a", 70),
	}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if stream.splitCount != 0 {
		t.Fatal("should not split under soft limit")
	}

	// Push 10 runes ending with a sentence period → 80 runes total,
	// above soft (75) but under hard (100). Text ends with "." → natural break.
	if err := stream.Push(context.Background(), StreamEvent{
		Type:  StreamEventDelta,
		Delta: strings.Repeat("b", 9) + ".",
	}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if stream.splitCount != 1 {
		t.Fatalf("expected split at natural break point, got %d splits", stream.splitCount)
	}
	if stream.deltaRunes != 0 {
		t.Fatalf("deltaRunes should reset after natural-break split, got %d", stream.deltaRunes)
	}

	// Verify stream 0 got both deltas and a Final.
	s0 := reo.streams[0].Events()
	deltaCount := 0
	hasFinal := false
	for _, e := range s0 {
		if e.Type == StreamEventDelta {
			deltaCount++
		}
		if e.Type == StreamEventFinal {
			hasFinal = true
		}
	}
	if deltaCount != 2 {
		t.Fatalf("stream 0: expected 2 deltas, got %d", deltaCount)
	}
	if !hasFinal {
		t.Fatal("stream 0 should have a Final event")
	}

	// Push more content to the continuation stream.
	if err := stream.Push(context.Background(), StreamEvent{
		Type:  StreamEventDelta,
		Delta: "continuation text",
	}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	s1 := reo.streams[1].Events()
	hasStarted := false
	for _, e := range s1 {
		if e.Type == StreamEventStatus && e.Status == StreamStatusStarted {
			hasStarted = true
		}
	}
	if !hasStarted {
		t.Fatal("continuation stream should receive Status(Started)")
	}
}

func TestPushDelta_NoEarlySplitWithoutBreakPoint(t *testing.T) {
	t.Parallel()
	// limit=100, softLimit=75
	stream, _, _ := newDeltaSplitTestStream(t, 100, 3)

	// Push 90 runes of plain text (no break point) — above soft, under hard.
	for range 9 {
		if err := stream.Push(context.Background(), StreamEvent{
			Type:  StreamEventDelta,
			Delta: strings.Repeat("x", 10),
		}); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
	}
	if stream.splitCount != 0 {
		t.Fatalf("should NOT split in soft zone without a natural break, got %d", stream.splitCount)
	}
	if stream.deltaRunes != 90 {
		t.Fatalf("expected 90 accumulated runes, got %d", stream.deltaRunes)
	}

	// Push 20 more runes → 110 total → exceeds hard limit → force split.
	if err := stream.Push(context.Background(), StreamEvent{
		Type:  StreamEventDelta,
		Delta: strings.Repeat("x", 20),
	}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if stream.splitCount != 1 {
		t.Fatalf("expected force-split at hard limit, got %d splits", stream.splitCount)
	}
}

func TestPushDelta_NewlineTriggersBreak(t *testing.T) {
	t.Parallel()
	stream, _, _ := newDeltaSplitTestStream(t, 100, 3)

	// 76 runes — just past soft limit (75).
	if err := stream.Push(context.Background(), StreamEvent{
		Type:  StreamEventDelta,
		Delta: strings.Repeat("w", 76),
	}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if stream.splitCount != 0 {
		t.Fatal("no break yet")
	}

	// Newline → natural break.
	if err := stream.Push(context.Background(), StreamEvent{
		Type:  StreamEventDelta,
		Delta: "\n",
	}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if stream.splitCount != 1 {
		t.Fatalf("expected split on newline in soft zone, got %d", stream.splitCount)
	}
}

func TestPushDelta_ChinesePunctuationBreak(t *testing.T) {
	t.Parallel()
	stream, _, _ := newDeltaSplitTestStream(t, 100, 3)

	if err := stream.Push(context.Background(), StreamEvent{
		Type:  StreamEventDelta,
		Delta: strings.Repeat("字", 76),
	}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Chinese period → natural break.
	if err := stream.Push(context.Background(), StreamEvent{
		Type:  StreamEventDelta,
		Delta: "。",
	}); err != nil {
		t.Fatalf("Push failed: %v", err)
	}
	if stream.splitCount != 1 {
		t.Fatalf("expected split on Chinese period, got %d", stream.splitCount)
	}
}

func TestIsNaturalBreakPoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		text   string
		expect bool
	}{
		{"empty", "", false},
		{"plain word", "hello", false},
		{"period", "end.", true},
		{"period with trailing space", "end. ", true},
		{"question mark", "why?", true},
		{"exclamation", "wow!", true},
		{"Chinese period", "结束。", true},
		{"Chinese question", "什么？", true},
		{"Chinese exclamation", "好！", true},
		{"ellipsis", "wait…", true},
		{"newline", "line\n", true},
		{"double newline", "paragraph\n\n", true},
		{"semicolon", "clause;", true},
		{"Chinese semicolon", "分号；", true},
		{"comma", "hello,", false},
		{"mid-word", "incompl", false},
		{"version number", "v2.0", false},
		{"code dot", "obj.method", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isNaturalBreakPoint(tc.text)
			if got != tc.expect {
				t.Errorf("isNaturalBreakPoint(%q) = %v, want %v", tc.text, got, tc.expect)
			}
		})
	}
}

func TestNormalizeOutboundMessage_MarkdownDetected(t *testing.T) {
	t.Parallel()
	msg := normalizeOutboundMessage(Message{Text: "Hello **world**"})
	if msg.Format != MessageFormatMarkdown {
		t.Errorf("expected %q, got %q", MessageFormatMarkdown, msg.Format)
	}
}

func TestNormalizeOutboundMessage_PlainText(t *testing.T) {
	t.Parallel()
	msg := normalizeOutboundMessage(Message{Text: "Hello world"})
	if msg.Format != MessageFormatPlain {
		t.Errorf("expected %q, got %q", MessageFormatPlain, msg.Format)
	}
}

func TestNormalizeOutboundMessage_ExplicitFormatPreserved(t *testing.T) {
	t.Parallel()
	msg := normalizeOutboundMessage(Message{Text: "Hello **world**", Format: MessageFormatPlain})
	if msg.Format != MessageFormatPlain {
		t.Errorf("expected explicit format %q preserved, got %q", MessageFormatPlain, msg.Format)
	}
}

func TestNormalizeOutboundMessage_RichParts(t *testing.T) {
	t.Parallel()
	msg := normalizeOutboundMessage(Message{Parts: []MessagePart{{Type: "text", Text: "hello"}}})
	if msg.Format != MessageFormatRich {
		t.Errorf("expected %q, got %q", MessageFormatRich, msg.Format)
	}
}

func TestNormalizeOutboundMessage_MovesTextIntoRichParts(t *testing.T) {
	t.Parallel()

	msg := normalizeOutboundMessage(Message{
		Text: "intro",
		Parts: []MessagePart{
			{Type: MessagePartHeading, Text: "Title"},
		},
	})
	if msg.Text != "" {
		t.Fatalf("Text should be canonicalized into parts, got %q", msg.Text)
	}
	if msg.Format != MessageFormatRich {
		t.Fatalf("Format = %q, want rich", msg.Format)
	}
	if len(msg.Parts) != 2 {
		t.Fatalf("Parts len = %d, want 2: %#v", len(msg.Parts), msg.Parts)
	}
	if msg.Parts[0].Type != MessagePartText || msg.Parts[0].Text != "intro" {
		t.Fatalf("first part should preserve original text, got %#v", msg.Parts[0])
	}
}

func TestNormalizeOutboundMessage_TextOnlyRichPreservedUntilCapsKnown(t *testing.T) {
	t.Parallel()

	msg := normalizeOutboundMessage(Message{Text: "Hello world", Format: MessageFormatRich})
	if msg.Format != MessageFormatRich {
		t.Fatalf("text-only rich should be preserved until target capabilities are known, got %q", msg.Format)
	}
}

// TestNormalizeOutboundMessage_BulletListFalsePositive is the regression guard
// for the silent-command-failure bug on plain-text-only channels (Weixin,
// WeChat OA, Local-Web). The /help body looks like:
//
//	**Available commands**
//
//	- /help — Show available commands
//	- /new — Start a new conversation
//	...
//
// After the renderer strips ** markers for non-markdown channels, the leading
// "- " bullets remain and ContainsMarkdown's ^[-*]\s pattern matches them. If
// Format is left empty, the auto-detect promotes the message to Markdown and
// validateMessageCapabilities rejects it with "channel does not support
// markdown" — which is logged but never replied, so the user sees silence.
//
// The renderer guards against this by setting Format=Plain explicitly. This
// test pins the contract: an EXPLICIT Plain format survives, regardless of
// what auto-detect would have inferred from the bullet list.
func TestNormalizeOutboundMessage_BulletListExplicitPlainSurvives(t *testing.T) {
	t.Parallel()
	bulletHelp := "Available commands\n\n- /help — Show available commands\n- /new — Start a new conversation\n- /stop — Stop the current reply\n"
	msg := normalizeOutboundMessage(Message{Text: bulletHelp, Format: MessageFormatPlain})
	if msg.Format != MessageFormatPlain {
		t.Fatalf("explicit plain format must survive bullet-list auto-detect, got %q", msg.Format)
	}
	// Sanity: without the explicit Plain, auto-detect IS fooled by the bullets.
	autoDetected := normalizeOutboundMessage(Message{Text: bulletHelp})
	if autoDetected.Format != MessageFormatMarkdown {
		t.Fatalf("auto-detect is supposed to promote bullet lists to markdown (this is the bug the renderer guards against); got %q — if the auto-detect rule changed, also update applyMessageFormat", autoDetected.Format)
	}
}

// TestCoerceFormatForCaps_DegradesMarkdownOnPlainChannel pins the outbound
// boundary defense: when bullet-auto-detect (or any other path) promotes a
// message to Markdown but the target channel can only render plain text, the
// message must degrade to Plain (with inline markup stripped) instead of being
// rejected by validateMessageCapabilities. This makes the Format=Plain
// invariant a property of the outbound layer rather than discipline at every
// channel.Message{...} construction site upstream — the previous failure mode
// was a silent drop (logged but never replied) on Weixin/WeChat OA/Local-Web.
func TestCoerceFormatForCaps_DegradesMarkdownOnPlainChannel(t *testing.T) {
	t.Parallel()
	plainCaps := ChannelCapabilities{Text: true} // no Markdown, no RichText
	msg := Message{Text: "Hello **world** and `code`", Format: MessageFormatMarkdown}
	coerced := coerceFormatForCaps(msg, plainCaps)
	if coerced.Format != MessageFormatPlain {
		t.Fatalf("plain-only caps must coerce Markdown → Plain, got %q", coerced.Format)
	}
	if strings.Contains(coerced.Text, "**") || strings.Contains(coerced.Text, "`") {
		t.Fatalf("coercion must strip inline markup, got %q", coerced.Text)
	}
	if !strings.Contains(coerced.Text, "Hello world and code") {
		t.Fatalf("coercion must preserve readable text content, got %q", coerced.Text)
	}
}

// TestCoerceFormatForCaps_PreservesMarkdownOnCapableChannel — Telegram and
// other Markdown-capable channels must not lose formatting.
func TestCoerceFormatForCaps_PreservesMarkdownOnCapableChannel(t *testing.T) {
	t.Parallel()
	mdCaps := ChannelCapabilities{Text: true, Markdown: true}
	msg := Message{Text: "Hello **world**", Format: MessageFormatMarkdown}
	coerced := coerceFormatForCaps(msg, mdCaps)
	if coerced.Format != MessageFormatMarkdown {
		t.Fatalf("markdown-capable channel must keep markdown format, got %q", coerced.Format)
	}
	if !strings.Contains(coerced.Text, "**world**") {
		t.Fatalf("coercion must not strip markup on capable channel, got %q", coerced.Text)
	}
}

// TestCoerceFormatForCaps_PreservesPartsOnRichTextChannel locks in the
// invariant that the rich-text path (Telegram sendRichMessage, Feishu
// interactive card) keeps Parts intact through the outbound boundary. If
// this regresses, those adapters' rich rendering becomes silently
// unreachable in production because coerce would have stripped Parts to
// markdown text before the adapter ever sees it.
func TestCoerceFormatForCaps_PreservesPartsOnRichTextChannel(t *testing.T) {
	t.Parallel()
	msg := Message{
		Format: MessageFormatRich,
		Parts: []MessagePart{
			{Type: MessagePartText, Text: "hello", Styles: []MessageTextStyle{MessageStyleBold}},
		},
	}
	caps := ChannelCapabilities{Text: true, Markdown: true, RichText: true}
	coerced := coerceFormatForCaps(msg, caps)
	if coerced.Format != MessageFormatRich {
		t.Fatalf("format should stay rich, got %q", coerced.Format)
	}
	if len(coerced.Parts) != 1 || coerced.Parts[0].Text != "hello" {
		t.Fatalf("Parts must survive on a rich-capable channel, got %+v", coerced.Parts)
	}
}

func TestValidateMessageAgainstCapabilities_URLButtonsOnly(t *testing.T) {
	t.Parallel()

	caps := ChannelCapabilities{Text: true, URLButtons: true}
	urlMsg := Message{
		Text: "Read this",
		Actions: []Action{
			{Label: "Open", URL: "https://example.com"},
		},
	}
	if err := validateMessageAgainstCapabilities(caps, true, urlMsg); err != nil {
		t.Fatalf("URL action should be allowed with URLButtons: %v", err)
	}

	callbackMsg := Message{
		Text: "Choose",
		Actions: []Action{
			{Label: "Approve", Value: "approve:1"},
		},
	}
	if err := validateMessageAgainstCapabilities(caps, true, callbackMsg); err == nil || !strings.Contains(err.Error(), "callback actions") {
		t.Fatalf("callback action error = %v, want callback actions rejection", err)
	}
}

// TestCoerceFormatForCaps_PreservesPlainEverywhere — explicit Plain is never
// upgraded by coercion.
func TestCoerceFormatForCaps_PreservesPlainEverywhere(t *testing.T) {
	t.Parallel()
	for name, caps := range map[string]ChannelCapabilities{
		"plain-only": {Text: true},
		"markdown":   {Text: true, Markdown: true},
		"rich":       {Text: true, RichText: true},
		"buttons":    {Text: true, Markdown: true, Buttons: true},
	} {
		t.Run(name, func(t *testing.T) {
			msg := Message{Text: "Plain reply", Format: MessageFormatPlain}
			coerced := coerceFormatForCaps(msg, caps)
			if coerced.Format != MessageFormatPlain {
				t.Fatalf("plain must stay plain on %s, got %q", name, coerced.Format)
			}
		})
	}
}
