package codex

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
)

// recordThreadMetadata publishes a matching id/path pair. An unknown path
// must clear the previous thread's path when the native session changes.
func recordThreadMetadata(delta *map[string]any, previous map[string]any, threadID, rolloutPath string) {
	changed := threadID != metadataString(previous, metadataThreadIDKey)
	if !changed && (rolloutPath == "" || rolloutPath == metadataString(previous, metadataRolloutPathKey)) {
		return
	}
	if *delta == nil {
		*delta = map[string]any{}
	}
	if changed {
		(*delta)[metadataThreadIDKey] = threadID
		(*delta)[metadataRolloutPathKey] = nil
	}
	if rolloutPath != "" {
		(*delta)[metadataRolloutPathKey] = rolloutPath
	}
}

// resumeThread treats a stale path as a hint. Codex may successfully resume
// that file's thread even when it differs from the requested id; try the id
// before deciding that the intended conversation can no longer be resumed.
func (s *appServer) resumeThread(ctx context.Context, params protocol.ThreadResumeParams) (protocol.ThreadResumeResponse, error) {
	var response protocol.ThreadResumeResponse
	err := s.conn.Call(ctx, protocol.MethodThreadResume, params, &response)
	var refused *protocol.RPCError
	if params.Path != nil && ((err == nil && response.Thread.ID != params.ThreadID) || errors.As(err, &refused)) {
		params.Path = nil
		response = protocol.ThreadResumeResponse{}
		err = s.conn.Call(ctx, protocol.MethodThreadResume, params, &response)
	}
	return response, err
}
