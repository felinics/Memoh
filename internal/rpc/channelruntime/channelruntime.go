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
	"github.com/felinics/memoh/internal/rpc"
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

func (c *Client) Status() webhooktunnel.Status {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out webhooktunnel.Status
	if c.call(ctx, MethodTunnelStatus, nil, &out) != nil {
		return webhooktunnel.Status{Enabled: false, Mode: "unavailable", Status: webhooktunnel.StatusError}
	}
	return out
}

// reasons is the wire table of the channel sentinels, read by the server
// encoding and the client decoding. Entries are matched in order.
var reasons = rpc.Reasons{
	{Err: channel.ErrChannelConfigNotFound, Reason: reasonConfigNotFound, Code: codes.NotFound, Message: "channel config not found"},
	{Err: channel.ErrChannelDiscoveryFailed, Reason: reasonDiscoveryFailed, Code: codes.FailedPrecondition, Message: "channel discovery failed"},
	{Err: channel.ErrEnableChannelFailed, Reason: reasonEnableFailed, Code: codes.FailedPrecondition, Message: "channel enable failed"},
	{Err: channel.ErrInvalidWebhookEndpoint, Reason: reasonInvalidWebhook, Code: codes.InvalidArgument, Message: "invalid channel webhook endpoint"},
	{Err: channel.ErrWebhookEndpointUnsupported, Reason: reasonWebhookUnsupported, Code: codes.Unimplemented, Message: "channel webhook endpoint unsupported"},
}

// reasonDetailSep separates the stable reason token from the original error
// text in the legacy status message. A non-printable unit separator cannot
// collide with error message content.
const reasonDetailSep = "\x1f"

func (c *Client) call(ctx context.Context, method string, input, output any) error {
	err := c.rpc.Call(ctx, method, input, output)
	if err == nil || errors.Is(err, runtimeRpc.ErrUnavailable) {
		return err
	}
	return restoreChannelError(err)
}

// restoreChannelError maps a wire error back to its channel sentinel, keeping
// any transported cause text so operators keep seeing the platform-side cause
// (e.g. the getMe failure behind a discovery error). The envelope comes first,
// then the legacy status message.
func restoreChannelError(err error) error {
	if restored := reasons.Decode(err); restored != nil {
		return restored
	}
	message := status.Convert(err).Message()
	for _, entry := range reasons {
		if message == entry.Reason {
			return rpc.Restored(entry.Err, err)
		}
		if detail, ok := strings.CutPrefix(message, entry.Reason+reasonDetailSep); ok {
			return rpc.Restored(rpc.WithAdapterMessage(entry.Err, detail), err)
		}
	}
	return err
}

// safeChannelError encodes a channel sentinel as its stable reason token
// followed by the full original error text, letting the peer restore both the
// sentinel identity and the pre-split message.
func safeChannelError(err error) error {
	entry, ok := reasons.Lookup(err)
	if !ok {
		return err
	}
	return status.Error(entry.Code, entry.Reason+reasonDetailSep+err.Error())
}

func Handlers(channelRuntime channel.Runtime, tunnel *webhooktunnel.Manager) map[string]runtimeRpc.Handler {
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
		MethodTunnelStatus: func(context.Context, json.RawMessage) (any, error) { return tunnel.Status(), nil },
	}
}

var _ channel.Runtime = (*Client)(nil)
