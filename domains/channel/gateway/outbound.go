package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ChunkerMode selects the text chunking strategy.
type ChunkerMode string

const (
	ChunkerModeText     ChunkerMode = "text"
	ChunkerModeMarkdown ChunkerMode = "markdown"
)

const streamFinalFirstChunkTimeout = 3 * time.Second

// OutboundOrder controls the delivery order of text and media messages.
type OutboundOrder string

const (
	OutboundOrderMediaFirst OutboundOrder = "media_first"
	OutboundOrderTextFirst  OutboundOrder = "text_first"
)

// Chunker splits text into pieces that respect a character limit.
type Chunker func(text string, limit int) []string

// OutboundPolicy configures how outbound messages are chunked, ordered, and retried.
type OutboundPolicy struct {
	TextChunkLimit      int           `json:"text_chunk_limit,omitempty"`
	RichTextChunkLimit  int           `json:"rich_text_chunk_limit,omitempty"`
	ChunkerMode         ChunkerMode   `json:"chunker_mode,omitempty"`
	Chunker             Chunker       `json:"-"`
	MediaOrder          OutboundOrder `json:"media_order,omitempty"`
	InlineTextWithMedia bool          `json:"inline_text_with_media,omitempty"`
	RetryMax            int           `json:"retry_max,omitempty"`
	RetryBackoffMs      int           `json:"retry_backoff_ms,omitempty"`
}

// NormalizeOutboundPolicy fills zero-value fields with sensible defaults.
func NormalizeOutboundPolicy(policy OutboundPolicy) OutboundPolicy {
	if policy.TextChunkLimit <= 0 {
		policy.TextChunkLimit = 2000
	}
	if policy.RichTextChunkLimit <= 0 {
		policy.RichTextChunkLimit = policy.TextChunkLimit
	}
	if policy.MediaOrder == "" {
		policy.MediaOrder = OutboundOrderMediaFirst
	}
	if policy.ChunkerMode == "" {
		policy.ChunkerMode = ChunkerModeText
	}
	if policy.RetryMax <= 0 {
		policy.RetryMax = 3
	}
	if policy.RetryBackoffMs <= 0 {
		policy.RetryBackoffMs = 500
	}
	if policy.Chunker == nil {
		policy.Chunker = DefaultChunker(policy.ChunkerMode)
	}
	return policy
}

// DefaultChunker returns the built-in Chunker for the given mode.
func DefaultChunker(mode ChunkerMode) Chunker {
	switch mode {
	case ChunkerModeMarkdown:
		return ChunkMarkdownText
	default:
		return ChunkText
	}
}

// ChunkText splits text at newline boundaries, respecting the rune limit.
func ChunkText(text string, limit int) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if limit <= 0 || runeLen(trimmed) <= limit {
		return []string{trimmed}
	}
	lines := strings.Split(trimmed, "\n")
	chunks := make([]string, 0)
	buf := make([]string, 0, len(lines))
	bufLen := 0
	for _, line := range lines {
		lineLen := runeLen(line)
		sepLen := 0
		if len(buf) > 0 {
			sepLen = 1
		}
		if bufLen+sepLen+lineLen <= limit {
			buf = append(buf, line)
			bufLen += sepLen + lineLen
			continue
		}
		if len(buf) > 0 {
			chunks = append(chunks, strings.Join(buf, "\n"))
			buf = buf[:0]
			bufLen = 0
		}
		if lineLen <= limit {
			buf = append(buf, line)
			bufLen = lineLen
			continue
		}
		chunks = append(chunks, splitLongLine(line, limit)...)
	}
	if len(buf) > 0 {
		chunks = append(chunks, strings.Join(buf, "\n"))
	}
	return chunks
}

// ChunkMarkdownText splits text at paragraph boundaries (double newlines), respecting the rune limit.
func ChunkMarkdownText(text string, limit int) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if limit <= 0 || runeLen(trimmed) <= limit {
		return []string{trimmed}
	}
	paragraphs := strings.Split(trimmed, "\n\n")
	chunks := make([]string, 0)
	buf := make([]string, 0, len(paragraphs))
	bufLen := 0
	for _, para := range paragraphs {
		paraLen := runeLen(para)
		sepLen := 0
		if len(buf) > 0 {
			sepLen = 2
		}
		if bufLen+sepLen+paraLen <= limit {
			buf = append(buf, para)
			bufLen += sepLen + paraLen
			continue
		}
		if len(buf) > 0 {
			chunks = append(chunks, strings.Join(buf, "\n\n"))
			buf = buf[:0]
			bufLen = 0
		}
		if paraLen <= limit {
			buf = append(buf, para)
			bufLen = paraLen
			continue
		}
		chunks = append(chunks, ChunkText(para, limit)...)
	}
	if len(buf) > 0 {
		chunks = append(chunks, strings.Join(buf, "\n\n"))
	}
	return chunks
}

func runeLen(value string) int {
	return utf8.RuneCountInString(value)
}

func splitLongLine(line string, limit int) []string {
	if limit <= 0 {
		return []string{line}
	}
	runes := []rune(line)
	chunks := make([]string, 0)
	for start := 0; start < len(runes); start += limit {
		end := start + limit
		if end > len(runes) {
			end = len(runes)
		}
		segment := strings.TrimSpace(string(runes[start:end]))
		if segment == "" {
			continue
		}
		chunks = append(chunks, segment)
	}
	return chunks
}

// --- Outbound pipeline methods (used by Manager) ---

