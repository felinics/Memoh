package access

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

// Store is the Channel Access persistence port. SQLC stays behind the
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
