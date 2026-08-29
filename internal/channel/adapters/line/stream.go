package line

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func (a *Adapter) OpenStream(_ context.Context, cfg channel.ChannelConfig, target string, _ channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	target = normalizeTarget(target)
	if target == "" {
		return nil, errors.New("line target is required")
	}
	return &outboundStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
	}, nil
}

type outboundStream struct {
	adapter *Adapter
	cfg     channel.ChannelConfig
	target  string

	mu          sync.Mutex
	closed      bool
	textBuilder strings.Builder
	attachments []channel.PreparedAttachment
}

func (s *outboundStream) Push(ctx context.Context, event channel.PreparedStreamEvent) error {
	if s == nil || s.adapter == nil {
		return errors.New("line stream is not configured")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("line stream is closed")
	}
	switch event.Type {
	case channel.StreamEventDelta:
		if strings.TrimSpace(event.Delta) != "" && event.Phase != channel.StreamPhaseReasoning {
			s.textBuilder.WriteString(event.Delta)
		}
		s.mu.Unlock()
		return nil
	case channel.StreamEventAttachment:
		if len(event.Attachments) > 0 {
			s.attachments = append(s.attachments, event.Attachments...)
		}
		s.mu.Unlock()
		return nil
	case channel.StreamEventFinal:
		prepared := s.snapshotFinalLocked(event.Final)
		s.mu.Unlock()
		return s.sendSnapshot(ctx, prepared)
	case channel.StreamEventError:
		errText := redact.Text(strings.TrimSpace(event.Error))
		s.textBuilder.Reset()
		s.attachments = nil
		s.mu.Unlock()
		if errText == "" {
			return nil
		}
		return s.sendSnapshot(ctx, channel.PreparedMessage{
			Message: channel.Message{
				Format: channel.MessageFormatPlain,
				Text:   "Error: " + errText,
			},
		})
	default:
		s.mu.Unlock()
		return nil
	}
}

func (s *outboundStream) Close(ctx context.Context) error {
	if s == nil || s.adapter == nil {
		return errors.New("line stream is not configured")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	prepared := s.snapshotBufferedLocked()
	s.mu.Unlock()
	return s.sendSnapshot(ctx, prepared)
}

func (s *outboundStream) snapshotFinalLocked(final *channel.PreparedStreamFinalizePayload) channel.PreparedMessage {
	var prepared channel.PreparedMessage
	if final != nil {
		prepared = final.Message
	}
	if strings.TrimSpace(prepared.Message.Text) == "" {
		prepared.Message.Text = strings.TrimSpace(s.textBuilder.String())
	}
	if len(prepared.Attachments) == 0 && len(s.attachments) > 0 {
		prepared.Attachments = append([]channel.PreparedAttachment(nil), s.attachments...)
		prepared.Message.Attachments = lineLogicalAttachments(s.attachments)
	}
	s.textBuilder.Reset()
	s.attachments = nil
	return prepared
}

func (s *outboundStream) snapshotBufferedLocked() channel.PreparedMessage {
	if strings.TrimSpace(s.textBuilder.String()) == "" && len(s.attachments) == 0 {
		return channel.PreparedMessage{}
	}
	prepared := channel.PreparedMessage{
		Message: channel.Message{
			Format: channel.MessageFormatPlain,
			Text:   strings.TrimSpace(s.textBuilder.String()),
		},
	}
	if len(s.attachments) > 0 {
		prepared.Attachments = append([]channel.PreparedAttachment(nil), s.attachments...)
		prepared.Message.Attachments = lineLogicalAttachments(s.attachments)
	}
	s.textBuilder.Reset()
	s.attachments = nil
	return prepared
}

func (s *outboundStream) sendSnapshot(ctx context.Context, prepared channel.PreparedMessage) error {
	if prepared.Message.IsEmpty() && len(prepared.Attachments) == 0 {
		return nil
	}
	sent, err := s.adapter.sendPrepared(ctx, s.cfg, channel.PreparedOutboundMessage{
		Target:  s.target,
		Message: prepared,
	})
	if err != nil && sent > 0 {
		s.adapter.logWarn("line stream partial send failed after push",
			slog.String("config_id", s.cfg.ID),
			slog.String("bot_id", s.cfg.BotID),
			slog.String("target_hash", hashValue(s.target)),
			slog.String("reason", "partial_failure_suppressed"),
		)
		return nil
	}
	return err
}
