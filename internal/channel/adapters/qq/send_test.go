package qq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/channeltest"
	"github.com/felinics/memoh/internal/media"
)

func sendPreparedQQ(t *testing.T, adapter *QQAdapter, cfg channel.ChannelConfig, store channel.OutboundAttachmentStore, msg channel.OutboundMessage) error {
	t.Helper()
	if cfg.ChannelType == "" {
		cfg.ChannelType = Type
	}
	if store == nil {
		store = channeltest.NewMemoryAttachmentStore()
	}
	prepared, err := channel.PrepareOutboundMessage(context.Background(), store, cfg, msg)
	if err != nil {
		return err
	}
	return adapter.Send(context.Background(), cfg, prepared)
}

func TestQQSendTextReply(t *testing.T) {
	t.Parallel()

	var tokenCalls int
	var messageBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/v2/users/user-openid/messages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			messageBodies = append(messageBodies, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-1",
		BotID: "bot-1",
		Credentials: map[string]any{
			"appId":        "1024",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "c2c:user-openid",
		Message: channel.Message{
			Text:  "hello",
			Reply: &channel.ReplyRef{MessageID: "source-msg"},
		},
	})
	if err != nil {
		t.Fatalf("send text: %v", err)
	}

	if tokenCalls != 1 {
		t.Fatalf("unexpected token calls: %d", tokenCalls)
	}
	if len(messageBodies) != 1 {
		t.Fatalf("unexpected message calls: %d", len(messageBodies))
	}
	if messageBodies[0]["msg_id"] != "source-msg" {
		t.Fatalf("unexpected msg_id: %#v", messageBodies[0]["msg_id"])
	}
	if messageBodies[0]["msg_type"] != float64(0) {
		t.Fatalf("unexpected msg_type: %#v", messageBodies[0]["msg_type"])
	}
	if messageBodies[0]["content"] != "hello" {
		t.Fatalf("unexpected content: %#v", messageBodies[0]["content"])
	}
}

func TestQQSendMarkdownFallsBackToPlainWhenRuntimeMarkdownDisabled(t *testing.T) {
	t.Parallel()

	var messageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/v2/groups/group-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-md-fallback"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-md-fallback",
		BotID: "bot-md-fallback",
		Credentials: map[string]any{
			"appId":           "1025",
			"clientSecret":    "secret",
			"markdownSupport": false,
		},
	}, nil, channel.OutboundMessage{
		Target: "group:group-openid",
		Message: channel.Message{
			Format: channel.MessageFormatMarkdown,
			Text:   "**bold** and `code`",
		},
	})
	if err != nil {
		t.Fatalf("send markdown fallback: %v", err)
	}

	if messageBody["msg_type"] != float64(0) {
		t.Fatalf("unexpected msg_type: %#v", messageBody["msg_type"])
	}
	if messageBody["content"] != "bold and code" {
		t.Fatalf("unexpected content: %#v", messageBody["content"])
	}
}

func TestQQOutboundCapabilitiesReflectRuntimeMarkdownGate(t *testing.T) {
	t.Parallel()

	adapter := &QQAdapter{}
	base := adapter.Descriptor().Capabilities
	cases := []struct {
		name            string
		credentials     map[string]any
		target          string
		wantMarkdown    bool
		wantAttachments bool
		wantMedia       bool
	}{
		{
			name: "group markdown enabled",
			credentials: map[string]any{
				"appId":           "1024",
				"clientSecret":    "secret",
				"markdownSupport": true,
			},
			target:          "group:group-openid",
			wantMarkdown:    true,
			wantAttachments: true,
			wantMedia:       true,
		},
		{
			name: "group markdown disabled",
			credentials: map[string]any{
				"appId":           "1024",
				"clientSecret":    "secret",
				"markdownSupport": false,
			},
			target:          "group:group-openid",
			wantMarkdown:    false,
			wantAttachments: true,
			wantMedia:       true,
		},
		{
			name: "channel target",
			credentials: map[string]any{
				"appId":           "1024",
				"clientSecret":    "secret",
				"markdownSupport": true,
			},
			target:          "channel:channel-id",
			wantMarkdown:    false,
			wantAttachments: false,
			wantMedia:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := adapter.ResolveOutboundCapabilities(channel.ChannelConfig{Credentials: tc.credentials}, tc.target, base)
			if caps.Markdown != tc.wantMarkdown {
				t.Fatalf("Markdown = %v, want %v", caps.Markdown, tc.wantMarkdown)
			}
			if caps.Attachments != tc.wantAttachments {
				t.Fatalf("Attachments = %v, want %v", caps.Attachments, tc.wantAttachments)
			}
			if caps.Media != tc.wantMedia {
				t.Fatalf("Media = %v, want %v", caps.Media, tc.wantMedia)
			}
		})
	}
}

