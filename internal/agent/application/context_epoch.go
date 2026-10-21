package application

import (
	"context"
	"time"

	"github.com/felinics/memoh/internal/chat/contextepoch"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// loadAnchoredSessionMessages loads the session's byte-budgeted history
// window starting from the session's persisted Context Epoch anchor instead
// of a wall-clock window (docs/design/context-epoch-spec.md, rules 1-2). When
// the byte budget admits fewer rows than the session holds since the anchor,
// the store records an explicit anchor advance (reason "budget") — the slide
// leaves an audit trail instead of silently rewriting the request prefix.
//
// Without a configured epoch store the load still runs, anchored at the
// beginning of the session and bounded by maxBytes, so behavior stays
// deterministic and memory-safe.
func (s *Service) loadAnchoredSessionMessages(
	ctx context.Context,
	sessionID, path string,
	maxBytes int64,
	list func(context.Context, string, time.Time, int64) ([]messagepkg.Message, error),
) ([]messagepkg.Message, error) {
	if s == nil || s.contextEpochs == nil {
		return list(ctx, sessionID, contextepoch.Beginning, maxBytes)
	}
	return s.contextEpochs.LoadWindow(ctx, sessionID, path,
		func(ctx context.Context, since time.Time) (int64, error) {
			measure, err := s.messageService.MeasureActiveBySession(ctx, sessionID, since)
			if err != nil {
				return 0, err
			}
			return measure.MessageCount, nil
		},
		func(ctx context.Context, since time.Time) ([]messagepkg.Message, error) {
			return list(ctx, sessionID, since, maxBytes)
		})
}
