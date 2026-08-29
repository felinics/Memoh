package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestIsMatrixBotMentionedByMentionsMetadata(t *testing.T) {
	content := map[string]any{
		"body": "hi bot",
		"m.mentions": map[string]any{
			"user_ids": []any{"@memoh:example.com"},
		},
	}
	if !isMatrixBotMentioned("@memoh:example.com", content) {
		t.Fatal("expected mention metadata to be detected")
	}
}

func TestIsMatrixBotMentionedByFormattedBody(t *testing.T) {
	content := map[string]any{
		"body":           "hello Memoh",
		"formatted_body": `<a href="https://matrix.to/#/@memoh:example.com">Memoh</a> hello`,
	}
	if !isMatrixBotMentioned("@memoh:example.com", content) {
		t.Fatal("expected formatted body mention to be detected")
	}
}

func TestIsMatrixBotMentionedByBodyFallback(t *testing.T) {
	content := map[string]any{
		"body": "@memoh:example.com ping",
	}
	if !isMatrixBotMentioned("@memoh:example.com", content) {
		t.Fatal("expected body fallback mention to be detected")
	}
}

func TestIsMatrixBotMentionedByLocalpartBodyFallback(t *testing.T) {
	content := map[string]any{
		"body": "@memoh ping",
	}
	if !isMatrixBotMentioned("@memoh:example.com", content) {
		t.Fatal("expected localpart body fallback mention to be detected")
	}
}

func TestMatrixBotMentionAliasesIncludeFullIDAndLocalpart(t *testing.T) {
	aliases := matrixBotMentionAliases("@memoh:example.com")
	if len(aliases) != 2 || aliases[0] != "@memoh:example.com" || aliases[1] != "@memoh" {
		t.Fatalf("aliases = %#v, want full Matrix ID and localpart", aliases)
	}
}

func TestIsMatrixBotMentionedDoesNotMatchSubstring(t *testing.T) {
	content := map[string]any{
		"body": "@memoh-helper:example.com ping",
	}
	if isMatrixBotMentioned("@memoh:example.com", content) {
		t.Fatal("expected substring match not to count as mention")
	}
}

func TestIsMatrixBotMentionedDoesNotMatchPlainMatrixURL(t *testing.T) {
	content := map[string]any{
		"body": "see https://matrix.to/#/@memoh:example.com",
	}
	if isMatrixBotMentioned("@memoh:example.com", content) {
		t.Fatal("expected plain Matrix URL not to count as mention")
	}
}

func TestMatrixSinceTokenFromRouting(t *testing.T) {
	routing := map[string]any{
		matrixRoutingStateKey: map[string]any{"since_token": "s123"},
	}
	if got := matrixSinceTokenFromRouting(routing); got != "s123" {
		t.Fatalf("unexpected since token: %q", got)
	}
}

func TestPersistSinceTokenUsesConfiguredSaver(t *testing.T) {
	var gotConfigID string
	var gotSince string
	adapter := NewMatrixAdapter(nil)
	adapter.SetSyncStateSaver(func(_ context.Context, configID string, since string) error {
		gotConfigID = configID
		gotSince = since
		return nil
	})
	if err := adapter.persistSinceToken(context.Background(), "cfg-1", "token-1"); err != nil {
		t.Fatalf("persistSinceToken returned error: %v", err)
	}
	if gotConfigID != "cfg-1" || gotSince != "token-1" {
		t.Fatalf("unexpected saver args: %q %q", gotConfigID, gotSince)
	}
}

