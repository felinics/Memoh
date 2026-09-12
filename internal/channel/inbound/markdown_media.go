package inbound

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"time"

	"github.com/felinics/memoh/internal/channel"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/markdownmedia"
)

// Read publication-owned bindings after the run completes. This also works in
// split mode: Channel uses the persisted snapshot rather than rereading a file
// that a later step may already have overwritten or removed.
func (p *ChannelInboundProcessor) publishedMedia(ctx context.Context, sessionID, runID string, since time.Time) []messagepkg.Message {
	reader, ok := p.message.(interface {
		ListSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error)
	})
	if !ok {
		return nil
	}
	messages, err := reader.ListSinceBySession(ctx, sessionID, since)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("load published media", slog.Any("error", err), slog.String("run_id", runID))
		}
		return nil
	}
	var result []messagepkg.Message
	for _, msg := range messages {
		if msg.Metadata == nil {
			_ = json.Unmarshal(msg.RawMetadata, &msg.Metadata)
		}
		if msg.Metadata["markdown_media_run_id"] == runID {
			result = append(result, msg)
		}
	}
	return result
}

func bindPublishedMedia(msg channel.Message, published *[]messagepkg.Message, locale string) channel.Message {
	refs := markdownmedia.Parse(msg.Text)
	if len(refs) == 0 {
		return msg
	}
	msg.Metadata = maps.Clone(msg.Metadata)
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg.Metadata["locale"] = locale
	for i, candidate := range *published {
		if candidate.Metadata["markdown_media_source"] != msg.Text {
			continue
		}
		msg.Metadata[markdownmedia.MetadataKey] = markdownmedia.Bindings(candidate.Metadata)
		*published = append((*published)[:i], (*published)[i+1:]...)
		return msg
	}
	// A missing publication snapshot must not trigger another read of mutable files.
	failed := make([]markdownmedia.Binding, 0, len(refs))
	for _, ref := range refs {
		failed = append(failed, markdownmedia.Binding{Reference: ref, ErrorCode: "media.reference_unavailable"})
	}
	msg.Metadata[markdownmedia.MetadataKey] = failed
	return msg
}
