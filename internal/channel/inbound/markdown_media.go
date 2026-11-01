package inbound

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/markdownmedia"
)

// bindRunMedia attaches what the run published to its final reply: the
// reply's own references, and the media earlier steps wrote before a tool
// call, which the outbound stream carries to the final. Delivery uses these
// persisted snapshots, so it never rereads a file a later step may have
// changed, and it works in split mode where Channel cannot reach the
// workspace. A run with media but no reply text still gets a final so that
// carried media is delivered.
func (p *ChannelInboundProcessor) bindRunMedia(ctx context.Context, channelType channel.ChannelType, finalMessages []turn.ModelMessage, msg channel.Message, hasReply bool, sessionID, runID string, since time.Time, botID string) (channel.Message, bool) {
	if markdownmedia.IsWorkspaceSurface(string(channelType)) || !runPublishesMarkdownMedia(finalMessages) {
		return msg, hasReply
	}
	locale := p.localizer(ctx, botID).Locale()
	msg, earlier := bindFinalMedia(msg, hasReply, p.publishedMedia(ctx, sessionID, runID, since), locale)
	if len(earlier) > 0 {
		msg = withEarlierMedia(msg, earlier, locale)
		hasReply = true
	}
	return msg, hasReply
}

// publishedMedia reads the run's publication snapshots in publication order.
// A continuation does not know its run id; it accepts any publication in the
// session since it started.
func (p *ChannelInboundProcessor) publishedMedia(ctx context.Context, sessionID, runID string, since time.Time) []messagepkg.Message {
	reader, ok := p.message.(interface {
		ListSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error)
	})
	if !ok || sessionID == "" {
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
		published, _ := msg.Metadata["markdown_media_run_id"].(string)
		if _, ok := msg.Metadata[markdownmedia.MetadataKey]; ok && (published == runID || (runID == "" && published != "")) {
			result = append(result, msg)
		}
	}
	return result
}

// bindFinalMedia binds the reply's references to the latest matching
// snapshots and returns the rest, in order, as the earlier steps' media. A
// reference without a snapshot fails rather than rereading the file.
func bindFinalMedia(msg channel.Message, hasReply bool, published []messagepkg.Message, locale string) (channel.Message, []markdownmedia.Binding) {
	var snapshots []markdownmedia.Binding
	for _, candidate := range published {
		snapshots = append(snapshots, markdownmedia.Bindings(candidate.Metadata)...)
	}
	if !hasReply || len(markdownmedia.Parse(msg.Text)) == 0 {
		return msg, snapshots
	}
	final, earlier := markdownmedia.BindFinal(msg.Text, snapshots)
	msg.Metadata = maps.Clone(msg.Metadata)
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg.Metadata["locale"] = locale
	msg.Metadata[markdownmedia.MetadataKey] = final
	return msg, earlier
}

// runPublishesMarkdownMedia reports whether any assistant message of the run,
// including text written before a tool call, references media, so the
// published snapshots are worth loading.
func runPublishesMarkdownMedia(messages []turn.ModelMessage) bool {
	for _, msg := range messages {
		if msg.Role == "assistant" && len(markdownmedia.Parse(msg.TextContent())) > 0 {
			return true
		}
	}
	return false
}

func withEarlierMedia(msg channel.Message, earlier []markdownmedia.Binding, locale string) channel.Message {
	msg.Metadata = maps.Clone(msg.Metadata)
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg.Metadata["locale"] = locale
	msg.Metadata[markdownmedia.EarlierMetadataKey] = earlier
	return msg
}
