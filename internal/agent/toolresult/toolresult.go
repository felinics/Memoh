// Package toolresult defines the result records of the native tools that
// external runtimes (Codex, Claude Code, ACP agents) also report under the
// native name. A call labelled with a native tool name must carry that tool's
// input and result format; these types are that format, shared by the native
// tools that produce it, the runtime adapters that translate into it and the
// tests that check both.
//
// Values travel through event.StreamEvent.Result as map[string]any: IM
// formatters, transcript recording and UI message conversion all read results
// as maps. Map produces that map from a record, so the JSON on the wire is
// exactly the record's JSON.
package toolresult

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Record is a result type of this package.
type Record interface {
	Map() map[string]any
}

// toMap converts a record to its JSON object form.
func toMap(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// Merge adds the keys of extra that the record does not already carry. It is
// for fields an agent reported beyond the native format: they stay with the
// result, and never override a field of the format.
func Merge(record Record, extra map[string]any) map[string]any {
	out := record.Map()
	for key, value := range extra {
		if _, taken := out[key]; !taken {
			out[key] = value
		}
	}
	return out
}

// Decode reads a stored or streamed result into T. Unknown fields and fields
// of an older shape are ignored, so a row written before a format existed
// decodes to the zero value of the missing fields instead of failing.
func Decode[T any](value any) (T, error) {
	return decode[T](value, false)
}

// DecodeStrict is Decode but rejects fields T does not define.
func DecodeStrict[T any](value any) (T, error) {
	return decode[T](value, true)
}

func decode[T any](value any, strict bool) (T, error) {
	var out T
	var raw []byte
	switch v := value.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return out, fmt.Errorf("encode tool result: %w", err)
		}
		raw = encoded
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("decode tool result: %w", err)
	}
	return out, nil
}

// Ptr returns a pointer to v, for the optional fields of the records below.
func Ptr[T any](v T) *T { return &v }

// Failure is the result of a tool call that did not run to completion
// (refused, failed to start, cancelled). A command that ran and exited
// non-zero is an Exec, not a Failure.
type Failure struct {
	IsError bool          `json:"isError"`
	Content []TextContent `json:"content"`
}

// TextContent is one text block of an MCP-style content list.
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NewFailure builds a Failure carrying text as its explanation.
func NewFailure(text string) Failure {
	return Failure{IsError: true, Content: []TextContent{{Type: "text", Text: text}}}
}

func (f Failure) Map() map[string]any { return toMap(f) }

// Text is the explanation carried by the failure.
func (f Failure) Text() string {
	if len(f.Content) == 0 {
		return ""
	}
	return f.Content[0].Text
}
