package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
)

var (
	testBotToken          = strings.Join([]string{"xoxb", "test", "token"}, "-")
	testShortBotToken     = strings.Join([]string{"xoxb", "test"}, "-")
	testAppToken          = strings.Join([]string{"xapp", "test"}, "-")
	testBadAppToken       = strings.Join([]string{"xapp", "bad"}, "-")
	testDownloadAuthValue = "Bearer " + testBotToken
)

func TestSlackRegistryExposesSupportedInterfaces(t *testing.T) {
	t.Parallel()

	reg := channel.NewRegistry()
	reg.MustRegister(NewSlackAdapter(nil))

	if sender, ok := reg.GetSender(Type); !ok || sender == nil {
		t.Fatal("expected Slack adapter to implement channel.Sender")
	}
	if streamSender, ok := reg.GetStreamSender(Type); !ok || streamSender == nil {
		t.Fatal("expected Slack adapter to implement channel.StreamSender")
	}
	if editor, ok := reg.GetMessageEditor(Type); ok || editor != nil {
		t.Fatal("did not expect Slack adapter to implement channel.MessageEditor")
	}
}

func TestSlackDescriptorDoesNotAdvertiseEdit(t *testing.T) {
	t.Parallel()

	if NewSlackAdapter(nil).Descriptor().Capabilities.Edit {
		t.Fatal("Slack descriptor should not advertise edit support")
	}
}

func TestSlackDescriptorAdvertisesRichText(t *testing.T) {
	t.Parallel()

	if !NewSlackAdapter(nil).Descriptor().Capabilities.RichText {
		t.Fatal("Slack descriptor must advertise rich text so Message.Parts reaches the Slack renderer")
	}
}

func TestSlackDescriptorAdvertisesURLButtonsOnly(t *testing.T) {
	t.Parallel()

	caps := NewSlackAdapter(nil).Descriptor().Capabilities
	if caps.Buttons {
		t.Fatal("Slack descriptor must not advertise callback button support")
	}
	if !caps.URLButtons {
		t.Fatal("Slack descriptor must advertise URL button support")
	}
}

func TestSlackDescriptorUsesRichTextChunkLimit(t *testing.T) {
	t.Parallel()

	policy := channel.NormalizeOutboundPolicy(NewSlackAdapter(nil).Descriptor().OutboundPolicy)
	if policy.RichTextChunkLimit != slackMaxLength {
		t.Fatalf("RichTextChunkLimit = %d, want %d", policy.RichTextChunkLimit, slackMaxLength)
	}
}

func TestRenderSlackOutboundBodyFallsBackWhenEscapedRichOverflows(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("&", slackMaxLength/4+1)
	got := renderSlackOutboundBody(channel.Message{
		Parts: []channel.MessagePart{{
			Type: channel.MessagePartText,
			Text: text,
		}},
	})
	if got.Text != text {
		t.Fatalf("expected raw plain fallback text, got len=%d prefix=%q", len([]rune(got.Text)), got.Text[:min(len(got.Text), 20)])
	}
	if got.BlockText != slackEscapeMrkdwn(text) {
		t.Fatalf("expected escaped block fallback, got len=%d prefix=%q", len([]rune(got.BlockText)), got.BlockText[:min(len(got.BlockText), 20)])
	}
	if !got.DisableMarkdown {
		t.Fatal("fallback should disable Slack markdown for raw plain text")
	}
	if strings.Contains(got.Text, "*") || strings.HasSuffix(truncateSlackText(got.Text), "...") {
		t.Fatalf("fallback should avoid rich wrapper/truncation, got prefix=%q", got.Text[:min(len(got.Text), 20)])
	}
}

func TestRenderSlackOutboundBodyEscapesPlainOverflowFallbackTags(t *testing.T) {
	t.Parallel()

	raw := "<!channel>" + strings.Repeat("&", slackMaxLength/4+1)
	got := renderSlackOutboundBody(channel.Message{
		Parts: []channel.MessagePart{{
			Type: channel.MessagePartText,
			Text: raw,
		}},
	})
	if got.Text != raw {
		t.Fatalf("expected raw top-level fallback text, got %q", got.Text[:min(len(got.Text), 32)])
	}
	if !got.DisableMarkdown {
		t.Fatal("fallback should disable Slack markdown for raw control tags")
	}
	if strings.Contains(got.BlockText, "<!channel>") {
		t.Fatalf("fallback block text must not re-enable Slack control tags: %q", got.BlockText[:min(len(got.BlockText), 32)])
	}
	if !strings.Contains(got.BlockText, "&lt;!channel&gt;") {
		t.Fatalf("expected escaped Slack tag in fallback block text, got %q", got.BlockText[:min(len(got.BlockText), 64)])
	}
}

func TestSlackResolveOutboundTargetUsesDMForUserID(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	adapter.openConversation = func(_ context.Context, _ *slack.Client, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
		if len(params.Users) != 1 || params.Users[0] != "U123" {
			t.Fatalf("unexpected users: %#v", params.Users)
		}
		if !params.ReturnIM {
			t.Fatal("expected ReturnIM to be true")
		}
		return &slack.Channel{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "D123"}}}, false, false, nil
	}

	target, err := adapter.resolveOutboundTarget(context.Background(), slack.New(testShortBotToken), "U123")
	if err != nil {
		t.Fatalf("resolveOutboundTarget: %v", err)
	}
	if target != "D123" {
		t.Fatalf("expected DM channel target, got %q", target)
	}
}

func TestSlackResolveOutboundTargetRejectsEmptyDMChannel(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	adapter.openConversation = func(_ context.Context, _ *slack.Client, _ *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
		return &slack.Channel{}, false, false, nil
	}

	_, err := adapter.resolveOutboundTarget(context.Background(), slack.New(testShortBotToken), "U123")
	if err == nil || !strings.Contains(err.Error(), "empty channel") {
		t.Fatalf("expected empty channel error, got %v", err)
	}
}

func TestSlackCollectAttachmentsOmitsPrivateURLForInboundFiles(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	attachments := adapter.collectAttachments(&slack.Msg{
		Files: []slack.File{{
			ID:                 "F123",
			Name:               "cat.png",
			Mimetype:           "image/png",
			Size:               42,
			URLPrivateDownload: "https://files.slack.test/F123",
		}},
	})

	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if attachments[0].URL != "" {
		t.Fatalf("expected private URL to be omitted, got %q", attachments[0].URL)
	}
	if attachments[0].PlatformKey != "F123" {
		t.Fatalf("unexpected platform key: %q", attachments[0].PlatformKey)
	}
	if attachments[0].Type != channel.AttachmentImage {
		t.Fatalf("unexpected attachment type: %q", attachments[0].Type)
	}
}

