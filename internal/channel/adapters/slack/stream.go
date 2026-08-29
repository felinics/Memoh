package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	slackapi "github.com/slack-go/slack"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

const (
	slackStreamUpdateThrottle  = 1500 * time.Millisecond
	slackStreamRetryFallback   = 2 * time.Second
	slackStreamFinalMaxRetries = 3
)

type slackOutboundStream struct {
	adapter      *SlackAdapter
	cfg          channel.ChannelConfig
	target       string
	reply        *channel.ReplyRef
	api          *slackapi.Client
	closed       atomic.Bool
	mu           sync.Mutex
	msgTS        string // Slack message timestamp (used as message ID)
	buffer       strings.Builder
	lastSent     string
	lastUpdate   time.Time
	nextUpdate   time.Time
	toolMessages map[string]string
}

var _ channel.PreparedOutboundStream = (*slackOutboundStream)(nil)

func (s *slackOutboundStream) Push(ctx context.Context, event channel.PreparedStreamEvent) error {
	if s == nil || s.adapter == nil {
		return errors.New("slack stream not configured")
	}
	if s.closed.Load() {
		return errors.New("slack stream is closed")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	switch event.Type {
	case channel.StreamEventStatus:
		if event.Status == channel.StreamStatusStarted {
			return s.ensureMessage(ctx, "Thinking...")
		}
		return nil

	case channel.StreamEventDelta:
		if event.Delta == "" || event.Phase == channel.StreamPhaseReasoning {
			return nil
		}
		s.mu.Lock()
		s.buffer.WriteString(event.Delta)
		s.mu.Unlock()

		return s.updateMessage(ctx)

	case channel.StreamEventFinal:
		if event.Final == nil {
			return errors.New("slack stream final payload is required")
		}
		s.mu.Lock()
		bufText := strings.TrimSpace(s.buffer.String())
		s.mu.Unlock()
		finalBody := renderSlackStreamFinalBody(event.Final.Message.Message, bufText)
		if finalBody.Text != "" {
			if err := s.finalizeMessageBody(ctx, finalBody, event.Final.Message.Message.Actions); err != nil {
				return err
			}
		} else if err := s.clearPlaceholder(ctx); err != nil {
			return err
		}
		for _, att := range event.Final.Message.Attachments {
			if err := s.sendAttachment(ctx, att); err != nil {
				return err
			}
		}
		return nil

	case channel.StreamEventError:
		errText := redact.Text(strings.TrimSpace(event.Error))
		if errText == "" {
			return nil
		}
		return s.finalizeMessage(ctx, "Error: "+errText, nil)

	case channel.StreamEventAttachment:
		if len(event.Attachments) == 0 {
			return nil
		}
		s.mu.Lock()
		finalText := strings.TrimSpace(s.buffer.String())
		s.mu.Unlock()
		if finalText != "" {
			if err := s.finalizeMessage(ctx, finalText, nil); err != nil {
				return err
			}
		} else if err := s.clearPlaceholder(ctx); err != nil {
			return err
		}
		for _, att := range event.Attachments {
			if err := s.sendAttachment(ctx, att); err != nil {
				return err
			}
		}
		return nil

	case channel.StreamEventToolCallStart:
		s.mu.Lock()
		bufText := strings.TrimSpace(s.buffer.String())
		s.mu.Unlock()
		if bufText != "" {
			if err := s.finalizeMessage(ctx, bufText, nil); err != nil {
				return err
			}
		} else if err := s.clearPlaceholder(ctx); err != nil {
			return err
		}
		s.resetStreamState()
		return s.sendToolCallMessage(ctx, event.ToolCall, channel.BuildToolCallStart(event.ToolCall))
	case channel.StreamEventToolCallEnd:
		return s.sendToolCallMessage(ctx, event.ToolCall, channel.BuildToolCallEnd(event.ToolCall))

	case channel.StreamEventAgentStart, channel.StreamEventAgentEnd,
		channel.StreamEventPhaseStart, channel.StreamEventPhaseEnd,
		channel.StreamEventProcessingStarted, channel.StreamEventProcessingCompleted,
		channel.StreamEventProcessingFailed,
		channel.StreamEventReaction, channel.StreamEventSpeech:
		return nil

	default:
		return fmt.Errorf("unsupported stream event type: %s", event.Type)
	}
}

func (s *slackOutboundStream) Close(ctx context.Context) error {
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

func (s *slackOutboundStream) ensureMessage(ctx context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.msgTS != "" {
		return nil
	}

	text = truncateSlackText(text)

	ts, err := s.postMessageWithRetry(ctx, slackDefaultBody(text), nil)
	if err != nil {
		return err
	}

	s.msgTS = ts
	s.lastSent = normalizeSlackStreamText(text)
	s.lastUpdate = time.Now()
	s.nextUpdate = s.lastUpdate.Add(slackStreamUpdateThrottle)
	return nil
}

func (s *slackOutboundStream) updateMessage(ctx context.Context) error {
	s.mu.Lock()
	msgTS := s.msgTS
	content := truncateSlackText(strings.TrimSpace(s.buffer.String()))
	lastSent := s.lastSent
	nextUpdate := s.nextUpdate
	s.mu.Unlock()

	if msgTS == "" || content == "" {
		return nil
	}
	if normalizeSlackStreamText(content) == normalizeSlackStreamText(lastSent) {
		return nil
	}
	if time.Now().Before(nextUpdate) {
		return nil
	}

	err := s.updateMessageText(ctx, msgTS, slackDefaultBody(content), nil)
	if err == nil {
		s.mu.Lock()
		s.lastSent = normalizeSlackStreamText(content)
		s.lastUpdate = time.Now()
		s.nextUpdate = s.lastUpdate.Add(slackStreamUpdateThrottle)
		s.mu.Unlock()
		return nil
	}
	if delay, ok := slackRetryDelay(err); ok {
		s.mu.Lock()
		s.nextUpdate = time.Now().Add(delay)
		s.mu.Unlock()
		if s.adapter != nil && s.adapter.logger != nil {
			s.adapter.logger.Warn("slack stream update throttled",
				slog.String("config_id", s.cfg.ID),
				slog.String("target", s.target),
				slog.Duration("retry_after", delay),
				slog.Any("error", err),
			)
		}
		return nil
	}
	if s.adapter != nil && s.adapter.logger != nil {
		s.adapter.logger.Warn("slack stream update failed",
			slog.String("config_id", s.cfg.ID),
			slog.String("target", s.target),
			slog.Any("error", err),
		)
	}
	return nil
}

func renderSlackStreamFinalBody(msg channel.Message, buffered string) slackOutboundBody {
	body := renderSlackOutboundBody(msg)
	body.Text = strings.TrimSpace(body.Text)
	body.BlockText = strings.TrimSpace(body.BlockText)
	if body.Text != "" {
		return body
	}
	return slackDefaultBody(strings.TrimSpace(buffered))
}

func (s *slackOutboundStream) finalizeMessage(ctx context.Context, text string, actions []channel.Action) error {
	return s.finalizeMessageBody(ctx, slackDefaultBody(text), actions)
}

func (s *slackOutboundStream) finalizeMessageBody(ctx context.Context, body slackOutboundBody, actions []channel.Action) error {
	s.mu.Lock()
	text := truncateSlackText(body.Text)
	blockText := truncateSlackText(body.BlockText)
	if blockText == "" {
		blockText = text
	}
	msgTS := s.msgTS
	lastSent := s.lastSent
	s.mu.Unlock()

	if len(actions) == 0 && normalizeSlackStreamText(text) == normalizeSlackStreamText(lastSent) && msgTS != "" {
		return nil
	}

	if msgTS == "" {
		body.Text = text
		body.BlockText = blockText
		ts, err := s.postMessageWithRetry(ctx, body, actions)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.msgTS = ts
		s.lastSent = normalizeSlackStreamText(text)
		s.lastUpdate = time.Now()
		s.nextUpdate = s.lastUpdate.Add(slackStreamUpdateThrottle)
		s.mu.Unlock()
		return nil
	}

	body.Text = text
	body.BlockText = blockText
	err := s.updateMessageTextWithRetry(ctx, msgTS, body, actions)
	if err == nil {
		s.mu.Lock()
		s.lastSent = normalizeSlackStreamText(text)
		s.lastUpdate = time.Now()
		s.nextUpdate = s.lastUpdate.Add(slackStreamUpdateThrottle)
		s.mu.Unlock()
		return nil
	}

	if s.adapter != nil && s.adapter.logger != nil {
		s.adapter.logger.Warn("slack stream final update failed, falling back to new message",
			slog.String("config_id", s.cfg.ID),
			slog.String("target", s.target),
			slog.Any("error", err),
		)
	}

	if err := s.clearPlaceholder(ctx); err != nil {
		return err
	}

	ts, postErr := s.postMessageWithRetry(ctx, body, actions)
	if postErr != nil {
		return postErr
	}
	s.mu.Lock()
	s.msgTS = ts
	s.lastSent = normalizeSlackStreamText(text)
	s.lastUpdate = time.Now()
	s.nextUpdate = s.lastUpdate.Add(slackStreamUpdateThrottle)
	s.mu.Unlock()
	return nil
}

func (s *slackOutboundStream) clearPlaceholder(ctx context.Context) error {
	s.mu.Lock()
	msgTS := s.msgTS
	s.mu.Unlock()

	if msgTS == "" {
		return nil
	}
	if _, _, err := s.api.DeleteMessageContext(ctx, s.target, msgTS); err != nil {
		return err
	}

	s.mu.Lock()
	s.msgTS = ""
	s.lastSent = ""
	s.lastUpdate = time.Time{}
	s.nextUpdate = time.Time{}
	s.mu.Unlock()
	return nil
}

func (s *slackOutboundStream) sendAttachment(ctx context.Context, att channel.PreparedAttachment) error {
	threadTS := ""
	if s.reply != nil && s.reply.MessageID != "" {
		threadTS = s.reply.MessageID
	}
	return s.adapter.uploadPreparedAttachment(ctx, s.api, s.target, threadTS, att)
}

// sendToolCallMessage posts a message for tool_call_start and updates the same
// message on tool_call_end via chat.update so the running → completed/failed
// transition shares one visible post. If the edit fails (or no prior message
// is tracked), it falls back to posting a new message.
func (s *slackOutboundStream) sendToolCallMessage(
	ctx context.Context,
	tc *channel.StreamToolCall,
	p channel.ToolCallPresentation,
) error {
	text := truncateSlackText(strings.TrimSpace(channel.RenderToolCallMessageMarkdown(p)))
	if text == "" {
		return nil
	}
	callID := ""
	if tc != nil {
		callID = strings.TrimSpace(tc.CallID)
	}
	if p.Status != channel.ToolCallStatusRunning && callID != "" {
		if ts, ok := s.lookupToolCallMessage(callID); ok {
			if err := s.updateMessageTextWithRetry(ctx, ts, slackDefaultBody(text), nil); err == nil {
				s.forgetToolCallMessage(callID)
				return nil
			}
			s.forgetToolCallMessage(callID)
		}
	}
	ts, err := s.postMessageWithRetry(ctx, slackDefaultBody(text), nil)
	if err != nil {
		return err
	}
	if p.Status == channel.ToolCallStatusRunning && callID != "" && ts != "" {
		s.storeToolCallMessage(callID, ts)
	}
	return nil
}

func (s *slackOutboundStream) lookupToolCallMessage(callID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		return "", false
	}
	v, ok := s.toolMessages[callID]
	return v, ok
}

func (s *slackOutboundStream) storeToolCallMessage(callID, ts string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		s.toolMessages = make(map[string]string)
	}
	s.toolMessages[callID] = ts
}

