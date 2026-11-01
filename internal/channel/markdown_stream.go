package channel

import (
	"context"
	"maps"
	"strings"

	"github.com/felinics/memoh/internal/markdownmedia"
)

// Markdown media on a stream: prose streams as usual, and only text that may
// still become a media reference is withheld. Once a complete reference is
// withheld, everything after it waits for the final message, so the published
// files keep their place in the text. A reference written before a tool call
// carries its segment to the final, which binds it to the run's persisted
// snapshot instead of reading a workspace file that a later step may change.
// Channel never archives: references reach it already bound by the agent.

// streamSegment is one text segment of the reply, delimited by tool calls.
type streamSegment struct {
	text     string // everything the model wrote in the segment
	streamed int    // bytes of text the adapter has shown
	carried  bool   // the rest waits for the final message
}

func (s *managerOutboundStream) markdownMediaEnabled() bool {
	return s.send != nil && !markdownmedia.IsWorkspaceSurface(string(s.channelType))
}

// pushMarkdown handles the stream events that publication depends on. It
// reports whether the event was fully handled; otherwise Push continues with
// the (possibly updated) event.
func (s *managerOutboundStream) pushMarkdown(ctx context.Context, event *StreamEvent) (bool, error) {
	if !s.markdownMediaEnabled() {
		return false, nil
	}
	switch event.Type {
	case StreamEventDelta:
		if event.Phase == StreamPhaseReasoning || event.Delta == "" {
			return false, nil
		}
		s.markdownFinalParts = nil
		s.markdownFinalNext = 0
		return true, s.pushTextDelta(ctx, *event)
	case StreamEventPhaseEnd:
		if event.Phase == StreamPhaseText && !s.holdingMedia {
			return false, s.releaseHeld(ctx)
		}
	case StreamEventSegmentEnd, StreamEventToolCallStart:
		return false, s.endTextSegment(ctx)
	case StreamEventReset:
		s.resetMarkdown()
	case StreamEventFinal:
		return s.pushMarkdownFinal(ctx, event)
	}
	return false, nil
}

func (s *managerOutboundStream) pushTextDelta(ctx context.Context, event StreamEvent) error {
	if s.holdingMedia {
		s.heldText.WriteString(event.Delta)
		return nil
	}
	pending := s.heldText.String() + event.Delta
	s.heldText.Reset()
	// Resume from a line start the last scan reached, so code fences opened
	// earlier still count as code without rescanning the whole segment.
	forwarded := s.segmentText.Len()
	scan := s.segmentText.String()[s.scanFrom:] + pending
	offset, media, resume := markdownmedia.StreamHold(scan)
	hold := max(s.scanFrom+offset-forwarded, 0)
	s.scanFrom += resume
	if hold > 0 {
		release := event
		release.Delta = pending[:hold]
		if err := s.forwardText(ctx, release); err != nil {
			return err
		}
	}
	s.heldText.WriteString(pending[hold:])
	s.holdingMedia = media
	s.scanFrom = min(s.scanFrom, s.segmentText.Len())
	return nil
}

// forwardText streams text in pieces no longer than the platform limit, so a
// released buffer cannot exceed it as one delta after a split.
func (s *managerOutboundStream) forwardText(ctx context.Context, event StreamEvent) error {
	text := event.Delta
	step := s.policy.TextChunkLimit
	for text != "" {
		piece := text
		if step > 0 {
			piece = runePrefix(text, step)
		}
		chunk := event
		chunk.Delta = piece
		splits := s.splitCount
		if err := s.pushDelta(ctx, chunk); err != nil {
			return err
		}
		switch {
		case s.splitCount == splits:
			s.adapterText.WriteString(piece)
		case s.deltaRunes > 0:
			// The piece opened the continuation message.
			s.adapterText.Reset()
			s.adapterText.WriteString(piece)
		default:
			// The piece closed the previous message at a natural break.
			s.adapterText.Reset()
		}
		s.segmentText.WriteString(piece)
		text = text[len(piece):]
	}
	return nil
}

// releaseHeld streams withheld text that did not become a reference.
func (s *managerOutboundStream) releaseHeld(ctx context.Context) error {
	if s.heldText.Len() == 0 {
		s.holdingMedia = false
		return nil
	}
	held := s.heldText.String()
	s.heldText.Reset()
	s.holdingMedia = false
	return s.forwardText(ctx, StreamEvent{Type: StreamEventDelta, Delta: held, Phase: StreamPhaseText})
}