func TestResolveSlackEmoji(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "unicode", in: "👍", want: "+1"},
		{name: "shortcode with colons", in: ":thumbsup:", want: "thumbsup"},
		{name: "shortcode plain", in: "thumbsup", want: "thumbsup"},
		{name: "skin tone unicode", in: "👍🏽", want: "+1::skin-tone-4"},
		{name: "variation selector unicode", in: "✌️", want: "v"},
		{name: "skin tone wave", in: "👋🏻", want: "wave::skin-tone-2"},
		{name: "unknown passthrough", in: "not-an-emoji", want: "not-an-emoji"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSlackEmoji(tt.in); got != tt.want {
				t.Fatalf("resolveSlackEmoji(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildSlackReplyRefFromThread(t *testing.T) {
	t.Parallel()

	ref := buildSlackReplyRef("C123", "1710000001.000000", "1710000000.000000", "U123")
	if ref == nil {
		t.Fatal("expected reply ref")
		return
	}
	if ref.Target != "C123" || ref.MessageID != "1710000000.000000" || ref.Sender != "U123" {
		t.Fatalf("unexpected reply ref: %#v", ref)
	}
	if root := buildSlackReplyRef("C123", "1710000000.000000", "1710000000.000000", "U123"); root != nil {
		t.Fatalf("root thread message should not be reply: %#v", root)
	}
}

func TestSlackConnectClearsCachedClientWhenAuthFails(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	clientTokens := make(map[*slack.Client]string)
	factoryCalls := 0
	adapter.socketOpen = func(cfg Config) (*slack.Client, *socketmode.Client) {
		factoryCalls++
		api := slack.New(cfg.BotToken)
		clientTokens[api] = cfg.BotToken
		return api, socketmode.New(api)
	}
	adapter.authTest = func(api *slack.Client) (*slack.AuthTestResponse, error) {
		if clientTokens[api] == "bad-token" {
			return nil, errors.New("invalid bot token")
		}
		return &slack.AuthTestResponse{UserID: "U123"}, nil
	}
	adapter.socketRun = func(ctx context.Context, sm *socketmode.Client) error {
		select {
		case sm.Events <- socketmode.Event{Type: socketmode.EventTypeConnected}:
		case <-ctx.Done():
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	}

	cfg := channel.ChannelConfig{
		ID:          "cfg-auth-retry",
		BotID:       "bot-1",
		ChannelType: Type,
		Credentials: map[string]any{
			"botToken": "bad-token",
			"appToken": testAppToken,
		},
	}
	if _, err := adapter.Connect(context.Background(), cfg, func(context.Context, channel.ChannelConfig, channel.InboundMessage) error {
		return nil
	}); err == nil {
		t.Fatal("expected auth failure")
	}
	if len(adapter.connections) != 0 {
		t.Fatal("failed startup should not leave cached Slack clients behind")
	}

	cfg.Credentials["botToken"] = "good-token"
	conn, err := adapter.Connect(context.Background(), cfg, func(context.Context, channel.ChannelConfig, channel.InboundMessage) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Connect retry should succeed: %v", err)
	}
	if factoryCalls != 2 {
		t.Fatalf("expected 2 client constructions after retry, got %d", factoryCalls)
	}
	if err := conn.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestSlackConnectFailsWhenSocketModeStartupFails(t *testing.T) {
	t.Parallel()

	startErr := errors.New("invalid app token")
	adapter := NewSlackAdapter(nil)
	adapter.socketOpen = func(cfg Config) (*slack.Client, *socketmode.Client) {
		api := slack.New(cfg.BotToken)
		return api, socketmode.New(api)
	}
	adapter.authTest = func(*slack.Client) (*slack.AuthTestResponse, error) {
		return &slack.AuthTestResponse{UserID: "U123"}, nil
	}
	adapter.socketRun = func(ctx context.Context, sm *socketmode.Client) error {
		select {
		case sm.Events <- socketmode.Event{
			Type: socketmode.EventTypeConnectionError,
			Data: &slack.ConnectionErrorEvent{ErrorObj: startErr},
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return startErr
	}

	cfg := channel.ChannelConfig{
		ID:          "cfg-startup-error",
		BotID:       "bot-1",
		ChannelType: Type,
		Credentials: map[string]any{
			"botToken": testShortBotToken,
			"appToken": testBadAppToken,
		},
	}
	conn, err := adapter.Connect(context.Background(), cfg, func(context.Context, channel.ChannelConfig, channel.InboundMessage) error {
		return nil
	})
	if err == nil {
		if conn != nil {
			_ = conn.Stop(context.Background())
		}
		t.Fatal("expected socket mode startup failure")
	}
	if !strings.Contains(err.Error(), "invalid app token") {
		t.Fatalf("unexpected startup error: %v", err)
	}
	if len(adapter.connections) != 0 {
		t.Fatal("startup failure should clear cached Slack connection")
	}
}

func TestSlackResolveAttachmentDownloadsPrivateURLWithBearerToken(t *testing.T) {
	t.Parallel()

	var gotAuth string
	adapter := NewSlackAdapter(nil)
	client := slack.New(
		testBotToken,
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://files.slack.test/private/file.txt" {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
			gotAuth = r.Header.Get("Authorization")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("slack-private-file")),
			}, nil
		})}),
		slack.OptionRetry(3),
	)

	payload, err := adapter.resolveAttachmentWithClient(context.Background(), client, channel.Attachment{
		URL:         "https://files.slack.test/private/file.txt",
		Name:        "file.txt",
		Mime:        "text/plain",
		Size:        18,
		Type:        channel.AttachmentFile,
		PlatformKey: "F123",
	})
	if err != nil {
		t.Fatalf("ResolveAttachment: %v", err)
	}
	defer func() { _ = payload.Reader.Close() }()

	data, err := io.ReadAll(payload.Reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "slack-private-file" {
		t.Fatalf("unexpected payload: %q", string(data))
	}
	if gotAuth != testDownloadAuthValue {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
}

func TestSlackResolveAttachmentFallsBackToFilesInfo(t *testing.T) {
	t.Parallel()

	var gotFileToken string
	var gotDownloadAuth string
	adapter := NewSlackAdapter(nil)
	client := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://slack.test/api/files.info":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm: %v", err)
				}
				gotFileToken = r.FormValue("token")
				body, _ := json.Marshal(map[string]any{
					"ok": true,
					"file": map[string]any{
						"id":                   "F123",
						"name":                 "fallback.txt",
						"mimetype":             "text/plain",
						"size":                 13,
						"url_private_download": "https://files.slack.test/download/F123",
					},
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://files.slack.test/download/F123":
				gotDownloadAuth = r.Header.Get("Authorization")
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/plain"}},
					Body:       io.NopCloser(strings.NewReader("fallback-file")),
				}, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
		})}),
		slack.OptionRetry(3),
	)

	payload, err := adapter.resolveAttachmentWithClient(context.Background(), client, channel.Attachment{
		PlatformKey: "F123",
		Type:        channel.AttachmentFile,
	})
	if err != nil {
		t.Fatalf("resolveAttachmentWithClient: %v", err)
	}
	defer func() { _ = payload.Reader.Close() }()

	data, err := io.ReadAll(payload.Reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "fallback-file" {
		t.Fatalf("unexpected payload: %q", string(data))
	}
	if gotFileToken != testBotToken {
		t.Fatalf("unexpected files.info token: %q", gotFileToken)
	}
	if gotDownloadAuth != testDownloadAuthValue {
		t.Fatalf("unexpected download auth header: %q", gotDownloadAuth)
	}
	if payload.Name != "fallback.txt" {
		t.Fatalf("unexpected name: %q", payload.Name)
	}
	if payload.Mime != "text/plain" {
		t.Fatalf("unexpected mime: %q", payload.Mime)
	}
}

