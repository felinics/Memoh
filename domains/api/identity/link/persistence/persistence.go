// Package persistence defines account-link persistence ports and records.
package persistence

import (
	"context"
	"time"
)

// CreateLinkCodeCommand is the persistence input for issuing a link code.
type CreateLinkCodeCommand struct {
	Token       string
	UserID      string
	ChannelType string
	ExpiresAt   time.Time
}

// LinkCodeState is the diagnostic projection used when redeem fails.
type LinkCodeState struct {
	ExpiresAt time.Time
	Consumed  bool
}

// ChannelIdentity is the current Channel-owned identity projection used to
// enrich API-owned bindings.
type ChannelIdentity struct {
	ID               string
	Channel          string
	ChannelSubjectID string
	DisplayName      string
	AvatarURL        string
}

// ChannelIdentityReader returns current details for the requested identities.
// Missing identities are omitted.
type ChannelIdentityReader interface {
	ListChannelIdentities(context.Context, []string) ([]ChannelIdentity, error)
}

// Store is the Identity Link persistence port. SQLC stays behind the
// owner-private postgres adapter.
type Store interface {
	CreateLinkCode(ctx context.Context, command CreateLinkCodeCommand) (LinkCode, error)
	FindLinkCode(ctx context.Context, token string) (LinkCodeState, bool, error)
	RedeemLinkCode(ctx context.Context, token, channelIdentityID string) (Binding, bool, error)
	ListBindingsForUser(ctx context.Context, userID string) ([]Binding, error)
	ListBindingsForBot(ctx context.Context, botID string) ([]Binding, error)
	DeleteBinding(ctx context.Context, userID, channelIdentityID string) error
	ListUserIDsByChannelIdentity(ctx context.Context, channelIdentityID string) ([]string, error)
}

// LinkCode is a one-time code a user generates in the web app and then sends as
// `/link <code>` to a bot in IM to bind the sending channel identity to their account.
type LinkCode struct {
	Token       string    `json:"token"`
	UserID      string    `json:"user_id"`
	ChannelType string    `json:"channel_type,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// Binding is a global account-level link between a web user and an IM channel identity.
type Binding struct {
	ID                         string    `json:"id"`
	UserID                     string    `json:"user_id"`
	ChannelIdentityID          string    `json:"channel_identity_id"`
	ChannelType                string    `json:"channel_type,omitempty"`
	ChannelSubjectID           string    `json:"channel_subject_id,omitempty"`
	ChannelIdentityDisplayName string    `json:"channel_identity_display_name,omitempty"`
	ChannelIdentityAvatarURL   string    `json:"channel_identity_avatar_url,omitempty"`
	CreatedAt                  time.Time `json:"created_at"`
}
