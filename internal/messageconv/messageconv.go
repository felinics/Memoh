package messageconv

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/turn"
)

func SDKMessagesToModelMessages(msgs []sdk.Message) []turn.ModelMessage {
	return SDKMessagesToModelMessagesWithLogger(nil, msgs)
}

func SDKMessagesToModelMessagesWithLogger(log *slog.Logger, msgs []sdk.Message) []turn.ModelMessage {
	result := make([]turn.ModelMessage, 0, len(msgs))
	for _, msg := range msgs {
		data, err := marshalJSON(msg)
		if err != nil {
			if log != nil {
				log.Warn("messageconv: sdk message marshal failed", slog.String("role", string(msg.Role)), slog.Any("error", err))
			}
			continue
		}
		var envelope struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			if log != nil {
				log.Warn("messageconv: sdk message content extract failed", slog.String("role", string(msg.Role)), slog.Any("error", err))
			}
			continue
		}
		var usage json.RawMessage
		if msg.Usage != nil {
			usage, _ = marshalJSON(msg.Usage)
		}
		result = append(result, turn.ModelMessage{
			Role:    string(msg.Role),
			Content: storedPartsFromSDK(envelope.Content),
			Usage:   usage,
		})
	}
	return result
}

// SDKMessagesJSONToModelMessages types a serialized []sdk.Message — the
// Messages payload of step and terminal stream events — as stored-shape model
// messages. Readers written for the persisted row shape (tool input as the
// plain object, Memoh annotations as nested objects under providerMetadata)
// consume the result unchanged.
func SDKMessagesJSONToModelMessages(raw json.RawMessage) ([]turn.ModelMessage, error) {
	var envelopes []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &envelopes); err != nil {
		return nil, err
	}
	out := make([]turn.ModelMessage, 0, len(envelopes))
	for _, envelope := range envelopes {
		out = append(out, turn.ModelMessage{
			Role:    envelope.Role,
			Content: storedPartsFromSDK(envelope.Content),
			Usage:   envelope.Usage,
		})
	}
	return out, nil
}

func ModelMessageToSDKMessage(mm turn.ModelMessage) sdk.Message {
	// Decoding a parts array as a string fails only after scanning all of
	// it, so only content that can be a string is tried as one.
	if trimmed := bytes.TrimSpace(mm.Content); len(trimmed) == 0 || (trimmed[0] != '[' && trimmed[0] != '{') {
		var s string
		if err := json.Unmarshal(mm.Content, &s); err == nil {
			return sdk.Message{
				Role:    sdk.MessageRole(mm.Role),
				Content: []sdk.MessagePart{sdk.TextPart{Text: s}},
			}
		}
	}

	var msg sdk.Message
	if err := json.Unmarshal(sdkEnvelope(mm.Role, sdkPartsFromStored(mm.Content)), &msg); err == nil {
		return msg
	}

	return sdk.Message{Role: sdk.MessageRole(mm.Role)}
}

// sdkEnvelope is the {"role","content"} document sdk.Message decodes. It is
// read once and dropped, so content goes in as it is rather than compacted;
// content that is not valid JSON makes the whole document fail to decode.
func sdkEnvelope(role string, content json.RawMessage) []byte {
	encodedRole, _ := marshalJSON(role)
	if len(bytes.TrimSpace(content)) == 0 {
		content = json.RawMessage("null")
	}
	envelope := make([]byte, 0, len(content)+len(encodedRole)+24)
	envelope = append(envelope, `{"role":`...)
	envelope = append(envelope, encodedRole...)
	envelope = append(envelope, `,"content":`...)
	envelope = append(envelope, content...)
	return append(envelope, '}')
}

func ModelMessagesToSDKMessages(msgs []turn.ModelMessage) []sdk.Message {
	result := make([]sdk.Message, 0, len(msgs))
	for _, mm := range msgs {
		result = append(result, ModelMessageToSDKMessage(mm))
	}
	return result
}

func PrependUserMessage(query string, output []turn.ModelMessage) []turn.ModelMessage {
	if strings.TrimSpace(query) == "" {
		return output
	}
	round := make([]turn.ModelMessage, 0, 1+len(output))
	round = append(round, turn.ModelMessage{
		Role:    "user",
		Content: turn.NewTextContent(query),
	})
	return append(round, output...)
}

// marshalJSON encodes v without HTML escaping. The rows and the SDK keep
// tool arguments and outputs as literal text; json.Marshal would turn "&",
// "<" and ">" into \u escapes on every store, so a command read back would
// no longer match the bytes the model produced and the live turn sent.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