func (m *Manager) resolveOutboundPolicy(channelType ChannelType) OutboundPolicy {
	policy, ok := m.registry.GetOutboundPolicy(channelType)
	if !ok {
		policy = OutboundPolicy{}
	}
	return NormalizeOutboundPolicy(policy)
}

// buildOutboundMessages splits an outbound message into multiple messages based on the policy.
func buildOutboundMessages(msg OutboundMessage, policy OutboundPolicy) ([]OutboundMessage, error) {
	return buildOutboundMessagesWithCaps(msg, policy, ChannelCapabilities{}, false)
}

func buildOutboundMessagesWithCaps(msg OutboundMessage, policy OutboundPolicy, caps ChannelCapabilities, hasCaps bool) ([]OutboundMessage, error) {
	if msg.Message.IsEmpty() {
		return nil, errors.New("message is required")
	}
	policy = NormalizeOutboundPolicy(policy)
	normalized := normalizeOutboundMessage(msg.Message)
	if hasCaps {
		normalized = coerceFormatForCaps(normalized, caps)
		normalized = coerceOversizedRichPartsForChunking(normalized, policy, caps)
	}
	attachments := append([]Attachment(nil), normalized.Attachments...)
	chunker := policy.Chunker
	if normalized.Format == MessageFormatMarkdown {
		chunker = ChunkMarkdownText
	}
	if chunker == nil {
		chunker = DefaultChunker(policy.ChunkerMode)
	}
	base := normalized
	if shouldInlineTextWithMedia(policy, base, attachments) {
		attachments[0].Caption = strings.TrimSpace(base.Text)
		base.Text = ""
	}
	mediaCarriesActions := mediaShouldCarryActions(policy, base, attachments)
	if mediaCarriesActions {
		base.Actions = nil
	}
	base.Attachments = nil
	textMessages := make([]OutboundMessage, 0)
	// An edit (Message.ID set) targets exactly one existing message, so it must
	// never be chunked: splitting would edit the first piece and post the rest as
	// NEW messages, with the keyboard drifting onto a fresh message — the
	// interactive card visibly falls apart. Edit and chunking are incompatible by
	// definition; keep an edit as a single message. (Interactive cards are far
	// below any platform limit, so this never truncates in practice.)
	shouldChunk := policy.TextChunkLimit > 0 && strings.TrimSpace(base.Text) != "" && len(base.Parts) == 0 && strings.TrimSpace(base.ID) == ""
	if shouldChunk {
		chunks := chunker(base.Text, policy.TextChunkLimit)
		for idx, chunk := range chunks {
			chunk = strings.TrimSpace(chunk)
			if chunk == "" {
				continue
			}
			actions := base.Actions
			if len(chunks) > 1 && idx < len(chunks)-1 {
				actions = nil
			}
			// Message.ID signals an edit operation; only the first chunk carries it
			// so subsequent chunks are delivered as new messages rather than repeated edits.
			var messageID string
			if idx == 0 {
				messageID = base.ID
			}
			item := OutboundMessage{
				Target: msg.Target,
				Message: Message{
					ID:          messageID,
					Format:      base.Format,
					Text:        chunk,
					Parts:       base.Parts,
					Attachments: nil,
					Actions:     actions,
					Thread:      base.Thread,
					Reply:       base.Reply,
					Metadata:    base.Metadata,
				},
			}
			textMessages = append(textMessages, item)
		}
	} else if !base.IsEmpty() {
		textMessages = append(textMessages, OutboundMessage{Target: msg.Target, Message: base})
	}

	attachmentMessages := make([]OutboundMessage, 0)
	if len(attachments) > 0 {
		media := normalized
		media.Format = ""
		media.Text = ""
		media.Parts = nil
		if !mediaCarriesActions {
			media.Actions = nil
		}
		media.Attachments = attachments
		attachmentMessages = append(attachmentMessages, OutboundMessage{Target: msg.Target, Message: media})
	}

	if len(textMessages) == 0 && len(attachmentMessages) == 0 {
		return nil, errors.New("message is required")
	}
	if policy.MediaOrder == OutboundOrderTextFirst {
		return append(textMessages, attachmentMessages...), nil
	}
	return append(attachmentMessages, textMessages...), nil
}

func coerceOversizedRichPartsForChunking(msg Message, policy OutboundPolicy, caps ChannelCapabilities) Message {
	limit := richPartsChunkLimit(policy, caps)
	if limit <= 0 || len(msg.Parts) == 0 || strings.TrimSpace(msg.ID) != "" {
		return msg
	}
	plain := strings.TrimSpace(RenderPartsAsPlain(msg.Parts))
	if plain == "" || runeLen(plain) <= limit {
		return msg
	}
	if caps.Markdown {
		msg.Text = RenderPartsAsMarkdown(msg.Parts)
		msg.Format = MessageFormatMarkdown
	} else {
		msg.Text = plain
		msg.Format = MessageFormatPlain
	}
	msg.Parts = nil
	return msg
}

func richPartsChunkLimit(policy OutboundPolicy, caps ChannelCapabilities) int {
	if caps.RichText {
		return policy.RichTextChunkLimit
	}
	return policy.TextChunkLimit
}

func shouldInlineTextWithMedia(policy OutboundPolicy, msg Message, attachments []Attachment) bool {
	if !policy.InlineTextWithMedia {
		return false
	}
	if strings.TrimSpace(msg.Text) == "" || len(msg.Parts) > 0 || len(attachments) == 0 {
		return false
	}
	if strings.TrimSpace(attachments[0].Caption) != "" {
		return false
	}
	switch attachments[0].Type {
	case AttachmentImage, AttachmentGIF, AttachmentVideo, AttachmentAudio, AttachmentVoice:
		return true
	default:
		return false
	}
}

