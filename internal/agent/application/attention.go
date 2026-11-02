package application

import (
	"context"
	"log/slog"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/chat/attention"
)

// attentionMarker records that a session needs its creator; the attention
// service decides whether the session is one that raises attention at all.
type attentionMarker interface {
	Mark(ctx context.Context, sessionID string, kind attention.Kind) (bool, error)
}

// SetAttentionMarker installs the sink for unread state. Without one, runs
// finish exactly as before.
func (s *Service) SetAttentionMarker(marker attentionMarker) {
	s.attention = marker
}

// markTerminalAttention raises attention once per durable terminal: replays
// (Applied=false) and runs the user stopped raise nothing. A failure to record
// it never affects the run.
func (s *Service) markTerminalAttention(ctx context.Context, terminal sessionruntime.TerminalRun) {
	if !terminal.Applied {
		return
	}
	kind, ok := attention.KindForTerminalState(terminal.State)
	if !ok {
		return
	}
	if kind == attention.KindReply && s.quietCompletion(ctx, terminal.RunID) {
		return
	}
	s.markAttention(ctx, terminal.SessionID, kind)
}

// quietCompletionReader tells a scheduled run that completed without asking
// for attention apart from a reply the owner should see.
type quietCompletionReader interface {
	QuietCompletion(ctx context.Context, runID string) (bool, error)
}

// quietCompletion reports whether a completed run raises no reply because a
// schedule fired it and only notify_user, failures and decisions reach its
// owner. When that cannot be read the run raises its reply as before.
func (s *Service) quietCompletion(ctx context.Context, runID string) bool {
	reader, ok := s.attention.(quietCompletionReader)
	if !ok || runID == "" {
		return false
	}
	quiet, err := reader.QuietCompletion(ctx, runID)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "read schedule notify policy failed",
				slog.String("run_id", runID), slog.Any("error", err))
		}
		return false
	}
	return quiet
}

func (s *Service) markWaitingAttention(ctx context.Context, _, sessionID string) {
	s.markAttention(ctx, sessionID, attention.KindNeedsInput)
}

func (s *Service) markAttention(ctx context.Context, sessionID string, kind attention.Kind) {
	if s.attention == nil {
		return
	}
	if _, err := s.attention.Mark(ctx, sessionID, kind); err != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "record session attention failed",
			slog.String("session_id", sessionID),
			slog.String("attention_kind", string(kind)),
			slog.Any("error", err))
	}
}