func TestQQSendImageAttachment(t *testing.T) {
	t.Parallel()

	var uploadBody map[string]any
	var messageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/v2/groups/group-openid/files":
			if err := json.NewDecoder(r.Body).Decode(&uploadBody); err != nil {
				t.Fatalf("decode upload body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file_uuid": "file-uuid-1",
				"file_info": "file-info-1",
				"ttl":       60,
			})
		case "/v2/groups/group-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-2"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	adapter.SetAssetOpener(&trackingAssetOpener{
		data: []byte("png-bytes"),
		asset: media.Asset{
			ContentHash: "hash-1",
			BotID:       "bot-2",
			Mime:        "image/png",
		},
	})
	store := channeltest.NewMemoryAttachmentStore()
	asset, err := store.SeedAsset("bot-2", []byte("png-bytes"), "image/png", ".png")
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	err = sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-2",
		BotID: "bot-2",
		Credentials: map[string]any{
			"appId":        "2048",
			"clientSecret": "secret",
		},
	}, store, channel.OutboundMessage{
		Target: "group:group-openid",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type:        channel.AttachmentImage,
				ContentHash: asset.ContentHash,
				Name:        "image.png",
			}},
			Reply: &channel.ReplyRef{MessageID: "source-msg"},
		},
	})
	if err != nil {
		t.Fatalf("send attachment: %v", err)
	}

	if uploadBody["file_type"] != float64(qqMediaTypeImage) {
		t.Fatalf("unexpected file_type: %#v", uploadBody["file_type"])
	}
	if uploadBody["file_data"] != base64.StdEncoding.EncodeToString([]byte("png-bytes")) {
		t.Fatalf("unexpected file_data: %#v", uploadBody["file_data"])
	}
	if _, ok := uploadBody["file_name"]; ok {
		t.Fatalf("unexpected file_name for image upload: %#v", uploadBody["file_name"])
	}
	if messageBody["msg_type"] != float64(7) {
		t.Fatalf("unexpected msg_type: %#v", messageBody["msg_type"])
	}
	if messageBody["msg_id"] != "source-msg" {
		t.Fatalf("unexpected msg_id: %#v", messageBody["msg_id"])
	}
	media, ok := messageBody["media"].(map[string]any)
	if !ok {
		t.Fatalf("expected media payload: %#v", messageBody["media"])
	}
	if media["file_info"] != "file-info-1" {
		t.Fatalf("unexpected media.file_info: %#v", media["file_info"])
	}
	if len(media) != 1 {
		t.Fatalf("unexpected media payload size: %#v", media)
	}
}

func TestQQSendImageAttachmentCaptionUsesMediaContent(t *testing.T) {
	t.Parallel()

	var uploadBody map[string]any
	var messageBody map[string]any
	var messageCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/v2/users/user-openid/files":
			if err := json.NewDecoder(r.Body).Decode(&uploadBody); err != nil {
				t.Fatalf("decode upload body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file_uuid": "file-uuid-2",
				"file_info": "file-info-2",
				"ttl":       60,
			})
		case "/v2/users/user-openid/messages":
			messageCalls++
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-2b"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-2b",
		BotID: "bot-2b",
		Credentials: map[string]any{
			"appId":        "2049",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "c2c:user-openid",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type:    channel.AttachmentImage,
				Base64:  "data:image/png;base64,cG5nLWJ5dGVz",
				Caption: "test.jpg from QQ",
			}},
		},
	})
	if err != nil {
		t.Fatalf("send attachment with caption: %v", err)
	}

	if uploadBody["file_type"] != float64(qqMediaTypeImage) {
		t.Fatalf("unexpected file_type: %#v", uploadBody["file_type"])
	}
	if uploadBody["file_data"] != "cG5nLWJ5dGVz" {
		t.Fatalf("unexpected file_data: %#v", uploadBody["file_data"])
	}
	if messageCalls != 1 {
		t.Fatalf("unexpected message calls: %d", messageCalls)
	}
	if messageBody["msg_type"] != float64(7) {
		t.Fatalf("unexpected msg_type: %#v", messageBody["msg_type"])
	}
	if messageBody["content"] != "test.jpg from QQ" {
		t.Fatalf("unexpected content: %#v", messageBody["content"])
	}
}

