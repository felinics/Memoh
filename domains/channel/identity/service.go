package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/memohai/memoh/domains/channel"
)

// Service provides channel identity lifecycle operations.
type Service struct {
	store Store
}

var ErrChannelIdentityNotFound = errors.New("channel identity not found")

// NewService creates a new channel identity service.
func NewService(_ *slog.Logger, store Store) *Service {
	return &Service{store: store}
}

// Create creates a new channel identity for the given channel subject.
func (s *Service) Create(ctx context.Context, channel, channelSubjectID, displayName string) (ChannelIdentity, error) {
	if s.store == nil {
		return ChannelIdentity{}, errors.New("channel identity persistence not configured")
	}
	channel = normalizeChannel(channel)
	channelSubjectID = strings.TrimSpace(channelSubjectID)
	if channel == "" || channelSubjectID == "" {
		return ChannelIdentity{}, errors.New("channel and channel_subject_id are required")
	}
	return s.store.Create(ctx, WriteInput{
		Channel:          channel,
		ChannelSubjectID: channelSubjectID,
		DisplayName:      strings.TrimSpace(displayName),
		Metadata:         map[string]any{},
	})
}

// GetByID returns a channel identity by its ID.
func (s *Service) GetByID(ctx context.Context, channelIdentityID string) (ChannelIdentity, error) {
	if s.store == nil {
		return ChannelIdentity{}, errors.New("channel identity persistence not configured")
	}
	return s.store.FindByID(ctx, channelIdentityID)
}

// ListIdentityProjections returns current details for the requested identities.
// Missing identities are omitted.
func (s *Service) ListIdentityProjections(ctx context.Context, ids []string) ([]channel.IdentityProjection, error) {
	if s.store == nil {
		return nil, errors.New("channel identity persistence not configured")
	}
	if len(ids) == 0 {
		return []channel.IdentityProjection{}, nil
	}
	rows, err := s.store.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]channel.IdentityProjection, 0, len(rows))
	for _, row := range rows {
		items = append(items, channel.IdentityProjection{
			ID:               row.ID,
			Channel:          row.Channel,
			ChannelSubjectID: row.ChannelSubjectID,
			DisplayName:      row.DisplayName,
			AvatarURL:        row.AvatarURL,
		})
	}
	return items, nil
}

// Canonicalize validates and returns the same channel identity ID.
func (s *Service) Canonicalize(ctx context.Context, channelIdentityID string) (string, error) {
	if s.store == nil {
		return "", errors.New("channel identity persistence not configured")
	}
	_, err := s.store.FindByID(ctx, channelIdentityID)
	if err != nil {
		return "", err
	}
	return channelIdentityID, nil
}

// ResolveByChannelIdentity looks up or creates a channel identity for (channel, channel_subject_id).
// Optional meta may contain avatar_url which is stored as a dedicated column.
func (s *Service) ResolveByChannelIdentity(ctx context.Context, channel, channelSubjectID, displayName string, meta map[string]any) (ChannelIdentity, error) {
	if s.store == nil {
		return ChannelIdentity{}, errors.New("channel identity persistence not configured")
	}
	channel = normalizeChannel(channel)
	channelSubjectID = strings.TrimSpace(channelSubjectID)
	if channel == "" || channelSubjectID == "" {
		return ChannelIdentity{}, errors.New("channel and channel_subject_id are required")
	}

	avatarURL := ""
	if meta != nil {
		if raw, ok := meta["avatar_url"]; ok {
			avatarURL = strings.TrimSpace(fmt.Sprint(raw))
		}
	}

	return s.store.Upsert(ctx, WriteInput{
		Channel:          channel,
		ChannelSubjectID: channelSubjectID,
		DisplayName:      strings.TrimSpace(displayName),
		AvatarURL:        avatarURL,
		Metadata:         map[string]any{},
	})
}

// UpsertChannelIdentity creates or updates a channel identity mapping.
func (s *Service) UpsertChannelIdentity(ctx context.Context, channel, channelSubjectID, displayName string, metadata map[string]any) (ChannelIdentity, error) {
	if s.store == nil {
		return ChannelIdentity{}, errors.New("channel identity persistence not configured")
	}
	channel = normalizeChannel(channel)
	channelSubjectID = strings.TrimSpace(channelSubjectID)
	if metadata == nil {
		metadata = map[string]any{}
	}
	avatarURL := ""
	if raw, ok := metadata["avatar_url"]; ok {
		avatarURL = strings.TrimSpace(fmt.Sprint(raw))
	}
	return s.store.Upsert(ctx, WriteInput{
		Channel:          channel,
		ChannelSubjectID: channelSubjectID,
		DisplayName:      strings.TrimSpace(displayName),
		AvatarURL:        avatarURL,
		Metadata:         metadata,
	})
}

// ListCanonicalChannelIdentities returns the requested channel identity.
func (s *Service) ListCanonicalChannelIdentities(ctx context.Context, channelIdentityID string) ([]ChannelIdentity, error) {
	if s.store == nil {
		return nil, errors.New("channel identity persistence not configured")
	}
	identity, err := s.store.FindByID(ctx, channelIdentityID)
	if err != nil {
		return nil, err
	}
	return []ChannelIdentity{identity}, nil
}

// ListUserIDsByChannelIdentity returns account user IDs currently linked to a
// channel identity. Callers that need a runtime owner should require exactly one
// result; an unbound or ambiguously bound channel identity cannot safely own a
// workspace runtime.
func (s *Service) ListUserIDsByChannelIdentity(ctx context.Context, channelIdentityID string) ([]string, error) {
	if s.store == nil {
		return nil, errors.New("channel identity persistence not configured")
	}
	return s.store.ListUserIDs(ctx, channelIdentityID)
}

// Search returns locally observed channel identities for UI search.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if s.store == nil {
		return nil, errors.New("channel identity persistence not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.store.Search(ctx, strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	items := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		items = append(items, SearchResult{ChannelIdentity: row})
	}
	return items, nil
}

func normalizeChannel(channel string) string {
	return strings.ToLower(strings.TrimSpace(channel))
}