func TestSlackResolveAttachmentRejectsOversizedKnownSlackFile(t *testing.T) {
	t.Parallel()

	var downloadCalls int
	adapter := NewSlackAdapter(nil)
	client := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://slack.test/api/files.info":
				body, _ := json.Marshal(map[string]any{
					"ok": true,
					"file": map[string]any{
						"id":                   "F999",
						"name":                 "huge.bin",
						"mimetype":             "application/octet-stream",
						"size":                 media.MaxAssetBytes + 1,
						"url_private_download": "https://files.slack.test/download/F999",
					},
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://files.slack.test/download/F999":
				downloadCalls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
					Body:       io.NopCloser(strings.NewReader("should-not-download")),
				}, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
		})}),
		slack.OptionRetry(3),
	)

	_, err := adapter.resolveAttachmentWithClient(context.Background(), client, channel.Attachment{
		PlatformKey: "F999",
		Type:        channel.AttachmentFile,
	})
	if err == nil {
		t.Fatal("expected oversized attachment error")
	}
	if !strings.Contains(err.Error(), "media asset too large") {
		t.Fatalf("unexpected error: %v", err)
	}
	if downloadCalls != 0 {
		t.Fatalf("expected oversized file to be rejected before download, got %d download calls", downloadCalls)
	}
}

func TestSlackHandleMessageEventStoresDMChannelID(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	conn := &slackConnection{}
	cfg := channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1"}
	msgCh := make(chan channel.InboundMessage, 1)

	adapter.handleMessageEvent(context.Background(), conn, &slackevents.MessageEvent{
		User:        "U123",
		Text:        "hello",
		TimeStamp:   "1710000000.000100",
		Channel:     "D123",
		ChannelType: "im",
		Message:     &slack.Msg{Text: "hello"},
	}, cfg, func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
		msgCh <- msg
		return nil
	}, "UBOT")

	select {
	case msg := <-msgCh:
		if got := msg.Sender.Attribute("channel_id"); got != "D123" {
			t.Fatalf("unexpected channel_id: %q", got)
		}
		if got, _ := msg.Metadata["bot_alias"].(string); got != "UBOT" {
			t.Fatalf("bot_alias = %q, want UBOT", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestSlackHandleMessageEventSkipsChannelIDOutsideDM(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	conn := &slackConnection{}
	cfg := channel.ChannelConfig{ID: "cfg-2", BotID: "bot-1"}
	msgCh := make(chan channel.InboundMessage, 1)

	adapter.handleMessageEvent(context.Background(), conn, &slackevents.MessageEvent{
		User:        "U123",
		Text:        "hello",
		TimeStamp:   "1710000000.000101",
		Channel:     "C123",
		ChannelType: "channel",
		Message:     &slack.Msg{Text: "hello"},
	}, cfg, func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
		msgCh <- msg
		return nil
	}, "UBOT")

	select {
	case msg := <-msgCh:
		if got := msg.Sender.Attribute("channel_id"); got != "" {
			t.Fatalf("expected empty channel_id, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestSlackHandleMessageEventResolvesConversationName(t *testing.T) {
	t.Parallel()

	var conversationsInfoCalls int
	adapter := NewSlackAdapter(nil)
	conn := &slackConnection{
		api: slack.New(
			testBotToken,
			slack.OptionAPIURL("https://slack.test/api/"),
			slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.String() {
				case "https://slack.test/api/conversations.info":
					conversationsInfoCalls++
					if err := r.ParseForm(); err != nil {
						t.Fatalf("ParseForm: %v", err)
					}
					body, _ := json.Marshal(map[string]any{
						"ok": true,
						"channel": map[string]any{
							"id":   "C123",
							"name": "general",
						},
					})
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(string(body))),
					}, nil
				default:
					return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
				}
			})}),
			slack.OptionRetry(3),
		),
	}
	cfg := channel.ChannelConfig{ID: "cfg-name", BotID: "bot-1"}
	msgCh := make(chan channel.InboundMessage, 1)

	adapter.handleMessageEvent(context.Background(), conn, &slackevents.MessageEvent{
		User:        "U123",
		Text:        "hello",
		TimeStamp:   "1710000000.000102",
		Channel:     "C123",
		ChannelType: "channel",
		Message:     &slack.Msg{Text: "hello"},
	}, cfg, func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
		msgCh <- msg
		return nil
	}, "UBOT")

	select {
	case msg := <-msgCh:
		if msg.Conversation.Name != "general" {
			t.Fatalf("unexpected conversation name: %q", msg.Conversation.Name)
		}
		gotMeta, _ := msg.Metadata["channel_name"].(string)
		if gotMeta != "general" {
			t.Fatalf("unexpected metadata channel_name: %q", gotMeta)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
	if conversationsInfoCalls != 1 {
		t.Fatalf("unexpected conversations.info calls: %d", conversationsInfoCalls)
	}
}

func TestSlackLookupConversationNameCachesResolvedNames(t *testing.T) {
	t.Parallel()

	var conversationsInfoCalls int
	adapter := NewSlackAdapter(nil)
	api := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://slack.test/api/conversations.info":
				conversationsInfoCalls++
				body, _ := json.Marshal(map[string]any{
					"ok": true,
					"channel": map[string]any{
						"id":   "C123",
						"name": "general",
					},
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
		})}),
		slack.OptionRetry(3),
	)

	first := adapter.lookupConversationName(context.Background(), api, "cfg-cache", "C123")
	second := adapter.lookupConversationName(context.Background(), api, "cfg-cache", "C123")
	if first != "general" || second != "general" {
		t.Fatalf("unexpected cached names: %q / %q", first, second)
	}
	if conversationsInfoCalls != 1 {
		t.Fatalf("unexpected conversations.info calls: %d", conversationsInfoCalls)
	}
}