func TestQQProcessingStartedSendsInputHintForDirectMessages(t *testing.T) {
	t.Parallel()

	var messageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/v2/users/user-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode notify body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-3"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	_, err := adapter.ProcessingStarted(context.Background(), channel.ChannelConfig{
		ID: "cfg-3",
		Credentials: map[string]any{
			"appId":           "4096",
			"clientSecret":    "secret",
			"enableInputHint": true,
		},
	}, channel.InboundMessage{}, channel.ProcessingStatusInfo{
		ReplyTarget:     "c2c:user-openid",
		SourceMessageID: "source-msg",
	})
	if err != nil {
		t.Fatalf("processing started: %v", err)
	}
	if messageBody["msg_type"] != float64(6) {
		t.Fatalf("unexpected msg_type: %#v", messageBody["msg_type"])
	}
	if messageBody["msg_id"] != "source-msg" {
		t.Fatalf("unexpected msg_id: %#v", messageBody["msg_id"])
	}
}

func TestQQSendChannelImageIsUnsupported(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-4",
		BotID: "bot-4",
		Credentials: map[string]any{
			"appId":        "8192",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "channel:channel-1",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type:   channel.AttachmentImage,
				Base64: "iVBORw0KGgo=",
				Name:   "output.png",
			}},
		},
	})
	if err == nil {
		t.Fatal("expected channel image error")
	}
	if !strings.Contains(err.Error(), "does not support image attachments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQQSendChannelReplyIncludesMessageReference(t *testing.T) {
	t.Parallel()

	var messageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/channels/channel-1/messages":
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode channel body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-6"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-5",
		BotID: "bot-5",
		Credentials: map[string]any{
			"appId":        "16384",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "channel:channel-1",
		Message: channel.Message{
			Text:  "hello",
			Reply: &channel.ReplyRef{MessageID: "source-msg"},
		},
	})
	if err != nil {
		t.Fatalf("send channel reply: %v", err)
	}

	if messageBody["msg_id"] != "source-msg" {
		t.Fatalf("unexpected msg_id: %#v", messageBody["msg_id"])
	}
	ref, ok := messageBody["message_reference"].(map[string]any)
	if !ok {
		t.Fatalf("expected message_reference: %#v", messageBody["message_reference"])
	}
	if ref["message_id"] != "source-msg" {
		t.Fatalf("unexpected message_reference.message_id: %#v", ref["message_id"])
	}
}

