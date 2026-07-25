package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
)

// ErrStopNotSupported is returned when a connection does not support graceful shutdown.
var ErrStopNotSupported = errors.New("channel connection stop not supported")

// InboundHandler is a callback invoked when a message arrives from a channel.
type InboundHandler func(ctx context.Context, cfg ChannelConfig, msg InboundMessage) error

// StreamReplySender sends replies within a single inbound-processing scope.
// It supports both one-shot delivery and streaming sessions.
type StreamReplySender interface {
	Send(ctx context.Context, msg OutboundMessage) error
	OpenStream(ctx context.Context, target string, opts StreamOptions) (OutboundStream, error)
}

// PreparedOutboundStream is the adapter-facing stream session.
type PreparedOutboundStream interface {
	Push(ctx context.Context, event PreparedStreamEvent) error
	Close(ctx context.Context) error
}

// OutboundStream is a live stream session for emitting outbound events.
// Implementations are not required to be safe for concurrent use; callers
// must serialize Push and Close calls within a single goroutine.
type OutboundStream interface {
	Push(ctx context.Context, event StreamEvent) error
	Close(ctx context.Context) error
}

// ProcessingStatusInfo carries context for channel-level processing status updates.
type ProcessingStatusInfo struct {
	BotID             string
	ChatID            string
	RouteID           string
	ChannelIdentityID string
	UserID            string
	Query             string
	ReplyTarget       string
	SourceMessageID   string
}

// ProcessingStatusHandle stores channel-specific state between status callbacks.
type ProcessingStatusHandle struct {
	Token string
}

// ProcessingStatusNotifier reports processing lifecycle updates to channel platforms.
// Implementations should be best-effort and idempotent.
type ProcessingStatusNotifier interface {
	ProcessingStarted(ctx context.Context, cfg ChannelConfig, msg InboundMessage, info ProcessingStatusInfo) (ProcessingStatusHandle, error)
	ProcessingCompleted(ctx context.Context, cfg ChannelConfig, msg InboundMessage, info ProcessingStatusInfo, handle ProcessingStatusHandle) error
	ProcessingFailed(ctx context.Context, cfg ChannelConfig, msg InboundMessage, info ProcessingStatusInfo, handle ProcessingStatusHandle, cause error) error
}

// AttachmentPayload contains resolved attachment bytes and optional metadata.
// Caller must close Reader.
type AttachmentPayload struct {
	Reader io.ReadCloser
	Mime   string
	Name   string
	Size   int64
}

// AttachmentResolver resolves attachment references (for example platform_key)
// into readable bytes for persistence or transformation pipelines.
type AttachmentResolver interface {
	ResolveAttachment(ctx context.Context, cfg ChannelConfig, attachment Attachment) (AttachmentPayload, error)
}

// Adapter is the base interface every channel adapter must implement.
type Adapter interface {
	Type() ChannelType
	Descriptor() Descriptor
}

// Descriptor holds read-only metadata and static policy flags for a registered
// channel type. Request handling and platform operations are expressed through
// optional interfaces.
type Descriptor struct {
	Type               ChannelType
	DisplayName        string
	Configless         bool
	AckDisabledWebhook bool
	Capabilities       ChannelCapabilities
	OutboundPolicy     OutboundPolicy
	ConfigSchema       ConfigSchema
	UserConfigSchema   ConfigSchema
	TargetSpec         TargetSpec
}

// ConfigNormalizer validates and normalizes channel and user-binding configurations.
type ConfigNormalizer interface {
	NormalizeConfig(raw map[string]any) (map[string]any, error)
	NormalizeUserConfig(raw map[string]any) (map[string]any, error)
}

// TargetResolver handles delivery target normalization and resolution from user bindings.
type TargetResolver interface {
	NormalizeTarget(raw string) string
	ResolveTarget(userConfig map[string]any) (string, error)
}

// BindingMatcher matches user-channel bindings and constructs binding configs from identities.
type BindingMatcher interface {
	MatchBinding(config map[string]any, criteria BindingCriteria) bool
	BuildUserConfig(identity Identity) map[string]any
}

// Sender is an adapter capable of sending outbound messages.
type Sender interface {
	Send(ctx context.Context, cfg ChannelConfig, msg PreparedOutboundMessage) error
}

// PreparedOutboundValidator lets adapters reject platform-invalid prepared
// messages before a batched/chunked send starts delivering earlier items.
type PreparedOutboundValidator interface {
	ValidatePreparedOutbound(ctx context.Context, cfg ChannelConfig, target string, msg PreparedOutboundMessage) error
}

// OutboundCapabilityResolver can refine static descriptor capabilities for a
// specific outbound config and target when platform support is runtime-gated.
type OutboundCapabilityResolver interface {
	ResolveOutboundCapabilities(cfg ChannelConfig, target string, base ChannelCapabilities) ChannelCapabilities
}

