package channelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/email"
	runtimeRpc "github.com/felinics/memoh/internal/rpc/runtime"
	"github.com/felinics/memoh/internal/webhooktunnel"
)

const (
	MethodUpsertConfig = "channel.config.upsert"
	MethodSetStatus    = "channel.config.status"
	MethodDeleteConfig = "channel.config.delete"
	MethodSetWebhook   = "channel.webhook.set"
	MethodSend         = "channel.message.send"
	MethodReact        = "channel.message.react"
	MethodStatuses     = "channel.connection.statuses"
	MethodRefreshEmail = "channel.email.refresh"
	MethodSendEmail    = "channel.email.send"
	MethodTunnelStatus = "channel.tunnel.status"

	reasonConfigNotFound     = "channel.config_not_found"
	reasonDiscoveryFailed    = "channel.discovery_failed"
	reasonEnableFailed       = "channel.enable_failed"
	reasonInvalidWebhook     = "channel.invalid_webhook"
	reasonWebhookUnsupported = "channel.webhook_unsupported"
)

type Client struct{ rpc *runtimeRpc.Client }

func NewClient(rpc *runtimeRpc.Client) *Client { return &Client{rpc: rpc} }

type channelInput struct {
	BotID       string
	ChannelType channel.ChannelType
	Config      channel.UpsertConfigRequest
	Disabled    bool
	Webhook     channel.SetWebhookEndpointRequest
	Send        channel.SendRequest
	React       channel.ReactRequest
}

func (c *Client) UpsertBotChannelConfig(ctx context.Context, botID string, typ channel.ChannelType, req channel.UpsertConfigRequest) (channel.ChannelConfig, error) {
	var out channel.ChannelConfig
	return out, c.call(ctx, MethodUpsertConfig, channelInput{BotID: botID, ChannelType: typ, Config: req}, &out)
}

func (c *Client) SetBotChannelStatus(ctx context.Context, botID string, typ channel.ChannelType, disabled bool) (channel.ChannelConfig, error) {
	var out channel.ChannelConfig
	return out, c.call(ctx, MethodSetStatus, channelInput{BotID: botID, ChannelType: typ, Disabled: disabled}, &out)
}

func (c *Client) DeleteBotChannelConfig(ctx context.Context, botID string, typ channel.ChannelType) error {
	return c.call(ctx, MethodDeleteConfig, channelInput{BotID: botID, ChannelType: typ}, nil)
}

func (c *Client) SetWebhookEndpoint(ctx context.Context, botID string, typ channel.ChannelType, req channel.SetWebhookEndpointRequest) (channel.SetWebhookEndpointResponse, error) {
	var out channel.SetWebhookEndpointResponse
	return out, c.call(ctx, MethodSetWebhook, channelInput{BotID: botID, ChannelType: typ, Webhook: req}, &out)
}

func (c *Client) Send(ctx context.Context, botID string, typ channel.ChannelType, req channel.SendRequest) error {
	return c.call(ctx, MethodSend, channelInput{BotID: botID, ChannelType: typ, Send: req}, nil)
}

func (c *Client) React(ctx context.Context, botID string, typ channel.ChannelType, req channel.ReactRequest) error {
	return c.call(ctx, MethodReact, channelInput{BotID: botID, ChannelType: typ, React: req}, nil)
}

func (c *Client) ConnectionStatusesByBot(botID string) []channel.ConnectionStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out []channel.ConnectionStatus
	if c.call(ctx, MethodStatuses, botID, &out) != nil {
		return nil
	}
	return out
}

func (c *Client) RefreshProvider(ctx context.Context, providerID string) error {
	return c.call(ctx, MethodRefreshEmail, providerID, nil)
}

func (c *Client) SendEmail(ctx context.Context, botID, providerID string, msg email.OutboundEmail) (string, error) {
	in := struct {
		BotID, ProviderID string
		Message           email.OutboundEmail
	}{botID, providerID, msg}
	var out string
	return out, c.call(ctx, MethodSendEmail, in, &out)
}

func (c *Client) Status() webhooktunnel.Status {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out webhooktunnel.Status
	if c.call(ctx, MethodTunnelStatus, nil, &out) != nil {
		return webhooktunnel.Status{Enabled: false, Mode: "unavailable", Status: webhooktunnel.StatusError}
	}
	return out
}

// reasonSentinels maps wire reason strings back to channel sentinels.
var reasonSentinels = map[string]error{
	reasonConfigNotFound:     channel.ErrChannelConfigNotFound,
	reasonDiscoveryFailed:    channel.ErrChannelDiscoveryFailed,
	reasonEnableFailed:       channel.ErrEnableChannelFailed,
	reasonInvalidWebhook:     channel.ErrInvalidWebhookEndpoint,
	reasonWebhookUnsupported: channel.ErrWebhookEndpointUnsupported,
}

// reasonDetailSep separates the stable reason token from the original error
// text on the wire. A non-printable unit separator cannot collide with
// error message content.
const reasonDetailSep = "\x1f"