func TestSlackHandleMessageEventKeepsFlowWhenConversationNameLookupFails(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	conn := &slackConnection{
		api: slack.New(
			testBotToken,
			slack.OptionAPIURL("https://slack.test/api/"),
			slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.String() {
				case "https://slack.test/api/conversations.info":
					body, _ := json.Marshal(map[string]any{
						"ok":    false,
						"error": "missing_scope",
					})
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(string(body))),
					}, nil
				default:
					return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
				}
			})}),
			slack.OptionRetry(3),
		),
	}
	cfg := channel.ChannelConfig{ID: "cfg-name-fail", BotID: "bot-1"}
	msgCh := make(chan channel.InboundMessage, 1)

	adapter.handleMessageEvent(context.Background(), conn, &slackevents.MessageEvent{
		User:        "U123",
		Text:        "hello",
		TimeStamp:   "1710000000.000103",
		Channel:     "C123",
		ChannelType: "channel",
		Message:     &slack.Msg{Text: "hello"},
	}, cfg, func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
		msgCh <- msg
		return nil
	}, "UBOT")

	select {
	case msg := <-msgCh:
		if msg.Conversation.Name != "" {
			t.Fatalf("expected empty conversation name, got %q", msg.Conversation.Name)
		}
		gotMeta, _ := msg.Metadata["channel_name"].(string)
		if gotMeta != "" {
			t.Fatalf("expected empty metadata channel_name, got %q", gotMeta)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestSlackHandleAppMentionEventPreservesAttachments(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	adapter.historyFetch = func(_ context.Context, _ *slack.Client, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
		if params == nil || params.ChannelID != "C123" || params.Oldest != "1710000000.000200" || !params.Inclusive {
			t.Fatalf("unexpected history params: %+v", params)
		}
		return &slack.GetConversationHistoryResponse{
			Messages: []slack.Message{{
				Msg: slack.Msg{
					Text: "hi <@UBOT>",
					Files: []slack.File{{
						ID:                 "F123",
						Name:               "cat.png",
						Mimetype:           "image/png",
						Size:               42,
						URLPrivateDownload: "https://files.slack.test/F123",
					}},
				},
			}},
		}, nil
	}

	conn := &slackConnection{api: slack.New(testBotToken)}
	cfg := channel.ChannelConfig{ID: "cfg-mention", BotID: "bot-1"}
	msgCh := make(chan channel.InboundMessage, 1)

	adapter.handleAppMentionEvent(context.Background(), conn, &slackevents.AppMentionEvent{
		User:      "U123",
		Text:      "hi <@UBOT>",
		TimeStamp: "1710000000.000200",
		Channel:   "C123",
	}, cfg, func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
		msgCh <- msg
		return nil
	}, "UBOT")

	select {
	case msg := <-msgCh:
		if len(msg.Message.Attachments) != 1 {
			t.Fatalf("expected 1 attachment, got %d", len(msg.Message.Attachments))
		}
		if got := msg.Message.Attachments[0].PlatformKey; got != "F123" {
			t.Fatalf("unexpected attachment platform key: %q", got)
		}
		if got, _ := msg.Metadata["bot_alias"].(string); got != "UBOT" {
			t.Fatalf("bot_alias = %q, want UBOT", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound mention message")
	}
}

func TestSlackHandleAppMentionEventPreservesPrivateChannelType(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	adapter.mu.Lock()
	adapter.channelNames["cfg-private:C999"] = cachedSlackChannelName{
		name:     "ops-private",
		chatType: channel.ConversationTypeGroup,
		cachedAt: time.Now().UTC(),
	}
	adapter.mu.Unlock()

	conn := &slackConnection{api: slack.New(testBotToken)}
	cfg := channel.ChannelConfig{ID: "cfg-private", BotID: "bot-1"}
	msgCh := make(chan channel.InboundMessage, 1)

	adapter.handleAppMentionEvent(context.Background(), conn, &slackevents.AppMentionEvent{
		User:      "U123",
		Text:      "hi <@UBOT>",
		TimeStamp: "1710000000.000201",
		Channel:   "C999",
	}, cfg, func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
		msgCh <- msg
		return nil
	}, "UBOT")

	select {
	case msg := <-msgCh:
		if msg.Conversation.Type != channel.ConversationTypeGroup {
			t.Fatalf("unexpected conversation type: %q", msg.Conversation.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound mention message")
	}
}

func TestSlackSendReturnsAttachmentUploadFailures(t *testing.T) {
	t.Parallel()

	var postMessageCalls int
	adapter := NewSlackAdapter(nil)
	api := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://slack.test/api/files.getUploadURLExternal":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm: %v", err)
				}
				body, _ := json.Marshal(map[string]any{
					"ok":         true,
					"upload_url": "https://upload.slack.test/fail",
					"file_id":    "F123",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://upload.slack.test/fail":
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("upload failed")),
					Header:     make(http.Header),
				}, nil
			case "https://slack.test/api/files.completeUploadExternal":
				t.Fatal("completeUploadExternal should not be called after failed upload")
				return nil, nil
			case "https://slack.test/api/chat.postMessage":
				postMessageCalls++
				t.Fatal("chat.postMessage should not be called after failed attachment upload")
				return nil, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
		})}),
		slack.OptionRetry(3),
	)

	err := adapter.sendSlackMessage(context.Background(), api, "C123", channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "hello",
			},
			Attachments: []channel.PreparedAttachment{preparedSlackUploadAttachment("hello.txt", "text/plain", "hello")},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "UploadToURL") {
		t.Fatalf("unexpected error: %v", err)
	}
	if postMessageCalls != 0 {
		t.Fatalf("unexpected chat.postMessage calls: %d", postMessageCalls)
	}
}

