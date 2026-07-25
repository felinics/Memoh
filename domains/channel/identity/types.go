package identity

import (
	"context"
	"time"
)

// ChannelIdentity is a unified inbound identity subject across channels.
type ChannelIdentity struct {
	ID               string         `json:"id"`
	Channel          string         `json:"channel"`
	ChannelSubjectID string         `json:"channel_subject_id"`
	DisplayName      string         `json:"display_name,omitempty"`
	AvatarURL        string         `json:"avatar_url,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type SearchResult struct {
	ChannelIdentity
}

// WriteInput is the persistence-neutral input for creating or updating an identity.
type WriteInput struct {
	Channel          string
	ChannelSubjectID string
	DisplayName      string
	AvatarURL        string
	Metadata         map[string]any
}

// Store is the identity persistence surface consumed by Service.
type Store interface {
	Create(context.Context, WriteInput) (ChannelIdentity, error)
	FindByID(context.Context, string) (ChannelIdentity, error)
	ListByIDs(context.Context, []string) ([]ChannelIdentity, error)
	Upsert(context.Context, WriteInput) (ChannelIdentity, error)
	ListUserIDs(context.Context, string) ([]string, error)
	Search(context.Context, string, int) ([]ChannelIdentity, error)
}
