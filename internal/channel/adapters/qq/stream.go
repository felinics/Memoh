package qq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

// defaultStreamShardInterval throttles C2C stream shards; QQ allows 50 QPS
// on the endpoint, but the client-side typewriter reads better at ~1/s and
// fewer shards means fewer chances for the known middle-shard drops.
const defaultStreamShardInterval = time.Second

type qqOutboundStream struct {
	target string
	reply  *channel.ReplyRef
	send   func(context.Context, channel.PreparedOutboundMessage) error

	// C2C streaming via /v2/users/{openid}/stream_messages. A nil streamSend
	// keeps the legacy buffer-till-final behavior (group/channel targets,
	// streaming disabled, or no passive-reply anchor message).
	streamSend     func(context.Context, qqStreamShardRequest) (qqStreamShardResponse, error)
	streamInterval time.Duration
	now            func() time.Time
	logger         *slog.Logger

	closed      atomic.Bool
	mu          sync.Mutex
	buffer      strings.Builder
	attachments []channel.PreparedAttachment
	sentText    bool

	streamMsgID  string
	streamIndex  int
	lastShardAt  time.Time
	streamBroken bool
}

func (a *QQAdapter) OpenStream(_ context.Context, cfg channel.ChannelConfig, target string, opts channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	parsed, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, fmt.Errorf("qq open stream: %w", err)
	}
	redact.SetSecrets("qq:"+parsed.AppID, parsed.AppSecret)
	stream := &qqOutboundStream{
		target: target,
		reply:  opts.Reply,
		send: func(ctx context.Context, msg channel.PreparedOutboundMessage) error {
			if msg.Target == "" {
				msg.Target = target
			}
			if msg.Message.Message.Reply == nil && opts.Reply != nil {
				msg.Message.Message.Reply = opts.Reply
			}
			return a.Send(ctx, cfg, msg)
		},
		streamInterval: defaultStreamShardInterval,
		now:            time.Now,
		logger:         a.logger,
	}
	// Streamed shards are markdown payloads (QQ markdown messages), so the
	// C2C stream is only wired when markdown rendering is enabled; otherwise
	// the legacy path keeps its inline-markup stripping semantics.
	if parsed.EnableStreaming && parsed.MarkdownSupport {
		if parsedTarget, targetErr := parseTarget(target); targetErr == nil && parsedTarget.Kind == qqTargetC2C {
			if replyTo := streamReplyMessageID(opts); replyTo != "" {
				client := a.getOrCreateClient(cfg, parsed)
				stream.streamSend = func(ctx context.Context, req qqStreamShardRequest) (qqStreamShardResponse, error) {
					return client.sendStreamShard(ctx, parsedTarget.ID, replyTo, req)
				}
			}
		}
	}
	return stream, nil
}

func streamReplyMessageID(opts channel.StreamOptions) string {
	if opts.Reply != nil && strings.TrimSpace(opts.Reply.MessageID) != "" {
		return strings.TrimSpace(opts.Reply.MessageID)
	}
	return strings.TrimSpace(opts.SourceMessageID)
}