func TestSlackSendAttachmentOnlyReturnsUploadFailures(t *testing.T) {
	t.Parallel()

	api := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://slack.test/api/files.getUploadURLExternal":
				body, _ := json.Marshal(map[string]any{
					"ok":         true,
					"upload_url": "https://upload.slack.test/fail-only-attachment",
					"file_id":    "F124",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://upload.slack.test/fail-only-attachment":
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("upload failed")),
					Header:     make(http.Header),
				}, nil
			case "https://slack.test/api/files.completeUploadExternal":
				t.Fatal("completeUploadExternal should not be called after failed attachment upload")
				return nil, nil
			case "https://slack.test/api/chat.postMessage":
				t.Fatal("chat.postMessage should not be called for attachment-only failure")
				return nil, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
		})}),
		slack.OptionRetry(3),
	)

	adapter := NewSlackAdapter(nil)
	err := adapter.sendSlackMessage(context.Background(), api, "C123", channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Attachments: []channel.Attachment{{
					Type: channel.AttachmentFile,
					Name: "hello.txt",
					Mime: "text/plain",
					Size: 5,
				}},
			},
			Attachments: []channel.PreparedAttachment{preparedSlackUploadAttachment("hello.txt", "text/plain", "hello")},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "UploadToURL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSlackStreamAttachmentOnlyClearsPlaceholder(t *testing.T) {
	t.Parallel()

	var postCalls, deleteCalls, uploadCalls int
	client := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://slack.test/api/chat.postMessage":
				postCalls++
				body, _ := json.Marshal(map[string]any{
					"ok":      true,
					"channel": "C123",
					"ts":      "1710000000.000300",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://slack.test/api/chat.delete":
				deleteCalls++
				body, _ := json.Marshal(map[string]any{
					"ok":      true,
					"channel": "C123",
					"ts":      "1710000000.000300",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://slack.test/api/files.getUploadURLExternal":
				uploadCalls++
				body, _ := json.Marshal(map[string]any{
					"ok":         true,
					"upload_url": "https://upload.slack.test/F200",
					"file_id":    "F200",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://upload.slack.test/F200":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
				}, nil
			case "https://slack.test/api/files.completeUploadExternal":
				body, _ := json.Marshal(map[string]any{"ok": true, "files": []map[string]any{{"id": "F200"}}})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
		})}),
		slack.OptionRetry(3),
	)

	stream := &slackOutboundStream{
		adapter: NewSlackAdapter(nil),
		cfg:     channel.ChannelConfig{ID: "cfg-stream"},
		target:  "C123",
		api:     client,
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:   channel.StreamEventStatus,
		Status: channel.StreamStatusStarted,
	}); err != nil {
		t.Fatalf("status push: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventAttachment,
		Attachments: []channel.PreparedAttachment{
			preparedSlackUploadAttachment("hello.txt", "text/plain", "hello"),
		},
	}); err != nil {
		t.Fatalf("attachment push: %v", err)
	}

	if postCalls != 1 {
		t.Fatalf("expected 1 placeholder post, got %d", postCalls)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected placeholder delete before attachment upload, got %d", deleteCalls)
	}
	if uploadCalls != 1 {
		t.Fatalf("expected 1 attachment upload, got %d", uploadCalls)
	}
	if stream.msgTS != "" {
		t.Fatalf("expected placeholder state to be cleared, got msgTS=%q", stream.msgTS)
	}
}

func TestSlackStreamFinalFallbackDeletesOldPlaceholder(t *testing.T) {
	t.Parallel()

	var postCalls, updateCalls, deleteCalls int
	client := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://slack.test/api/chat.postMessage":
				postCalls++
				ts := "1710000000.000400"
				if postCalls > 1 {
					ts = "1710000000.000401"
				}
				body, _ := json.Marshal(map[string]any{
					"ok":      true,
					"channel": "C123",
					"ts":      ts,
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://slack.test/api/chat.update":
				updateCalls++
				body, _ := json.Marshal(map[string]any{
					"ok":    false,
					"error": "cant_update_message",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://slack.test/api/chat.delete":
				deleteCalls++
				body, _ := json.Marshal(map[string]any{
					"ok":      true,
					"channel": "C123",
					"ts":      "1710000000.000400",
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
		})}),
		slack.OptionRetry(3),
	)

	stream := &slackOutboundStream{
		adapter: NewSlackAdapter(nil),
		cfg:     channel.ChannelConfig{ID: "cfg-stream-fallback"},
		target:  "C123",
		api:     client,
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:   channel.StreamEventStatus,
		Status: channel.StreamStatusStarted,
	}); err != nil {
		t.Fatalf("status push: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{
				Message: channel.Message{Text: "final answer"},
			},
		},
	}); err != nil {
		t.Fatalf("final push: %v", err)
	}

	if postCalls != 2 {
		t.Fatalf("expected 2 postMessage calls, got %d", postCalls)
	}
	if updateCalls != 1 {
		t.Fatalf("expected 1 update attempt, got %d", updateCalls)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected stale placeholder to be deleted once, got %d", deleteCalls)
	}
	if stream.msgTS != "1710000000.000401" {
		t.Fatalf("expected stream to track replacement message, got %q", stream.msgTS)
	}
}

