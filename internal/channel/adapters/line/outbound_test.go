package line

import (
	"context"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/publicmedia"
)

const testPublicMediaSignSecret = "line-public-media-test-secret"

func TestSplitLINETextByUTF16CodeUnits(t *testing.T) {
	t.Parallel()

	chunks := splitLINETextByUTF16CodeUnits("abc😀de", 5)
	if got, want := strings.Join(chunks, "|"), "abc😀|de"; got != want {
		t.Fatalf("chunks = %q, want %q", got, want)
	}

	chunks = splitLINETextByUTF16CodeUnits("😀😀", 2)
	if got, want := strings.Join(chunks, "|"), "😀|😀"; got != want {
		t.Fatalf("emoji chunks = %q, want %q", got, want)
	}
}

func TestAllowedLineImageURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		att  channel.PreparedAttachment
		want bool
	}{
		{
			name: "public png",
			att: channel.PreparedAttachment{
				Kind:      channel.PreparedAttachmentPublicURL,
				PublicURL: "https://cdn.example.com/image.png",
				Mime:      "image/png",
				Size:      lineImageOriginalMaxBytes,
			},
			want: true,
		},
		{
			name: "http rejected",
			att: channel.PreparedAttachment{
				Kind:      channel.PreparedAttachmentPublicURL,
				PublicURL: "http://cdn.example.com/image.png",
			},
		},
		{
			name: "private host rejected",
			att: channel.PreparedAttachment{
				Kind:      channel.PreparedAttachmentPublicURL,
				PublicURL: "https://192.168.1.1/image.png",
			},
		},
		{
			name: "localhost trailing dot rejected",
			att: channel.PreparedAttachment{
				Kind:      channel.PreparedAttachmentPublicURL,
				PublicURL: "https://localhost./image.png",
			},
		},
		{
			name: "local domain trailing dot rejected",
			att: channel.PreparedAttachment{
				Kind:      channel.PreparedAttachmentPublicURL,
				PublicURL: "https://cache.local./image.png",
			},
		},
		{
			name: "localhost subdomain trailing dot rejected",
			att: channel.PreparedAttachment{
				Kind:      channel.PreparedAttachmentPublicURL,
				PublicURL: "https://foo.localhost./image.png",
			},
		},
		{
			name: "unsupported mime rejected",
			att: channel.PreparedAttachment{
				Kind:      channel.PreparedAttachmentPublicURL,
				PublicURL: "https://cdn.example.com/image.gif",
				Mime:      "image/gif",
			},
		},
		{
			name: "oversized original rejected",
			att: channel.PreparedAttachment{
				Kind:      channel.PreparedAttachmentPublicURL,
				PublicURL: "https://cdn.example.com/image.jpg",
				Size:      lineImageOriginalMaxBytes + 1,
			},
		},
		{
			name: "non public url attachment rejected",
			att: channel.PreparedAttachment{
				Kind: channel.PreparedAttachmentUpload,
				Logical: channel.Attachment{
					URL: "https://cdn.example.com/image.png",
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, ok, _ := allowedLineImageURL(tt.att, lineImageOriginalMaxBytes)
			if ok != tt.want {
				t.Fatalf("allowedLineImageURL() ok = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestLineImageMessageUsesPublicMediaURLForPersistedImage(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(nil)
	adapter.SetPublicBaseURLProvider(testPublicBaseURLProvider{base: "https://public.example.com"})
	image, ok := adapter.lineImageMessage(channel.ChannelConfig{
		BotID: "bot-1",
		ID:    "cfg-1",
	}, channel.PreparedAttachment{
		Kind: channel.PreparedAttachmentUpload,
		Logical: channel.Attachment{
			Type:        channel.AttachmentImage,
			ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Name:        "diagram.png",
			Mime:        "image/png",
			Size:        2 << 20,
		},
		Name: "diagram.png",
		Mime: "image/png",
		Size: 2 << 20,
	})
	if !ok {
		t.Fatal("lineImageMessage rejected persisted image")
	}
	msg, ok := image.(messaging_api.ImageMessage)
	if !ok {
		t.Fatalf("message type = %T, want messaging_api.ImageMessage", image)
	}
	assertSignedLinePublicMediaURL(t, msg.OriginalContentUrl, publicmedia.OriginalPath("line", "bot-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "diagram.png"))
	assertSignedLinePublicMediaURL(t, msg.PreviewImageUrl, publicmedia.PreviewPath("line", "bot-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
}

func TestLineImageMessageRejectsPersistedImageWithoutPublicBaseURL(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(nil)
	_, ok := adapter.lineImageMessage(channel.ChannelConfig{BotID: "bot-1"}, channel.PreparedAttachment{
		Kind: channel.PreparedAttachmentUpload,
		Logical: channel.Attachment{
			Type:        channel.AttachmentImage,
			ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Mime:        "image/png",
		},
		Mime: "image/png",
	})
	if ok {
		t.Fatal("lineImageMessage should reject persisted image without public base URL")
	}
}

func TestLineImageMessageRejectsPersistedImageWithNonHexContentHash(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(nil)
	adapter.SetPublicBaseURLProvider(testPublicBaseURLProvider{base: "https://public.example.com"})
	_, ok := adapter.lineImageMessage(channel.ChannelConfig{BotID: "bot-1"}, channel.PreparedAttachment{
		Kind: channel.PreparedAttachmentUpload,
		Logical: channel.Attachment{
			Type:        channel.AttachmentImage,
			ContentHash: strings.Repeat("g", 64),
			Mime:        "image/png",
		},
		Mime: "image/png",
	})
	if ok {
		t.Fatal("lineImageMessage should reject non-hex content hash")
	}
}

func TestSendPreparedReturnsErrorWhenOnlyUnsupportedAttachments(t *testing.T) {
	t.Parallel()

	client := &testMessagingClient{}
	adapter := NewAdapter(nil)
	adapter.client = testLineClientFactory{messaging: client}

	_, err := adapter.sendPrepared(context.Background(), testLineConfig(), channel.PreparedOutboundMessage{
		Target: "Uuser",
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Attachments: []channel.Attachment{{Type: channel.AttachmentFile, URL: "memoh://file"}},
			},
			Attachments: []channel.PreparedAttachment{
				{
					Kind: channel.PreparedAttachmentUpload,
					Logical: channel.Attachment{
						Type: channel.AttachmentFile,
						URL:  "memoh://file",
					},
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected unsupported attachment-only message to fail")
	}
	if client.pushCalls != 0 {
		t.Fatalf("push calls = %d, want 0", client.pushCalls)
	}
}

func TestSendPreparedDoesNotPushTextWhenAttachmentsUnsupported(t *testing.T) {
	t.Parallel()

	client := &testMessagingClient{}
	adapter := NewAdapter(nil)
	adapter.client = testLineClientFactory{messaging: client}

	_, err := adapter.sendPrepared(context.Background(), testLineConfig(), channel.PreparedOutboundMessage{
		Target: "Uuser",
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Format:      channel.MessageFormatPlain,
				Text:        "hello",
				Attachments: []channel.Attachment{{Type: channel.AttachmentFile, URL: "memoh://file"}},
			},
			Attachments: []channel.PreparedAttachment{
				{
					Kind: channel.PreparedAttachmentUpload,
					Logical: channel.Attachment{
						Type: channel.AttachmentFile,
						URL:  "memoh://file",
					},
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected mixed text and unsupported attachment message to fail")
	}
	if client.pushCalls != 0 {
		t.Fatalf("push calls = %d, want 0", client.pushCalls)
	}
}

type testLineClientFactory struct {
	messaging messagingClient
}

type testPublicBaseURLProvider struct {
	base string
}

func (p testPublicBaseURLProvider) PublicBaseURL() string {
	return p.base
}

func (testPublicBaseURLProvider) SignPublicMediaPath(path string) (string, bool) {
	signer := publicmedia.NewSigner(testPublicMediaSignSecret, publicmedia.SignedURLTTL)
	return signer.SignPath(path, time.Now().UTC())
}

func assertSignedLinePublicMediaURL(t *testing.T, rawURL, wantPath string) {
	t.Helper()
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	if got, want := parsed.Scheme+"://"+parsed.Host, "https://public.example.com"; got != want {
		t.Fatalf("URL origin = %q, want %q", got, want)
	}
	if got := parsed.EscapedPath(); got != wantPath {
		t.Fatalf("URL path = %q, want %q", got, wantPath)
	}
	signer := publicmedia.NewSigner(testPublicMediaSignSecret, publicmedia.SignedURLTTL)
	if !signer.Validate(parsed.EscapedPath(), parsed.Query(), time.Now().UTC()) {
		t.Fatalf("URL signature did not validate: %q", rawURL)
	}
}

func (f testLineClientFactory) NewMessagingClient(context.Context, string) (messagingClient, error) {
	return f.messaging, nil
}

func (testLineClientFactory) NewBlobClient(context.Context, string) (blobClient, error) {
	return nil, nil
}

type testMessagingClient struct {
	pushCalls       int
	pushRequests    []*messaging_api.PushMessageRequest
	pushErrOnCall   int
	pushErr         error
	webhookEndpoint string
}

func (*testMessagingClient) GetBotInfo() (*messaging_api.BotInfoResponse, error) {
	return &messaging_api.BotInfoResponse{UserId: "Ubot"}, nil
}

func (c *testMessagingClient) PushMessage(req *messaging_api.PushMessageRequest, _ string) (*messaging_api.PushMessageResponse, error) {
	c.pushCalls++
	c.pushRequests = append(c.pushRequests, req)
	if c.pushErr != nil && c.pushCalls == c.pushErrOnCall {
		return nil, c.pushErr
	}
	return &messaging_api.PushMessageResponse{}, nil
}

func (c *testMessagingClient) SetWebhookEndpoint(req *messaging_api.SetWebhookEndpointRequest) (struct{}, error) {
	if req != nil {
		c.webhookEndpoint = req.Endpoint
	}
	return struct{}{}, nil
}
