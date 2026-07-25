package matrix

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/memohai/memoh/domains/channel/gateway"
)

type matrixOutboundStream struct {
	adapter *MatrixAdapter
	cfg     Config
	target  string
	reply   *gateway.ReplyRef

	closed atomic.Bool
	mu     sync.Mutex

	roomID          string
	originalEventID string
	rawBuffer       strings.Builder
	lastText        string
	lastFormat      gateway.MessageFormat
	lastEditedAt    time.Time
	toolMessages    map[string]string
}

func (s *matrixOutboundStream) Push(ctx context.Context, event gateway.PreparedStreamEvent) error {
	if s == nil || s.adapter == nil {
		return errors.New("matrix stream not configured")
	}
	if s.closed.Load() {
		return errors.New("matrix stream is closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	switch event.Type {
	case gateway.StreamEventStatus,
		gateway.StreamEventPhaseStart,
		gateway.StreamEventAgentStart,
		gateway.StreamEventAgentEnd,
		gateway.StreamEventProcessingStarted,
		gateway.StreamEventProcessingCompleted,
		gateway.StreamEventProcessingFailed:
		return nil
	case gateway.StreamEventPhaseEnd:
		if event.Phase != gateway.StreamPhaseText {
			return nil
		}
		s.mu.Lock()
		text := strings.TrimSpace(s.rawBuffer.String())
		s.mu.Unlock()
		return s.upsertText(ctx, text, gateway.MessageFormatPlain, true)
	case gateway.StreamEventToolCallStart:
		s.mu.Lock()
		bufText := strings.TrimSpace(s.rawBuffer.String())
		s.mu.Unlock()
		if bufText != "" {
			if err := s.upsertText(ctx, bufText, gateway.MessageFormatPlain, true); err != nil {
				return err
			}
		}
		s.resetMessageState()
		return s.sendToolCallMessage(ctx, event.ToolCall, gateway.BuildToolCallStart(event.ToolCall))
	case gateway.StreamEventToolCallEnd:
		return s.sendToolCallMessage(ctx, event.ToolCall, gateway.BuildToolCallEnd(event.ToolCall))
	case gateway.StreamEventDelta:
		if event.Phase == gateway.StreamPhaseReasoning || event.Delta == "" {
			return nil
		}
		s.mu.Lock()
		s.rawBuffer.WriteString(event.Delta)
		s.mu.Unlock()
		return nil
	case gateway.StreamEventError:
		errText := strings.TrimSpace(event.Error)
		if errText == "" {
			return nil
		}
		return s.upsertText(ctx, "Error: "+errText, gateway.MessageFormatPlain, true)
	case gateway.StreamEventAttachment:
		return s.pushAttachments(ctx, event.Attachments)
	case gateway.StreamEventFinal:
		if event.Final == nil {
			return errors.New("matrix stream final payload is required")
		}
		finalMsg := event.Final.Message.Message
		if matrixMessageBody(finalMsg) == "" {
			s.mu.Lock()
			text := strings.TrimSpace(s.rawBuffer.String())
			s.mu.Unlock()
			finalMsg.Text = text
			if finalMsg.Format == "" {
				finalMsg.Format = gateway.MessageFormatPlain
			}
		}
		if err := s.upsertMessage(ctx, finalMsg, true); err != nil {
			return err
		}
		if err := s.pushAttachments(ctx, event.Final.Message.Attachments); err != nil {
			return err
		}
		s.resetMessageState()
		return nil
	default:
		return nil
	}
}

func (s *matrixOutboundStream) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.closed.Store(true)
	return nil
}

func (s *matrixOutboundStream) upsertText(ctx context.Context, text string, format gateway.MessageFormat, force bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if format == "" {
		format = gateway.MessageFormatPlain
	}
	return s.upsertMessage(ctx, gateway.Message{Text: text, Format: format}, force)
}

func (s *matrixOutboundStream) upsertMessage(ctx context.Context, msg gateway.Message, force bool) error {
	text := matrixMessageBody(msg)
	if text == "" {
		return nil
	}
	format := msg.Format
	if format == "" {
		if len(msg.Parts) > 0 {
			format = gateway.MessageFormatRich
		} else {
			format = gateway.MessageFormatPlain
		}
		msg.Format = format
	}

	s.mu.Lock()
	roomID := s.roomID
	originalEventID := s.originalEventID
	lastText := s.lastText
	lastFormat := s.lastFormat
	lastEditedAt := s.lastEditedAt
	reply := s.reply
	s.mu.Unlock()

	if roomID == "" {
		resolvedRoomID, err := s.adapter.resolveRoomTarget(ctx, s.cfg, s.target)
		if err != nil {
			return err
		}
		roomID = resolvedRoomID
		s.mu.Lock()
		s.roomID = resolvedRoomID
		s.mu.Unlock()
	}

	if originalEventID == "" {
		if msg.Reply == nil {
			msg.Reply = reply
		}
		eventID, err := s.adapter.sendTextEvent(ctx, s.cfg, roomID, buildMatrixMessageContent(msg, false, ""))
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.originalEventID = eventID
		s.lastText = text
		s.lastFormat = format
		s.lastEditedAt = time.Now()
		s.mu.Unlock()
		return nil
	}

	if text == lastText && format == lastFormat {
		return nil
	}
	if !force && time.Since(lastEditedAt) < matrixEditThrottle {
		return nil
	}
	msg.Reply = nil
	_, err := s.adapter.sendTextEvent(ctx, s.cfg, roomID, buildMatrixMessageContent(msg, true, originalEventID))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.lastText = text
	s.lastFormat = format
	s.lastEditedAt = time.Now()
	s.mu.Unlock()
	return nil
}

