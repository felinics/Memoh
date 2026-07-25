package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
)

const namespaceDBHistoryMessage = "bot_history_message"

type usageInfo struct {
	InputTokens       *int `json:"inputTokens"`
	OutputTokens      *int `json:"outputTokens"`
	SnakeInputTokens  *int `json:"input_tokens"`
	SnakeOutputTokens *int `json:"output_tokens"`
}

func FromDBMessage(msg messagepkg.Message, fallback ScopeFallback) (HistoryRecord, error) {
	return FromDBMessageWithLogger(nil, msg, fallback)
}

func FromDBMessageWithLogger(log *slog.Logger, msg messagepkg.Message, fallback ScopeFallback) (HistoryRecord, error) {
	ref, err := DBMessageIdentityRef(msg.ID)
	if err != nil {
		return HistoryRecord{}, err
	}
	rowID := ref.ID

	modelMessage := modelMessageFromDBMessage(log, msg)
	inputTokens, outputTokens := parseUsage(msg.Usage)
	ref.HashAlgo = fragment.HashAlgoSHA256
	ref.HashScope = fragment.HashScopeSourcePayload
	ref.ContentHash = DBMessageSourceHash(msg).Value
	scope := scopeFromDBMessage(msg, fallback)
	provenance := fragment.Provenance{
		Source:    string(SourceDBMessage),
		SourceID:  rowID,
		Collector: CollectorHistoryRecords,
	}

	return HistoryRecord{
		Ref:        ref,
		Kind:       fragment.KindConversationEvent,
		SourceKind: SourceDBMessage,
		Lifecycle:  LifecyclePersisted,

		ModelMessage: modelMessage,
		Assets:       mediaRefsFromDBMessage(msg),
		Metadata:     cloneMetadata(msg.Metadata),

		Scope:      scope,
		Provenance: provenance,

		DBMessageID:       rowID,
		ExternalMessageID: strings.TrimSpace(msg.ExternalMessageID),
		EventID:           strings.TrimSpace(msg.EventID),
		SessionID:         strings.TrimSpace(msg.SessionID),
		SessionIDKnown:    true,
		BotID:             strings.TrimSpace(msg.BotID),

		SenderChannelIdentityID: strings.TrimSpace(msg.SenderChannelIdentityID),
		SenderUserID:            strings.TrimSpace(msg.SenderUserID),
		SenderDisplayName:       strings.TrimSpace(msg.SenderDisplayName),
		Platform:                strings.TrimSpace(msg.Platform),
		SourceReplyToMessageID:  strings.TrimSpace(msg.SourceReplyToMessageID),

		CompactID: strings.TrimSpace(msg.CompactID),
		CreatedAt: msg.CreatedAt,

		UsageInputTokens:  inputTokens,
		UsageOutputTokens: outputTokens,
	}, nil
}

func DBMessageIdentityRef(id string) (fragment.ContextRef, error) {
	rowID := strings.TrimSpace(id)
	if rowID == "" {
		return fragment.ContextRef{}, errors.New("history db message id is required")
	}
	return fragment.ContextRef{
		Namespace:  namespaceDBHistoryMessage,
		ID:         rowID,
		Version:    1,
		Schema:     fragment.SchemaContextRef,
		Durability: fragment.RefDurable,
	}, nil
}

func DBMessageSourceHash(msg messagepkg.Message) fragment.FragmentHash {
	payload := dbMessageSourceHashPayload{
		ID:                      strings.TrimSpace(msg.ID),
		BotID:                   strings.TrimSpace(msg.BotID),
		SessionID:               strings.TrimSpace(msg.SessionID),
		SenderChannelIdentityID: strings.TrimSpace(msg.SenderChannelIdentityID),
		SenderUserID:            strings.TrimSpace(msg.SenderUserID),
		Platform:                strings.TrimSpace(msg.Platform),
		ExternalMessageID:       strings.TrimSpace(msg.ExternalMessageID),
		SourceReplyToMessageID:  strings.TrimSpace(msg.SourceReplyToMessageID),
		Role:                    strings.TrimSpace(msg.Role),
		Content:                 string(msg.Content),
		Metadata:                cloneMetadata(msg.Metadata),
		Usage:                   string(msg.Usage),
		EventID:                 strings.TrimSpace(msg.EventID),
		Assets:                  mediaRefsFromDBMessage(msg),
	}
	sort.Slice(payload.Assets, func(i, j int) bool {
		if payload.Assets[i].Ordinal != payload.Assets[j].Ordinal {
			return payload.Assets[i].Ordinal < payload.Assets[j].Ordinal
		}
		if payload.Assets[i].ContentHash != payload.Assets[j].ContentHash {
			return payload.Assets[i].ContentHash < payload.Assets[j].ContentHash
		}
		return payload.Assets[i].Role < payload.Assets[j].Role
	})
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return fragment.FragmentHash{
		Algo:  fragment.HashAlgoSHA256,
		Scope: fragment.HashScopeSourcePayload,
		Value: hex.EncodeToString(sum[:]),
	}
}