func TestBootstrapSinceTokenPersistsLatestCursor(t *testing.T) {
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"next_batch":"s123","rooms":{"join":{"!room:example.com":{"timeline":{"events":[{"event_id":"$evt1"}]}}}}}`)),
			Header:     make(http.Header),
		}, nil
	})}
	var gotConfigID string
	var gotSince string
	adapter.SetSyncStateSaver(func(_ context.Context, configID string, since string) error {
		gotConfigID = configID
		gotSince = since
		return nil
	})

	since, err := adapter.bootstrapSinceToken(context.Background(), channel.ChannelConfig{ID: "cfg-1"}, Config{
		HomeserverURL:   "https://matrix.example.com",
		AccessToken:     "tok",
		AutoJoinInvites: true,
	})
	if err != nil {
		t.Fatalf("bootstrapSinceToken returned error: %v", err)
	}
	if since != "s123" {
		t.Fatalf("unexpected since token: %q", since)
	}
	if gotConfigID != "cfg-1" || gotSince != "s123" {
		t.Fatalf("unexpected persisted cursor: %q %q", gotConfigID, gotSince)
	}
	if !adapter.seenEvent("cfg-1", "$evt1") {
		t.Fatal("expected bootstrap event to be remembered as seen")
	}
}

func TestBootstrapSinceTokenAutoJoinsInvitedRooms(t *testing.T) {
	joinRequests := 0
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/_matrix/client/v3/sync":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"next_batch":"s123","rooms":{"invite":{"!room:example.com":{"invite_state":{"events":[{"type":"m.room.member"}]}}}}}`)),
				Header:     make(http.Header),
			}, nil
		case "/_matrix/client/v3/join/!room:example.com":
			joinRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})}

	since, err := adapter.bootstrapSinceToken(context.Background(), channel.ChannelConfig{ID: "cfg-1"}, Config{
		HomeserverURL:   "https://matrix.example.com",
		AccessToken:     "tok",
		AutoJoinInvites: true,
	})
	if err != nil {
		t.Fatalf("bootstrapSinceToken returned error: %v", err)
	}
	if since != "s123" {
		t.Fatalf("unexpected since token: %q", since)
	}
	if joinRequests != 1 {
		t.Fatalf("expected invited room to be auto-joined once, got %d", joinRequests)
	}
}

