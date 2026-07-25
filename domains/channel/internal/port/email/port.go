// Package email is the Channel-owned private SPI for email persistence and
// provider adapters. Public domains/channel/email and private implementations
// both consume these types; adapters must not import the public email package.
package email

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

var ErrNotFound = errors.New("email record not found")

type ProviderName string

type FieldSchema struct {
	Key         string
	Type        string
	Title       string
	Description string
	Required    bool
	Enum        []string
	Example     any
	Order       int
}

type ConfigSchema struct {
	Fields []FieldSchema
}

type ProviderMeta struct {
	Provider     string
	DisplayName  string
	ConfigSchema ConfigSchema
}

type OutboundEmail struct {
	To      []string
	Subject string
	Body    string
	HTML    bool
}

type InboundEmail struct {
	MessageID   string
	From        string
	To          []string
	Subject     string
	BodyText    string
	BodyHTML    string
	Attachments []any
	Headers     map[string]any
	ReceivedAt  time.Time
}

// Adapter is the base interface every email adapter must implement.
type Adapter interface {
	Type() ProviderName
	Meta() ProviderMeta
	NormalizeConfig(raw map[string]any) (map[string]any, error)
}

// Sender sends outbound emails.
type Sender interface {
	Send(ctx context.Context, config map[string]any, msg OutboundEmail) (messageID string, err error)
}

// Receiver establishes a long-lived connection (IMAP IDLE / polling) to receive emails.
type Receiver interface {
	StartReceiving(ctx context.Context, config map[string]any, handler InboundHandler) (Stopper, error)
}

// WebhookReceiver handles inbound emails via HTTP webhook callbacks.
type WebhookReceiver interface {
	HandleWebhook(ctx context.Context, config map[string]any, r *http.Request) (*InboundEmail, error)
}

// MailboxReader lists and reads emails directly from the remote mailbox.
type MailboxReader interface {
	ListMailbox(ctx context.Context, config map[string]any, page, pageSize int) ([]InboundEmail, int, error)
	ReadMailbox(ctx context.Context, config map[string]any, uid uint32) (*InboundEmail, error)
}

// Deleter removes an email from the remote mailbox.
type Deleter interface {
	DeleteRemote(ctx context.Context, config map[string]any, messageID string) error
}

// InboundHandler is invoked when a new email arrives.
type InboundHandler func(ctx context.Context, providerID string, email InboundEmail) error

// Stopper represents a stoppable background process.
type Stopper interface {
	Stop(ctx context.Context) error
}

// OAuthClient is the narrow OAuth client descriptor consumed by Gmail.
type OAuthClient struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// OAuthClientResolver looks up built-in OAuth client credentials by ref.
type OAuthClientResolver interface {
	Get(ref string) (OAuthClient, bool)
	HasUsableClient(ref string) bool
}

// ProviderRecord is the persistence-neutral email provider state.
type ProviderRecord struct {
	ID        string
	UserID    string
	Name      string
	Provider  string
	Config    json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateProviderInput struct {
	UserID   string
	Name     string
	Provider string
	Config   json.RawMessage
}

type UpdateProviderInput struct {
	ID       string
	UserID   string
	Name     string
	Provider string
	Config   json.RawMessage
}

// ProviderStore is the persistence port consumed by provider operations.
type ProviderStore interface {
	CreateProvider(context.Context, CreateProviderInput) (ProviderRecord, error)
	FindProvider(context.Context, string) (ProviderRecord, error)
	FindProviderForUser(context.Context, string, string) (ProviderRecord, error)
	FindProviderByName(context.Context, string, string) (ProviderRecord, error)
	ListProviders(context.Context, string) ([]ProviderRecord, error)
	ListProvidersForUser(context.Context, string, string) ([]ProviderRecord, error)
	UpdateProvider(context.Context, UpdateProviderInput) (ProviderRecord, error)
	DeleteProvider(context.Context, string, string) error
}

// BindingRecord is the persistence-neutral bot email binding state.
type BindingRecord struct {
	ID              string
	BotID           string
	EmailProviderID string
	EmailAddress    string
	CanRead         bool
	CanWrite        bool
	CanDelete       bool
	Config          json.RawMessage
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CreateBindingInput struct {
	BotID           string
	EmailProviderID string
	EmailAddress    string
	CanRead         bool
	CanWrite        bool
	CanDelete       bool
	Config          json.RawMessage
}

type UpdateBindingInput struct {
	ID           string
	EmailAddress string
	CanRead      bool
	CanWrite     bool
	CanDelete    bool
	Config       json.RawMessage
}

// BindingStore is the persistence port consumed by binding operations.
type BindingStore interface {
	CreateBinding(context.Context, CreateBindingInput) (BindingRecord, error)
	FindBinding(context.Context, string) (BindingRecord, error)
	ListBindings(context.Context, string) ([]BindingRecord, error)
	ListReadableBindings(context.Context, string) ([]BindingRecord, error)
	UpdateBinding(context.Context, UpdateBindingInput) (BindingRecord, error)
	DeleteBinding(context.Context, string) error
}

// OutboxRecord is the persistence-neutral outbound email audit state.
type OutboxRecord struct {
	ID          string
	ProviderID  string
	BotID       string
	MessageID   string
	FromAddress string
	ToAddresses json.RawMessage
	Subject     string
	BodyText    string
	BodyHTML    string
	Attachments json.RawMessage
	Status      string
	Error       string
	SentAt      time.Time
	CreatedAt   time.Time
}

type CreateOutboxInput struct {
	ProviderID  string
	BotID       string
	FromAddress string
	ToAddresses json.RawMessage
	Subject     string
	BodyText    string
	BodyHTML    string
	Attachments json.RawMessage
	Status      string
}

// OutboxStore is the persistence port consumed by outbox operations.
type OutboxStore interface {
	CreateOutbox(context.Context, CreateOutboxInput) (OutboxRecord, error)
	MarkOutboxSent(context.Context, string, string) error
	MarkOutboxFailed(context.Context, string, string) error
	FindOutbox(context.Context, string) (OutboxRecord, error)
	ListOutboxByBot(context.Context, string, int32, int32) ([]OutboxRecord, error)
	CountOutboxByBot(context.Context, string) (int64, error)
}

// OAuthToken holds a stored OAuth2 token for an email provider.
type OAuthToken struct {
	ProviderID   string
	EmailAddress string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scope        string
}

// OAuthTokenStore persists and retrieves OAuth tokens for email providers.
type OAuthTokenStore interface {
	Get(ctx context.Context, providerID string) (*OAuthToken, error)
	Save(ctx context.Context, t OAuthToken) error
	SetPendingState(ctx context.Context, providerID, state string) error
	GetByState(ctx context.Context, state string) (*OAuthToken, error)
	Delete(ctx context.Context, providerID string) error
}