// endTextSegment runs before the adapter commits the segment's text as its
// own message. Every segment is recorded, so the final can align the run's
// snapshots with the references in order.
func (s *managerOutboundStream) endTextSegment(ctx context.Context) error {
	if s.holdingMedia {
		s.segments = append(s.segments, streamSegment{text: s.segmentText.String() + s.heldText.String(), streamed: s.segmentText.Len(), carried: true})
		s.heldText.Reset()
	} else {
		if err := s.releaseHeld(ctx); err != nil {
			return err
		}
		if s.segmentText.Len() > 0 {
			s.segments = append(s.segments, streamSegment{text: s.segmentText.String(), streamed: s.segmentText.Len()})
		}
	}
	s.segmentText.Reset()
	s.adapterText.Reset()
	s.scanFrom = 0
	s.segmentSplits = s.splitCount
	return nil
}

func (s *managerOutboundStream) resetMarkdown() {
	s.segmentText.Reset()
	s.heldText.Reset()
	s.adapterText.Reset()
	s.holdingMedia = false
	s.segments = nil
	s.scanFrom = 0
	s.segmentSplits = s.splitCount
	s.markdownFinalSource = ""
	s.markdownFinalParts = nil
	s.markdownFinalNext = 0
}

func (s *managerOutboundStream) carriesMedia() bool {
	for _, segment := range s.segments {
		if segment.carried {
			return true
		}
	}
	return false
}

func (s *managerOutboundStream) pushMarkdownFinal(ctx context.Context, event *StreamEvent) (bool, error) {
	if event.Final == nil {
		return false, nil
	}
	final := *event.Final
	if s.markdownFinalParts != nil && s.markdownFinalSource == final.Message.Text {
		// A retried final continues after the parts already acknowledged.
		return true, s.deliverMarkdownFinal(ctx)
	}
	var earlier []markdownmedia.Binding
	if value, ok := final.Message.Metadata[markdownmedia.EarlierMetadataKey]; ok {
		earlier = markdownmedia.Bindings(map[string]any{markdownmedia.MetadataKey: value})
		final.Message.Metadata = maps.Clone(final.Message.Metadata)
		delete(final.Message.Metadata, markdownmedia.EarlierMetadataKey)
	}
	if len(markdownmedia.Bindings(final.Message.Metadata)) == 0 && !s.carriesMedia() {
		// Nothing to publish: the withheld text belongs to the reply body.
		if err := s.releaseHeld(ctx); err != nil {
			return true, err
		}
		event.Final = &final
		return false, nil
	}
	parts, adapter, err := s.prepareMarkdownFinal(final.Message, earlier)
	if err != nil {
		return true, err
	}
	s.markdownFinalSource = final.Message.Text
	s.markdownFinalParts = parts
	s.markdownFinalAdapter = adapter
	s.markdownFinalNext = 0
	return true, s.deliverMarkdownFinal(ctx)
}

// prepareMarkdownFinal orders everything the final publishes: carried
// segments from earlier steps, then the final message. Text the adapter has
// already shown is not repeated. It also picks the part that completes the
// adapter's open message; adapters may show a placeholder there (Discord's
// "Thinking..."), so that message always ends with text when there is any.
func (s *managerOutboundStream) prepareMarkdownFinal(msg Message, earlier []markdownmedia.Binding) ([]OutboundMessage, int, error) {
	caps, hasCaps := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, s.target)
	var parts []OutboundMessage
	duplicate := false
	finalText := strings.TrimSpace(msg.PlainText())
	for _, segment := range s.segments {
		// Consume every segment's snapshots in order, delivered or not, so a
		// later reference to the same path gets its own snapshot.
		bindings := markdownmedia.Rebind(segment.text, &earlier)
		duplicate = duplicate || (finalText != "" && strings.TrimSpace(segment.text) == finalText)
		if !segment.carried {
			continue
		}
		carried := Message{Format: MessageFormatMarkdown, Text: segment.text, Reply: msg.Reply, Thread: msg.Thread, Metadata: maps.Clone(msg.Metadata)}
		if carried.Metadata == nil {
			carried.Metadata = map[string]any{}
		}
		carried.Metadata[markdownmedia.MetadataKey] = bindings
		built, err := s.markdownParts(carried, segment.text[:segment.streamed], caps, hasCaps)
		if err != nil {
			return nil, -1, err
		}
		parts = append(parts, built...)
	}

	// Some runtimes repeat the text before a tool call as the final output;
	// that segment was already shown or carried.
	if !msg.IsEmpty() && !duplicate {
		if !s.carriesMedia() && s.splitCount == s.segmentSplits {
			// The adapter's message holds this whole segment, so the leading
			// prose replaces it and nothing streamed is lost or repeated.
			built, err := s.markdownParts(msg, "", caps, hasCaps)
			if err != nil {
				return nil, -1, err
			}
			if len(built) > 0 && isTextPart(built[0]) {
				return append(parts, built...), len(parts), nil
			}
			parts = append(parts, built...)
		} else {
			built, err := s.markdownParts(msg, s.segmentText.String(), caps, hasCaps)
			if err != nil {
				return nil, -1, err
			}
			parts = append(parts, built...)
		}
	}

	commit := OutboundMessage{Target: s.target, Message: Message{Reply: msg.Reply, Thread: msg.Thread, Metadata: maps.Clone(msg.Metadata)}}
	delete(commit.Message.Metadata, markdownmedia.MetadataKey)
	if strings.TrimSpace(s.adapterText.String()) != "" {
		// Keep what the adapter shows, then send the rest separately.
		return append([]OutboundMessage{commit}, parts...), 0, nil
	}
	for i, part := range parts {
		if isTextPart(part) {
			return parts, i, nil
		}
	}
	return append(parts, commit), len(parts), nil
}