func TestValidateConnectionChecksHomeserverVersions(t *testing.T) {
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/_matrix/client/versions" {
			t.Fatalf("unexpected request path: %s", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("not found")),
			Header:     make(http.Header),
		}, nil
	})}

	err := adapter.validateConnection(context.Background(), Config{
		HomeserverURL: "https://matrix.example.com",
		AccessToken:   "tok",
		UserID:        "@memoh:example.com",
	})
	if err == nil {
		t.Fatal("expected homeserver validation to fail")
	}
	if !strings.Contains(err.Error(), "homeserver check failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConnectionRejectsTokenUserMismatch(t *testing.T) {
	requests := make([]string, 0, 2)
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.Path)
		switch req.URL.Path {
		case "/_matrix/client/versions":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"versions":["v1.11"]}`)),
				Header:     make(http.Header),
			}, nil
		case "/_matrix/client/v3/account/whoami":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"user_id":"@alice:example.com"}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})}

	err := adapter.validateConnection(context.Background(), Config{
		HomeserverURL: "https://matrix.example.com",
		AccessToken:   "tok",
		UserID:        "@memoh:example.com",
	})
	if err == nil {
		t.Fatal("expected token mismatch validation to fail")
	}
	if !strings.Contains(err.Error(), "token belongs to @alice:example.com, expected @memoh:example.com") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected homeserver and whoami checks, got %d requests", len(requests))
	}
}

func TestValidateConnectionSkipsSyncProbe(t *testing.T) {
	requests := make([]string, 0, 3)
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.RequestURI())
		switch req.URL.Path {
		case "/_matrix/client/versions":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"versions":["v1.11"]}`)),
				Header:     make(http.Header),
			}, nil
		case "/_matrix/client/v3/account/whoami":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"user_id":"@memoh:example.com"}`)),
				Header:     make(http.Header),
			}, nil
		case "/_matrix/client/v3/sync":
			t.Fatal("did not expect /sync probe during connection validation")
			return nil, nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})}

	err := adapter.validateConnection(context.Background(), Config{
		HomeserverURL: "https://matrix.example.com",
		AccessToken:   "tok",
		UserID:        "@memoh:example.com",
	})
	if err != nil {
		t.Fatalf("validateConnection returned error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected homeserver and whoami checks only, got %d requests", len(requests))
	}
}

func TestHandleInvitesSkipsWhenAutoJoinDisabled(t *testing.T) {
	joinRequests := 0
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/_matrix/client/v3/join/!room:example.com" {
			joinRequests++
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})}

	joined, err := adapter.handleInvites(
		context.Background(),
		channel.ChannelConfig{ID: "cfg-1"},
		Config{HomeserverURL: "https://matrix.example.com", AccessToken: "tok", AutoJoinInvites: false},
		matrixSyncResponse{Rooms: struct {
			Join   map[string]matrixSyncJoinedRoom  `json:"join"`
			Invite map[string]matrixSyncInvitedRoom `json:"invite"`
		}{Invite: map[string]matrixSyncInvitedRoom{"!room:example.com": {}}}},
	)
	if err != nil {
		t.Fatalf("handleInvites returned error: %v", err)
	}
	if joined {
		t.Fatal("expected no room to be joined")
	}
	if joinRequests != 0 {
		t.Fatalf("expected no join requests, got %d", joinRequests)
	}
}

func TestBuildMatrixMessageContentIncludesFormattedHTMLForMarkdown(t *testing.T) {
	content := buildMatrixMessageContent(channel.Message{
		Text:   "**bold**\n\n- item",
		Format: channel.MessageFormatMarkdown,
	}, false, "")

	if got := content["body"]; got != "**bold**\n\n- item" {
		t.Fatalf("unexpected body: %#v", got)
	}
	if got := content["format"]; got != matrixHTMLFormat {
		t.Fatalf("unexpected format: %#v", got)
	}
	html, ok := content["formatted_body"].(string)
	if !ok || !strings.Contains(html, "<strong>bold</strong>") || !strings.Contains(html, "<ul>") {
		t.Fatalf("unexpected formatted body: %#v", content["formatted_body"])
	}
}

func TestMatrixDescriptorAdvertisesRichText(t *testing.T) {
	t.Parallel()

	caps := NewMatrixAdapter(nil).Descriptor().Capabilities
	if !caps.RichText {
		t.Fatal("Matrix descriptor must advertise RichText for direct Parts rendering")
	}
}

func TestBuildMatrixMessageContentRendersPartsDirectly(t *testing.T) {
	content := buildMatrixMessageContent(channel.Message{
		Format: channel.MessageFormatRich,
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartText, Text: "ping", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
			{Type: channel.MessagePartMention, Text: "Alice", ChannelIdentityID: "@alice:example.com"},
			{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/a?b=1&c=2"},
			{Type: channel.MessagePartText, Text: `<script>alert(1)</script>`},
		},
	}, false, "")

	if got := content["format"]; got != matrixHTMLFormat {
		t.Fatalf("unexpected format: %#v", got)
	}
	html, ok := content["formatted_body"].(string)
	if !ok {
		t.Fatalf("unexpected formatted body: %#v", content["formatted_body"])
	}
	for _, want := range []string{
		"<strong>ping</strong>",
		`<a href="https://matrix.to/#/@alice:example.com">Alice</a>`,
		`<a href="https://example.test/a?b=1&amp;c=2">docs</a>`,
		`&lt;script&gt;alert(1)&lt;/script&gt;`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected formatted body to contain %q, got %q", want, html)
		}
	}
	if strings.Contains(html, "<script>") {
		t.Fatalf("formatted body contains unescaped script tag: %q", html)
	}
}

func TestBuildMatrixMessageContentAddsFormattedHTMLToEdits(t *testing.T) {
	content := buildMatrixMessageContent(channel.Message{
		Text:   "`code`",
		Format: channel.MessageFormatMarkdown,
	}, true, "$evt1")

	newContent, ok := content["m.new_content"].(map[string]any)
	if !ok {
		t.Fatalf("expected m.new_content map, got %#v", content["m.new_content"])
	}
	if got := newContent["format"]; got != matrixHTMLFormat {
		t.Fatalf("unexpected edit format: %#v", got)
	}
	html, ok := newContent["formatted_body"].(string)
	if !ok || !strings.Contains(html, "<code>code</code>") {
		t.Fatalf("unexpected edit formatted body: %#v", newContent["formatted_body"])
	}
}

func TestStripMatrixReplyFallback(t *testing.T) {
	body := "> <@memoh:example.com> This looks like Antelope Canyon\n>\nWhere is Antelope Canyon?"
	if got := stripMatrixReplyFallback(body); got != "Where is Antelope Canyon?" {
		t.Fatalf("unexpected stripped body: %q", got)
	}
}

func TestMatrixHandleEventExpandsRepliedImageContext(t *testing.T) {
	adapter := NewMatrixAdapter(nil)
	adapter.rememberRoomConversationType("cfg-1", "!room:example.com", "group")
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.URL.Path, "/rooms/!room:example.com/event/$img1") {
			t.Fatalf("unexpected request path: %s", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"event_id":"$img1",
				"type":"m.room.message",
				"sender":"@memoh:example.com",
				"unsigned":{"displayname":"Memoh"},
				"content":{
					"msgtype":"m.image",
					"body":"canyon.jpg",
					"url":"mxc://matrix.example.com/media123",
					"info":{"mimetype":"image/jpeg","w":640,"h":480}
				}
			}`)),
			Header: make(http.Header),
		}, nil
	})}

	var captured channel.InboundMessage
	delivered, err := adapter.handleEvent(
		context.Background(),
		channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1"},
		Config{HomeserverURL: "https://matrix.example.com", AccessToken: "tok", UserID: "@memoh:example.com"},
		matrixEvent{
			EventID: "$evt2",
			Type:    "m.room.message",
			Sender:  "@alex:example.com",
			RoomID:  "!room:example.com",
			Content: map[string]any{
				"msgtype": "m.text",
				"body":    "> <@memoh:example.com> photo\n>\nWhere is Antelope Canyon?",
				"m.relates_to": map[string]any{
					"m.in_reply_to": map[string]any{"event_id": "$img1"},
				},
			},
		},
		func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
			captured = msg
			return nil
		},
	)
	if err != nil {
		t.Fatalf("handleEvent returned error: %v", err)
	}
	if !delivered {
		t.Fatal("expected event to be delivered")
	}
	if got := captured.Message.Text; got != "Where is Antelope Canyon?" {
		t.Fatalf("unexpected message text: %q", got)
	}
	if captured.Message.Reply == nil {
		t.Fatal("expected ReplyRef to be set")
	}
	if captured.Message.Reply.Sender != "Memoh" {
		t.Fatalf("unexpected reply sender: %q", captured.Message.Reply.Sender)
	}
	if len(captured.Message.Attachments) != 0 {
		t.Fatalf("expected no current attachments, got %d", len(captured.Message.Attachments))
	}
	if len(captured.Message.Reply.Attachments) != 1 {
		t.Fatalf("expected one quoted attachment, got %d", len(captured.Message.Reply.Attachments))
	}
	if captured.Message.Reply.Attachments[0].PlatformKey != "mxc://matrix.example.com/media123" {
		t.Fatalf("unexpected quoted attachment: %#v", captured.Message.Reply.Attachments[0])
	}
	isReplyToBot, _ := captured.Metadata["is_reply_to_bot"].(bool)
	if !isReplyToBot {
		t.Fatalf("expected is_reply_to_bot metadata to be true")
	}
}