func (s *slackOutboundStream) forgetToolCallMessage(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolMessages == nil {
		return
	}
	delete(s.toolMessages, callID)
}

func (s *slackOutboundStream) resetStreamState() {
	s.mu.Lock()
	s.msgTS = ""
	s.buffer.Reset()
	s.lastSent = ""
	s.lastUpdate = time.Time{}
	s.nextUpdate = time.Time{}
	s.mu.Unlock()
}

func (s *slackOutboundStream) postMessageWithRetry(ctx context.Context, body slackOutboundBody, actions []channel.Action) (string, error) {
	text := body.Text
	blockText := body.BlockText
	if blockText == "" {
		blockText = text
	}
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionText(text, false),
	}
	if body.DisableMarkdown {
		opts = append(opts, slackapi.MsgOptionDisableMarkdown())
	}
	if len(actions) > 0 {
		blocks, err := slackURLActionBlocks(blockText, actions)
		if err != nil {
			return "", err
		}
		if len(blocks) > 0 {
			opts = append(opts, slackapi.MsgOptionBlocks(blocks...))
		}
	}
	if s.reply != nil && s.reply.MessageID != "" {
		opts = append(opts, slackapi.MsgOptionTS(s.reply.MessageID))
	}

	var lastErr error
	for attempt := 0; attempt < slackStreamFinalMaxRetries; attempt++ {
		_, ts, err := s.api.PostMessageContext(ctx, s.target, opts...)
		if err == nil {
			return ts, nil
		}
		lastErr = err
		delay, ok := slackRetryDelay(err)
		if !ok {
			return "", err
		}
		if err := sleepWithContext(ctx, delay); err != nil {
			return "", err
		}
	}
	return "", lastErr
}

