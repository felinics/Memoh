package identities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// Service provides channel identity lifecycle operations.
type Service struct {
	queries dbstore.Queries
	logger  *slog.Logger
}

var ErrChannelIdentityNotFound = errors.New("channel identity not found")

// NewService creates a new channel identity service.
func NewService(log *slog.Logger, queries dbstore.Queries) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries: queries,
		logger:  log.With(slog.String("service", "channel/identities")),
	}
}

// Create creates a new channel identity for the given channel subject.
func (s *Service) Create(ctx context.Context, channel, channelSubjectID, displayName string) (ChannelIdentity, error) {
	if s.queries == nil {
		return ChannelIdentity{}, errors.New("channel identity queries not configured")
	}
	channel = normalizeChannel(channel)
	channelSubjectID = strings.TrimSpace(channelSubjectID)
	if channel == "" || channelSubjectID == "" {
		return ChannelIdentity{}, errors.New("channel and channel_subject_id are required")
	}
	row, err := s.queries.CreateChannelIdentity(ctx, sqlc.CreateChannelIdentityParams{
		ChannelType:      channel,
		ChannelSubjectID: channelSubjectID,
		DisplayName:      toPgText(displayName),
		AvatarUrl:        pgtype.Text{},
		Metadata:         emptyMetadataBytes(),
	})
	if err != nil {
		return ChannelIdentity{}, err
	}
	return toChannelIdentity(row), nil
}

// GetByID returns a channel identity by its ID.
func (s *Service) GetByID(ctx context.Context, channelIdentityID string) (ChannelIdentity, error) {
	if s.queries == nil {
		return ChannelIdentity{}, errors.New("channel identity queries not configured")
	}
	pgID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return ChannelIdentity{}, err
	}
	row, err := s.queries.GetChannelIdentityByID(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelIdentity{}, ErrChannelIdentityNotFound
		}
		return ChannelIdentity{}, err
	}
	return toChannelIdentity(row), nil
}

// Canonicalize validates and returns the same channel identity ID.
func (s *Service) Canonicalize(ctx context.Context, channelIdentityID string) (string, error) {
	if s.queries == nil {
		return "", errors.New("channel identity queries not configured")
	}
	pgID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return "", err
	}
	_, err = s.queries.GetChannelIdentityByID(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrChannelIdentityNotFound
		}
		return "", err
	}
	return channelIdentityID, nil
}

// ResolveByChannelIdentity looks up or creates a channel identity for (channel, channel_subject_id).
// Optional meta may contain avatar_url which is stored as a dedicated column.
func (s *Service) ResolveByChannelIdentity(ctx context.Context, channel, channelSubjectID, displayName string, meta map[string]any) (ChannelIdentity, error) {
	if s.queries == nil {
		return ChannelIdentity{}, errors.New("channel identity queries not configured")
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

	row, err := s.queries.UpsertChannelIdentityByChannelSubject(ctx, sqlc.UpsertChannelIdentityByChannelSubjectParams{
		ChannelType:      channel,
		ChannelSubjectID: channelSubjectID,
		DisplayName:      toPgText(displayName),
		AvatarUrl:        toPgText(avatarURL),
		Metadata:         emptyMetadataBytes(),
	})
	if err != nil {
		return ChannelIdentity{}, err
	}
	return toChannelIdentity(row), nil
}

// UpsertChannelIdentity creates or updates a channel identity mapping.
func (s *Service) UpsertChannelIdentity(ctx context.Context, channel, channelSubjectID, displayName string, metadata map[string]any) (ChannelIdentity, error) {
	if s.queries == nil {
		return ChannelIdentity{}, errors.New("channel identity queries not configured")
	}
	channel = normalizeChannel(channel)
	channelSubjectID = strings.TrimSpace(channelSubjectID)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return ChannelIdentity{}, err
	}
	avatarURL := ""
	if raw, ok := metadata["avatar_url"]; ok {
		avatarURL = strings.TrimSpace(fmt.Sprint(raw))
	}
	row, err := s.queries.UpsertChannelIdentityByChannelSubject(ctx, sqlc.UpsertChannelIdentityByChannelSubjectParams{
		ChannelType:      channel,
		ChannelSubjectID: channelSubjectID,
		DisplayName:      toPgText(displayName),
		AvatarUrl:        toPgText(avatarURL),
		Metadata:         metaBytes,
	})
	if err != nil {
		return ChannelIdentity{}, err
	}
	return toChannelIdentity(row), nil
}