func mediaShouldCarryActions(policy OutboundPolicy, msg Message, attachments []Attachment) bool {
	if len(attachments) == 0 || len(msg.Actions) == 0 {
		return false
	}
	hasBody := strings.TrimSpace(msg.Text) != "" || len(msg.Parts) > 0
	return !hasBody || policy.MediaOrder == OutboundOrderTextFirst
}

func normalizeOutboundMessage(msg Message) Message {
	msg = normalizeMixedRichParts(msg)
	if msg.Format == "" {
		if len(msg.Parts) > 0 {
			msg.Format = MessageFormatRich
		} else if strings.TrimSpace(msg.Text) != "" {
			if ContainsMarkdown(msg.Text) {
				msg.Format = MessageFormatMarkdown
			} else {
				msg.Format = MessageFormatPlain
			}
		}
	}
	return msg
}

func normalizeMixedRichParts(msg Message) Message {
	if len(msg.Parts) == 0 || strings.TrimSpace(msg.Text) == "" {
		return msg
	}
	msg.Parts = append([]MessagePart{{Type: MessagePartText, Text: msg.Text}}, msg.Parts...)
	msg.Text = ""
	if msg.Format == "" {
		msg.Format = MessageFormatRich
	}
	return msg
}

func validateMessageCapabilities(registry *Registry, channelType ChannelType, msg Message) error {
	caps, ok := registry.GetCapabilities(channelType)
	return validateMessageAgainstCapabilities(caps, ok, msg)
}

func validateMessageAgainstCapabilities(caps ChannelCapabilities, ok bool, msg Message) error {
	if !ok {
		return nil
	}
	switch msg.Format {
	case MessageFormatPlain:
		if !caps.Text {
			return errors.New("channel does not support plain text")
		}
	case MessageFormatMarkdown:
		if !caps.Markdown && !caps.RichText {
			return errors.New("channel does not support markdown")
		}
	case MessageFormatRich:
		if !caps.RichText {
			return errors.New("channel does not support rich text")
		}
	}
	if len(msg.Parts) > 0 && !caps.RichText {
		return errors.New("channel does not support rich text")
	}
	if len(msg.Attachments) > 0 && !caps.Attachments {
		return errors.New("channel does not support attachments")
	}
	if len(msg.Attachments) > 0 && requiresMedia(msg.Attachments) && !caps.Media {
		return errors.New("channel does not support media")
	}
	if len(msg.Actions) > 0 && !caps.Buttons {
		if !caps.URLButtons {
			return errors.New("channel does not support actions")
		}
		if _, ok := urlActionParts(msg.Actions); !ok {
			return errors.New("channel supports url actions only; callback actions are unsupported")
		}
	}
	if msg.Thread != nil && !caps.Threads {
		return errors.New("channel does not support threads")
	}
	if msg.Reply != nil && !caps.Reply {
		return errors.New("channel does not support reply")
	}
	if strings.TrimSpace(msg.ID) != "" && !caps.Edit {
		return errors.New("channel does not support edit")
	}
	return nil
}

type preparedOutboundDelivery struct {
	target   string
	message  Message
	prepared PreparedOutboundMessage
	editor   MessageEditor
}