func TestMatrixHandleEventUsesImageCaptionAsMessageText(t *testing.T) {
	adapter := NewMatrixAdapter(nil)
	adapter.rememberRoomConversationType("cfg-1", "!room:example.com", "group")

	var captured channel.InboundMessage
	delivered, err := adapter.handleEvent(
		context.Background(),
		channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1"},
		Config{HomeserverURL: "https://matrix.example.com", AccessToken: "tok", UserID: "@memoh:example.com"},
		matrixEvent{
			EventID: "$evt2",
			Type:    "m.room.message",
			Sender:  "@alex:example.com",
			RoomID:  "!room:example.com",
			Content: map[string]any{
				"msgtype":  "m.image",
				"body":     "A hand-drawn system architecture diagram",
				"filename": "diagram.png",
				"url":      "mxc://matrix.example.com/media123",
				"info": map[string]any{
					"mimetype": "image/png",
				},
			},
		},
		func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
			captured = msg
			return nil
		},
	)
	if err != nil {
		t.Fatalf("handleEvent returned error: %v", err)
	}
	if !delivered {
		t.Fatal("expected event to be delivered")
	}
	if got := captured.Message.Text; got != "A hand-drawn system architecture diagram" {
		t.Fatalf("unexpected message text: %q", got)
	}
	if len(captured.Message.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %d", len(captured.Message.Attachments))
	}
	att := captured.Message.Attachments[0]
	if att.Name != "diagram.png" || att.Caption != "A hand-drawn system architecture diagram" {
		t.Fatalf("unexpected attachment metadata: %#v", att)
	}
	if rawText, _ := captured.Metadata["raw_text"].(string); rawText != "A hand-drawn system architecture diagram" {
		t.Fatalf("unexpected raw_text metadata: %q", rawText)
	}
	if alias, _ := captured.Metadata["bot_alias"].(string); alias != "@memoh:example.com" {
		t.Fatalf("bot_alias = %q, want Matrix bot user id", alias)
	}
	aliases, _ := captured.Metadata["bot_aliases"].([]string)
	if len(aliases) != 2 || aliases[0] != "@memoh:example.com" || aliases[1] != "@memoh" {
		t.Fatalf("bot_aliases = %#v, want full Matrix ID and localpart", aliases)
	}
}