func (s *slackOutboundStream) updateMessageText(ctx context.Context, msgTS string, body slackOutboundBody, actions []channel.Action) error {
	text := body.Text
	blockText := body.BlockText
	if blockText == "" {
		blockText = text
	}
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionText(text, false),
	}
	if body.DisableMarkdown {
		opts = append(opts, slackapi.MsgOptionDisableMarkdown())
	}
	if len(actions) > 0 {
		blocks, err := slackURLActionBlocks(blockText, actions)
		if err != nil {
			return err
		}
		if len(blocks) > 0 {
			opts = append(opts, slackapi.MsgOptionBlocks(blocks...))
		}
	}
	_, _, _, err := s.api.UpdateMessageContext(ctx, s.target, msgTS, opts...)
	return err
}

func (s *slackOutboundStream) updateMessageTextWithRetry(ctx context.Context, msgTS string, body slackOutboundBody, actions []channel.Action) error {
	var lastErr error
	for attempt := 0; attempt < slackStreamFinalMaxRetries; attempt++ {
		err := s.updateMessageText(ctx, msgTS, body, actions)
		if err == nil {
			return nil
		}
		lastErr = err
		delay, ok := slackRetryDelay(err)
		if !ok {
			return err
		}
		if err := sleepWithContext(ctx, delay); err != nil {
			return err
		}
	}
	return lastErr
}

func slackRetryDelay(err error) (time.Duration, bool) {
	var rateLimitedErr *slackapi.RateLimitedError
	if errors.As(err, &rateLimitedErr) {
		if rateLimitedErr.RetryAfter > 0 {
			return rateLimitedErr.RetryAfter, true
		}
		return slackStreamRetryFallback, true
	}
	return 0, false
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeSlackStreamText(value string) string {
	return strings.TrimSpace(value)
}