func TestQQSendGroupFileUsesNativeUpload(t *testing.T) {
	t.Parallel()

	var uploadBody map[string]any
	var messageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/v2/groups/group-openid/files":
			if err := json.NewDecoder(r.Body).Decode(&uploadBody); err != nil {
				t.Fatalf("decode upload body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file_uuid": "file-uuid-7",
				"file_info": "file-info-7",
				"ttl":       60,
			})
		case "/v2/groups/group-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-7"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-6",
		BotID: "bot-6",
		Credentials: map[string]any{
			"appId":        "32768",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "group:group-openid",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type:   channel.AttachmentFile,
				Base64: "JVBERi0xLjQ=",
				Name:   "report.pdf",
			}},
			Reply: &channel.ReplyRef{MessageID: "source-msg"},
		},
	})
	if err != nil {
		t.Fatalf("send group file: %v", err)
	}

	if uploadBody["file_type"] != float64(qqMediaTypeFile) {
		t.Fatalf("unexpected file_type: %#v", uploadBody["file_type"])
	}
	if uploadBody["file_name"] != "report.pdf" {
		t.Fatalf("unexpected file_name: %#v", uploadBody["file_name"])
	}
	if uploadBody["file_data"] != "JVBERi0xLjQ=" {
		t.Fatalf("unexpected file_data: %#v", uploadBody["file_data"])
	}
	if messageBody["msg_type"] != float64(7) {
		t.Fatalf("unexpected msg_type: %#v", messageBody["msg_type"])
	}
	if messageBody["msg_id"] != "source-msg" {
		t.Fatalf("unexpected msg_id: %#v", messageBody["msg_id"])
	}
	media, ok := messageBody["media"].(map[string]any)
	if !ok {
		t.Fatalf("expected media payload: %#v", messageBody["media"])
	}
	if media["file_info"] != "file-info-7" {
		t.Fatalf("unexpected media.file_info: %#v", media["file_info"])
	}
}

func TestQQSendChannelFileIsUnsupported(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-7",
		BotID: "bot-7",
		Credentials: map[string]any{
			"appId":        "65536",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "channel:channel-1",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type:   channel.AttachmentFile,
				Base64: "JVBERi0xLjQ=",
				Name:   "report.pdf",
			}},
			Reply: &channel.ReplyRef{MessageID: "source-msg"},
		},
	})
	if err == nil {
		t.Fatal("expected channel file error")
	}
	if !strings.Contains(err.Error(), "does not support file attachments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQQSendImageWithLocalPathFailsBeforeAPI(t *testing.T) {
	t.Parallel()

	var tokenCalls int
	var fileCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/v2/groups/group-openid/files":
			fileCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "unused"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-8",
		BotID: "bot-8",
		Credentials: map[string]any{
			"appId":        "131072",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "group:group-openid",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type: channel.AttachmentImage,
				URL:  "/tmp/output.png",
				Name: "output.png",
			}},
		},
	})
	if err == nil {
		t.Fatal("expected local path error")
	}
	if !strings.Contains(err.Error(), "attachment reference is required") {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenCalls != 0 {
		t.Fatalf("unexpected token calls: %d", tokenCalls)
	}
	if fileCalls != 0 {
		t.Fatalf("unexpected file upload calls: %d", fileCalls)
	}
}

func TestQQSendChannelImageFromStoredAssetIsUnsupported(t *testing.T) {
	t.Parallel()

	var tokenCalls int
	var messageCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/channels/channel-1/messages":
			messageCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-5"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	opener := &trackingAssetOpener{
		data: []byte("png-bytes"),
		asset: media.Asset{
			ContentHash: "hash-1",
			BotID:       "bot-4",
			Mime:        "image/png",
		},
	}
	adapter.SetAssetOpener(opener)
	store := channeltest.NewMemoryAttachmentStore()
	asset, err := store.SeedAsset("bot-4", []byte("png-bytes"), "image/png", ".png")
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	err = sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-4",
		BotID: "bot-4",
		Credentials: map[string]any{
			"appId":        "8192",
			"clientSecret": "secret",
		},
	}, store, channel.OutboundMessage{
		Target: "channel:channel-1",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type:        channel.AttachmentImage,
				ContentHash: asset.ContentHash,
				Name:        "image.png",
			}},
		},
	})
	if err == nil {
		t.Fatal("expected channel image error")
	}
	if !strings.Contains(err.Error(), "does not support image attachments") {
		t.Fatalf("unexpected error: %v", err)
	}
	if opener.called {
		t.Fatal("expected stored asset opener to be skipped for channel images")
	}
	if tokenCalls != 0 {
		t.Fatalf("unexpected token calls: %d", tokenCalls)
	}
	if messageCalls != 0 {
		t.Fatalf("unexpected channel message calls: %d", messageCalls)
	}
}

