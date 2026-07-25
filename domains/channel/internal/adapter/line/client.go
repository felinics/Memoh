package line

import (
	"context"
	"net/http"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
)

const (
	lineAPITimeout  = 10 * time.Second
	lineBlobTimeout = 30 * time.Second
)

type messagingClient interface {
	GetBotInfo() (*messaging_api.BotInfoResponse, error)
	SetWebhookEndpoint(*messaging_api.SetWebhookEndpointRequest) (struct{}, error)
	PushMessage(*messaging_api.PushMessageRequest, string) (*messaging_api.PushMessageResponse, error)
}

type blobClient interface {
	GetMessageContent(messageID string) (*http.Response, error)
}

type lineClientFactory interface {
	NewMessagingClient(ctx context.Context, token string) (messagingClient, error)
	NewBlobClient(ctx context.Context, token string) (blobClient, error)
}

type defaultClientFactory struct{}

func (defaultClientFactory) NewMessagingClient(ctx context.Context, token string) (messagingClient, error) {
	api, err := messaging_api.NewMessagingApiAPI(
		token,
		messaging_api.WithHTTPClient(lineHTTPClient(lineAPITimeout)),
	)
	if err != nil {
		return nil, err
	}
	return api.WithContext(ctx), nil
}

func (defaultClientFactory) NewBlobClient(ctx context.Context, token string) (blobClient, error) {
	api, err := messaging_api.NewMessagingApiBlobAPI(
		token,
		messaging_api.WithBlobHTTPClient(lineHTTPClient(lineBlobTimeout)),
	)
	if err != nil {
		return nil, err
	}
	return api.WithContext(ctx), nil
}

func lineHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: stripEmptyRetryKeyTransport{
			base: http.DefaultTransport,
		},
	}
}

type stripEmptyRetryKeyTransport struct {
	base http.RoundTripper
}

func (t stripEmptyRetryKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && req.Header.Get("X-Line-Retry-Key") == "" {
		req.Header.Del("X-Line-Retry-Key")
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
