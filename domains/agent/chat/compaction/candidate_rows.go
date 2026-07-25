package compaction

import (
	"encoding/json"
	"fmt"
	"strings"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
)

// itemsFromRows classifies each uncompacted row into a typed CompactionCandidate.
// A row that cannot be classified remains as a must-keep barrier rather than
// aborting the whole compaction. Keeping its position prevents compact spans on
// either side from sharing an ID and being reordered by the read path.
func itemsFromRows(rows []CandidateRecord) ([]CompactionCandidate, int) {
	items := make([]CompactionCandidate, 0, len(rows))
	barrierCount := 0
	for _, row := range rows {
		record, err := history.FromDBMessage(rowToMessage(row), rowScopeFallback(row))
		if err != nil {
			barrierCount++
			preserveToolClosure, toolResult := rawToolShape(row)
			policies := []CompactPolicy{CompactPolicyMustKeep}
			if preserveToolClosure {
				policies = appendPolicy(policies, CompactPolicyPreserveToolClosure)
			}
			items = append(items, CompactionCandidate{
				ID:           row.ID,
				RawContent:   row.Content,
				RawUsage:     row.Usage,
				Policies:     policies,
				IsToolResult: toolResult,
			})
			continue
		}
		items = append(items, CompactionCandidate{
			ID:           row.ID,
			RawContent:   row.Content,
			RawUsage:     row.Usage,
			Record:       record,
			Policies:     candidatePolicies(record),
			IsToolResult: strings.EqualFold(strings.TrimSpace(record.ModelMessage.Role), "tool"),
		})
	}
	if len(items) > 0 {
		propagateMustKeepAcrossToolExchanges(items)
		markSelectionPolicies(items)
	}
	return items, barrierCount
}

func candidatesWithAssets(items []CompactionCandidate, rows []CandidateRecord, assetRows []AssetRecord) ([]CompactionCandidate, error) {
	rowByID := make(map[string]CandidateRecord, len(rows))
	for _, row := range rows {
		rowByID[row.ID] = row
	}
	assets := assetsByMessageID(assetRows)
	out := append([]CompactionCandidate(nil), items...)
	for i := range out {
		if out[i].Record.Ref.ID == "" {
			continue
		}
		row, ok := rowByID[out[i].ID]
		if !ok {
			return nil, fmt.Errorf("compaction candidate %s missing source row", out[i].ID)
		}
		msg := rowToMessage(row)
		msg.Assets = assets[row.ID]
		record, err := history.FromDBMessage(msg, rowScopeFallback(row))
		if err != nil {
			return nil, fmt.Errorf("rebuild compaction candidate %s with assets: %w", out[i].ID, err)
		}
		out[i].Record = record
	}
	return out, nil
}

func assetsByMessageID(rows []AssetRecord) map[string][]messagepkg.MessageAsset {
	assets := make(map[string][]messagepkg.MessageAsset)
	for _, row := range rows {
		assets[row.MessageID] = append(assets[row.MessageID], messagepkg.MessageAsset{
			ContentHash: strings.TrimSpace(row.ContentHash),
			Role:        strings.TrimSpace(row.Role),
			Ordinal:     row.Ordinal,
			Name:        strings.TrimSpace(row.Name),
			Metadata:    metadataMap(row.Metadata),
		})
	}
	return assets
}

func rawToolShape(row CandidateRecord) (preserveToolClosure, toolResult bool) {
	toolResult = strings.EqualFold(strings.TrimSpace(row.Role), "tool")
	if toolResult {
		return true, true
	}

	content := row.Content
	var modelMessage agentdomain.ModelMessage
	if json.Unmarshal(row.Content, &modelMessage) == nil {
		if len(modelMessage.ToolCalls) > 0 || strings.TrimSpace(modelMessage.ToolCallID) != "" {
			return true, false
		}
		if modelMessage.HasContent() {
			content = modelMessage.Content
		}
	}
	var barePart entryPart
	if json.Unmarshal(content, &barePart) == nil && isToolPartType(barePart.Type) {
		return true, false
	}
	for _, part := range parseEntryParts(content) {
		if isToolPartType(part.Type) {
			return true, false
		}
	}
	return false, false
}

func rowToMessage(row CandidateRecord) messagepkg.Message {
	return messagepkg.Message{
		ID:                      row.ID,
		BotID:                   row.BotID,
		SessionID:               row.SessionID,
		SenderChannelIdentityID: row.SenderChannelIdentityID,
		SenderUserID:            row.SenderUserID,
		SenderDisplayName:       row.SenderDisplayName,
		SenderAvatarURL:         row.SenderAvatarURL,
		Platform:                row.Platform,
		ExternalMessageID:       row.ExternalMessageID,
		SourceReplyToMessageID:  row.SourceReplyToMessageID,
		Role:                    row.Role,
		Content:                 row.Content,
		Metadata:                metadataMap(row.Metadata),
		Usage:                   row.Usage,
		CompactID:               row.CompactID,
		EventID:                 row.EventID,
		DisplayContent:          row.DisplayText,
		CreatedAt:               row.CreatedAt,
	}
}

func rowScopeFallback(row CandidateRecord) history.ScopeFallback {
	return history.ScopeFallback{
		ConversationType: row.ConversationType,
		ConversationName: strings.TrimSpace(row.ConversationName),
		ReplyTarget:      row.ReplyTarget,
	}
}

func metadataMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