func TestMatrixHandleEventMarksDirectConversationFromJoinedMembers(t *testing.T) {
	joinedMembersRequests := 0
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/_matrix/client/v3/rooms/!room:example.com/joined_members":
			joinedMembersRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"joined": {
						"@alex:example.com": {"display_name": "Alex"},
						"@memoh:example.com": {"display_name": "Memoh"}
					}
				}`)),
				Header: make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})}

	var captured []channel.InboundMessage
	for i := 0; i < 2; i++ {
		delivered, err := adapter.handleEvent(
			context.Background(),
			channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1"},
			Config{HomeserverURL: "https://matrix.example.com", AccessToken: "tok", UserID: "@memoh:example.com"},
			matrixEvent{
				EventID: fmt.Sprintf("$evt%d", i+1),
				Type:    "m.room.message",
				Sender:  "@alex:example.com",
				RoomID:  "!room:example.com",
				Content: map[string]any{
					"msgtype": "m.text",
					"body":    "ping",
				},
			},
			func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
				captured = append(captured, msg)
				return nil
			},
		)
		if err != nil {
			t.Fatalf("handleEvent returned error: %v", err)
		}
		if !delivered {
			t.Fatal("expected event to be delivered")
		}
	}

	if len(captured) != 2 {
		t.Fatalf("expected two captured messages, got %d", len(captured))
	}
	if captured[0].Conversation.Type != "direct" {
		t.Fatalf("expected direct conversation type, got %q", captured[0].Conversation.Type)
	}
	if joinedMembersRequests != 1 {
		t.Fatalf("expected joined_members lookup to be cached, got %d requests", joinedMembersRequests)
	}
}

func TestMatrixSyncOnceAutoJoinsInvitedRooms(t *testing.T) {
	joinRequests := 0
	adapter := NewMatrixAdapter(nil)
	adapter.rememberRoomConversationType("cfg-1", "!joined:example.com", "group")
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/_matrix/client/v3/sync":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"next_batch":"s124",
					"rooms":{
						"invite":{"!invite:example.com":{"invite_state":{"events":[{"type":"m.room.member"}]}}},
						"join":{"!joined:example.com":{"timeline":{"events":[{"event_id":"$evt1","type":"m.room.message","sender":"@alex:example.com","content":{"msgtype":"m.text","body":"ping"}}]}}}
					}
				}`)),
				Header: make(http.Header),
			}, nil
		case "/_matrix/client/v3/join/!invite:example.com":
			joinRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})}

	var captured channel.InboundMessage
	nextSince, healthy, err := adapter.syncOnce(
		context.Background(),
		channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1"},
		Config{HomeserverURL: "https://matrix.example.com", AccessToken: "tok", UserID: "@memoh:example.com", SyncTimeoutSeconds: 30, AutoJoinInvites: true},
		"s123",
		func(_ context.Context, _ channel.ChannelConfig, msg channel.InboundMessage) error {
			captured = msg
			return nil
		},
	)
	if err != nil {
		t.Fatalf("syncOnce returned error: %v", err)
	}
	if nextSince != "s124" {
		t.Fatalf("unexpected next since token: %q", nextSince)
	}
	if !healthy {
		t.Fatal("expected sync session to be marked healthy")
	}
	if joinRequests != 1 {
		t.Fatalf("expected invited room to be auto-joined once, got %d", joinRequests)
	}
	if captured.ReplyTarget != "!joined:example.com" || captured.Message.Text != "ping" {
		t.Fatalf("unexpected captured message: %#v", captured)
	}
}

func TestExtractMatrixDirectRoomIDs(t *testing.T) {
	roomIDs := extractMatrixDirectRoomIDs(matrixSyncResponse{
		AccountData: struct {
			Events []matrixSyncEvent `json:"events"`
		}{
			Events: []matrixSyncEvent{{
				Type: "m.direct",
				Content: map[string]any{
					"@alice:example.com": []any{"!dm:example.com", " !dm2:example.com "},
				},
			}},
		},
	})

	if _, ok := roomIDs["!dm:example.com"]; !ok {
		t.Fatal("expected first direct room id to be extracted")
	}
	if _, ok := roomIDs["!dm2:example.com"]; !ok {
		t.Fatal("expected second direct room id to be extracted")
	}
}

