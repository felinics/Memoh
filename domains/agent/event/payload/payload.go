// Package payload defines the on-wire shapes for agent-emitted events
// forwarded to per-session SSE subscribers. Producers build their payloads
// through these helpers so the contract is exercised by one set of tests
// instead of being duplicated as map literals across packages.
//
// Any field added to the returned maps lands on the wire. The top-level
// `session_id` placement is load-bearing: domains/api/http/chat/message_stream.go
// routes events by reading that key directly, without unwrapping nested
// objects.
package payload

import "github.com/memohai/memoh/domains/agent/engine/background"

// BackgroundTask builds the wire payload for a background task event. The
// publisher in the core composition root marshals this map and forwards it
// verbatim to the per-session SSE handler, which stamps `type` and `bot_id`
// on the way out.
func BackgroundTask(evt background.TaskEvent) map[string]any {
	return map[string]any{
		"event":      evt.Event,
		"session_id": evt.SessionID,
		"task":       evt,
	}
}