// ListCanonicalChannelIdentities returns the requested channel identity.
func (s *Service) ListCanonicalChannelIdentities(ctx context.Context, channelIdentityID string) ([]ChannelIdentity, error) {
	if s.queries == nil {
		return nil, errors.New("channel identity queries not configured")
	}
	pgChannelIdentityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return nil, err
	}
	row, err := s.queries.GetChannelIdentityByID(ctx, pgChannelIdentityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrChannelIdentityNotFound
		}
		return nil, err
	}
	return []ChannelIdentity{toChannelIdentity(row)}, nil
}

// ListUserIDsByChannelIdentity returns account user IDs currently linked to a
// channel identity. Callers that need a runtime owner should require exactly one
// result; an unbound or ambiguously bound channel identity cannot safely own a
// workspace runtime.
func (s *Service) ListUserIDsByChannelIdentity(ctx context.Context, channelIdentityID string) ([]string, error) {
	if s.queries == nil {
		return nil, errors.New("channel identity queries not configured")
	}
	pgChannelIdentityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListUserIDsByChannelIdentity(ctx, pgChannelIdentityID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		userID := strings.TrimSpace(id.String())
		if userID != "" {
			out = append(out, userID)
		}
	}
	return out, nil
}

// Search returns locally observed channel identities for UI search.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if s.queries == nil {
		return nil, errors.New("channel identity queries not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.queries.SearchChannelIdentities(ctx, sqlc.SearchChannelIdentitiesParams{
		Query:      strings.TrimSpace(query),
		LimitCount: int32(limit), //nolint:gosec // limit is capped above
	})
	if err != nil {
		return nil, err
	}
	items := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		item := SearchResult{
			ChannelIdentity: toChannelIdentity(sqlc.ChannelIdentity{
				ID:               row.ID,
				ChannelType:      row.ChannelType,
				ChannelSubjectID: row.ChannelSubjectID,
				DisplayName:      row.DisplayName,
				AvatarUrl:        row.AvatarUrl,
				Metadata:         row.Metadata,
				CreatedAt:        row.CreatedAt,
				UpdatedAt:        row.UpdatedAt,
			}),
		}
		items = append(items, item)
	}
	return items, nil
}

func toChannelIdentity(row sqlc.ChannelIdentity) ChannelIdentity {
	var metadata map[string]any
	if len(row.Metadata) > 0 {
		_ = json.Unmarshal(row.Metadata, &metadata)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	displayName := ""
	if row.DisplayName.Valid {
		displayName = strings.TrimSpace(row.DisplayName.String)
	}
	avatarURL := ""
	if row.AvatarUrl.Valid {
		avatarURL = strings.TrimSpace(row.AvatarUrl.String)
	}
	return ChannelIdentity{
		ID:               row.ID.String(),
		Channel:          row.ChannelType,
		ChannelSubjectID: row.ChannelSubjectID,
		DisplayName:      displayName,
		AvatarURL:        avatarURL,
		Metadata:         metadata,
		CreatedAt:        db.TimeFromPg(row.CreatedAt),
		UpdatedAt:        db.TimeFromPg(row.UpdatedAt),
	}
}

func normalizeChannel(channel string) string {
	return strings.ToLower(strings.TrimSpace(channel))
}

func toPgText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{
		String: value,
		Valid:  value != "",
	}
}

func emptyMetadataBytes() []byte {
	return []byte("{}")
}