func TestExtractMatrixDirectRooms(t *testing.T) {
	directRooms := extractMatrixDirectRooms(matrixSyncResponse{
		AccountData: struct {
			Events []matrixSyncEvent `json:"events"`
		}{
			Events: []matrixSyncEvent{{
				Type: "m.direct",
				Content: map[string]any{
					"@alice:example.com": []any{"!dm:example.com", "!ignored:example.com"},
					"@bob:example.com":   []any{" !bob:example.com "},
				},
			}},
		},
	})

	if got := directRooms["@alice:example.com"]; got != "!dm:example.com" {
		t.Fatalf("unexpected Alice direct room: %q", got)
	}
	if got := directRooms["@bob:example.com"]; got != "!bob:example.com" {
		t.Fatalf("unexpected Bob direct room: %q", got)
	}
}

func TestEnsureDirectRoomReusesExistingRoom(t *testing.T) {
	joinedRoomsRequests := 0
	joinedMembersRequests := 0
	createRoomRequests := 0
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/_matrix/client/v3/joined_rooms":
			joinedRoomsRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"joined_rooms":["!dm:example.com"]}`)),
				Header:     make(http.Header),
			}, nil
		case "/_matrix/client/v3/rooms/!dm:example.com/joined_members":
			joinedMembersRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"joined":{"@memoh:example.com":{},"@alice:example.com":{}}}`)),
				Header:     make(http.Header),
			}, nil
		case "/_matrix/client/v3/createRoom":
			createRoomRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"room_id":"!new:example.com"}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})}

	cfg := Config{
		HomeserverURL: "https://matrix.example.com",
		AccessToken:   "tok",
		UserID:        "@memoh:example.com",
	}

	roomID, err := adapter.ensureDirectRoom(context.Background(), cfg, "@alice:example.com")
	if err != nil {
		t.Fatalf("ensureDirectRoom returned error: %v", err)
	}
	if roomID != "!dm:example.com" {
		t.Fatalf("unexpected room id: %q", roomID)
	}
	roomID, err = adapter.ensureDirectRoom(context.Background(), cfg, "@alice:example.com")
	if err != nil {
		t.Fatalf("ensureDirectRoom second call returned error: %v", err)
	}
	if roomID != "!dm:example.com" {
		t.Fatalf("unexpected cached room id: %q", roomID)
	}
	if joinedRoomsRequests != 1 {
		t.Fatalf("expected joined room lookup once, got %d", joinedRoomsRequests)
	}
	if joinedMembersRequests != 1 {
		t.Fatalf("expected joined members lookup once, got %d", joinedMembersRequests)
	}
	if createRoomRequests != 0 {
		t.Fatalf("expected no createRoom requests, got %d", createRoomRequests)
	}
}

