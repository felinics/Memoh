package link

import linkpersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"

// LinkCode is the public one-time account-link code returned by the service.
type LinkCode = linkpersistence.LinkCode

// IssueLinkCodeRequest is the body for generating a link code.
type IssueLinkCodeRequest struct {
	ChannelType string `json:"channel_type,omitempty"`
}

// ListBindingsResponse wraps the connected channel identities for a user.
type ListBindingsResponse struct {
	Items []linkpersistence.Binding `json:"items"`
}