// markdownParts expands a message into delivered parts, dropping the prefix
// of its leading prose that the adapter already streamed.
func (s *managerOutboundStream) markdownParts(msg Message, streamed string, caps ChannelCapabilities, hasCaps bool) ([]OutboundMessage, error) {
	expanded, ok := expandMarkdownMessage(OutboundMessage{Target: s.target, Message: msg})
	if !ok {
		expanded = []OutboundMessage{{Target: s.target, Message: msg}}
	}
	if streamed != "" && len(expanded) > 0 && len(expanded[0].Message.Attachments) == 0 {
		expanded[0].Message.Text = trimStreamedProse(expanded[0].Message.Text, streamed)
	}
	nonEmpty := expanded[:0:0]
	for _, part := range expanded {
		if !part.Message.IsEmpty() {
			nonEmpty = append(nonEmpty, part)
		}
	}
	if len(nonEmpty) == 0 {
		return nil, nil
	}
	return buildExpandedMarkdownMessages(nonEmpty, s.policy, caps, hasCaps)
}

func isTextPart(part OutboundMessage) bool {
	return len(part.Message.Attachments) == 0 && strings.TrimSpace(part.Message.PlainText()) != ""
}

// trimStreamedProse removes the prefix the adapter already delivered.
func trimStreamedProse(prose, streamed string) string {
	if streamed == "" {
		return prose
	}
	if rest, ok := strings.CutPrefix(prose, streamed); ok {
		return rest
	}
	trimmedStreamed := strings.TrimSpace(streamed)
	trimmedProse := strings.TrimLeft(prose, " \t\r\n")
	if rest, ok := strings.CutPrefix(trimmedProse, trimmedStreamed); ok {
		return rest
	}
	if strings.HasPrefix(trimmedStreamed, strings.TrimSpace(prose)) {
		return ""
	}
	return prose
}

// deliverMarkdownFinal keeps progress across a retried final event, so parts
// that were already acknowledged are not republished. Platform transport
// retries retain their existing semantics.
func (s *managerOutboundStream) deliverMarkdownFinal(ctx context.Context) error {
	for s.markdownFinalNext < len(s.markdownFinalParts) {
		part := s.markdownFinalParts[s.markdownFinalNext]
		var err error
		if s.markdownFinalNext == s.markdownFinalAdapter {
			// Completes the adapter's open message. Attachments always go
			// through the ordinary sender: some adapters finalize text only.
			msg := normalizeOutboundMessage(part.Message)
			if caps, ok := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, s.target); ok {
				msg = coerceFormatForCaps(msg, caps)
			}
			err = s.pushPrepared(ctx, StreamEvent{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: msg}})
		} else {
			err = s.send(ctx, part)
		}
		if err != nil {
			return err
		}
		s.markdownFinalNext++
	}
	s.segments = nil
	s.heldText.Reset()
	s.holdingMedia = false
	return nil
}

func runePrefix(text string, n int) string {
	count := 0
	for i := range text {
		if count == n {
			return text[:i]
		}
		count++
	}
	return text
}
