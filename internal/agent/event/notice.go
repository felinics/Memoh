package event

import (
	"encoding/json"
	"maps"
	"strings"
)

// RuntimeNoticesMetadataKey stores UI-only runtime facts alongside a round.
// Notices must never become model prose or enter the provider transcript.
const RuntimeNoticesMetadataKey = "runtime_notices"

// Notice is the public part of a RuntimeNotice event. Producers must supply
// public text and arguments, just as they do for the live conversation stream.
type Notice struct {
	Code    string            `json:"code,omitempty"`
	Content string            `json:"content"`
	Args    map[string]string `json:"args,omitempty"`
}

// NoticeFromStream excludes diagnostic fields and non-string metadata from
// the durable record, matching the existing public notice projection.
func NoticeFromStream(ev StreamEvent) (Notice, bool) {
	n := Notice{Code: strings.TrimSpace(ev.Code), Content: strings.TrimSpace(ev.Delta)}
	if ev.Type != RuntimeNotice || n.Content == "" {
		return Notice{}, false
	}
	for key, value := range ev.Metadata {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			if n.Args == nil {
				n.Args = make(map[string]string)
			}
			n.Args[key] = strings.TrimSpace(text)
		}
	}
	return n, true
}

// NoticesFromMetadata accepts both in-process values and JSON-decoded rows.
func NoticesFromMetadata(metadata map[string]any) []Notice {
	raw, ok := metadata[RuntimeNoticesMetadataKey]
	if !ok {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var notices []Notice
	if json.Unmarshal(data, &notices) != nil {
		return nil
	}
	var out []Notice
	for _, n := range notices {
		n.Code, n.Content = strings.TrimSpace(n.Code), strings.TrimSpace(n.Content)
		if n.Content != "" {
			out = AppendNotice(out, n)
		}
	}
	return out
}

// AppendNotice coalesces repeated delivery of the same public fact in a run.
func AppendNotice(notices []Notice, notice Notice) []Notice {
	for _, existing := range notices {
		if existing.Code == notice.Code && existing.Content == notice.Content && maps.Equal(existing.Args, notice.Args) {
			return notices
		}
	}
	notice.Args = maps.Clone(notice.Args)
	return append(notices, notice)
}
