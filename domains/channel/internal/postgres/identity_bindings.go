package postgres

import (
	"context"

	"github.com/memohai/memoh/domains/channel/gateway"
)

func (s *Store) UpsertIdentityBinding(ctx context.Context, input gateway.IdentityBindingWrite) (gateway.ChannelIdentityBinding, error) {
	return s.identityBindings.UpsertIdentityBinding(ctx, input)
}

func (s *Store) FindIdentityBinding(ctx context.Context, identityID string, channelType gateway.ChannelType) (gateway.ChannelIdentityBinding, error) {
	return s.identityBindings.FindIdentityBinding(ctx, identityID, channelType)
}

func (s *Store) ListIdentityBindingsByType(ctx context.Context, channelType gateway.ChannelType) ([]gateway.ChannelIdentityBinding, error) {
	return s.identityBindings.ListIdentityBindingsByType(ctx, channelType)
}