func (m *Manager) sendAllWithConfig(ctx context.Context, sender Sender, cfg ChannelConfig, msgs []OutboundMessage, policy OutboundPolicy) error {
	prepared := make([]preparedOutboundDelivery, 0, len(msgs))
	for _, msg := range msgs {
		item, err := m.prepareOutboundForSend(ctx, sender, cfg, msg)
		if err != nil {
			return err
		}
		prepared = append(prepared, item)
	}
	for _, item := range prepared {
		if err := m.sendPreparedWithConfig(ctx, sender, cfg, item, policy); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) sendWithConfig(ctx context.Context, sender Sender, cfg ChannelConfig, msg OutboundMessage, policy OutboundPolicy) error {
	prepared, err := m.prepareOutboundForSend(ctx, sender, cfg, msg)
	if err != nil {
		return err
	}
	return m.sendPreparedWithConfig(ctx, sender, cfg, prepared, policy)
}

func (m *Manager) prepareOutboundForSend(ctx context.Context, sender Sender, cfg ChannelConfig, msg OutboundMessage) (preparedOutboundDelivery, error) {
	if sender == nil {
		return preparedOutboundDelivery{}, fmt.Errorf("unsupported channel type: %s", cfg.ChannelType)
	}
	target := strings.TrimSpace(msg.Target)
	if target == "" {
		return preparedOutboundDelivery{}, errors.New("target is required")
	}
	if msg.Message.IsEmpty() {
		return preparedOutboundDelivery{}, errors.New("message is required")
	}
	normalized := msg
	attachments, err := normalizeAttachmentRefs(msg.Message.Attachments, cfg.ChannelType)
	if err != nil {
		return preparedOutboundDelivery{}, err
	}
	normalized.Message.Attachments = attachments
	// Coerce Format down to what the channel can render BEFORE validation.
	// Rich Parts degrade to Markdown/Plain when needed; Markdown degrades to
	// Plain on plain-only channels.
	caps, hasCaps := m.registry.GetOutboundCapabilities(cfg.ChannelType, cfg, target)
	normalized.Message = normalizeOutboundMessage(normalized.Message)
	if hasCaps {
		normalized.Message = coerceFormatForCaps(normalized.Message, caps)
	}
	if err := validateMessageAgainstCapabilities(caps, hasCaps, normalized.Message); err != nil {
		return preparedOutboundDelivery{}, err
	}
	prepared, err := PrepareOutboundMessage(ctx, m.attachmentStore, cfg, OutboundMessage{
		Target:  target,
		Message: normalized.Message,
	})
	if err != nil {
		return preparedOutboundDelivery{}, err
	}
	if validator, ok := sender.(PreparedOutboundValidator); ok {
		if err := validator.ValidatePreparedOutbound(ctx, cfg, target, prepared); err != nil {
			return preparedOutboundDelivery{}, err
		}
	}
	editor, _ := m.registry.GetMessageEditor(cfg.ChannelType)
	if strings.TrimSpace(normalized.Message.ID) != "" {
		if editor == nil {
			return preparedOutboundDelivery{}, errors.New("channel does not support edit")
		}
	}
	return preparedOutboundDelivery{
		target:   target,
		message:  normalized.Message,
		prepared: prepared,
		editor:   editor,
	}, nil
}

func (m *Manager) sendPreparedWithConfig(ctx context.Context, sender Sender, cfg ChannelConfig, item preparedOutboundDelivery, policy OutboundPolicy) error {
	if strings.TrimSpace(item.message.ID) != "" {
		var lastErr error
		for i := 0; i < policy.RetryMax; i++ {
			err := item.editor.Update(ctx, cfg, item.target, strings.TrimSpace(item.message.ID), item.prepared.Message)
			if err == nil {
				if m.logger != nil {
					m.logger.DebugContext(ctx, "edit outbound success",
						slog.String("channel", cfg.ChannelType.String()),
						slog.String("bot_id", cfg.BotID),
						slog.String("target", item.target),
					)
				}
				return nil
			}
			lastErr = err
			if m.logger != nil {
				m.logger.WarnContext(ctx, "edit outbound retry",
					slog.String("channel", cfg.ChannelType.String()),
					slog.Int("attempt", i+1),
					slog.Any("error", err))
			}
			if !sleepWithContext(ctx, time.Duration(i+1)*time.Duration(policy.RetryBackoffMs)*time.Millisecond) {
				return fmt.Errorf("edit outbound cancelled: %w", ctx.Err())
			}
		}
		return fmt.Errorf("edit outbound failed after retries: %w", lastErr)
	}
	var lastErr error
	for i := 0; i < policy.RetryMax; i++ {
		err := sender.Send(ctx, cfg, item.prepared)
		if err == nil {
			if m.logger != nil {
				m.logger.DebugContext(ctx, "send outbound success",
					slog.String("channel", cfg.ChannelType.String()),
					slog.String("bot_id", cfg.BotID),
					slog.String("target", item.target),
				)
			}
			return nil
		}
		lastErr = err
		if m.logger != nil {
			m.logger.WarnContext(ctx, "send outbound retry",
				slog.String("channel", cfg.ChannelType.String()),
				slog.Int("attempt", i+1),
				slog.Any("error", err))
		}
		if !sleepWithContext(ctx, time.Duration(i+1)*time.Duration(policy.RetryBackoffMs)*time.Millisecond) {
			return fmt.Errorf("send outbound cancelled: %w", ctx.Err())
		}
	}
	return fmt.Errorf("send outbound failed after retries: %w", lastErr)
}

func normalizeAttachmentRefs(attachments []Attachment, defaultPlatform ChannelType) ([]Attachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	normalized := make([]Attachment, 0, len(attachments))
	for _, att := range attachments {
		item := att
		item.URL = strings.TrimSpace(item.URL)
		item.Path = strings.TrimSpace(item.Path)
		item.PlatformKey = strings.TrimSpace(item.PlatformKey)
		item.ContentHash = strings.TrimSpace(item.ContentHash)
		item.Base64 = strings.TrimSpace(item.Base64)
		item.SourcePlatform = strings.TrimSpace(item.SourcePlatform)
		if item.SourcePlatform == "" && item.PlatformKey != "" {
			item.SourcePlatform = defaultPlatform.String()
		}
		if item.URL == "" && item.Path == "" && item.PlatformKey == "" && item.ContentHash == "" && item.Base64 == "" {
			return nil, errors.New("attachment reference is required")
		}
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func requiresMedia(attachments []Attachment) bool {
	for _, att := range attachments {
		switch att.Type {
		case AttachmentAudio, AttachmentVideo, AttachmentVoice, AttachmentGIF:
			return true
		default:
			continue
		}
	}
	return false
}

func validateStreamEvent(registry *Registry, channelType ChannelType, event StreamEvent) error {
	caps, ok := registry.GetCapabilities(channelType)
	switch event.Type {
	case StreamEventStatus:
		if event.Status == "" {
			return errors.New("stream status is required")
		}
	case StreamEventDelta:
		if !caps.Streaming && !caps.BlockStreaming {
			return errors.New("channel does not support streaming")
		}
	case StreamEventPhaseStart, StreamEventPhaseEnd:
		if !caps.Streaming && !caps.BlockStreaming {
			return errors.New("channel does not support streaming")
		}
	case StreamEventToolCallStart, StreamEventToolCallEnd:
		if !caps.Streaming && !caps.BlockStreaming {
			return errors.New("channel does not support streaming")
		}
		if event.ToolCall == nil {
			return errors.New("stream tool call payload is required")
		}
	case StreamEventAttachment:
		if len(event.Attachments) == 0 {
			return errors.New("stream attachments are required")
		}
		if _, err := normalizeAttachmentRefs(event.Attachments, channelType); err != nil {
			return err
		}
		if err := validateMessageAgainstCapabilities(caps, ok, Message{Attachments: event.Attachments}); err != nil {
			return err
		}
	case StreamEventAgentStart, StreamEventAgentEnd, StreamEventProcessingStarted, StreamEventProcessingCompleted:
		return nil
	case StreamEventProcessingFailed:
		if strings.TrimSpace(event.Error) == "" {
			return errors.New("processing failure error is required")
		}
	case StreamEventFinal:
		if event.Final == nil {
			return errors.New("stream final payload is required")
		}
		// Validate against the format the channel will actually receive, but do
		// NOT mutate event.Final here: the event may be fanned out to other
		// channels (tee), and coercing the shared payload would strip markup for
		// a later Markdown-capable channel. The real downgrade is applied to a
		// local copy in Push.
		final := event.Final.Message
		final = normalizeOutboundMessage(final)
		if caps, ok := registry.GetCapabilities(channelType); ok {
			final = coerceFormatForCaps(final, caps)
		}
		if err := validateMessageCapabilities(registry, channelType, final); err != nil {
			return err
		}
		if _, err := normalizeAttachmentRefs(event.Final.Message.Attachments, channelType); err != nil {
			return err
		}
	case StreamEventError:
		if strings.TrimSpace(event.Error) == "" {
			return errors.New("stream error is required")
		}
	default:
		return fmt.Errorf("unsupported stream event type: %s", event.Type)
	}
	return nil
}

func (m *Manager) newReplySender(cfg ChannelConfig, channelType ChannelType) StreamReplySender {
	sender, _ := m.registry.GetSender(channelType)
	streamSender, _ := m.registry.GetStreamSender(channelType)
	return &managerReplySender{
		manager:      m,
		sender:       sender,
		streamSender: streamSender,
		channelType:  channelType,
		config:       cfg,
	}
}

type managerReplySender struct {
	manager      *Manager
	sender       Sender
	streamSender StreamSender
	channelType  ChannelType
	config       ChannelConfig
}

func (s *managerReplySender) Send(ctx context.Context, msg OutboundMessage) error {
	if s.manager == nil {
		return errors.New("channel manager not configured")
	}
	target, err := s.manager.resolveOutboundTarget(ctx, s.channelType, s.config, msg.Target)
	if err != nil {
		return err
	}
	msg.Target = target
	policy := s.manager.resolveOutboundPolicy(s.channelType)
	caps, hasCaps := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, msg.Target)
	outbound, err := buildOutboundMessagesWithCaps(msg, policy, caps, hasCaps)
	if err != nil {
		return err
	}
	return s.manager.sendAllWithConfig(ctx, s.sender, s.config, outbound, policy)
}

func (s *managerReplySender) OpenStream(ctx context.Context, target string, opts StreamOptions) (OutboundStream, error) {
	if s.manager == nil {
		return nil, errors.New("channel manager not configured")
	}
	if s.streamSender == nil {
		return nil, errors.New("channel stream sender not configured")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("target is required")
	}
	var err error
	target, err = s.manager.resolveOutboundTarget(ctx, s.channelType, s.config, target)
	if err != nil {
		return nil, err
	}
	caps, _ := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, target)
	if !caps.Streaming && !caps.BlockStreaming {
		return nil, errors.New("channel does not support streaming")
	}
	stream, err := s.streamSender.OpenStream(ctx, s.config, target, opts)
	if err != nil {
		return nil, err
	}
	return &managerOutboundStream{
		manager:     s.manager,
		config:      s.config,
		stream:      stream,
		channelType: s.channelType,
		target:      target,
		reply:       opts.Reply,
		policy:      s.manager.resolveOutboundPolicy(s.channelType),
		sender:      s.sender,
		send: func(ctx context.Context, msg OutboundMessage) error {
			msg.Target = target
			return s.Send(ctx, msg)
		},
		reopen: func(ctx context.Context) (PreparedOutboundStream, error) {
			return s.streamSender.OpenStream(ctx, s.config, target, StreamOptions{
				SourceMessageID: opts.SourceMessageID,
				Metadata:        opts.Metadata,
			})
		},
	}, nil
}

// managerOutboundStream wraps a PreparedOutboundStream and adds text-chunking,
// stream-splitting, and attachment fallback on top of the raw adapter stream.
//
// Push and Close must be called from a single goroutine; this type is not
// safe for concurrent use.
type managerOutboundStream struct {
	manager     *Manager
	config      ChannelConfig
	stream      PreparedOutboundStream
	channelType ChannelType
	target      string
	reply       *ReplyRef
	policy      OutboundPolicy // cached at open time; immutable after creation
	sender      Sender
	send        func(ctx context.Context, msg OutboundMessage) error
	reopen      func(ctx context.Context) (PreparedOutboundStream, error)
	deltaRunes  int
	deltaText   strings.Builder
	splitCount  int
}

func (s *managerOutboundStream) Push(ctx context.Context, event StreamEvent) error {
	if s.manager == nil || s.stream == nil {
		return errors.New("stream is not configured")
	}
	if err := validateStreamEvent(s.manager.registry, s.channelType, event); err != nil {
		return err
	}
	if event.Type == StreamEventAttachment {
		if caps, ok := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, s.target); ok {
			if err := validateMessageAgainstCapabilities(caps, true, Message{Attachments: event.Attachments}); err != nil {
				return err
			}
		}
	}
	if event.Type == StreamEventToolCallStart && event.ToolCall != nil && hasUserInputAction(event.ToolCall.Actions) {
		caps, _ := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, s.target)
		if !caps.NativeUserInput {
			return s.pushPlainTextUserInput(ctx, event.ToolCall)
		}
	}

	// Downgrade the final's Format to what the channel can render, on a LOCAL
	// copy, so the adapter actually receives the coerced payload. Done here (not
	// in validateStreamEvent) so the shared event is never mutated — it may be
	// fanned out to other channels via tee, where stripping markup for a
	// plain-text channel would corrupt a Markdown-capable channel's copy.
	originalFinalText := ""
	if event.Type == StreamEventFinal && event.Final != nil {
		originalFinalText = strings.TrimSpace(event.Final.Message.PlainText())
		if caps, ok := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, s.target); ok {
			final := *event.Final
			final.Message = normalizeOutboundMessage(final.Message)
			final.Message = coerceFormatForCaps(final.Message, caps)
			if err := validateMessageAgainstCapabilities(caps, true, final.Message); err != nil {
				return err
			}
			event.Final = &final
		}
	}

	if event.Type == StreamEventDelta && event.Delta != "" && event.Phase != StreamPhaseReasoning {
		return s.pushDelta(ctx, event)
	}

	if event.Type == StreamEventFinal && event.Final != nil && s.send != nil {
		if s.splitCount > 0 {
			return s.pushFinalAfterSplit(ctx, event, originalFinalText)
		}
		return s.pushFinalWithChunking(ctx, event)
	}
	return s.pushPrepared(ctx, event)
}

