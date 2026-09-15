package postgresstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// attachmentMetadataKeyFileID is the platform file id every Telegram
// attachment carries, sticker or not.
const attachmentMetadataKeyFileID = "file_id"

// recentStickerSightingsMax bounds the scan. A save names a sticker the
// conversation just showed, so reaching further back buys nothing and only
// widens the window in which two different stickers share a description.
const recentStickerSightingsMax = 50

func (s *Store) RecentStickerSightings(ctx context.Context, sessionID string, limit int, before time.Time) ([]dbstore.StickerSighting, error) {
	sessionUUID, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > recentStickerSightingsMax {
		limit = recentStickerSightingsMax
	}
	// A zero cutoff would select nothing, which reads as "this conversation has
	// no stickers" — a caller that forgot to pass its turn boundary must not be
	// silently answered with an empty list.
	if before.IsZero() {
		before = time.Now()
	}
	rows, err := s.queries.ListRecentStickerAssetsBySession(ctx, dbsqlc.ListRecentStickerAssetsBySessionParams{
		SessionID:    sessionUUID,
		VisibleUntil: pgtype.Timestamptz{Time: before.UTC(), Valid: true},
		MaxCount:     int32(limit),
	})
	if err != nil {
		return nil, mapQueryErr(err)
	}
	sightings := make([]dbstore.StickerSighting, 0, len(rows))
	for _, row := range rows {
		metadata := map[string]any{}
		if len(row.Metadata) > 0 {
			if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
				// Asset metadata is written by the ingress adapters; a row we
				// cannot read is a bad row, not a reason to fail the lookup.
				continue
			}
		}
		sighting, ok := stickerSightingFromMetadata(metadata)
		if !ok {
			continue
		}
		sighting.ContentHash = strings.TrimSpace(row.ContentHash)
		sighting.Mime = strings.TrimSpace(row.Mime)
		sighting.Name = strings.TrimSpace(row.Name)
		sighting.SeenAt = row.CreatedAt.Time
		sightings = append(sightings, sighting)
	}
	return sightings, nil
}

// stickerSightingFromMetadata reads one sighting out of asset metadata.
//
// The sendable reference is sticker_file_id when it exists and the plain
// file_id otherwise, because the adapter only records the former when a
// preview replaced the sticker. Reading file_id first would address that
// preview and send a still picture of an animated sticker; requiring
// sticker_file_id would lose every sticker stored as itself.
func stickerSightingFromMetadata(metadata map[string]any) (dbstore.StickerSighting, bool) {
	uniqueID := metadataString(metadata, attachment.MetadataKeyStickerUniqueID)
	if uniqueID == "" {
		return dbstore.StickerSighting{}, false
	}
	ref := metadataString(metadata, attachment.MetadataKeyStickerFileID)
	if ref == "" {
		ref = metadataString(metadata, attachmentMetadataKeyFileID)
	}
	if ref == "" {
		return dbstore.StickerSighting{}, false
	}
	return dbstore.StickerSighting{
		Ref:      ref,
		UniqueID: uniqueID,
		Pack:     metadataString(metadata, attachment.MetadataKeyStickerSet),
		Emoji:    metadataString(metadata, attachment.MetadataKeyStickerEmoji),
		Kind:     metadataString(metadata, attachment.MetadataKeyStickerKind),
	}, true
}

func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
