package channel

import "context"

// IdentityProjection is the public, persistence-neutral identity view exposed
// across the Channel boundary.
type IdentityProjection struct {
	ID               string
	Channel          string
	ChannelSubjectID string
	DisplayName      string
	AvatarURL        string
}

// IdentityReader returns existing identities and omits requested IDs that are
// not found.
type IdentityReader interface {
	ListIdentityProjections(context.Context, []string) ([]IdentityProjection, error)
}
