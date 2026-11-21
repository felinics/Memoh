package acp

import "github.com/felinics/memoh/internal/agent/runtime/external"

// NativeSessionKey keeps the agent's own session ID in thread runtime
// metadata, with the thread it belongs to: forking a thread or restoring a
// backup copies runtime metadata, and the copy must not continue the source
// thread's conversation.
const NativeSessionKey = "acp_native_session"

func nativeSessionMetadata(sessionID, threadID string) map[string]any {
	return map[string]any{NativeSessionKey: map[string]any{"id": sessionID, "thread_id": threadID}}
}

// storedNativeSession returns the stored session ID, if any, and whether it
// belongs to threadID.
func storedNativeSession(metadata map[string]any, threadID string) (id string, owned bool) {
	stored, _ := metadata[NativeSessionKey].(map[string]any)
	id = external.MetadataString(stored, "id")
	return id, id != "" && threadID != "" && external.MetadataString(stored, "thread_id") == threadID
}
