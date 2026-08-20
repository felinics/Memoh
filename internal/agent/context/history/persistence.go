package historyfrag

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/messageconv"
)

// MarshalStoredModelMessage encodes the existing turn message payload used by
// bot_history_messages. Keeping this codec in the history boundary makes the
// database format explicit without changing that format.
func MarshalStoredModelMessage(message turn.ModelMessage) (json.RawMessage, error) {
	return json.Marshal(storedModelMessageV0FromTurn(message))
}

// MarshalStoredSDKMessage converts one Agent message to the existing history
// payload after removing file bytes. Usage remains in the message table's
// separate usage column, as it did before this codec was introduced.
func MarshalStoredSDKMessage(message sdk.Message) (json.RawMessage, error) {
	if _, err := json.Marshal(message); err != nil {
		return nil, err
	}
	converted := ToStoredModelMessages([]sdk.Message{message})
	if len(converted) != 1 {
		return nil, errors.New("message conversion produced no stored message")
	}
	return MarshalStoredModelMessage(converted[0])
}

// DecodeStoredModelMessage reads both the current turn envelope and the
// legacy content shapes found in existing history rows.
func DecodeStoredModelMessage(log *slog.Logger, messageID, role string, raw json.RawMessage) turn.ModelMessage {
	var stored storedModelMessageV0
	if err := json.Unmarshal(raw, &stored); err != nil {
		if log != nil {
			log.Warn("historyfrag: content unmarshal failed, treating as raw text",
				slog.String("message_id", messageID),
				slog.String("role", role),
				slog.Any("error", err),
			)
		}
		return turn.ModelMessage{
			Role:    strings.TrimSpace(role),
			Content: cloneRawMessage(raw),
		}
	}

	// Older rows may contain one content part directly, without the role and
	// content envelope. Unmarshal accepts those unknown fields and produces an
	// empty ModelMessage, so recover the part before applying the row role.
	message := stored.turnMessage()
	if message.Role == "" && !message.HasContent() {
		if wrapped, ok := bareContentPartArray(raw); ok {
			message.Content = wrapped
		}
	}

	message.Role = strings.TrimSpace(role)
	return message
}

// storedModelMessageV0 is the JSON shape already present in
// bot_history_messages.content. It deliberately remains private: callers use
// turn.ModelMessage while this package owns the durable representation.
type storedModelMessageV0 struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []turn.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

func storedModelMessageV0FromTurn(message turn.ModelMessage) storedModelMessageV0 {
	return storedModelMessageV0{
		Role:       message.Role,
		Content:    message.Content,
		ToolCalls:  message.ToolCalls,
		ToolCallID: message.ToolCallID,
		Name:       message.Name,
	}
}

func (message storedModelMessageV0) turnMessage() turn.ModelMessage {
	return turn.ModelMessage{
		Role:       message.Role,
		Content:    message.Content,
		ToolCalls:  message.ToolCalls,
		ToolCallID: message.ToolCallID,
		Name:       message.Name,
	}
}

// bareContentPartArray wraps a raw JSON object into a single-element parts
// array, e.g. {"type":"text","text":"hello"} -> [{"type":"text","text":"hello"}].
// Only a non-empty object is wrapped because `{}` carries no content to recover.
func bareContentPartArray(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '{' || trimmed == "{}" {
		return nil, false
	}
	return json.RawMessage("[" + trimmed + "]"), true
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// ToStoredModelMessages converts Agent messages into the existing history
// message shape. File bytes are replaced with a readable placeholder before
// conversion; the media store remains the source of the attachment itself.
func ToStoredModelMessages(messages []sdk.Message) []turn.ModelMessage {
	return messageconv.SDKMessagesToModelMessages(RedactFileParts(messages))
}

// RedactFileParts returns a copy only when a message contains a FilePart.
// History rows must never contain document bytes, while unrelated message
// parts keep their original order and values.
func RedactFileParts(messages []sdk.Message) []sdk.Message {
	out := messages
	copied := false
	for i, message := range messages {
		hasFile := false
		for _, part := range message.Content {
			if _, ok := part.(sdk.FilePart); ok {
				hasFile = true
				break
			}
		}
		if !hasFile {
			continue
		}
		if !copied {
			out = append([]sdk.Message(nil), messages...)
			copied = true
		}
		kept := make([]sdk.MessagePart, 0, len(message.Content))
		for _, part := range message.Content {
			file, ok := part.(sdk.FilePart)
			if !ok {
				kept = append(kept, part)
				continue
			}
			name := strings.TrimSpace(file.Filename)
			if name == "" {
				name = "attachment"
			}
			mime := strings.TrimSpace(file.MediaType)
			if mime == "" {
				mime = "application/pdf"
			}
			kept = append(kept, sdk.TextPart{
				Text: "[attachment " + name + " (" + mime + ") was shown to the model this turn; its bytes are not persisted — use the read tool to view it again]",
			})
		}
		out[i].Content = kept
	}
	return out
}