// channelReasonError restores a sentinel identity plus the original error
// text after crossing the internal RPC, so operators keep seeing the
// platform-side cause (e.g. the getMe failure behind a discovery error)
// exactly as the in-process path rendered it.
type channelReasonError struct {
	sentinel error
	text     string
}

func (e *channelReasonError) Error() string {
	if e.text != "" {
		return e.text
	}
	return e.sentinel.Error()
}

func (e *channelReasonError) Unwrap() error { return e.sentinel }

func (c *Client) call(ctx context.Context, method string, input, output any) error {
	err := c.rpc.Call(ctx, method, input, output)
	if err == nil || errors.Is(err, runtimeRpc.ErrUnavailable) {
		return err
	}
	return restoreChannelError(err)
}

// restoreChannelError maps a wire error back to its channel sentinel,
// keeping any transported cause text.
func restoreChannelError(err error) error {
	message := status.Convert(err).Message()
	for reason, sentinel := range reasonSentinels {
		if message == reason {
			return sentinel
		}
		if detail, ok := strings.CutPrefix(message, reason+reasonDetailSep); ok {
			return &channelReasonError{sentinel: sentinel, text: detail}
		}
	}
	return err
}

func safeChannelError(err error) error {
	switch {
	case errors.Is(err, channel.ErrChannelConfigNotFound):
		return reasonStatus(codes.NotFound, reasonConfigNotFound, err)
	case errors.Is(err, channel.ErrChannelDiscoveryFailed):
		return reasonStatus(codes.FailedPrecondition, reasonDiscoveryFailed, err)
	case errors.Is(err, channel.ErrEnableChannelFailed):
		return reasonStatus(codes.FailedPrecondition, reasonEnableFailed, err)
	case errors.Is(err, channel.ErrInvalidWebhookEndpoint):
		return reasonStatus(codes.InvalidArgument, reasonInvalidWebhook, err)
	case errors.Is(err, channel.ErrWebhookEndpointUnsupported):
		return reasonStatus(codes.Unimplemented, reasonWebhookUnsupported, err)
	default:
		return err
	}
}

// reasonStatus encodes a sentinel as its stable reason token followed by
// the full original error text, letting the peer restore both the sentinel
// identity and the pre-split message.
func reasonStatus(code codes.Code, reason string, err error) error {
	return status.Error(code, reason+reasonDetailSep+err.Error())
}

func Handlers(channelRuntime channel.Runtime, emailRuntime email.Runtime, tunnel *webhooktunnel.Manager) map[string]runtimeRpc.Handler {
	decode := func(raw json.RawMessage, dst any) error { return json.Unmarshal(raw, dst) }
	return map[string]runtimeRpc.Handler{
		MethodUpsertConfig: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in channelInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			out, err := channelRuntime.UpsertBotChannelConfig(ctx, in.BotID, in.ChannelType, in.Config)
			return out, safeChannelError(err)
		},
		MethodSetStatus: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in channelInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			out, err := channelRuntime.SetBotChannelStatus(ctx, in.BotID, in.ChannelType, in.Disabled)
			return out, safeChannelError(err)
		},
		MethodDeleteConfig: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in channelInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			return nil, safeChannelError(channelRuntime.DeleteBotChannelConfig(ctx, in.BotID, in.ChannelType))
		},
		MethodSetWebhook: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in channelInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			out, err := channelRuntime.SetWebhookEndpoint(ctx, in.BotID, in.ChannelType, in.Webhook)
			return out, safeChannelError(err)
		},
		MethodSend: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in channelInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			// Public: send failures carry the platform adapter's own text
			// ("telegram: chat not found"), which callers surface to users
			// and the agent uses to self-correct — sanitizing it regresses
			// the pre-split behavior.
			return nil, runtimeRpc.Public(channelRuntime.Send(ctx, in.BotID, in.ChannelType, in.Send))
		},
		MethodReact: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in channelInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			return nil, runtimeRpc.Public(channelRuntime.React(ctx, in.BotID, in.ChannelType, in.React))
		},
		MethodStatuses: func(_ context.Context, raw json.RawMessage) (any, error) {
			var botID string
			if err := decode(raw, &botID); err != nil {
				return nil, err
			}
			return channelRuntime.ConnectionStatusesByBot(botID), nil
		},
		MethodRefreshEmail: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var id string
			if err := decode(raw, &id); err != nil {
				return nil, err
			}
			return nil, runtimeRpc.Public(emailRuntime.RefreshProvider(ctx, id))
		},
		MethodSendEmail: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				BotID, ProviderID string
				Message           email.OutboundEmail
			}
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			// Public: SMTP/Mailgun failure detail feeds the send_email
			// tool's self-correction loop.
			out, err := emailRuntime.SendEmail(ctx, in.BotID, in.ProviderID, in.Message)
			return out, runtimeRpc.Public(err)
		},
		MethodTunnelStatus: func(context.Context, json.RawMessage) (any, error) { return tunnel.Status(), nil },
	}
}

var (
	_ channel.Runtime = (*Client)(nil)
	_ email.Runtime   = (*Client)(nil)
)