// pushPlainTextUserInput closes any buffered assistant preamble, then sends the
// pending question as a normal message. This keeps ask_user usable even when a
// block-streaming adapter ignores tool lifecycle events entirely.
func (s *managerOutboundStream) pushPlainTextUserInput(ctx context.Context, tc *StreamToolCall) error {
	text := strings.TrimSpace(RenderToolCallMessage(BuildToolCallStart(tc)))
	if text == "" {
		return nil
	}
	if s.deltaRunes > 0 {
		if err := s.splitStream(ctx); err != nil {
			return err
		}
	}
	if s.send == nil {
		return errors.New("plain-text user input sender is not configured")
	}
	return s.send(ctx, OutboundMessage{Message: Message{
		Format: MessageFormatPlain,
		Text:   text,
		Reply:  s.reply,
	}})
}

// streamSplitSoftRatio controls the soft-limit window. The soft limit is
// hardLimit - hardLimit/streamSplitSoftRatio (75% of hard limit). Between
// soft and hard the manager watches for natural break points to split
// gracefully; if none is found it force-splits at the hard limit.
const streamSplitSoftRatio = 4

// pushDelta forwards a text delta and splits the stream into a new message
// when accumulated text approaches the platform's TextChunkLimit. Between
// the soft and hard limits it looks for natural break points (sentence ends,
// line breaks) so messages don't get cut mid-sentence.
func (s *managerOutboundStream) pushDelta(ctx context.Context, event StreamEvent) error {
	policy := s.policy
	if policy.TextChunkLimit <= 0 || s.reopen == nil {
		s.deltaRunes += runeLen(event.Delta)
		return s.pushPrepared(ctx, event)
	}

	newRunes := runeLen(event.Delta)
	afterRunes := s.deltaRunes + newRunes
	hardLimit := policy.TextChunkLimit
	softLimit := hardLimit - hardLimit/streamSplitSoftRatio

	if afterRunes <= softLimit {
		s.deltaRunes = afterRunes
		s.deltaText.WriteString(event.Delta)
		return s.pushPrepared(ctx, event)
	}

	if afterRunes <= hardLimit {
		s.deltaRunes = afterRunes
		s.deltaText.WriteString(event.Delta)
		if err := s.pushPrepared(ctx, event); err != nil {
			return err
		}
		if isNaturalBreakPoint(s.deltaText.String()) {
			s.deltaRunes = 0
			s.deltaText.Reset()
			return s.splitStream(ctx)
		}
		return nil
	}

	if err := s.splitStream(ctx); err != nil {
		return err
	}
	s.deltaRunes = newRunes
	s.deltaText.Reset()
	s.deltaText.WriteString(event.Delta)
	return s.pushPrepared(ctx, event)
}