func (s *qqOutboundStream) Push(ctx context.Context, event channel.PreparedStreamEvent) error {
	if s == nil || s.send == nil {
		return errors.New("qq stream not configured")
	}
	if s.closed.Load() {
		return errors.New("qq stream is closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	switch event.Type {
	case channel.StreamEventStatus,
		channel.StreamEventPhaseStart,
		channel.StreamEventPhaseEnd,
		channel.StreamEventToolCallStart,
		channel.StreamEventAgentStart,
		channel.StreamEventAgentEnd,
		channel.StreamEventProcessingStarted,
		channel.StreamEventProcessingCompleted,
		channel.StreamEventProcessingFailed:
		return nil
	case channel.StreamEventToolCallEnd:
		text := strings.TrimSpace(channel.RenderToolCallMessage(channel.BuildToolCallEnd(event.ToolCall)))
		if text == "" {
			return nil
		}
		return s.send(ctx, channel.PreparedOutboundMessage{
			Target: s.target,
			Message: channel.PreparedMessage{
				Message: channel.Message{Format: channel.MessageFormatPlain, Text: text, Reply: s.reply},
			},
		})
	case channel.StreamEventDelta:
		if event.Phase == channel.StreamPhaseReasoning || event.Delta == "" {
			return nil
		}
		s.mu.Lock()
		s.buffer.WriteString(event.Delta)
		content := s.buffer.String()
		due := s.streamSend != nil && !s.streamBroken && s.now().Sub(s.lastShardAt) >= s.streamInterval
		s.mu.Unlock()
		if !due {
			return nil
		}
		// Shard failures must not fail the turn: the stream is marked broken
		// and the final flush falls back to a regular message.
		_ = s.pushShard(ctx, qqStreamInputGenerating, content)
		return nil
	case channel.StreamEventAttachment:
		if len(event.Attachments) == 0 {
			return nil
		}
		s.mu.Lock()
		s.attachments = append(s.attachments, event.Attachments...)
		s.mu.Unlock()
		return nil
	case channel.StreamEventError:
		errText := redact.Text(strings.TrimSpace(event.Error))
		if errText == "" {
			return nil
		}
		return s.flush(ctx, channel.PreparedMessage{
			Message: channel.Message{
				Text: "Error: " + errText,
			},
		})
	case channel.StreamEventFinal:
		if event.Final == nil {
			return errors.New("qq stream final payload is required")
		}
		return s.flush(ctx, event.Final.Message)
	default:
		return nil
	}
}

func (s *qqOutboundStream) Close(ctx context.Context) error {
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

// pushShard sends one replace-mode shard carrying the full text so far.
// Cumulative content keeps the server-side prefix valid even when QQ drops
// middle shards, so a stream can always be completed by the final shard.
func (s *qqOutboundStream) pushShard(ctx context.Context, state int, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	s.mu.Lock()
	if s.streamSend == nil {
		s.mu.Unlock()
		return errors.New("qq stream shards not configured")
	}
	req := qqStreamShardRequest{
		StreamMsgID: s.streamMsgID,
		Index:       s.streamIndex,
		InputState:  state,
		ContentRaw:  content,
	}
	s.mu.Unlock()

	resp, err := s.streamSend(ctx, req)
	if err != nil {
		s.mu.Lock()
		s.streamBroken = true
		logger := s.logger
		s.mu.Unlock()
		if logger != nil {
			logger.Warn("qq stream shard failed", slog.String("target", s.target), slog.Int("index", req.Index), slog.Any("error", err))
		}
		return err
	}
	s.mu.Lock()
	if strings.TrimSpace(resp.ID) != "" {
		s.streamMsgID = strings.TrimSpace(resp.ID)
	}
	s.streamIndex++
	s.lastShardAt = s.now()
	s.streamBroken = false
	s.mu.Unlock()
	return nil
}

func (s *qqOutboundStream) flush(ctx context.Context, msg channel.PreparedMessage) error {
	s.mu.Lock()
	bufferedText := strings.TrimSpace(s.buffer.String())
	bufferedAttachments := append([]channel.PreparedAttachment(nil), s.attachments...)
	alreadySentText := s.sentText
	canStream := s.streamSend != nil
	s.buffer.Reset()
	s.attachments = nil
	s.mu.Unlock()

	logicalMsg := msg.LogicalMessage()
	if bufferedText != "" {
		logicalMsg.Text = bufferedText
		logicalMsg.Parts = nil
		if logicalMsg.Format == "" {
			logicalMsg.Format = channel.MessageFormatPlain
		}
	} else if alreadySentText && len(bufferedAttachments) == 0 && len(msg.Attachments) == 0 && strings.TrimSpace(logicalMsg.PlainText()) != "" {
		return nil
	}
	preparedAttachments := append([]channel.PreparedAttachment(nil), bufferedAttachments...)
	if len(bufferedAttachments) > 0 {
		logicalMsg.Attachments = append(preparedAttachmentLogicals(bufferedAttachments), logicalMsg.Attachments...)
		preparedAttachments = append(preparedAttachments, msg.Attachments...)
	} else {
		preparedAttachments = append(preparedAttachments, msg.Attachments...)
	}
	if logicalMsg.Reply == nil && s.reply != nil {
		logicalMsg.Reply = s.reply
	}

	text := strings.TrimSpace(logicalMsg.PlainText())
	if canStream && text != "" {
		// The final shard completes the stream with the full text; replace
		// mode heals any middle shards QQ dropped. Only a failed final shard
		// falls back to a regular message.
		if err := s.pushShard(ctx, qqStreamInputDone, text); err == nil {
			s.mu.Lock()
			s.sentText = true
			s.mu.Unlock()
			logicalMsg.Text = ""
			logicalMsg.Parts = nil
			logicalMsg.Format = ""
			if logicalMsg.IsEmpty() && len(preparedAttachments) == 0 {
				return nil
			}
		}
	}
	if logicalMsg.IsEmpty() && len(preparedAttachments) == 0 {
		return nil
	}
	if err := s.send(ctx, channel.PreparedOutboundMessage{
		Target: s.target,
		Message: channel.PreparedMessage{
			Message:     logicalMsg,
			Attachments: preparedAttachments,
		},
	}); err != nil {
		return err
	}
	if strings.TrimSpace(logicalMsg.PlainText()) != "" {
		s.mu.Lock()
		s.sentText = true
		s.mu.Unlock()
	}
	return nil
}

func preparedAttachmentLogicals(attachments []channel.PreparedAttachment) []channel.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	logical := make([]channel.Attachment, 0, len(attachments))
	for _, att := range attachments {
		logical = append(logical, att.Logical)
	}
	return logical
}
