package email

import (
	"context"
	"errors"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

type oauthTokenStore struct {
	inner emailport.OAuthTokenStore
}

func newOAuthTokenStore(inner emailport.OAuthTokenStore) OAuthTokenStore {
	return &oauthTokenStore{inner: inner}
}

func (s *oauthTokenStore) Get(ctx context.Context, providerID string) (*OAuthToken, error) {
	token, err := s.inner.Get(ctx, providerID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return fromPortOAuthToken(token), nil
}

func (s *oauthTokenStore) Save(ctx context.Context, token OAuthToken) error {
	return mapStoreErr(s.inner.Save(ctx, toPortOAuthToken(token)))
}

func (s *oauthTokenStore) SetPendingState(ctx context.Context, providerID, state string) error {
	return mapStoreErr(s.inner.SetPendingState(ctx, providerID, state))
}

func (s *oauthTokenStore) GetByState(ctx context.Context, state string) (*OAuthToken, error) {
	token, err := s.inner.GetByState(ctx, state)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return fromPortOAuthToken(token), nil
}

func (s *oauthTokenStore) Delete(ctx context.Context, providerID string) error {
	return mapStoreErr(s.inner.Delete(ctx, providerID))
}

type oauthTokenStoreBridge struct {
	public OAuthTokenStore
}

func (b *oauthTokenStoreBridge) Get(ctx context.Context, providerID string) (*emailport.OAuthToken, error) {
	token, err := b.public.Get(ctx, providerID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, emailport.ErrNotFound
		}
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	out := toPortOAuthToken(*token)
	return &out, nil
}

func (b *oauthTokenStoreBridge) Save(ctx context.Context, token emailport.OAuthToken) error {
	return b.public.Save(ctx, *fromPortOAuthToken(&token))
}

func (b *oauthTokenStoreBridge) SetPendingState(ctx context.Context, providerID, state string) error {
	return b.public.SetPendingState(ctx, providerID, state)
}

func (b *oauthTokenStoreBridge) GetByState(ctx context.Context, state string) (*emailport.OAuthToken, error) {
	token, err := b.public.GetByState(ctx, state)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, emailport.ErrNotFound
		}
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	out := toPortOAuthToken(*token)
	return &out, nil
}

func (b *oauthTokenStoreBridge) Delete(ctx context.Context, providerID string) error {
	return b.public.Delete(ctx, providerID)
}
