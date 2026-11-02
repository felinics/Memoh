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
	s.markAttention(ctx, terminal.SessionID, kind)
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
