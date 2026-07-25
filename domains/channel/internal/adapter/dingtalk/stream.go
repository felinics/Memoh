package dingtalk

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/memohai/memoh/domains/channel/gateway"
)

// dingtalkOutboundStream accumulates streaming events and flushes the final message
// to DingTalk when closed. DingTalk has no native streaming API, so the stream
// is buffered and sent as a single message on Close.
type dingtalkOutboundStream struct {
	adapter *DingTalkAdapter
	cfg     gateway.ChannelConfig
	target  string
	reply   *gateway.ReplyRef

	mu          sync.Mutex
	closed      atomic.Bool
	finalSent   atomic.Bool
	textBuilder strings.Builder
	attachments []gateway.PreparedAttachment
	final       *gateway.PreparedMessage
}

func (s *dingtalkOutboundStream) Push(ctx context.Context, event gateway.PreparedStreamEvent) error {
	if s.closed.Load() {
		return errors.New("dingtalk stream is closed")
	}
	if s.finalSent.Load() {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	switch event.Type {
	case gateway.StreamEventStatus,
		gateway.StreamEventPhaseStart,
		gateway.StreamEventPhaseEnd,
		gateway.StreamEventToolCallStart,
		gateway.StreamEventAgentStart,
		gateway.StreamEventAgentEnd,
		gateway.StreamEventProcessingStarted,
		gateway.StreamEventProcessingCompleted,
		gateway.StreamEventProcessingFailed:
		// Non-content events: no-op.
		return nil

	case gateway.StreamEventToolCallEnd:
		text := strings.TrimSpace(gateway.RenderToolCallMessage(gateway.BuildToolCallEnd(event.ToolCall)))
		if text == "" {
			return nil
		}
		return s.adapter.Send(ctx, s.cfg, gateway.PreparedOutboundMessage{
			Target: s.target,
			Message: gateway.PreparedMessage{
				Message: gateway.Message{Format: gateway.MessageFormatPlain, Text: text, Reply: s.reply},
			},
		})

	case gateway.StreamEventDelta:
		if strings.TrimSpace(event.Delta) == "" || event.Phase == gateway.StreamPhaseReasoning {
			return nil
		}
		s.mu.Lock()
		s.textBuilder.WriteString(event.Delta)
		s.mu.Unlock()
		return nil

	case gateway.StreamEventAttachment:
		if len(event.Attachments) == 0 {
			return nil
		}
		s.mu.Lock()
		s.attachments = append(s.attachments, event.Attachments...)
		s.mu.Unlock()
		return nil

	case gateway.StreamEventFinal:
		if event.Final == nil {
			return nil
		}
		s.mu.Lock()
		final := event.Final.Message
		s.final = &final
		s.mu.Unlock()
		return s.flush(ctx)

	case gateway.StreamEventError:
		text := strings.TrimSpace(event.Error)
		if text == "" {
			return nil
		}
		s.mu.Lock()
		s.final = &gateway.PreparedMessage{
			Message: gateway.Message{Format: gateway.MessageFormatPlain, Text: "Error: " + text},
		}
		s.mu.Unlock()
		return s.flush(ctx)
	}
	return nil
}

func (s *dingtalkOutboundStream) Close(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.closed.Store(true)
	if s.finalSent.Load() {
		return nil
	}
	return s.flush(ctx)
}

func (s *dingtalkOutboundStream) flush(ctx context.Context) error {
	if s.finalSent.Load() {
		return nil
	}
	prepared := s.snapshotPrepared()
	if prepared.Message.IsEmpty() && len(prepared.Attachments) == 0 {
		return nil
	}
	if err := s.adapter.Send(ctx, s.cfg, gateway.PreparedOutboundMessage{
		Target:  s.target,
		Message: prepared,
	}); err != nil {
		return err
	}
	s.finalSent.Store(true)
	return nil
}

func (s *dingtalkOutboundStream) snapshotPrepared() gateway.PreparedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	var prepared gateway.PreparedMessage
	if s.final != nil {
		prepared = *s.final
	}
	if strings.TrimSpace(prepared.Message.Text) == "" {
		prepared.Message.Text = strings.TrimSpace(s.textBuilder.String())
	}
	if len(prepared.Attachments) == 0 && len(s.attachments) > 0 {
		prepared.Attachments = append(prepared.Attachments, s.attachments...)
		prepared.Message.Attachments = make([]gateway.Attachment, 0, len(s.attachments))
		for _, att := range s.attachments {
			prepared.Message.Attachments = append(prepared.Message.Attachments, att.Logical)
		}
	}
	if prepared.Message.Reply == nil && s.reply != nil {
		prepared.Message.Reply = s.reply
	}
	return prepared
}