func TestSlackStreamFinalUsesPartsRenderer(t *testing.T) {
	t.Parallel()

	var gotText string
	client := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://slack.test/api/chat.postMessage" {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			gotText = r.FormValue("text")
			body, _ := json.Marshal(map[string]any{"ok": true, "channel": "C123", "ts": "1710000000.000500"})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(string(body))),
			}, nil
		})}),
		slack.OptionRetry(3),
	)
	stream := &slackOutboundStream{
		adapter: NewSlackAdapter(nil),
		cfg:     channel.ChannelConfig{ID: "cfg-stream-rich"},
		target:  "C123",
		api:     client,
	}

	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{
				Message: channel.Message{
					Text:   "plain fallback",
					Format: channel.MessageFormatRich,
					Parts: []channel.MessagePart{
						{Type: channel.MessagePartText, Text: "Hello", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
						{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/a"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("rich final push: %v", err)
	}

	want := "*Hello*\n\n<https://example.test/a|docs>"
	if gotText != want {
		t.Fatalf("Slack stream rich text mismatch\n  got:  %q\n  want: %q", gotText, want)
	}
}

func TestSlackStreamFinalPreservesURLActionsOnUpdate(t *testing.T) {
	t.Parallel()

	var gotUpdateBlocks string
	client := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://slack.test/api/chat.postMessage":
				body, _ := json.Marshal(map[string]any{"ok": true, "channel": "C123", "ts": "1710000000.000600"})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			case "https://slack.test/api/chat.update":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm: %v", err)
				}
				gotUpdateBlocks = r.FormValue("blocks")
				body, _ := json.Marshal(map[string]any{"ok": true, "channel": "C123", "ts": "1710000000.000600"})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
		})}),
		slack.OptionRetry(3),
	)
	stream := &slackOutboundStream{
		adapter: NewSlackAdapter(nil),
		cfg:     channel.ChannelConfig{ID: "cfg-stream-actions"},
		target:  "C123",
		api:     client,
	}

	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:   channel.StreamEventStatus,
		Status: channel.StreamStatusStarted,
	}); err != nil {
		t.Fatalf("status push: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{
				Message: channel.Message{
					Text: "Read this",
					Actions: []channel.Action{
						{Label: "Open docs", URL: "https://example.test/docs"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("final push: %v", err)
	}

	if !strings.Contains(gotUpdateBlocks, `"type":"button"`) ||
		!strings.Contains(gotUpdateBlocks, `"text":"Open docs"`) ||
		!strings.Contains(gotUpdateBlocks, `"url":"https://example.test/docs"`) {
		t.Fatalf("expected Slack final update button blocks, got %s", gotUpdateBlocks)
	}
}

func preparedSlackUploadAttachment(name string, mime string, content string) channel.PreparedAttachment {
	return channel.PreparedAttachment{
		Logical: channel.Attachment{
			Type: channel.AttachmentFile,
			Name: name,
			Mime: mime,
			Size: int64(len(content)),
		},
		Kind: channel.PreparedAttachmentUpload,
		Name: name,
		Mime: mime,
		Size: int64(len(content)),
		Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(content)), nil
		},
	}
}

func TestSlackSendResolvesUserTargetToDMChannel(t *testing.T) {
	t.Parallel()

	var gotChannel string
	adapter := NewSlackAdapter(nil)
	adapter.apiFactory = func(cfg Config, options ...slack.Option) *slack.Client {
		return slack.New(
			cfg.BotToken,
			append(options,
				slack.OptionAPIURL("https://slack.test/api/"),
				slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != "https://slack.test/api/chat.postMessage" {
						return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
					}
					if err := r.ParseForm(); err != nil {
						t.Fatalf("ParseForm: %v", err)
					}
					gotChannel = r.FormValue("channel")
					body, _ := json.Marshal(map[string]any{"ok": true, "channel": gotChannel, "ts": "1710000000.000100"})
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(string(body))),
					}, nil
				})}),
			)...,
		)
	}
	adapter.openConversation = func(_ context.Context, _ *slack.Client, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
		if len(params.Users) != 1 || params.Users[0] != "U123" {
			t.Fatalf("unexpected users: %#v", params.Users)
		}
		return &slack.Channel{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "D456"}}}, false, false, nil
	}

	err := adapter.Send(context.Background(), channel.ChannelConfig{
		Credentials: map[string]any{
			"botToken": testShortBotToken,
			"appToken": testAppToken,
		},
	}, channel.PreparedOutboundMessage{
		Target: "U123",
		Message: channel.PreparedMessage{
			Message: channel.Message{Text: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotChannel != "D456" {
		t.Fatalf("expected postMessage channel D456, got %q", gotChannel)
	}
}

func TestSlackSendUsesPartsRenderer(t *testing.T) {
	t.Parallel()

	var gotText string
	adapter := NewSlackAdapter(nil)
	api := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://slack.test/api/chat.postMessage" {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			gotText = r.FormValue("text")
			body, _ := json.Marshal(map[string]any{"ok": true, "channel": "C123", "ts": "1710000000.000200"})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(string(body))),
			}, nil
		})}),
		slack.OptionRetry(3),
	)

	err := adapter.sendSlackMessage(context.Background(), api, "C123", channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Format: channel.MessageFormatRich,
				Parts: []channel.MessagePart{
					{Type: channel.MessagePartText, Text: "Hello", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
					{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/a"},
					{Type: channel.MessagePartMention, Text: "@alice", ChannelIdentityID: "U12345ABC"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("sendSlackMessage: %v", err)
	}
	want := "*Hello*\n\n<https://example.test/a|docs>\n\n<@U12345ABC>"
	if gotText != want {
		t.Fatalf("Slack rich text mismatch\n  got:  %q\n  want: %q", gotText, want)
	}
}

func TestSlackSendPlainFallbackDisablesMarkdownAndEscapesBlocks(t *testing.T) {
	t.Parallel()

	raw := "<!channel>" + strings.Repeat("&", slackMaxLength/4+1)
	var gotText string
	var gotMrkdwn string
	var gotBlocks string
	adapter := NewSlackAdapter(nil)
	api := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://slack.test/api/chat.postMessage" {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			gotText = r.FormValue("text")
			gotMrkdwn = r.FormValue("mrkdwn")
			gotBlocks = r.FormValue("blocks")
			body, _ := json.Marshal(map[string]any{"ok": true, "channel": "C123", "ts": "1710000000.000201"})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(string(body))),
			}, nil
		})}),
		slack.OptionRetry(3),
	)

	err := adapter.sendSlackMessage(context.Background(), api, "C123", channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Format: channel.MessageFormatRich,
				Parts: []channel.MessagePart{{
					Type: channel.MessagePartText,
					Text: raw,
				}},
				Actions: []channel.Action{
					{Label: "Open docs", URL: "https://example.test/docs"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("sendSlackMessage: %v", err)
	}
	if gotText != raw {
		t.Fatalf("expected raw top-level fallback text, got %q", gotText[:min(len(gotText), 32)])
	}
	if gotMrkdwn != "false" {
		t.Fatalf("expected Slack markdown disabled, got %q", gotMrkdwn)
	}
	var payload []map[string]any
	if err := json.Unmarshal([]byte(gotBlocks), &payload); err != nil {
		t.Fatalf("decode blocks: %v (body=%s)", err, gotBlocks)
	}
	sectionText := payload[0]["text"].(map[string]any)["text"].(string)
	if strings.Contains(sectionText, "<!channel>") {
		t.Fatalf("block section must not contain raw Slack control tag: %q", sectionText[:min(len(sectionText), 32)])
	}
	if !strings.Contains(sectionText, "&lt;!channel&gt;") {
		t.Fatalf("expected escaped Slack control tag in block section, got %q", sectionText[:min(len(sectionText), 64)])
	}
}

func TestSlackSendRendersURLActionsAsButtons(t *testing.T) {
	t.Parallel()

	var gotBlocks string
	adapter := NewSlackAdapter(nil)
	api := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://slack.test/api/chat.postMessage" {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			gotBlocks = r.FormValue("blocks")
			body, _ := json.Marshal(map[string]any{"ok": true, "channel": "C123", "ts": "1710000000.000300"})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(string(body))),
			}, nil
		})}),
		slack.OptionRetry(3),
	)

	err := adapter.sendSlackMessage(context.Background(), api, "C123", channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "Read this",
				Actions: []channel.Action{
					{Label: "Open docs", URL: "https://example.test/docs"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("sendSlackMessage: %v", err)
	}
	if !strings.Contains(gotBlocks, `"type":"button"`) ||
		!strings.Contains(gotBlocks, `"text":"Open docs"`) ||
		!strings.Contains(gotBlocks, `"url":"https://example.test/docs"`) {
		t.Fatalf("expected Slack button block, got %s", gotBlocks)
	}
}

func TestSlackSendRendersURLActionsWithoutText(t *testing.T) {
	t.Parallel()

	var gotText string
	var gotBlocks string
	adapter := NewSlackAdapter(nil)
	api := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://slack.test/api/chat.postMessage" {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			gotText = r.FormValue("text")
			gotBlocks = r.FormValue("blocks")
			body, _ := json.Marshal(map[string]any{"ok": true, "channel": "C123", "ts": "1710000000.000300"})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(string(body))),
			}, nil
		})}),
		slack.OptionRetry(3),
	)

	err := adapter.sendSlackMessage(context.Background(), api, "C123", channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Actions: []channel.Action{
					{Label: "Open docs", URL: "https://example.test/docs"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("sendSlackMessage: %v", err)
	}
	if gotText != "Open docs" {
		t.Fatalf("expected action label fallback text, got %q", gotText)
	}
	if !strings.Contains(gotBlocks, `"type":"button"`) ||
		!strings.Contains(gotBlocks, `"text":"Open docs"`) ||
		!strings.Contains(gotBlocks, `"url":"https://example.test/docs"`) {
		t.Fatalf("expected Slack button block, got %s", gotBlocks)
	}
}

func TestSlackURLActionBlocksSplitAtPlatformElementLimit(t *testing.T) {
	t.Parallel()

	actions := make([]channel.Action, 26)
	for i := range actions {
		actions[i] = channel.Action{
			Label: "Open docs",
			URL:   "https://example.test/docs",
		}
	}

	blocks, err := slackURLActionBlocks("", actions)
	if err != nil {
		t.Fatalf("slackURLActionBlocks: %v", err)
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	var payload []map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode blocks: %v (body=%s)", err, raw)
	}
	if len(payload) != 2 {
		t.Fatalf("expected 2 action blocks, got %d: %s", len(payload), raw)
	}
	for i, want := range []int{25, 1} {
		elements, ok := payload[i]["elements"].([]any)
		if !ok {
			t.Fatalf("block %d missing elements: %#v", i, payload[i])
		}
		if len(elements) != want {
			t.Fatalf("block %d elements = %d, want %d", i, len(elements), want)
		}
	}
}

func TestSlackURLActionBlocksRejectsPlatformBlockOverflow(t *testing.T) {
	t.Parallel()

	actions := make([]channel.Action, slackMaxActionBlockElements+1)
	for i := range actions {
		actions[i] = channel.Action{
			Label: "Open docs",
			URL:   "https://example.test/docs",
		}
	}

	_, err := slackURLActionBlocks(strings.Repeat("b", slackMaxSectionText*49), actions)
	if err == nil || !strings.Contains(err.Error(), "50 blocks") {
		t.Fatalf("expected Slack block limit error, got %v", err)
	}
}

func TestSlackPreparedOutboundValidationRejectsPlatformBlockOverflow(t *testing.T) {
	t.Parallel()

	actions := make([]channel.Action, slackMaxActionBlockElements*slackMaxMessageBlocks+1)
	for i := range actions {
		actions[i] = channel.Action{
			Label: "Open docs",
			URL:   "https://example.test/docs",
		}
	}

	err := NewSlackAdapter(nil).ValidatePreparedOutbound(context.Background(), channel.ChannelConfig{}, "C123", channel.PreparedOutboundMessage{
		Target: "C123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Actions: actions,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "50 blocks") {
		t.Fatalf("expected Slack block preflight error, got %v", err)
	}
}

func TestSlackURLActionBlocksEnforcesFieldLimits(t *testing.T) {
	t.Parallel()

	const (
		maxSlackSectionTextRunes = 3000
		maxSlackButtonTextRunes  = 75
		maxSlackButtonURLRunes   = 3000
	)

	blocks, err := slackURLActionBlocks(strings.Repeat("b", maxSlackSectionTextRunes+1), []channel.Action{{
		Label: strings.Repeat("L", maxSlackButtonTextRunes+1),
		URL:   "https://example.test/docs",
	}})
	if err != nil {
		t.Fatalf("slackURLActionBlocks returned error for long text/label: %v", err)
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	var payload []map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode blocks: %v (body=%s)", err, raw)
	}
	sectionText := payload[0]["text"].(map[string]any)["text"].(string)
	if got := len([]rune(sectionText)); got != maxSlackSectionTextRunes {
		t.Fatalf("section text runes = %d, want %d", got, maxSlackSectionTextRunes)
	}
	buttonText := payload[len(payload)-1]["elements"].([]any)[0].(map[string]any)["text"].(map[string]any)["text"].(string)
	if got := len([]rune(buttonText)); got != maxSlackButtonTextRunes {
		t.Fatalf("button text runes = %d, want %d", got, maxSlackButtonTextRunes)
	}

	_, err = slackURLActionBlocks("", []channel.Action{{
		Label: "Open",
		URL:   "https://example.test/" + strings.Repeat("x", maxSlackButtonURLRunes),
	}})
	if err == nil || !strings.Contains(err.Error(), "url must be at most") {
		t.Fatalf("expected URL length error, got %v", err)
	}
}

func TestSlackURLActionBlocksSplitsLongTextIntoVisibleSections(t *testing.T) {
	t.Parallel()

	blocks, err := slackURLActionBlocks(strings.Repeat("b", slackMaxSectionText+1), []channel.Action{{
		Label: "Open",
		URL:   "https://example.test/docs",
	}})
	if err != nil {
		t.Fatalf("slackURLActionBlocks returned error: %v", err)
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	var payload []map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode blocks: %v (body=%s)", err, raw)
	}
	if len(payload) != 3 {
		t.Fatalf("expected two section blocks plus actions, got %d: %s", len(payload), raw)
	}
	first := payload[0]["text"].(map[string]any)["text"].(string)
	second := payload[1]["text"].(map[string]any)["text"].(string)
	if len([]rune(first)) != slackMaxSectionText || len([]rune(second)) != 1 {
		t.Fatalf("unexpected section lengths: first=%d second=%d", len([]rune(first)), len([]rune(second)))
	}
	if payload[2]["type"] != "actions" {
		t.Fatalf("last block should contain actions, got %#v", payload[2])
	}
}

func TestSlackOpenStreamResolvesUserTargetToDMChannel(t *testing.T) {
	t.Parallel()

	adapter := NewSlackAdapter(nil)
	adapter.openConversation = func(_ context.Context, _ *slack.Client, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
		if len(params.Users) != 1 || params.Users[0] != "U123" {
			t.Fatalf("unexpected users: %#v", params.Users)
		}
		return &slack.Channel{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "D999"}}}, false, false, nil
	}

	stream, err := adapter.OpenStream(context.Background(), channel.ChannelConfig{
		Credentials: map[string]any{
			"botToken": testShortBotToken,
			"appToken": testAppToken,
		},
	}, "U123", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	slackStream, ok := stream.(*slackOutboundStream)
	if !ok {
		t.Fatalf("unexpected stream type %T", stream)
	}
	if slackStream.target != "D999" {
		t.Fatalf("expected resolved DM target, got %q", slackStream.target)
	}
}

func TestSlackReactConvertsSkinToneEmojiToSlackName(t *testing.T) {
	t.Parallel()

	var gotName string
	adapter := NewSlackAdapter(nil)
	adapter.apiFactory = func(cfg Config, options ...slack.Option) *slack.Client {
		return slack.New(
			cfg.BotToken,
			append(options,
				slack.OptionAPIURL("https://slack.test/api/"),
				slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != "https://slack.test/api/reactions.add" {
						return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
					}
					if err := r.ParseForm(); err != nil {
						t.Fatalf("ParseForm: %v", err)
					}
					gotName = r.FormValue("name")
					body, _ := json.Marshal(map[string]any{"ok": true})
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(string(body))),
					}, nil
				})}),
			)...,
		)
	}

	err := adapter.React(context.Background(), channel.ChannelConfig{
		Credentials: map[string]any{
			"botToken": testShortBotToken,
			"appToken": testAppToken,
		},
	}, "C123", "1710000000.000100", "👍🏽")
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if gotName != "+1::skin-tone-4" {
		t.Fatalf("expected skin tone slack reaction name, got %q", gotName)
	}
}

