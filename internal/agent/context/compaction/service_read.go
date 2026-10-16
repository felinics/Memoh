package compaction

import "github.com/felinics/memoh/internal/db/postgres/sqlc"

const (
	minCompactionReadBytes = 512 << 10
	maxCompactionReadBytes = 8 << 20
)

func compactionReadMaxBytes(cfg TriggerConfig) int64 {
	tokens := cfg.MaxCompactTokens
	if tokens <= 0 {
		tokens = 30000
	}
	bytes := int64(tokens) * 8
	if bytes < minCompactionReadBytes {
		return minCompactionReadBytes
	}
	if bytes > maxCompactionReadBytes {
		return maxCompactionReadBytes
	}
	return bytes
}

func uncompactedRowsFromBounded(rows []sqlc.ListUncompactedMessagesBySessionWithinBytesRow) []sqlc.ListUncompactedMessagesBySessionRow {
	converted := make([]sqlc.ListUncompactedMessagesBySessionRow, len(rows))
	for i, row := range rows {
		converted[i] = sqlc.ListUncompactedMessagesBySessionRow{
			ID: row.ID, BotID: row.BotID, SessionID: row.SessionID,
			SenderChannelIdentityID: row.SenderChannelIdentityID, SenderUserID: row.SenderUserID,
			ExternalMessageID: row.ExternalMessageID, SourceReplyToMessageID: row.SourceReplyToMessageID,
			Role: row.Role, Content: row.Content, Metadata: row.Metadata, Usage: row.Usage,
			EventID: row.EventID, DisplayText: row.DisplayText, CompactID: row.CompactID, CreatedAt: row.CreatedAt,
			SenderDisplayName: row.SenderDisplayName, SenderAvatarUrl: row.SenderAvatarUrl,
			Platform: row.Platform, CompactionEpoch: row.CompactionEpoch,
			ConversationType: row.ConversationType, ConversationName: row.ConversationName, ReplyTarget: row.ReplyTarget,
		}
	}
	return converted
}
