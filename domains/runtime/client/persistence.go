package client

import (
	"context"
	"time"
)

// CredentialRecord is the durable portion of a registered Remote Runtime.
// Live connection metadata remains owned by Service and Hub.
type CredentialRecord struct {
	ID        string
	UserID    string
	Name      string
	APIToken  string //nolint:gosec // owner-readable Remote Runtime credential by product design
	CreatedAt time.Time
}

type CreateCredentialInput struct {
	UserID   string
	Name     string
	APIToken string //nolint:gosec // owner-readable Remote Runtime credential by product design
}

// CredentialStore is the consumer-owned persistence port for Remote Runtime
// credentials. Runtime connection routing is intentionally outside this port.
type CredentialStore interface {
	CreateCredential(context.Context, CreateCredentialInput) (CredentialRecord, error)
	FindCredentialByAPIToken(context.Context, string) (CredentialRecord, error)
	ListCredentials(context.Context, string) ([]CredentialRecord, error)
	RevokeCredential(context.Context, string, string) error
}

// MembershipReader reports whether a user currently has an active membership
// in the request's team and an active backing principal.
type MembershipReader interface {
	HasActiveTeamMembership(context.Context, string) (bool, error)
}