// splitStream finalizes the current adapter stream, opens a continuation
// stream, and sends Status(Started) so the adapter creates a new platform
// message before the first delta arrives.
func (s *managerOutboundStream) splitStream(ctx context.Context) error {
	if err := s.pushPrepared(ctx, StreamEvent{
		Type:  StreamEventFinal,
		Final: &StreamFinalizePayload{},
	}); err != nil {
		return err
	}
	if err := s.stream.Close(ctx); err != nil {
		return err
	}

	newStream, err := s.reopen(ctx)
	if err != nil {
		return err
	}
	s.stream = newStream
	s.splitCount++

	return s.pushPrepared(ctx, StreamEvent{
		Type:   StreamEventStatus,
		Status: StreamStatusStarted,
	})
}

const sentenceTerminators = ".。!！?？…⋯;；"

// isNaturalBreakPoint reports whether text ends at a position suitable for
// splitting a message — a line break or sentence-ending punctuation.
func isNaturalBreakPoint(text string) bool {
	if strings.HasSuffix(text, "\n") {
		return true
	}
	trimmed := strings.TrimRightFunc(text, unicode.IsSpace)
	if trimmed == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(trimmed)
	return strings.ContainsRune(sentenceTerminators, last)
}

// pushFinalAfterSplit handles StreamEventFinal when the adapter has already
// sent earlier portions of the response during streaming. It passes an
// empty-text Final so the adapter finalizes its internal buffer, then
// delivers any remaining attachments / actions via the non-streaming path.
func (s *managerOutboundStream) pushFinalAfterSplit(ctx context.Context, event StreamEvent, originalFinalText string) error {
	var bufferMessage Message
	if event.Final != nil && len(event.Final.Message.Attachments) == 0 && len(event.Final.Message.Actions) > 0 {
		bufferMessage = Message{
			Actions:  event.Final.Message.Actions,
			Thread:   event.Final.Message.Thread,
			Reply:    event.Final.Message.Reply,
			Metadata: event.Final.Message.Metadata,
		}
	}
	bufferFinal := StreamEvent{
		Type:     StreamEventFinal,
		Final:    &StreamFinalizePayload{Message: bufferMessage},
		Metadata: event.Metadata,
	}
	if err := s.pushPrepared(ctx, bufferFinal); err != nil {
		return err
	}

	if event.Final == nil {
		return nil
	}
	msg := event.Final.Message

	if len(msg.Parts) > 0 {
		msg.Parts = nil
	}
	msg.Text = trimSplitFinalDuplicateBody(msg.Text, originalFinalText)

	if strings.TrimSpace(msg.Text) != "" || len(msg.Attachments) > 0 {
		if err := s.send(ctx, OutboundMessage{
			Message: Message{
				Format:      msg.Format,
				Text:        msg.Text,
				Attachments: msg.Attachments,
				Thread:      msg.Thread,
				Reply:       msg.Reply,
				Metadata:    msg.Metadata,
				Actions:     msg.Actions,
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func trimSplitFinalDuplicateBody(text string, originalFinalText string) string {
	text = strings.TrimSpace(text)
	originalFinalText = strings.TrimSpace(originalFinalText)
	if text == "" || originalFinalText == "" {
		return text
	}
	if text == originalFinalText {
		return ""
	}
	if strings.HasPrefix(text, originalFinalText) {
		rest := strings.TrimSpace(strings.TrimPrefix(text, originalFinalText))
		rest = strings.TrimPrefix(rest, "\n\n")
		rest = strings.TrimPrefix(rest, "\n")
		return strings.TrimSpace(rest)
	}
	return text
}

func (s *managerOutboundStream) pushFinalWithChunking(ctx context.Context, event StreamEvent) error {
	policy := s.policy
	if policy.TextChunkLimit <= 0 {
		if s.manager.logger != nil {
			s.manager.logger.DebugContext(ctx, "stream final chunking skipped: non-positive chunk limit",
				slog.String("channel", s.channelType.String()),
				slog.Int("chunk_limit", policy.TextChunkLimit),
			)
		}
		return s.pushPrepared(ctx, event)
	}
	msg := normalizeOutboundMessage(event.Final.Message)
	if caps, ok := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, s.target); ok {
		msg = coerceOversizedRichPartsForChunking(msg, policy, caps)
	}
	text := strings.TrimSpace(msg.PlainText())
	textRunes := runeLen(text)
	if s.manager.logger != nil {
		s.manager.logger.DebugContext(ctx, "stream final chunking evaluate",
			slog.String("channel", s.channelType.String()),
			slog.Int("chunk_limit", policy.TextChunkLimit),
			slog.Int("text_runes", textRunes),
			slog.Int("attachments", len(msg.Attachments)),
			slog.String("format", string(msg.Format)),
		)
	}
	if text == "" || runeLen(text) <= policy.TextChunkLimit {
		if s.manager.logger != nil {
			s.manager.logger.DebugContext(ctx, "stream final chunking skipped: text within limit",
				slog.String("channel", s.channelType.String()),
				slog.Int("text_runes", textRunes),
				slog.Int("chunk_limit", policy.TextChunkLimit),
			)
		}
		return s.pushPrepared(ctx, event)
	}
	if len(msg.Parts) > 0 {
		if s.manager.logger != nil {
			s.manager.logger.DebugContext(ctx, "stream final chunking skipped: rich parts preserve adapter renderer",
				slog.String("channel", s.channelType.String()),
				slog.Int("text_runes", textRunes),
				slog.Int("chunk_limit", policy.TextChunkLimit),
			)
		}
		return s.pushPrepared(ctx, event)
	}

	chunker := policy.Chunker
	if msg.Format == MessageFormatMarkdown {
		chunker = ChunkMarkdownText
	}
	chunks := chunker(text, policy.TextChunkLimit)
	if len(chunks) <= 1 {
		if s.manager.logger != nil {
			s.manager.logger.DebugContext(ctx, "stream final chunking skipped: chunker returned single chunk",
				slog.String("channel", s.channelType.String()),
				slog.Int("chunks", len(chunks)),
			)
		}
		return s.pushPrepared(ctx, event)
	}

	hasAttachments := len(msg.Attachments) > 0
	if s.manager.logger != nil {
		s.manager.logger.InfoContext(ctx, "stream final chunking applied",
			slog.String("channel", s.channelType.String()),
			slog.Int("chunks", len(chunks)),
			slog.Bool("has_attachments", hasAttachments),
		)
	}

	preparedOverflow, err := s.prepareChunkedFinalSends(ctx, msg, chunks, 1, hasAttachments)
	if err != nil {
		return err
	}

	firstMsg := msg
	firstMsg.Text = chunks[0]
	firstMsg.Parts = nil
	firstMsg.Attachments = nil
	firstMsg.Actions = nil
	firstChunkEvent := StreamEvent{
		Type:     StreamEventFinal,
		Final:    &StreamFinalizePayload{Message: firstMsg},
		Metadata: event.Metadata,
	}
	firstChunkCtx, cancelFirstChunk := context.WithTimeout(ctx, streamFinalFirstChunkTimeout)
	defer cancelFirstChunk()
	if err := s.pushPrepared(firstChunkCtx, firstChunkEvent); err != nil {
		if s.manager.logger != nil {
			s.manager.logger.WarnContext(ctx, "stream final first chunk push failed, fallback to direct sends",
				slog.String("channel", s.channelType.String()),
				slog.Duration("timeout", streamFinalFirstChunkTimeout),
				slog.Any("error", err),
			)
		}
		return s.sendChunkedFinal(ctx, msg, chunks, 0, hasAttachments)
	}
	return s.sendPreparedChunkedFinal(ctx, preparedOverflow, 1, len(chunks), hasAttachments)
}

func (s *managerOutboundStream) pushPrepared(ctx context.Context, event StreamEvent) error {
	if s.manager == nil || s.stream == nil {
		return errors.New("stream is not configured")
	}
	prepared, err := PrepareStreamEvent(ctx, s.manager.attachmentStore, s.config, event)
	if err != nil {
		return err
	}
	return s.stream.Push(ctx, prepared)
}

func (s *managerOutboundStream) sendChunkedFinal(ctx context.Context, msg Message, chunks []string, startIndex int, hasAttachments bool) error {
	if startIndex < 0 {
		startIndex = 0
	}
	deliveries, err := s.prepareChunkedFinalSends(ctx, msg, chunks, startIndex, hasAttachments)
	if err != nil {
		return err
	}
	return s.sendPreparedChunkedFinal(ctx, deliveries, startIndex, len(chunks), hasAttachments)
}

func (s *managerOutboundStream) sendPreparedChunkedFinal(ctx context.Context, deliveries []preparedOutboundDelivery, startIndex int, totalChunks int, hasAttachments bool) error {
	if s.sender != nil {
		for idx, item := range deliveries {
			if err := s.manager.sendPreparedWithConfig(ctx, s.sender, s.config, item, s.policy); err != nil {
				if s.manager.logger != nil {
					s.manager.logger.ErrorContext(ctx, "stream final overflow chunk send failed",
						slog.String("channel", s.channelType.String()),
						slog.Int("chunk_index", startIndex+idx+1),
						slog.Int("total_chunks", totalChunks),
						slog.Any("error", err),
					)
				}
				return err
			}
		}
	} else {
		for idx, item := range deliveries {
			if err := s.send(ctx, OutboundMessage{Message: item.message}); err != nil {
				if s.manager.logger != nil {
					s.manager.logger.ErrorContext(ctx, "stream final overflow chunk send failed",
						slog.String("channel", s.channelType.String()),
						slog.Int("chunk_index", startIndex+idx+1),
						slog.Int("total_chunks", totalChunks),
						slog.Any("error", err),
					)
				}
				return err
			}
		}
	}
	if s.manager.logger != nil {
		s.manager.logger.InfoContext(ctx, "stream final chunking completed",
			slog.String("channel", s.channelType.String()),
			slog.Int("chunks", totalChunks),
			slog.Bool("has_attachments", hasAttachments),
		)
	}
	return nil
}

func (s *managerOutboundStream) prepareChunkedFinalSends(ctx context.Context, msg Message, chunks []string, startIndex int, hasAttachments bool) ([]preparedOutboundDelivery, error) {
	outbound := make([]OutboundMessage, 0, max(0, len(chunks)-startIndex)+1)
	for idx := startIndex; idx < len(chunks); idx++ {
		chunk := chunks[idx]
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		isLast := idx == len(chunks)-1
		var actions []Action
		if isLast && !hasAttachments {
			actions = msg.Actions
		}
		outbound = append(outbound, OutboundMessage{
			Message: Message{
				Format:   msg.Format,
				Text:     chunk,
				Thread:   msg.Thread,
				Reply:    msg.Reply,
				Metadata: msg.Metadata,
				Actions:  actions,
			},
		})
	}

	if hasAttachments {
		outbound = append(outbound, OutboundMessage{
			Message: Message{
				Attachments: msg.Attachments,
				Thread:      msg.Thread,
				Reply:       msg.Reply,
				Metadata:    msg.Metadata,
				Actions:     msg.Actions,
			},
		})
	}
	prepared := make([]preparedOutboundDelivery, 0, len(outbound))
	if s.sender != nil {
		for _, msg := range outbound {
			msg.Target = s.target
			item, err := s.manager.prepareOutboundForSend(ctx, s.sender, s.config, msg)
			if err != nil {
				return nil, err
			}
			prepared = append(prepared, item)
		}
		return prepared, nil
	}
	for _, msg := range outbound {
		prepared = append(prepared, preparedOutboundDelivery{message: msg.Message})
	}
	return prepared, nil
}

func (s *managerOutboundStream) Close(ctx context.Context) error {
	if s.stream == nil {
		return errors.New("stream is not configured")
	}
	return s.stream.Close(ctx)
}

// sleepWithContext waits for d or until ctx is cancelled.
// It returns true if the sleep completed normally, false if ctx was cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