// OutboundTargetResolver can resolve delivery aliases to the concrete platform
// target before outbound capability checks run.
type OutboundTargetResolver interface {
	ResolveOutboundTarget(ctx context.Context, cfg ChannelConfig, target string) (string, error)
}

// StreamSender is an adapter capable of opening outbound stream sessions.
type StreamSender interface {
	OpenStream(ctx context.Context, cfg ChannelConfig, target string, opts StreamOptions) (PreparedOutboundStream, error)
}

// MessageEditor updates and deletes already-sent messages when supported.
type MessageEditor interface {
	Update(ctx context.Context, cfg ChannelConfig, target string, messageID string, msg PreparedMessage) error
	Unsend(ctx context.Context, cfg ChannelConfig, target string, messageID string) error
}

// Reactor adds or removes emoji reactions on messages.
type Reactor interface {
	React(ctx context.Context, cfg ChannelConfig, target string, messageID string, emoji string) error
	Unreact(ctx context.Context, cfg ChannelConfig, target string, messageID string, emoji string) error
}

// SelfDiscoverer retrieves the adapter bot's own identity from the iam.
// The returned map is merged into ChannelConfig.SelfIdentity and persisted.
type SelfDiscoverer interface {
	DiscoverSelf(ctx context.Context, credentials map[string]any) (identity map[string]any, externalID string, err error)
}

// SelfIdentityPolicy describes adapter-specific requirements for persisting a
// platform bot identity discovered from credentials.
type SelfIdentityPolicy struct {
	RefreshOnCredentialsChange       bool
	RequireDiscoveryOnEnable         bool
	RequiredSelfIdentityKey          string
	DiscoveryErrorMessage            string
	MissingIdentityMessage           string
	DuplicateExternalIdentityMessage string
}

// SelfIdentityPolicyProvider lets adapters opt into stricter self-identity
// discovery semantics than the default best-effort behavior.
type SelfIdentityPolicyProvider interface {
	SelfIdentityPolicy() SelfIdentityPolicy
}

// WebhookEndpointSetter updates the platform-side webhook endpoint.
type WebhookEndpointSetter interface {
	SetWebhookEndpoint(ctx context.Context, credentials map[string]any, endpoint string) error
}

// Receiver is an adapter capable of establishing a long-lived connection to receive messages.
type Receiver interface {
	Connect(ctx context.Context, cfg ChannelConfig, handler InboundHandler) (Connection, error)
}

// WebhookReceiver handles inbound HTTP webhook callbacks for webhook-style channels.
// Implementations typically verify the request, parse the platform payload, and
// forward resulting inbound messages to the provided handler.
type WebhookReceiver interface {
	HandleWebhook(ctx context.Context, cfg ChannelConfig, handler InboundHandler, r *http.Request, w http.ResponseWriter) error
}

// Connection represents an active, long-lived link to a channel iam.
type Connection interface {
	ConfigID() string
	BotID() string
	ChannelType() ChannelType
	Stop(ctx context.Context) error
	Running() bool
}

// BaseConnection is a default Connection implementation backed by a stop function.
type BaseConnection struct {
	configID    string
	botID       string
	channelType ChannelType
	stop        func(ctx context.Context) error
	running     atomic.Bool
}

// NewConnection creates a BaseConnection for the given config and stop function.
func NewConnection(cfg ChannelConfig, stop func(ctx context.Context) error) *BaseConnection {
	conn := &BaseConnection{
		configID:    cfg.ID,
		botID:       cfg.BotID,
		channelType: cfg.ChannelType,
		stop:        stop,
	}
	conn.running.Store(true)
	return conn
}

// ConfigID returns the channel configuration identifier.
func (c *BaseConnection) ConfigID() string {
	return c.configID
}

// BotID returns the bot identifier that owns this connection.
func (c *BaseConnection) BotID() string {
	return c.botID
}

// ChannelType returns the type of channel this connection serves.
func (c *BaseConnection) ChannelType() ChannelType {
	return c.channelType
}

// Stop gracefully shuts down the connection.
func (c *BaseConnection) Stop(ctx context.Context) error {
	if c.stop == nil {
		return ErrStopNotSupported
	}
	if err := c.stop(ctx); err != nil {
		return err
	}
	c.running.Store(false)
	return nil
}

// MarkStopped flags the connection as no longer running without
// invoking its stop function. Adapters call this from internal
// goroutines that have given up (terminal protocol error etc.) so
// the manager's Running() check reflects reality and reconciliation
// can act on it.
func (c *BaseConnection) MarkStopped() {
	c.running.Store(false)
}

// Running reports whether the connection is still active.
func (c *BaseConnection) Running() bool {
	return c.running.Load()
}