func TestQQSendVoiceAttachmentFromHTTPURLUsesDetectedMime(t *testing.T) {
	t.Parallel()

	const voiceBytes = "remote-voice-bytes"

	var uploadBody map[string]any
	var messageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/remote/voice":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte(voiceBytes))
		case "/v2/groups/group-openid/files":
			if err := json.NewDecoder(r.Body).Decode(&uploadBody); err != nil {
				t.Fatalf("decode upload body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file_uuid": "file-uuid-voice",
				"file_info": "file-info-voice",
				"ttl":       60,
			})
		case "/v2/groups/group-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-voice"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-voice",
		BotID: "bot-voice",
		Credentials: map[string]any{
			"appId":        "524288",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "group:group-openid",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type: channel.AttachmentVoice,
				URL:  server.URL + "/remote/voice",
			}},
		},
	})
	if err != nil {
		t.Fatalf("send remote voice attachment: %v", err)
	}
	if uploadBody["file_type"] != float64(qqMediaTypeVoice) {
		t.Fatalf("unexpected file_type: %#v", uploadBody["file_type"])
	}
	if uploadBody["file_data"] != base64.StdEncoding.EncodeToString([]byte(voiceBytes)) {
		t.Fatalf("unexpected file_data: %#v", uploadBody["file_data"])
	}
	mediaPayload, ok := messageBody["media"].(map[string]any)
	if !ok {
		t.Fatalf("expected media payload: %#v", messageBody["media"])
	}
	if mediaPayload["file_info"] != "file-info-voice" {
		t.Fatalf("unexpected media.file_info: %#v", mediaPayload["file_info"])
	}
}

func TestQQSendImageAttachmentFromHTTPURLUsesFetchedBytes(t *testing.T) {
	t.Parallel()

	const imageBytes = "remote-image-bytes"

	var uploadBody map[string]any
	var messageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   7200,
			})
		case "/remote/test.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte(imageBytes))
		case "/v2/groups/group-openid/files":
			if err := json.NewDecoder(r.Body).Decode(&uploadBody); err != nil {
				t.Fatalf("decode upload body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file_uuid": "file-uuid-9",
				"file_info": "file-info-9",
				"ttl":       60,
			})
		case "/v2/groups/group-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode message body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-9"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := newTestQQAdapter(server)
	err := sendPreparedQQ(t, adapter, channel.ChannelConfig{
		ID:    "cfg-9",
		BotID: "bot-9",
		Credentials: map[string]any{
			"appId":        "262144",
			"clientSecret": "secret",
		},
	}, nil, channel.OutboundMessage{
		Target: "group:group-openid",
		Message: channel.Message{
			Attachments: []channel.Attachment{{
				Type: channel.AttachmentImage,
				URL:  server.URL + "/remote/test.jpg",
				Name: "test.jpg",
			}},
		},
	})
	if err != nil {
		t.Fatalf("send remote image attachment: %v", err)
	}
	if uploadBody["file_type"] != float64(qqMediaTypeImage) {
		t.Fatalf("unexpected file_type: %#v", uploadBody["file_type"])
	}
	if uploadBody["file_data"] != base64.StdEncoding.EncodeToString([]byte(imageBytes)) {
		t.Fatalf("unexpected file_data: %#v", uploadBody["file_data"])
	}
	if _, ok := uploadBody["url"]; ok {
		t.Fatalf("unexpected qq native url upload payload: %#v", uploadBody["url"])
	}
	mediaPayload, ok := messageBody["media"].(map[string]any)
	if !ok {
		t.Fatalf("expected media payload: %#v", messageBody["media"])
	}
	if mediaPayload["file_info"] != "file-info-9" {
		t.Fatalf("unexpected media.file_info: %#v", mediaPayload["file_info"])
	}
}

type trackingAssetOpener struct {
	called bool
	data   []byte
	asset  media.Asset
}

func (t *trackingAssetOpener) Open(context.Context, string, string) (io.ReadCloser, media.Asset, error) {
	t.called = true
	return io.NopCloser(bytes.NewReader(t.data)), t.asset, nil
}

func newTestQQAdapter(server *httptest.Server) *QQAdapter {
	adapter := NewQQAdapter(nil)
	adapter.httpClient = server.Client()
	adapter.apiBaseURL = server.URL
	adapter.tokenURL = server.URL + "/app/getAppAccessToken"
	return adapter
}