func TestSlackUnreactConvertsSkinToneEmojiToSlackName(t *testing.T) {
	t.Parallel()

	var gotName string
	adapter := NewSlackAdapter(nil)
	adapter.apiFactory = func(cfg Config, options ...slack.Option) *slack.Client {
		return slack.New(
			cfg.BotToken,
			append(options,
				slack.OptionAPIURL("https://slack.test/api/"),
				slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != "https://slack.test/api/reactions.remove" {
						return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
					}
					if err := r.ParseForm(); err != nil {
						t.Fatalf("ParseForm: %v", err)
					}
					gotName = r.FormValue("name")
					body, _ := json.Marshal(map[string]any{"ok": true})
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(string(body))),
					}, nil
				})}),
			)...,
		)
	}

	err := adapter.Unreact(context.Background(), channel.ChannelConfig{
		Credentials: map[string]any{
			"botToken": testShortBotToken,
			"appToken": testAppToken,
		},
	}, "C123", "1710000000.000100", "👍🏽")
	if err != nil {
		t.Fatalf("Unreact: %v", err)
	}
	if gotName != "+1::skin-tone-4" {
		t.Fatalf("expected skin tone slack reaction name, got %q", gotName)
	}
}

func TestSlackResolveUserDisplayNameScopesCacheByConfig(t *testing.T) {
	t.Parallel()

	newClient := func(apiURL, displayName string, calls *int) *slack.Client {
		return slack.New(
			testBotToken,
			slack.OptionAPIURL(apiURL),
			slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(r.URL.String(), "/users.info") {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader("not found")),
					}, nil
				}
				*calls++
				body, _ := json.Marshal(map[string]any{
					"ok": true,
					"user": map[string]any{
						"id":   "U123",
						"name": strings.ToLower(displayName),
						"profile": map[string]any{
							"display_name": displayName,
						},
					},
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(body))),
				}, nil
			})}),
		)
	}

	var callsA, callsB int
	adapter := NewSlackAdapter(nil)
	apiA := newClient("https://slack-a.test/api/", "Alice A", &callsA)
	apiB := newClient("https://slack-b.test/api/", "Alice B", &callsB)

	if got := adapter.resolveUserDisplayName(apiA, "cfg-a", "U123"); got != "Alice A" {
		t.Fatalf("cfg-a first lookup = %q", got)
	}
	if got := adapter.resolveUserDisplayName(apiB, "cfg-b", "U123"); got != "Alice B" {
		t.Fatalf("cfg-b first lookup = %q", got)
	}
	if got := adapter.resolveUserDisplayName(apiA, "cfg-a", "U123"); got != "Alice A" {
		t.Fatalf("cfg-a cached lookup = %q", got)
	}

	if callsA != 1 {
		t.Fatalf("expected cfg-a to fetch once, got %d", callsA)
	}
	if callsB != 1 {
		t.Fatalf("expected cfg-b to fetch once, got %d", callsB)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