// sendToolCallMessage posts a room event on tool_call_start and sends an
// m.replace edit event on tool_call_end so both lifecycle states share a
// single visible message. If no prior event is tracked (or the edit fails),
// it falls back to creating a new event.
func (s *matrixOutboundStream) sendToolCallMessage(
	ctx context.Context,
	tc *gateway.StreamToolCall,
	p gateway.ToolCallPresentation,
) error {
	text := strings.TrimSpace(gateway.RenderToolCallMessageMarkdown(p))
	format := gateway.MessageFormatMarkdown
	if text == "" {
		text = strings.TrimSpace(gateway.RenderToolCallMessage(p))
		format = gateway.MessageFormatPlain
	}
	if text == "" {
		return nil
	}

	s.mu.Lock()
	roomID := s.roomID
	reply := s.reply
	s.mu.Unlock()

	if roomID == "" {
		resolved, err := s.adapter.resolveRoomTarget(ctx, s.cfg, s.target)
		if err != nil {
			return err
		}
		roomID = resolved
		s.mu.Lock()
		s.roomID = roomID
		s.mu.Unlock()
	}

	callID := ""
	if tc != nil {
		callID = strings.TrimSpace(tc.CallID)
	}
	if p.Status != gateway.ToolCallStatusRunning && callID != "" {
		if eventID, ok := s.lookupToolCallMessage(callID); ok {
			editMsg := gateway.Message{Text: text, Format: format}
			if _, err := s.adapter.sendTextEvent(ctx, s.cfg, roomID, buildMatrixMessageContent(editMsg, true, eventID)); err == nil {
				s.forgetToolCallMessage(callID)
				return nil
			}
			s.forgetToolCallMessage(callID)
		}
	}
	msg := gateway.Message{
		Text:   text,
		Format: format,
		Reply:  reply,
	}
	eventID, err := s.adapter.sendTextEvent(ctx, s.cfg, roomID, buildMatrixMessageContent(msg, false, ""))
	if err != nil {
		return err
	}
	if p.Status == gateway.ToolCallStatusRunning && callID != "" && eventID != "" {
		s.storeToolCallMessage(callID, eventID)
	}
	return nil
}

func (s *matrixOutboundStream) lookupToolCallMessage(callID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		return "", false
	}
	v, ok := s.toolMessages[callID]
	return v, ok
}

func (s *matrixOutboundStream) storeToolCallMessage(callID, eventID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		s.toolMessages = make(map[string]string)
	}
	s.toolMessages[callID] = eventID
}

func (s *matrixOutboundStream) forgetToolCallMessage(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		return
	}
	delete(s.toolMessages, callID)
}

func (s *matrixOutboundStream) resetMessageState() {
	s.mu.Lock()
	s.originalEventID = ""
	s.rawBuffer.Reset()
	s.lastText = ""
	s.lastFormat = ""
	s.lastEditedAt = time.Time{}
	s.mu.Unlock()
}

func (s *matrixOutboundStream) pushAttachments(ctx context.Context, attachments []gateway.PreparedAttachment) error {
	if len(attachments) == 0 {
		return nil
	}

	s.mu.Lock()
	roomID := s.roomID
	originalEventID := s.originalEventID
	reply := s.reply
	s.mu.Unlock()

	if roomID == "" {
		resolvedRoomID, err := s.adapter.resolveRoomTarget(ctx, s.cfg, s.target)
		if err != nil {
			return err
		}
		roomID = resolvedRoomID
		s.mu.Lock()
		s.roomID = resolvedRoomID
		s.mu.Unlock()
	}

	for idx, att := range attachments {
		mediaMsg := gateway.Message{}
		if idx == 0 && originalEventID == "" {
			mediaMsg.Reply = reply
		}
		if err := s.adapter.sendMediaAttachment(ctx, s.cfg, roomID, mediaMsg, att); err != nil {
			return err
		}
	}
	return nil
}