func TestEnsureDirectRoomCachesCreatedRoom(t *testing.T) {
	joinedRoomsRequests := 0
	createRoomRequests := 0
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/_matrix/client/v3/joined_rooms":
			joinedRoomsRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"joined_rooms":[]}`)),
				Header:     make(http.Header),
			}, nil
		case "/_matrix/client/v3/createRoom":
			createRoomRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"room_id":"!new:example.com"}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})}

	cfg := Config{
		HomeserverURL: "https://matrix.example.com",
		AccessToken:   "tok",
		UserID:        "@memoh:example.com",
	}

	roomID, err := adapter.ensureDirectRoom(context.Background(), cfg, "@alice:example.com")
	if err != nil {
		t.Fatalf("ensureDirectRoom returned error: %v", err)
	}
	if roomID != "!new:example.com" {
		t.Fatalf("unexpected room id: %q", roomID)
	}
	roomID, err = adapter.ensureDirectRoom(context.Background(), cfg, "@alice:example.com")
	if err != nil {
		t.Fatalf("ensureDirectRoom second call returned error: %v", err)
	}
	if roomID != "!new:example.com" {
		t.Fatalf("unexpected cached room id: %q", roomID)
	}
	if joinedRoomsRequests != 1 {
		t.Fatalf("expected joined room lookup once, got %d", joinedRoomsRequests)
	}
	if createRoomRequests != 1 {
		t.Fatalf("expected createRoom once, got %d", createRoomRequests)
	}
}

func TestExtractMatrixInboundContentParsesImageAttachment(t *testing.T) {
	text, attachments := extractMatrixInboundContent(map[string]any{
		"msgtype": "m.image",
		"body":    "diagram.png",
		"url":     "mxc://matrix.example.com/media123",
		"info": map[string]any{
			"mimetype": "image/png",
			"size":     42,
			"w":        640,
			"h":        480,
		},
	})
	if text != "" {
		t.Fatalf("expected empty text for attachment message, got %q", text)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	att := attachments[0]
	if att.Type != channel.AttachmentImage {
		t.Fatalf("unexpected attachment type: %s", att.Type)
	}
	if att.PlatformKey != "mxc://matrix.example.com/media123" {
		t.Fatalf("unexpected platform key: %q", att.PlatformKey)
	}
	if att.Name != "diagram.png" || att.Mime != "image/png" {
		t.Fatalf("unexpected attachment metadata: %#v", att)
	}
	if att.Width != 640 || att.Height != 480 || att.Size != 42 {
		t.Fatalf("unexpected attachment dimensions: %#v", att)
	}
	if att.Caption != "" {
		t.Fatalf("expected empty caption, got %#v", att)
	}
}

func TestExtractMatrixInboundContentParsesImageCaption(t *testing.T) {
	text, attachments := extractMatrixInboundContent(map[string]any{
		"msgtype":  "m.image",
		"body":     "System architecture diagram",
		"filename": "diagram.png",
		"url":      "mxc://matrix.example.com/media123",
		"info": map[string]any{
			"mimetype": "image/png",
		},
	})
	if text != "System architecture diagram" {
		t.Fatalf("expected caption text, got %q", text)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	att := attachments[0]
	if att.Name != "diagram.png" {
		t.Fatalf("unexpected attachment name: %#v", att)
	}
	if att.Caption != "System architecture diagram" {
		t.Fatalf("unexpected attachment caption: %#v", att)
	}
}

func TestMatrixSendUploadsBase64AttachmentAndSendsMediaEvent(t *testing.T) {
	requests := make([]string, 0, 2)
	uploadedContentTypes := make([]string, 0, 1)
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.Path)
		if strings.Contains(req.URL.Path, "/_matrix/media/v3/upload") {
			uploadedContentTypes = append(uploadedContentTypes, req.Header.Get("Content-Type"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"content_uri":"mxc://matrix.example.com/uploaded1"}`)),
				Header:     make(http.Header),
			}, nil
		}
		payload, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		var content map[string]any
		if err := json.Unmarshal(payload, &content); err != nil {
			return nil, err
		}
		if got := content["msgtype"]; got != "m.image" {
			t.Fatalf("unexpected msgtype: %#v", got)
		}
		if got := content["url"]; got != "mxc://matrix.example.com/uploaded1" {
			t.Fatalf("unexpected uploaded uri: %#v", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"event_id":"$evt1"}`)),
			Header:     make(http.Header),
		}, nil
	})}

	logicalAttachment := channel.Attachment{
		Type: channel.AttachmentImage,
		Name: "chart.png",
		Mime: "image/png",
	}
	err := adapter.Send(context.Background(), channel.ChannelConfig{
		BotID: "bot-1",
		Credentials: map[string]any{
			"homeserverUrl": "https://matrix.example.com",
			"userId":        "@memoh:example.com",
			"accessToken":   "tok",
		},
	}, channel.PreparedOutboundMessage{
		Target: "!room:example.com",
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Attachments: []channel.Attachment{logicalAttachment},
			},
			Attachments: []channel.PreparedAttachment{{
				Logical: logicalAttachment,
				Kind:    channel.PreparedAttachmentUpload,
				Name:    "chart.png",
				Mime:    "image/png",
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("hello")), nil
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("send returned error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected upload and send requests, got %d", len(requests))
	}
	if len(uploadedContentTypes) != 1 || uploadedContentTypes[0] != "image/png" {
		t.Fatalf("unexpected upload content type: %#v", uploadedContentTypes)
	}
}

func TestMatrixSendResolvesRoomAlias(t *testing.T) {
	requests := make([]string, 0, 2)
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.Path)
		switch req.URL.Path {
		case "/_matrix/client/v3/directory/room/#ops:example.com":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"room_id":"!resolved:example.com"}`)),
				Header:     make(http.Header),
			}, nil
		default:
			if !strings.Contains(req.URL.Path, "/_matrix/client/v3/rooms/!resolved:example.com/send/m.room.message/") {
				t.Fatalf("unexpected request path: %s", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"event_id":"$evt1"}`)),
				Header:     make(http.Header),
			}, nil
		}
	})}

	err := adapter.Send(context.Background(), channel.ChannelConfig{
		Credentials: map[string]any{
			"homeserverUrl": "https://matrix.example.com",
			"userId":        "@memoh:example.com",
			"accessToken":   "tok",
		},
	}, channel.PreparedOutboundMessage{
		Target: "#ops:example.com",
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "ping",
			},
		},
	})
	if err != nil {
		t.Fatalf("send returned error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected alias lookup and send requests, got %d", len(requests))
	}
}

func TestMatrixSendPreservesRichPartsAsFormattedHTML(t *testing.T) {
	var payload string
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.URL.Path, "/_matrix/client/v3/rooms/!room:example.com/send/m.room.message/") {
			t.Fatalf("unexpected request path: %s", req.URL.Path)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		payload = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"event_id":"$evt1"}`)),
			Header:     make(http.Header),
		}, nil
	})}

	err := adapter.Send(context.Background(), channel.ChannelConfig{
		Credentials: map[string]any{
			"homeserverUrl": "https://matrix.example.com",
			"userId":        "@memoh:example.com",
			"accessToken":   "tok",
		},
	}, channel.PreparedOutboundMessage{
		Target: "!room:example.com",
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Format: channel.MessageFormatRich,
				Parts: []channel.MessagePart{
					{Type: channel.MessagePartText, Text: "hello", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
					{Type: channel.MessagePartMention, Text: "Alice", ChannelIdentityID: "@alice:example.com"},
					{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/docs"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("send returned error: %v", err)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(payload), &content); err != nil {
		t.Fatalf("unmarshal matrix payload: %v", err)
	}
	if got := content["format"]; got != matrixHTMLFormat {
		t.Fatalf("unexpected format: %#v", got)
	}
	html, ok := content["formatted_body"].(string)
	if !ok {
		t.Fatalf("unexpected formatted body: %#v", content["formatted_body"])
	}
	for _, want := range []string{
		`<strong>hello</strong>`,
		`https://matrix.to/#/@alice:example.com`,
		`https://example.test/docs`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected formatted body to contain %q, got %s", want, html)
		}
	}
}

func TestMatrixResolveAttachmentDownloadsMXC(t *testing.T) {
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.URL.Path, "/_matrix/client/v1/media/download/matrix.example.com/media123/image.png") {
			t.Fatalf("unexpected download path: %s", req.URL.Path)
		}
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("file-bytes")),
			Header:     make(http.Header),
		}
		resp.Header.Set("Content-Type", "image/png")
		resp.ContentLength = int64(len("file-bytes"))
		return resp, nil
	})}

	payload, err := adapter.ResolveAttachment(context.Background(), channel.ChannelConfig{
		Credentials: map[string]any{
			"homeserverUrl": "https://matrix.example.com",
			"userId":        "@memoh:example.com",
			"accessToken":   "tok",
		},
	}, channel.Attachment{
		PlatformKey: "mxc://matrix.example.com/media123",
		Name:        "image.png",
	})
	if err != nil {
		t.Fatalf("ResolveAttachment returned error: %v", err)
	}
	defer func() { _ = payload.Reader.Close() }()
	data, err := io.ReadAll(payload.Reader)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(data) != "file-bytes" {
		t.Fatalf("unexpected payload: %q", string(data))
	}
	if payload.Mime != "image/png" || payload.Name != "image.png" || payload.Size != int64(len("file-bytes")) {
		t.Fatalf("unexpected payload metadata: %#v", payload)
	}
}

