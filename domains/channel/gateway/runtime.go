package gateway

import "context"

// Runtime is the live Channel-process surface consumed by the Server. The
// in-process implementation is composed from Lifecycle, Store, and Manager;
// production Server wiring uses the authenticated RPC implementation.
type Runtime interface {
	UpsertBotChannelConfig(context.Context, string, ChannelType, UpsertConfigRequest) (ChannelConfig, error)
	SetBotChannelStatus(context.Context, string, ChannelType, bool) (ChannelConfig, error)
	DeleteBotChannelConfig(context.Context, string, ChannelType) error
	SetWebhookEndpoint(context.Context, string, ChannelType, SetWebhookEndpointRequest) (SetWebhookEndpointResponse, error)
	Send(context.Context, string, ChannelType, SendRequest) error
	React(context.Context, string, ChannelType, ReactRequest) error
	ConnectionStatusesByBot(string) []ConnectionStatus
}

type LocalRuntime struct {
	Lifecycle *Lifecycle
	Store     *Store
	Manager   *Manager
}

func (r *LocalRuntime) UpsertBotChannelConfig(ctx context.Context, botID string, typ ChannelType, req UpsertConfigRequest) (ChannelConfig, error) {
	return r.Lifecycle.UpsertBotChannelConfig(ctx, botID, typ, req)
}

func (r *LocalRuntime) SetBotChannelStatus(ctx context.Context, botID string, typ ChannelType, disabled bool) (ChannelConfig, error) {
	return r.Lifecycle.SetBotChannelStatus(ctx, botID, typ, disabled)
}

func (r *LocalRuntime) DeleteBotChannelConfig(ctx context.Context, botID string, typ ChannelType) error {
	return r.Lifecycle.DeleteBotChannelConfig(ctx, botID, typ)
}

func (r *LocalRuntime) SetWebhookEndpoint(ctx context.Context, botID string, typ ChannelType, req SetWebhookEndpointRequest) (SetWebhookEndpointResponse, error) {
	return r.Store.SetWebhookEndpoint(ctx, botID, typ, req)
}

func (r *LocalRuntime) Send(ctx context.Context, botID string, typ ChannelType, req SendRequest) error {
	return r.Manager.Send(ctx, botID, typ, req)
}

func (r *LocalRuntime) React(ctx context.Context, botID string, typ ChannelType, req ReactRequest) error {
	return r.Manager.React(ctx, botID, typ, req)
}

func (r *LocalRuntime) ConnectionStatusesByBot(botID string) []ConnectionStatus {
	return r.Manager.ConnectionStatusesByBot(botID)
}
