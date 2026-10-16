package channelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"google.golang.org/grpc/codes"

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
	reasonSendTargetRequired = "channel.send_target_required"
	reasonBindingRequired    = "channel.binding_required"
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
	{Err: channel.ErrSendTargetRequired, Reason: reasonSendTargetRequired, Code: codes.InvalidArgument, Message: "channel send target required"},
	{Err: channel.ErrChannelBindingRequired, Reason: reasonBindingRequired, Code: codes.FailedPrecondition, Message: "channel binding required"},
}

func (c *Client) call(ctx context.Context, method string, input, output any) error {
	err := c.rpc.Call(ctx, method, input, output)
	if err == nil || errors.Is(err, runtimeRpc.ErrUnavailable) {
		return err
	}
	return restoreChannelError(err)
}

// restoreChannelError maps a wire error back to its channel sentinel from the
// error envelope, keeping any transported cause text so operators keep seeing
// the platform-side cause (e.g. the getMe failure behind a discovery error).
// A status without the envelope is returned unchanged.
func restoreChannelError(err error) error {
	if restored := reasons.Decode(err); restored != nil {
		return restored
	}
	return err
}

// safeChannelError encodes a channel sentinel as its reason, with the full
// original error text as the adapter message, letting the peer restore both
// the sentinel identity and the pre-split message.
func safeChannelError(err error) error {
	entry, ok := reasons.Lookup(err)
	if !ok {
		return err
	}
	return entry.Status(err.Error())
}

// deliveryError encodes the failure of a send or a reaction. A channel
// sentinel travels as its reason. Any other failure carries the platform
// adapter's own text ("telegram: chat not found"), which callers surface to
// users and the agent uses to self-correct.
func deliveryError(err error) error {
	if _, ok := reasons.Lookup(err); ok {
		return safeChannelError(err)
	}
	return runtimeRpc.Public(err)
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
			return nil, deliveryError(channelRuntime.Send(ctx, in.BotID, in.ChannelType, in.Send))
		},
		MethodReact: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in channelInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			return nil, deliveryError(channelRuntime.React(ctx, in.BotID, in.ChannelType, in.React))
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
