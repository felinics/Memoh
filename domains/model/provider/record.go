package provider

import (
	providerport "github.com/memohai/memoh/domains/model/internal/port/provider"
)

// Persistence sentinels re-exported for handlers and consumers.
var (
	ErrProviderNotFound   = providerport.ErrProviderNotFound
	ErrProviderNameTaken  = providerport.ErrProviderNameTaken
	ErrOAuthTokenNotFound = providerport.ErrOAuthTokenNotFound
)

// ProviderRecord is the persistence-neutral provider snapshot used by credential
// resolution and owner adapters.
type ProviderRecord = providerport.ProviderRecord

// CreateProviderCommand is the persistence write input for creating a provider.
type CreateProviderCommand = providerport.CreateProviderCommand

// CreateProviderFromTemplateCommand is the persistence write input for template materialization.
type CreateProviderFromTemplateCommand = providerport.CreateProviderFromTemplateCommand

// UpdateProviderCommand is the persistence write input for updating a provider.
type UpdateProviderCommand = providerport.UpdateProviderCommand

// OAuthTokenRecord is provider-scoped OAuth credential state.
type OAuthTokenRecord = providerport.OAuthTokenRecord

// OAuthStateUpdate patches pending OAuth authorization state.
type OAuthStateUpdate = providerport.OAuthStateUpdate

// ProviderStore owns Provider CRUD behind the owner-private adapter.
type ProviderStore = providerport.ProviderStore

// OAuthStore owns Provider-scoped OAuth state and tokens.
type OAuthStore = providerport.OAuthStore
