package identity

import (
	"context"

	"github.com/memohai/memoh/domains/agent/application"
	"github.com/memohai/memoh/domains/channel/identity"
)

type identityReader interface {
	GetByID(context.Context, string) (identity.ChannelIdentity, error)
}

type Reader struct {
	identities identityReader
}

func NewReader(identities identityReader) *Reader {
	return &Reader{identities: identities}
}

func (r *Reader) GetByID(ctx context.Context, id string) (application.ChannelIdentity, error) {
	identity, err := r.identities.GetByID(ctx, id)
	if err != nil {
		return application.ChannelIdentity{}, err
	}
	return application.ChannelIdentity{
		ID:          identity.ID,
		DisplayName: identity.DisplayName,
	}, nil
}

var _ application.ChannelIdentityReader = (*Reader)(nil)