func TestMatrixResolveAttachmentFallsBackToLegacyMediaDownload(t *testing.T) {
	paths := make([]string, 0, 2)
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		if strings.Contains(req.URL.Path, "/_matrix/client/v1/media/download/") {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"errcode":"M_NOT_FOUND"}`)),
				Header:     make(http.Header),
			}, nil
		}
		if !strings.Contains(req.URL.Path, "/_matrix/media/v3/download/matrix.example.com/media123") {
			t.Fatalf("unexpected fallback path: %s", req.URL.Path)
		}
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("legacy-file")),
			Header:     make(http.Header),
		}
		resp.Header.Set("Content-Type", "application/octet-stream")
		return resp, nil
	})}

	payload, err := adapter.ResolveAttachment(context.Background(), channel.ChannelConfig{
		Credentials: map[string]any{
			"homeserverUrl": "https://matrix.example.com",
			"userId":        "@memoh:example.com",
			"accessToken":   "tok",
		},
	}, channel.Attachment{
		PlatformKey: "mxc://matrix.example.com/media123",
	})
	if err != nil {
		t.Fatalf("ResolveAttachment returned error: %v", err)
	}
	defer func() { _ = payload.Reader.Close() }()
	if len(paths) != 2 {
		t.Fatalf("expected authenticated and legacy download attempts, got %d", len(paths))
	}
}
