package messageconv

import (
	"bytes"
	"encoding/json"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/partmeta"
)

// The SDK types a tool call's arguments as sdk.ToolArguments{json|text}, a
// tool's output as sdk.ToolOutput{text|json} and provider metadata as
// namespace → name → string. bot_history_messages predates those types: it
// holds the arguments object itself, the output value itself and Memoh's
// annotations as nested objects. The two rewrites below keep the stored shape
// exactly as it was, so existing rows replay and rows written now read back
// on either side of the change.
//
// The SDK keeps the JSON it carries in RFC 8785 canonical form (see
// sdk.CanonicalJSON). A row read back goes through the SDK constructors, so a
// legacy row's argument and output documents come back canonical too: the
// same history gives the same request bytes whether it came from memory or
// from the database. Rows are JSONB, so their bytes were never the SDK's.

// storedPartsFromSDK rewrites the content array marshalJSON(sdk.Message)
// produced into the stored shape.
func storedPartsFromSDK(content json.RawMessage) json.RawMessage {
	return rewriteParts(content, func(part map[string]json.RawMessage, partType string) {
		switch partType {
		case "tool-call":
			if raw, ok := part["input"]; ok {
				var args sdk.ToolArguments
				if json.Unmarshal(raw, &args) == nil {
					part["input"] = storedArguments(args)
				}
			}
		case "tool-result":
			if raw, ok := part["result"]; ok {
				var output sdk.ToolOutput
				if json.Unmarshal(raw, &output) == nil {
					part["result"] = storedOutput(output)
				}
			}
		}
		if raw, ok := part["providerMetadata"]; ok {
			var meta sdk.ProviderMetadata
			if json.Unmarshal(raw, &meta) != nil {
				return
			}
			stored := partmeta.Unfold(meta)
			if len(stored) == 0 {
				delete(part, "providerMetadata")
				return
			}
			if encoded, err := marshalJSON(stored); err == nil {
				part["providerMetadata"] = encoded
			}
		}
	})
}

// sdkPartsFromStored rewrites a stored content array into the shape
// sdk.Message.UnmarshalJSON reads.
func sdkPartsFromStored(content json.RawMessage) json.RawMessage {
	return rewriteParts(content, func(part map[string]json.RawMessage, partType string) {
		switch partType {
		case "tool-call":
			if raw, ok := part["input"]; ok {
				if typed, ok := typedArguments(raw); ok {
					part["input"] = typed
				} else {
					delete(part, "input")
				}
			}
		case "tool-result":
			if raw, ok := part["result"]; ok {
				if typed, ok := typedOutput(raw); ok {
					part["result"] = typed
				} else {
					delete(part, "result")
				}
			}
		}
		if raw, ok := part["providerMetadata"]; ok {
			var stored map[string]any
			if json.Unmarshal(raw, &stored) != nil {
				delete(part, "providerMetadata")
				return
			}
			meta := partmeta.Fold(stored)
			if meta == nil {
				delete(part, "providerMetadata")
				return
			}
			if encoded, err := marshalJSON(meta); err == nil {
				part["providerMetadata"] = encoded
			}
		}
	})
}

// rewriteParts applies rewrite to every object in a content array. String
// content and anything that does not parse are returned unchanged.
func rewriteParts(content json.RawMessage, rewrite func(part map[string]json.RawMessage, partType string)) json.RawMessage {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return content
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return content
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, raw := range parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(raw, &part); err != nil || part == nil {
			out = append(out, raw)
			continue
		}
		var partType string
		_ = json.Unmarshal(part["type"], &partType)
		rewrite(part, partType)
		encoded, err := marshalJSON(part)
		if err != nil {
			out = append(out, raw)
			continue
		}
		out = append(out, encoded)
	}
	encoded, err := marshalJSON(out)
	if err != nil {
		return content
	}
	return encoded
}

// storedArguments is the arguments as the row has always held them: the
// document itself, or the invalid text as a string.
func storedArguments(args sdk.ToolArguments) json.RawMessage {
	if !args.Valid() {
		encoded, _ := marshalJSON(args.Text)
		return encoded
	}
	return canonicalOrRaw(args.Object())
}

// canonicalOrRaw re-encodes a document in the SDK's canonical form. Bytes
// that arrived through json.Marshal carry HTML escapes ("\u0026" for "&");
// the canonical form uses minimal escaping, so the row holds the text the
// model produced and the live turn sent. A document the canonical form
// rejects is kept as it is.
func canonicalOrRaw(raw json.RawMessage) json.RawMessage {
	if canonical, err := sdk.CanonicalJSON(raw); err == nil {
		return canonical
	}
	return raw
}

// typedArguments reads stored arguments: a string is the invalid text a
// provider kept verbatim, any other document is the arguments object.
//
// The encoding leaves one shape ambiguous: an argument document that is
// itself a JSON string literal ("x") is stored exactly like the invalid text
// x and reads back as that text. No provider emits a bare string as tool
// arguments and no tool accepts one, so the row format keeps the simple rule
// instead of a separate key for invalid text.
func typedArguments(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false
	}
	var args sdk.ToolArguments
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, false
		}
		args = sdk.ParseToolArguments(text)
	} else {
		// A stored document is canonicalized like a fresh one; a document the
		// canonical form rejects (a repeated member) is kept as it was.
		args = sdk.ParseToolArguments(string(trimmed))
		if !args.Valid() {
			args = sdk.ToolArguments{JSON: trimmed}
		}
	}
	encoded, err := marshalJSON(args)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// storedOutput is the output as the row has always held it: text as a
// string, a document as itself, and no output at all as null. The zero
// ToolOutput maps to null rather than "" so a row read back and written again
// keeps its shape.
func storedOutput(output sdk.ToolOutput) json.RawMessage {
	if output.IsJSON() {
		return canonicalOrRaw(output.JSON)
	}
	if output.Text == "" {
		return json.RawMessage("null")
	}
	encoded, _ := marshalJSON(output.Text)
	return encoded
}

// typedOutput reads a stored output: a string is text, anything else is a
// document. As with arguments, a JSON output that is itself a string literal
// is stored like text and reads back as text; the native path normalizes
// outputs through WrapToolOutputLimits before they are stored, so no live
// output takes that shape.
func typedOutput(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false
	}
	var output sdk.ToolOutput
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, false
		}
		output = sdk.TextOutput(text)
	} else {
		canonical, err := sdk.RawJSONOutput(trimmed)
		if err != nil {
			// The canonical form rejects it (a repeated member); keep the
			// document as the row holds it.
			canonical = sdk.ToolOutput{JSON: trimmed}
		}
		output = canonical
	}
	encoded, err := marshalJSON(output)
	if err != nil {
		return nil, false
	}
	return encoded, true
}
