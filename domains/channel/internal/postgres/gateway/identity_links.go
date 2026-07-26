package gateway

import (
	"context"
)

type identityLinkReader interface {
	ListUserIDsByChannelIdentity(context.Context, string) ([]string, error)
}

func (s *Store) ListUserIDs(ctx context.Context, identityID string) ([]string, error) {
	return s.identityLinks.ListUserIDsByChannelIdentity(ctx, identityID)
}