type dbMessageSourceHashPayload struct {
	ID                      string         `json:"id"`
	BotID                   string         `json:"bot_id,omitempty"`
	SessionID               string         `json:"session_id,omitempty"`
	SenderChannelIdentityID string         `json:"sender_channel_identity_id,omitempty"`
	SenderUserID            string         `json:"sender_user_id,omitempty"`
	Platform                string         `json:"platform,omitempty"`
	ExternalMessageID       string         `json:"external_message_id,omitempty"`
	SourceReplyToMessageID  string         `json:"source_reply_to_message_id,omitempty"`
	Role                    string         `json:"role"`
	Content                 string         `json:"content,omitempty"`
	Metadata                map[string]any `json:"metadata,omitempty"`
	Usage                   string         `json:"usage,omitempty"`
	EventID                 string         `json:"event_id,omitempty"`
	Assets                  []MediaRef     `json:"assets,omitempty"`
}

func modelMessageFromDBMessage(log *slog.Logger, msg messagepkg.Message) agentdomain.ModelMessage {
	var modelMessage agentdomain.ModelMessage
	if err := json.Unmarshal(msg.Content, &modelMessage); err != nil {
		if log != nil {
			log.Warn("historyfrag: content unmarshal failed, treating as raw text",
				slog.String("message_id", msg.ID),
				slog.String("role", msg.Role),
				slog.Any("error", err),
			)
		}
		modelMessage = agentdomain.ModelMessage{
			Role:    strings.TrimSpace(msg.Role),
			Content: cloneRawMessage(msg.Content),
		}
		return modelMessage
	}

	// A bare content-part object (e.g. {"type":"text","text":"hello"}) has no
	// role/content/tool_calls keys, so it unmarshals successfully into an empty
	// ModelMessage instead of erroring — unknown JSON fields are ignored. Detect
	// that shape before the row's Role column gets stamped below, and normalize
	// it into a one-element parts array so TextContent/ContentParts still see it.
	if modelMessage.Role == "" && !modelMessage.HasContent() {
		if wrapped, ok := bareContentPartArray(msg.Content); ok {
			modelMessage.Content = wrapped
		}
	}

	modelMessage.Role = strings.TrimSpace(msg.Role)
	return modelMessage
}

// bareContentPartArray wraps a raw JSON object into a single-element parts
// array, e.g. {"type":"text","text":"hello"} -> [{"type":"text","text":"hello"}].
// Only a non-empty object is wrapped: `{}` carries no content to recover, and
// unmarshal already guarantees non-object payloads (arrays/strings/scalars)
// took the error branch above.
func bareContentPartArray(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '{' || trimmed == "{}" {
		return nil, false
	}
	return json.RawMessage("[" + trimmed + "]"), true
}

func parseUsage(raw json.RawMessage) (*int, *int) {
	if len(raw) == 0 {
		return nil, nil
	}
	var usage usageInfo
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, nil
	}
	if usage.InputTokens == nil {
		usage.InputTokens = usage.SnakeInputTokens
	}
	if usage.OutputTokens == nil {
		usage.OutputTokens = usage.SnakeOutputTokens
	}
	return usage.InputTokens, usage.OutputTokens
}

func scopeFromDBMessage(msg messagepkg.Message, fallback ScopeFallback) fragment.Scope {
	return fragment.Scope{
		BotID:             strings.TrimSpace(msg.BotID),
		ChatID:            strings.TrimSpace(fallback.ChatID),
		SessionID:         strings.TrimSpace(msg.SessionID),
		ChannelIdentityID: strings.TrimSpace(msg.SenderChannelIdentityID),
		DisplayName:       strings.TrimSpace(msg.SenderDisplayName),
		Platform:          strings.TrimSpace(msg.Platform),
		ConversationType:  strings.TrimSpace(fallback.ConversationType),
		ConversationName:  strings.TrimSpace(fallback.ConversationName),
		ReplyTarget:       strings.TrimSpace(fallback.ReplyTarget),
		CurrentMessageID:  strings.TrimSpace(msg.ExternalMessageID),
		EventID:           strings.TrimSpace(msg.EventID),
		ReplyToMessageID:  strings.TrimSpace(msg.SourceReplyToMessageID),
	}
}

func mediaRefsFromDBMessage(msg messagepkg.Message) []MediaRef {
	if len(msg.Assets) == 0 {
		return nil
	}
	out := make([]MediaRef, 0, len(msg.Assets))
	for _, asset := range msg.Assets {
		contentHash := strings.TrimSpace(asset.ContentHash)
		if contentHash == "" {
			continue
		}
		out = append(out, MediaRef{
			ContentHash: contentHash,
			Role:        strings.TrimSpace(asset.Role),
			Ordinal:     asset.Ordinal,
			Name:        strings.TrimSpace(asset.Name),
			Metadata:    cloneMetadata(asset.Metadata),
		})
	}
	return out
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
